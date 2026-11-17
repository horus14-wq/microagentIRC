package daemon

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/horus14-wq/microagentIRC/internal/core"
	"github.com/horus14-wq/microagentIRC/internal/mcp"
)

// maxBody is the largest request body any endpoint reads.
const maxBody = 64 << 10

// sessionIdle is how long an MCP session may go unused before the retention ticker drops it.
const sessionIdle = 24 * time.Hour

// joinLine is the hook's answer to an unnamed agent on a session-start event.
const joinLine = "irc: call irc_name to join"

// sessionStart is the hook event that earns an unnamed agent the join line.
const sessionStart = "SessionStart"

// The HTTP header names of the protocol.
const (
	headerSession = "Mcp-Session-Id"
	headerSeat    = "X-Mairc-Seat"
	headerPid     = "X-Mairc-Pid"
)

// session is one MCP session: what the tools see, the process id a shim reported, and when it was last used.
type session struct {
	core.Session
	pid      int
	lastUsed time.Time
}

// server is the HTTP handler. Its mutable state (sessions and the pid index) sits behind one mutex.
type server struct {
	chat    core.Chat
	mcp     *mcp.Server
	auth    []byte // "Bearer <token>"; nil refuses every protected request
	version string
	reload  func() error
	clock   core.Clock
	logf    func(format string, args ...any)
	start   time.Time

	mu       sync.Mutex
	sessions map[string]*session
	// pids maps a shim's process id to the names of the live sessions that reported it, each with how many
	// sessions carry it. One name means the pid resolves; more than one means it is ambiguous.
	pids map[int]map[string]int
}

// Handler returns the HTTP handler alone, for tests (httptest) and for the end-to-end test.
func Handler(chat core.Chat, mcpServer *mcp.Server, token, version string, reload func() error,
	clock core.Clock,
) http.Handler {
	return newServer(chat, mcpServer, token, version, reload, clock, func(string, ...any) {})
}

func newServer(chat core.Chat, mcpServer *mcp.Server, token, version string, reload func() error,
	clock core.Clock, logf func(format string, args ...any),
) *server {
	var auth []byte
	if token != "" {
		auth = []byte("Bearer " + token)
	}
	return &server{
		chat:     chat,
		mcp:      mcpServer,
		auth:     auth,
		version:  version,
		reload:   reload,
		clock:    clock,
		logf:     logf,
		start:    clock.Now(),
		sessions: make(map[string]*session),
		pids:     make(map[int]map[string]int),
	}
}

// ServeHTTP routes a request. Every path but /healthz needs the bearer token.
func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		if !method(w, r, http.MethodGet) {
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok")
		return
	}
	if s.auth == nil || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), s.auth) != 1 {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch r.URL.Path {
	case "/mcp":
		s.serveMCP(w, r)
	case "/v1/hook":
		if method(w, r, http.MethodPost) {
			s.serveHook(w, r)
		}
	case "/v1/reload":
		if method(w, r, http.MethodPost) {
			s.serveReload(w)
		}
	case "/v1/status":
		if method(w, r, http.MethodGet) {
			s.serveStatus(w, r)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// method answers 405 and returns false unless the request uses want.
func method(w http.ResponseWriter, r *http.Request, want string) bool {
	if r.Method == want {
		return true
	}
	w.Header().Set("Allow", want)
	w.WriteHeader(http.StatusMethodNotAllowed)
	return false
}

// readBody reads at most maxBody bytes. Larger bodies answer 413; other read errors answer 400.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		} else {
			w.WriteHeader(http.StatusBadRequest)
		}
		return nil, false
	}
	return body, true
}

