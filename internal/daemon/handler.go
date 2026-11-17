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
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/horus14-wq/microagentIRC/internal/core"
	"github.com/horus14-wq/microagentIRC/internal/identity"
	"github.com/horus14-wq/microagentIRC/internal/mcp"
	"github.com/horus14-wq/microagentIRC/internal/names"
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
	// headerHookSession is the agent CLI's own session id, when the shim's environment has one.
	headerHookSession = "X-Mairc-Session"
	// headerWatch is "1" on an initialize whose shim is about to open a watch (GET /v1/watch).
	headerWatch = "X-Mairc-Watch"
)

const (
	// watchGrace is how long after such an initialize the session counts as live before its watch has arrived.
	watchGrace = 10 * time.Second
	// claimWindow is how long after an irc_name call a hook may claim the name that call gave.
	claimWindow = 60 * time.Second
	// bindingIdle is how long an unused hook-session binding is kept.
	bindingIdle = 7 * 24 * time.Hour
	// bindingSave is how often a binding in use has its last-used time written to the database.
	bindingSave = time.Hour
)

// session is one MCP session: what the tools see, the process id a shim reported, and when it was last used.
type session struct {
	core.Session
	pid         int
	hookSession string // the agent CLI's session id the shim reported, or ""
	lastUsed    time.Time
	// A session is known to be live while a shim watches it (serveWatch), and for watchGrace after an initialize
	// that announced a watch. Sessions of clients that never watch are not known to be live.
	watchers int
	grace    time.Time
}

// live reports whether the session's agent is known to be there now. Callers hold mu.
func (sess *session) live(now time.Time) bool {
	return sess.watchers > 0 || now.Before(sess.grace)
}

// pidKey is a shim's process id on one machine. The machine comes from the session's seat ("" for a client that
// sends no host), so a process id on one machine never matches a shim on another.
type pidKey struct {
	host string
	pid  int
}

// binding is what the daemon knows about one agent CLI session's hook: the name it runs for.
type binding struct {
	name     string
	lastUsed time.Time
	saved    time.Time // when it was last written to the database
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
	pids map[pidKey]map[string]int
	// bound maps an agent CLI session id to the name its hook runs for. It is learned (from a process id, a seat
	// or the result of irc_name), kept in memory for the hot path and stored through bindings.
	attach sync.Mutex // serializes newSession; taken before mu, never while holding it
	// stopping is closed when the daemon shuts down, to end the open watches.
	stopping chan struct{}
	stopOnce sync.Once
	bound    map[string]*binding
	// named is the irc_name call that last set or confirmed each name, for the claim window.
	named    map[string]namedBy
	bindings core.Bindings // nil: bindings live in memory only
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
		pids:     make(map[pidKey]map[string]int),
		bound:    make(map[string]*binding),
		named:    make(map[string]namedBy),
		stopping: make(chan struct{}),
	}
}

