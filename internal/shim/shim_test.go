package shim

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// reqSeen records what the fake daemon observed on POST /mcp.
type reqSeen struct {
	auth      string
	seat      string
	pid       string
	sessionID string
	body      string
}

// fakeDaemon serves /healthz and a scripted /mcp.
type fakeDaemon struct {
	t       *testing.T
	handler func(reqSeen, *http.Request) (int, string, http.Header)
	seen    chan reqSeen
	up      atomic.Bool
}

func newFakeDaemon(t *testing.T, handler func(reqSeen, *http.Request) (int, string, http.Header)) (*fakeDaemon, *httptest.Server) {
	d := &fakeDaemon{t: t, handler: handler, seen: make(chan reqSeen, 32)}
	d.up.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == healthPath {
			if !d.up.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		body, _ := io.ReadAll(r.Body)
		seen := reqSeen{
			auth:      r.Header.Get("Authorization"),
			seat:      r.Header.Get("X-Mairc-Seat"),
			pid:       r.Header.Get("X-Mairc-Pid"),
			sessionID: r.Header.Get("Mcp-Session-Id"),
			body:      string(body),
		}
		d.seen <- seen
		code, text, hdr := d.handler(seen, r)
		for k, vs := range hdr {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(code)
		if text != "" {
			_, _ = w.Write([]byte(text))
		}
	}))
	t.Cleanup(srv.Close)
	return d, srv
}

func runShim(o Options) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if o.StartDaemon == nil {
		o.StartDaemon = func() error { return nil }
	}
	return Run(ctx, o)
}