// serveMCP is MCP Streamable HTTP with JSON responses only.
func (s *server) serveMCP(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var id string
	var sess core.Session
	if isInitialize(body) {
		var err error
		if id, sess, err = s.newSession(r); err != nil {
			s.logf("error: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set(headerSession, id)
	} else {
		id = r.Header.Get(headerSession)
		if id == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		known := s.sessions[id]
		if known != nil {
			known.lastUsed = s.clock.Now()
			sess = known.Session
		}
		s.mu.Unlock()
		if known == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
	}
	before := sess.Name
	out := s.mcp.Handle(r.Context(), &sess, body)
	if sess.Name != before {
		s.rename(id, sess.Name)
	}
	if out == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// isInitialize reports whether the body is an initialize request (a method and an id).
func isInitialize(body []byte) bool {
	var msg struct {
		Method string          `json:"method"`
		ID     json.RawMessage `json:"id"`
	}
	if json.Unmarshal(body, &msg) != nil || msg.Method != "initialize" {
		return false
	}
	return len(msg.ID) > 0 && string(msg.ID) != "null"
}

// newSession creates a session from the shim's headers and returns its id and a copy of what the tools see.
func (s *server) newSession(r *http.Request) (string, core.Session, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", core.Session{}, err
	}
	id := hex.EncodeToString(raw[:])
	sess := &session{Session: core.Session{Seat: r.Header.Get(headerSeat)}, lastUsed: s.clock.Now()}
	if pid, err := strconv.Atoi(r.Header.Get(headerPid)); err == nil && pid > 0 {
		sess.pid = pid
	}
	if sess.Seat != "" {
		name, err := s.chat.NameForSeat(r.Context(), sess.Seat)
		if err != nil {
			return "", core.Session{}, err
		}
		sess.Name = name
	}
	s.mu.Lock()
	s.sessions[id] = sess
	s.index(sess.pid, sess.Name, 1)
	s.mu.Unlock()
	return id, sess.Session, nil
}

// rename records a session's new name (a tool changed it) and keeps the pid index in step.
func (s *server) rename(id, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[id]
	if sess == nil || sess.Name == name {
		return
	}
	s.index(sess.pid, sess.Name, -1)
	sess.Name = name
	s.index(sess.pid, sess.Name, 1)
}

// index adds delta sessions with this pid and name to the pid index. Callers hold mu.
func (s *server) index(pid int, name string, delta int) {
	if pid <= 0 || name == "" {
		return
	}
	names := s.pids[pid]
	if names == nil {
		names = make(map[string]int)
		s.pids[pid] = names
	}
	names[name] += delta
	if names[name] <= 0 {
		delete(names, name)
	}
	if len(names) == 0 {
		delete(s.pids, pid)
	}
}

// dropIdle removes sessions unused for more than sessionIdle.
func (s *server) dropIdle(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sess := range s.sessions {
		if now.Sub(sess.lastUsed) > sessionIdle {
			s.index(sess.pid, sess.Name, -1)
			delete(s.sessions, id)
		}
	}
}

// hookRequest is the body of POST /v1/hook. Every field is optional.
type hookRequest struct {
	Pids  []int  `json:"pids"`
	Seat  string `json:"seat"`
	Name  string `json:"name"`
	Event string `json:"event"`
}

// serveHook answers whether anything is due for the calling agent. It never fails loudly: errors answer 204.
func (s *server) serveHook(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var req hookRequest
	if len(body) > 0 && json.Unmarshal(body, &req) != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	name, err := s.resolve(r.Context(), req)
	if err != nil {
		s.logf("error: hook: %v", err)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if name == "" {
		if req.Event != sessionStart {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, joinLine)
		return
	}
	msgs, err := s.chat.HookEvent(r.Context(), name)
	if err != nil {
		s.logf("error: hook: %v", err)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(msgs) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var b strings.Builder
	for i, m := range msgs {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(m.Line())
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, b.String())
}

// resolve names the hook's agent: the explicit name, else the first pid that one named session owns, else the
// name remembered for the seat. "" means unnamed.
func (s *server) resolve(ctx context.Context, req hookRequest) (string, error) {
	if req.Name != "" {
		return req.Name, nil
	}
	if name := s.nameForPids(req.Pids); name != "" {
		return name, nil
	}
	if req.Seat == "" {
		return "", nil
	}
	return s.chat.NameForSeat(ctx, req.Seat)
}

// nameForPids returns the name of the first pid, in the order given, that live sessions of exactly one name
// reported. A pid shared by sessions with different names is ambiguous and skipped.
func (s *server) nameForPids(pids []int) string {
	if len(pids) == 0 {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, pid := range pids {
		names := s.pids[pid]
		if len(names) != 1 {
			continue
		}
		for name := range names {
			return name
		}
	}
	return ""
}

// serveReload re-reads the settings file through the reload function.
func (s *server) serveReload(w http.ResponseWriter) {
	if err := s.reload(); err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// status is the body of GET /v1/status.
type status struct {
	Version  string `json:"version"`
	UptimeS  int64  `json:"uptime_s"`
	Agents   int    `json:"agents"`
	Sessions int    `json:"sessions"`
}

func (s *server) serveStatus(w http.ResponseWriter, r *http.Request) {
	names, err := s.chat.Who(r.Context())
	if err != nil {
		s.logf("error: status: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	sessions := len(s.sessions)
	s.mu.Unlock()
	out, err := json.Marshal(status{
		Version:  s.version,
		UptimeS:  int64(s.clock.Now().Sub(s.start) / time.Second),
		Agents:   len(names),
		Sessions: sessions,
	})
	if err != nil {
		s.logf("error: status: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}