// useBindings gives the server its durable bindings and loads the stored ones, so a restarted daemon still knows
// which name each agent CLI session's hook runs for.
func (s *server) useBindings(ctx context.Context, b core.Bindings) error {
	stored, err := b.HookSessions(ctx)
	if err != nil {
		return err
	}
	now := s.clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindings = b
	for sessionID, name := range stored {
		s.bound[sessionID] = &binding{name: name, lastUsed: now, saved: now}
	}
	return nil
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
	case "/v1/watch":
		if method(w, r, http.MethodGet) {
			s.serveWatch(w, r)
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
		if sess.Seat != "" {
			w.Header().Set(headerSeat, sess.Seat) // the seat in force, which may be the session's own
		}
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
		s.rename(r.Context(), id, sess.Name)
	}
	if isNameCall(body) {
		s.noteNamed(id, sess.Name)
	}
	if out == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// serveWatch serves GET /v1/watch: a shim holds this request open for as long as it runs, which is how the daemon
// knows the session's agent is still there. Nothing is ever sent on it. When the last watch of a session ends (the
// shim exited, was killed, or lost its connection) the session is dropped at once: its name stops counting as in
// use and its process id stops resolving hooks. A shim that is still running initializes again on its next call.
func (s *server) serveWatch(w http.ResponseWriter, r *http.Request) {
	id := r.Header.Get(headerSession)
	if id == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	sess := s.sessions[id]
	if sess != nil {
		sess.watchers++
	}
	s.mu.Unlock()
	if sess == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// This one response has no end, so the server's read and write limits must not apply to it.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_ = rc.Flush()
	select {
	case <-r.Context().Done():
	case <-s.stopping:
	}
	s.mu.Lock()
	sess.watchers--
	if sess.watchers == 0 && s.sessions[id] == sess {
		s.index(sess, -1)
		delete(s.sessions, id)
	}
	s.mu.Unlock()
}

// stop ends every open watch, so that a shutdown does not wait for them. It may be called more than once.
func (s *server) stop() {
	s.stopOnce.Do(func() { close(s.stopping) })
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

// isNameCall reports whether the body is a tools/call of irc_name.
func isNameCall(body []byte) bool {
	var msg struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	return json.Unmarshal(body, &msg) == nil && msg.Method == "tools/call" && msg.Params.Name == "irc_name"
}

// newSession creates a session from the shim's headers and returns its id and a copy of what the tools see.
func (s *server) newSession(r *http.Request) (string, core.Session, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", core.Session{}, err
	}
	id := hex.EncodeToString(raw[:])
	now := s.clock.Now()
	sess := &session{Session: core.Session{Seat: r.Header.Get(headerSeat)}, lastUsed: now}
	if pid, err := strconv.Atoi(r.Header.Get(headerPid)); err == nil && pid > 0 {
		sess.pid = pid
	}
	if hookSession := r.Header.Get(headerHookSession); identity.ValidSession(hookSession) {
		sess.hookSession = hookSession
	}
	if r.Header.Get(headerWatch) == "1" {
		sess.grace = now.Add(watchGrace)
	}
	// One attach at a time: two agents arriving together from one seat must not both be given its name.
	s.attach.Lock()
	defer s.attach.Unlock()
	if sess.Seat != "" {
		name, err := s.nameForSeat(r.Context(), sess.Seat)
		if err != nil {
			return "", core.Session{}, err
		}
		s.mu.Lock()
		taken := false
		for _, other := range s.sessions {
			if other.live(now) && (other.Seat == sess.Seat || (name != "" && other.Name == name)) {
				taken = true
				break
			}
		}
		s.mu.Unlock()
		if taken {
			// A session known to be live is attached from this seat (it may not have a name yet: agent CLIs
			// started together attach before any of them calls irc_name) or is using the seat's name, so this is
			// a second agent in the same place.
			// It gets a seat of its own and starts without a name; irc_name then gives it one that is remembered
			// for that seat. The shim is told the seat and sends it again after a daemon restart.
			sess.Seat = identity.OwnSeat(sess.Seat, id)
			name = ""
		}
		sess.Name = name
	}
	s.mu.Lock()
	s.sessions[id] = sess
	s.index(sess, 1)
	s.mu.Unlock()
	if sess.Name != "" && sess.hookSession != "" {
		// Only while the session is bound to nobody (see rename).
		s.mu.Lock()
		free := s.bound[sess.hookSession] == nil
		s.mu.Unlock()
		if free {
			s.bind(r.Context(), sess.hookSession, sess.Name, now)
		}
	}
	return id, sess.Session, nil
}

// nameForSeat returns the name remembered for the seat. A host-qualified seat with no name also tries the form the
// same place had before hosts were added, and moves a name found there to the new form, so a name remembered by an
// older version is not lost.
func (s *server) nameForSeat(ctx context.Context, seat string) (string, error) {
	name, err := s.chat.NameForSeat(ctx, seat)
	if err != nil || name != "" {
		return name, err
	}
	legacy := identity.Legacy(seat)
	if legacy == seat {
		return "", nil
	}
	if name, err = s.chat.NameForSeat(ctx, legacy); err != nil || name == "" {
		return "", err
	}
	s.mu.Lock()
	bindings := s.bindings
	s.mu.Unlock()
	if bindings != nil {
		if err := bindings.MoveSeat(ctx, name, legacy, seat); err != nil {
			s.logf("error: move seat: %v", err)
		}
	}
	return name, nil
}

// rename records a session's new name (a tool changed it) and keeps the pid index and the hook-session bindings in
// step: the agent CLI session that ran for the old name now runs for the new one.
func (s *server) rename(ctx context.Context, id, name string) {
	now := s.clock.Now()
	s.mu.Lock()
	sess := s.sessions[id]
	if sess == nil || sess.Name == name {
		s.mu.Unlock()
		return
	}
	old := sess.Name
	s.index(sess, -1)
	sess.Name = name
	s.index(sess, 1)
	// The shim's own report of its agent CLI session is used only while that session is bound to nobody: a CLI
	// started from inside another CLI inherits the outer one's session id in its environment, and must not take
	// the outer agent's hook with it.
	moved := make([]string, 0, 1)
	if sess.hookSession != "" && s.bound[sess.hookSession] == nil {
		moved = append(moved, sess.hookSession)
	}
	// The agent CLI sessions that ran for the old name follow the rename, unless another live session still has
	// the old name: two agents in one seat start with the same remembered name, and when the second takes its
	// own name the first one's hook must stay with the first.
	if old != "" && !s.nameInUse(old) {
		for hookSession, b := range s.bound {
			if b.name == old {
				moved = append(moved, hookSession)
			}
		}
	}
	s.mu.Unlock()
	if name == "" {
		return
	}
	for _, hookSession := range moved {
		s.bind(ctx, hookSession, name, now)
	}
}

// nameInUse reports whether a live session has the name. Callers hold mu.
func (s *server) nameInUse(name string) bool {
	for _, sess := range s.sessions {
		if sess.Name == name {
			return true
		}
	}
	return false
}

// index adds delta to the count of sessions with this session's machine, pid and name. Callers hold mu.
func (s *server) index(sess *session, delta int) {
	if sess.pid <= 0 || sess.Name == "" {
		return
	}
	key := pidKey{host: identity.HostOf(sess.Seat), pid: sess.pid}
	names := s.pids[key]
	if names == nil {
		names = make(map[string]int)
		s.pids[key] = names
	}
	names[sess.Name] += delta
	if names[sess.Name] <= 0 {
		delete(names, sess.Name)
	}
	if len(names) == 0 {
		delete(s.pids, key)
	}
}

// namedBy is one irc_name call that set or confirmed a name: when, and which agent made it (the machine and process
// id its shim reported, and the seat its hook sends).
type namedBy struct {
	at   time.Time
	host string
	pid  int
	seat string
}

// noteNamed records that the session's irc_name call just set or confirmed the name: the start of its claim
// window, which only that agent's hook may use (claim).
func (s *server) noteNamed(id, name string) {
	if name == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[id]
	if sess == nil {
		return
	}
	s.named[name] = namedBy{
		at: s.clock.Now(), host: identity.HostOf(sess.Seat), pid: sess.pid, seat: identity.SharedSeat(sess.Seat),
	}
}

// bind records that the agent CLI session's hook runs for the name, in memory and, when the binding is new or
// changed or was last written more than bindingSave ago, in the database. A failed write is logged and the
// binding still holds until the daemon stops.
func (s *server) bind(ctx context.Context, hookSession, name string, now time.Time) {
	s.mu.Lock()
	b := s.bound[hookSession]
	if b != nil && b.name == name && now.Sub(b.saved) < bindingSave {
		b.lastUsed = now
		s.mu.Unlock()
		return
	}
	s.bound[hookSession] = &binding{name: name, lastUsed: now, saved: now}
	bindings := s.bindings
	s.mu.Unlock()
	if bindings == nil {
		return
	}
	if err := bindings.BindHookSession(ctx, hookSession, name, now); err != nil {
		s.logf("error: bind hook session: %v", err)
	}
}

// claim reports whether a hook may take the name it read from an irc_name result: irc_name set or confirmed that
// exact name within claimWindow. The result is the agent's own, read by its own hook, so a fresh claim also moves a
// name that an earlier session of the same agent held.
//
// The claim must come from the agent that made the call. The agent CLI's process is an ancestor of both its shim
// and its hook, so the hook's process chain must hold the process id the calling session's shim reported, on the
// same machine. When either side has no process id, the hook must be in the caller's seat instead. Without this,
// any session could take a name, and its private messages, in the minute after its owner confirmed it.
func (s *server) claim(req hookRequest, now time.Time) bool {
	name := req.Claimed
	if !names.Valid(name) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	by, ok := s.named[name]
	if !ok || now.Sub(by.at) > claimWindow || now.Before(by.at) {
		return false
	}
	if by.pid > 0 && len(req.Pids) > 0 {
		if identity.HostOf(req.Seat) != by.host || !slices.Contains(req.Pids, by.pid) {
			return false
		}
	} else if req.Seat == "" || req.Seat != by.seat {
		return false
	}
	for hookSession, b := range s.bound {
		if b.name == name {
			delete(s.bound, hookSession) // one agent CLI session per name; the claimant is bound by the caller
		}
	}
	return true
}

// dropIdle removes sessions unused for more than sessionIdle, hook-session bindings unused for more than
// bindingIdle (in memory and in the database) and claim windows that have closed.
func (s *server) dropIdle(ctx context.Context, now time.Time) {
	s.mu.Lock()
	for id, sess := range s.sessions {
		if now.Sub(sess.lastUsed) > sessionIdle {
			s.index(sess, -1)
			delete(s.sessions, id)
		}
	}
	for hookSession, b := range s.bound {
		if now.Sub(b.lastUsed) > bindingIdle {
			delete(s.bound, hookSession)
		}
	}
	for name, by := range s.named {
		if now.Sub(by.at) > claimWindow {
			delete(s.named, name)
		}
	}
	bindings := s.bindings
	s.mu.Unlock()
	if bindings == nil {
		return
	}
	if _, err := bindings.PruneHookSessions(ctx, now.Add(-bindingIdle)); err != nil {
		s.logf("error: prune hook sessions: %v", err)
	}
}

// hookRequest is the body of POST /v1/hook. Every field is optional.
type hookRequest struct {
	Pids    []int  `json:"pids"`
	Seat    string `json:"seat"`
	Name    string `json:"name"`
	Event   string `json:"event"`
	Session string `json:"session"` // the agent CLI's session id
	Claimed string `json:"claimed"` // the name read from the irc_name result this event follows
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
	now := s.clock.Now()
	hookSession := req.Session
	if !identity.ValidSession(hookSession) {
		hookSession = ""
	}
	if hookSession != "" {
		// A fresh irc_name result comes first: it also covers an agent that just changed its name.
		if req.Claimed != "" && s.claim(req, now) {
			s.bind(ctx, hookSession, req.Claimed, now)
			return req.Claimed, nil
		}
		if name := s.boundName(ctx, hookSession, now); name != "" {
			return name, nil
		}
	}
	name := s.nameForPids(identity.HostOf(req.Seat), req.Pids)
	if name == "" && req.Seat != "" {
		live, several := s.liveNameForSeat(req.Seat)
		switch {
		case several:
			// Agents with different names share this seat: like a shared process id, it is ambiguous and skipped.
		case live != "":
			name = live
		default:
			var err error
			if name, err = s.nameForSeat(ctx, req.Seat); err != nil {
				return "", err
			}
		}
	}
	if name != "" && hookSession != "" {
		// Learned: from now on this agent CLI session resolves by its id, whatever its process chain or seat.
		s.bind(ctx, hookSession, name, now)
	}
	return name, nil
}

// liveNameForSeat looks at the live sessions attached from the seat (a hook sends the seat its shim sent, before
// the daemon made it the session's own). It returns their name when they all have the same one, and several =
// true when they have different names.
func (s *server) liveNameForSeat(seat string) (name string, several bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		if sess.Name == "" || identity.SharedSeat(sess.Seat) != seat {
			continue
		}
		if name != "" && name != sess.Name {
			return "", true
		}
		name = sess.Name
	}
	return name, false
}

// boundName returns the name the agent CLI session is bound to, or "". It runs no SQL except to refresh the stored
// last-used time, at most once per bindingSave.
func (s *server) boundName(ctx context.Context, hookSession string, now time.Time) string {
	s.mu.Lock()
	b := s.bound[hookSession]
	if b == nil {
		s.mu.Unlock()
		return ""
	}
	name := b.name
	b.lastUsed = now
	stale := now.Sub(b.saved) >= bindingSave
	s.mu.Unlock()
	if stale {
		s.bind(ctx, hookSession, name, now)
	}
	return name
}

// nameForPids returns the name of the first pid on the machine, in the order given, that live sessions of exactly
// one name reported. A pid shared by sessions with different names is ambiguous and skipped.
func (s *server) nameForPids(host string, pids []int) string {
	if len(pids) == 0 {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, pid := range pids {
		names := s.pids[pidKey{host: host, pid: pid}]
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
