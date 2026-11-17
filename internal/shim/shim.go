// Package shim implements `mairc stdio`: it speaks MCP over stdio to the agent and forwards every
// message to the daemon over HTTP. It keeps no chat state; it exists so that many agents share one
// daemon while each still gets an ordinary stdio MCP server.
package shim

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Options carries everything Run needs: the daemon address, auth, who this agent is, the stdio
// ends, and how to start the daemon when it is not answering.
type Options struct {
	BaseURL     string       // e.g. "http://127.0.0.1:4747"
	Token       string       // Authorization: Bearer <token>
	Seat        string       // X-Mairc-Seat on initialize
	AgentPID    int          // X-Mairc-Pid on initialize: the parent agent CLI
	In          io.Reader    // the agent's stdout -> our stdin
	Out         io.Writer    // our stdout -> the agent
	StartDaemon func() error // starts the daemon detached; called when /healthz does not answer
	Client      *http.Client // default: 10 s timeout
}

const (
	errUnavailable = `{"code":-32000,"message":"mairc daemon unavailable"}`
	healthPath     = "/healthz"
	mcpPath        = "/mcp"
)

// Run forwards messages until In reaches end of file or ctx is cancelled. It returns nil on end of
// file.
func Run(ctx context.Context, o Options) error {
	client := o.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	s := &shim{o: o, client: client, br: bufio.NewReader(o.In)}
	if !s.healthy(ctx) {
		if o.StartDaemon != nil {
			_ = o.StartDaemon()
			for wait := 0; wait < 3000; wait += 50 {
				if s.healthy(ctx) {
					break
				}
				timer := time.NewTimer(50 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
		}
		s.down = !s.healthy(ctx)
	}
	return s.loop(ctx)
}

// shim is the running forwarder.
type shim struct {
	o         Options
	client    *http.Client
	br        *bufio.Reader
	down      bool   // daemon did not answer /healthz yet
	sessionID string // last Mcp-Session-Id seen
	initLine  []byte // the raw initialize request, for re-initializing after a daemon restart
}

// healthy reports whether GET /healthz answers within 500 ms.
func (s *shim) healthy(ctx context.Context) bool {
	hctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(hctx, http.MethodGet, s.o.BaseURL+healthPath, nil)
	if err != nil {
		return false
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// loop reads one line at a time and forwards it, strictly in order.
func (s *shim) loop(ctx context.Context) error {
	for {
		line, err := s.br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if e := s.forward(ctx, bytes.TrimSpace(line)); e != nil {
				return e
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// forward sends one raw message line to the daemon and writes the reply to Out.
func (s *shim) forward(ctx context.Context, line []byte) error {
	var probe struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	_ = json.Unmarshal(line, &probe) // invalid JSON is forwarded as it is
	if s.down {
		if s.healthy(ctx) {
			s.down = false
		} else {
			return s.unavailable(probe.ID)
		}
	}
	isInit := probe.Method == "initialize"
	resp, err := s.post(ctx, line, isInit)
	if err == nil && resp.StatusCode == http.StatusNotFound && len(s.initLine) > 0 {
		_ = resp.Body.Close()
		// The daemon restarted and forgot the session: re-initialize, then retry once.
		initResp, ierr := s.post(ctx, s.initLine, true)
		if ierr != nil {
			return s.unavailable(probe.ID)
		}
		s.takeSession(initResp)
		_ = initResp.Body.Close()
		resp, err = s.post(ctx, line, isInit)
	}
	if err != nil {
		return s.unavailable(probe.ID)
	}
	defer func() { _ = resp.Body.Close() }()
	if isInit && resp.StatusCode == http.StatusOK {
		s.sessionID = resp.Header.Get("Mcp-Session-Id")
		s.initLine = append([]byte(nil), line...)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return s.unavailable(probe.ID)
		}
		_, _ = s.o.Out.Write(append(bytes.TrimRight(body, " \r\n\t"), '\n'))
	case http.StatusAccepted:
		// A notification: nothing is written.
	default:
		return s.unavailable(probe.ID)
	}
	return nil
}

// post sends one raw message to POST /mcp with auth, seat/pid on initialize, and the session id
// when one is known.
func (s *shim) post(ctx context.Context, body []byte, isInit bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.o.BaseURL+mcpPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.o.Token)
	req.Header.Set("Content-Type", "application/json")
	if isInit {
		req.Header.Set("X-Mairc-Seat", s.o.Seat)
		req.Header.Set("X-Mairc-Pid", strconv.Itoa(s.o.AgentPID))
	}
	if s.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", s.sessionID)
	}
	return s.client.Do(req)
}

// takeSession remembers the session id off a re-initialize response.
func (s *shim) takeSession(resp *http.Response) {
	if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
		s.sessionID = id
	}
}

// unavailable answers a request with the -32000 error; a notification gets nothing.
func (s *shim) unavailable(id json.RawMessage) error {
	if id == nil {
		return nil
	}
	_, _ = fmt.Fprintf(s.o.Out, `{"jsonrpc":"2.0","id":%s,"error":%s}`+"\n", id, errUnavailable)
	return nil
}