func outLines(b *bytes.Buffer) []string {
	s := strings.TrimSuffix(b.String(), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func TestForwardsInitializeThenToolsList(t *testing.T) {
	d, srv := newFakeDaemon(t, func(s reqSeen, _ *http.Request) (int, string, http.Header) {
		var m map[string]json.RawMessage
		_ = json.Unmarshal([]byte(s.body), &m)
		method := ""
		_ = json.Unmarshal(m["method"], &method)
		h := http.Header{}
		if method == "initialize" {
			h.Set("Mcp-Session-Id", "sess-abc")
			return http.StatusOK, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18"}}`, h
		}
		return http.StatusOK, `{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`, h
	})
	var out bytes.Buffer
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n")
	err := runShim(Options{BaseURL: srv.URL, Token: "tok", Seat: "tmux:%9", AgentPID: 4242, In: in, Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	lines := outLines(&out)
	if len(lines) != 2 {
		t.Fatalf("out = %q", out.String())
	}
	if !strings.Contains(lines[0], `"id":1`) || !strings.Contains(lines[1], `"tools":[]`) {
		t.Fatalf("lines = %v", lines)
	}
	first := <-d.seen
	if first.auth != "Bearer tok" || first.seat != "tmux:%9" || first.pid != "4242" || first.sessionID != "" {
		t.Fatalf("initialize headers = %+v", first)
	}
	second := <-d.seen
	if second.sessionID != "sess-abc" || second.seat != "" || second.pid != "" {
		t.Fatalf("tools/list headers = %+v", second)
	}
}

func TestNotificationWritesNothing(t *testing.T) {
	_, srv := newFakeDaemon(t, func(_ reqSeen, _ *http.Request) (int, string, http.Header) {
		return http.StatusAccepted, "", nil
	})
	var out bytes.Buffer
	in := strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")
	if err := runShim(Options{BaseURL: srv.URL, Token: "t", In: in, Out: &out}); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("notification produced output %q", out.String())
	}
}

func TestStartsDaemonWhenDown(t *testing.T) {
	d, srv := newFakeDaemon(t, func(_ reqSeen, _ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `{"jsonrpc":"2.0","id":1,"result":{}}`, nil
	})
	d.up.Store(false)
	starts := 0
	go func() {
		time.Sleep(150 * time.Millisecond)
		d.up.Store(true)
	}()
	var out bytes.Buffer
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n")
	err := Run(context.Background(), Options{
		BaseURL: srv.URL, Token: "t", In: in, Out: &out,
		StartDaemon: func() error { starts++; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if starts != 1 {
		t.Fatalf("StartDaemon called %d times", starts)
	}
	if len(outLines(&out)) != 1 {
		t.Fatalf("out = %q", out.String())
	}
}

func TestDaemonNeverComesUp(t *testing.T) {
	d, srv := newFakeDaemon(t, func(_ reqSeen, _ *http.Request) (int, string, http.Header) {
		t.Error("a request reached the daemon while it was down")
		return 500, "", nil
	})
	d.up.Store(false)
	var out bytes.Buffer
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":7,"method":"tools/list"}` + "\n" +
			`{"jsonrpc":"2.0","id":"abc-1","method":"tools/list"}` + "\n" +
			`{"jsonrpc":"2.0","method":"notifications/x"}` + "\n")
	err := Run(context.Background(), Options{
		BaseURL: srv.URL, Token: "t", In: in, Out: &out,
		StartDaemon: func() error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := outLines(&out)
	if len(lines) != 2 {
		t.Fatalf("out = %q", out.String())
	}
	for i, id := range []string{"7", `"abc-1"`} {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(lines[i]), &m); err != nil {
			t.Fatalf("line %d not JSON: %v", i, err)
		}
		if string(m["id"]) != id || !strings.Contains(lines[i], "-32000") ||
			!strings.Contains(lines[i], "mairc daemon unavailable") {
			t.Fatalf("line %d = %q, want -32000 echoing id %s", i, lines[i], id)
		}
	}
}

func TestDaemonRestartReinitializes(t *testing.T) {
	var calls atomic.Int32
	_, srv := newFakeDaemon(t, func(s reqSeen, _ *http.Request) (int, string, http.Header) {
		n := calls.Add(1)
		var m map[string]json.RawMessage
		_ = json.Unmarshal([]byte(s.body), &m)
		method := ""
		_ = json.Unmarshal(m["method"], &method)
		h := http.Header{}
		switch {
		case method == "initialize":
			h.Set("Mcp-Session-Id", "sess-"+strings.Repeat("x", int(n)))
			return http.StatusOK, `{"jsonrpc":"2.0","id":1,"result":{}}`, h
		case n == 2: // the request after initialize: the daemon "restarted", session unknown
			return http.StatusNotFound, "", nil
		default:
			return http.StatusOK, `{"jsonrpc":"2.0","id":9,"result":{"tools":["irc_who"]}}`, h
		}
	})
	var out bytes.Buffer
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n" +
			`{"jsonrpc":"2.0","id":9,"method":"tools/list"}` + "\n")
	err := runShim(Options{BaseURL: srv.URL, Token: "t", In: in, Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	// initialize, failed tools/list, re-initialize, retried tools/list.
	if calls.Load() != 4 {
		t.Fatalf("daemon saw %d posts, want 4", calls.Load())
	}
	lines := outLines(&out)
	if len(lines) != 2 || !strings.Contains(lines[1], "irc_who") {
		t.Fatalf("out = %v", lines)
	}
}

func TestBigLinePasses(t *testing.T) {
	_, srv := newFakeDaemon(t, func(s reqSeen, _ *http.Request) (int, string, http.Header) {
		return http.StatusOK, s.body, nil
	})
	var out bytes.Buffer
	big := `{"jsonrpc":"2.0","id":1,"method":"x","pad":"` + strings.Repeat("y", 1<<20-64) + `"}`
	err := runShim(Options{BaseURL: srv.URL, Token: "t", In: strings.NewReader(big + "\n"), Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != big {
		t.Fatalf("1 MiB line: got %d bytes", out.Len())
	}
	for _, l := range outLines(&out) {
		if !json.Valid([]byte(l)) {
			t.Fatal("non-JSON line written to Out")
		}
	}
}

func TestInvalidJSONForwarded(t *testing.T) {
	_, srv := newFakeDaemon(t, func(_ reqSeen, _ *http.Request) (int, string, http.Header) {
		return http.StatusOK, `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}`, nil
	})
	var out bytes.Buffer
	err := runShim(Options{BaseURL: srv.URL, Token: "t", In: strings.NewReader("not json\n"), Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "parse error") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestEOFReturnsNil(t *testing.T) {
	_, srv := newFakeDaemon(t, func(_ reqSeen, _ *http.Request) (int, string, http.Header) {
		return http.StatusOK, "{}", nil
	})
	var out bytes.Buffer
	if err := runShim(Options{BaseURL: srv.URL, Token: "t", In: strings.NewReader(""), Out: &out}); err != nil {
		t.Fatalf("EOF = %v", err)
	}
	// And a final line without a trailing newline is still forwarded.
	err := runShim(Options{BaseURL: srv.URL, Token: "t", In: strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`), Out: &out})
	if err != nil || !strings.Contains(out.String(), "{}") {
		t.Fatalf("no-newline line: err=%v out=%q", err, out.String())
	}
}
