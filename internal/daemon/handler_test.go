package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/horus14-wq/microagentIRC/internal/chat"
	"github.com/horus14-wq/microagentIRC/internal/config"
	"github.com/horus14-wq/microagentIRC/internal/core"
	"github.com/horus14-wq/microagentIRC/internal/identity"
	"github.com/horus14-wq/microagentIRC/internal/mcp"
	"github.com/horus14-wq/microagentIRC/internal/store"
	"github.com/horus14-wq/microagentIRC/internal/tools"
)

const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// fixture is a daemon over a real store in a temporary home: h is the public Handler for black-box tests, srv an
// internal server (with a log capture) for tests that look inside. The two share the chat and the store, not the
// sessions.
type fixture struct {
	t     testing.TB
	paths config.Paths
	clock *fakeClock
	store *store.Store
	chat  *chat.Service
	srv   *server
	h     http.Handler
	logs  *[]string
}

// testSettings deliver room lines on every hook event, so one call shows both kinds.
func testSettings() core.Settings {
	s := core.DefaultSettings()
	s.CheckRoomEvery = 1
	return s
}

func newFixture(t testing.TB, settings core.Settings) *fixture {
	t.Helper()
	paths, err := config.ResolvePaths(func(key string) string {
		if key == "MAIRC_HOME" {
			return t.TempDir()
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.Context(), paths.DB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clk := newClock()
	next := 0
	service := chat.New(t.Context(), st, clk, settings, func(n int) int { next++; return next % n })
	var logs []string
	var mu sync.Mutex
	logf := func(format string, _ ...any) {
		mu.Lock()
		logs = append(logs, format)
		mu.Unlock()
	}
	reload := reloader(paths.Config, service, &current{settings: settings}, logf)
	srv := newServer(service, mcp.New(serverName, "test", tools.All(service)), testToken, "test", reload, clk, logf)
	return &fixture{
		t: t, paths: paths, clock: clk, store: st, chat: service, srv: srv,
		h: Handler(service, mcp.New(serverName, "test", tools.All(service)), testToken, "test", reload, clk), logs: &logs,
	}
}

// do sends one request with the token unless headers set Authorization themselves.
func do(h http.Handler, method, path string, headers map[string]string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
			continue
		}
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`

// initialize opens an MCP session with optional shim headers and returns its id.
func initialize(t testing.TB, h http.Handler, seat string, pid int) string {
	t.Helper()
	headers := map[string]string{}
	if seat != "" {
		headers[headerSeat] = seat
	}
	if pid != 0 {
		headers[headerPid] = strconv.Itoa(pid)
	}
	rec := do(h, http.MethodPost, "/mcp", headers, initializeBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("initialize: %d %q", rec.Code, rec.Body.String())
	}
	id := rec.Header().Get(headerSession)
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id) {
		t.Fatalf("session id %q", id)
	}
	return id
}

// call runs one tool in a session and returns its text.
func call(t testing.TB, h http.Handler, id, tool, args string) string {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"` + tool + `","arguments":` + args + `}}`
	rec := do(h, http.MethodPost, "/mcp", map[string]string{headerSession: id}, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: %d %q", tool, rec.Code, rec.Body.String())
	}
	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || len(resp.Result.Content) != 1 {
		t.Fatalf("%s: bad response %q", tool, rec.Body.String())
	}
	return resp.Result.Content[0].Text
}

func hook(h http.Handler, body string) *httptest.ResponseRecorder {
	return do(h, http.MethodPost, "/v1/hook", nil, body)
}

func TestAuth(t *testing.T) {
	f := newFixture(t, testSettings())
	endpoints := []struct{ method, path string }{
		{http.MethodPost, "/mcp"},
		{http.MethodGet, "/mcp"},
		{http.MethodPost, "/v1/hook"},
		{http.MethodPost, "/v1/reload"},
		{http.MethodGet, "/v1/status"},
	}
	tokens := []struct{ name, header string }{
		{"missing", ""},
		{"wrong", "Bearer " + strings.Repeat("0", 64)},
		{"not bearer", testToken},
		{"empty bearer", "Bearer "},
	}
	for _, e := range endpoints {
		for _, tok := range tokens {
			t.Run(e.method+" "+e.path+" "+tok.name, func(t *testing.T) {
				rec := do(f.h, e.method, e.path, map[string]string{"Authorization": tok.header}, initializeBody)
				if rec.Code != http.StatusUnauthorized || rec.Body.Len() != 0 {
					t.Fatalf("got %d %q, want 401 and an empty body", rec.Code, rec.Body.String())
				}
			})
		}
	}
	t.Run("healthz needs no token", func(t *testing.T) {
		rec := do(f.h, http.MethodGet, "/healthz", map[string]string{"Authorization": ""}, "")
		if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
			t.Fatalf("got %d %q, want 200 ok", rec.Code, rec.Body.String())
		}
	})
}

func TestMCP(t *testing.T) {
	f := newFixture(t, testSettings())
	id := initialize(t, f.h, "", 0)
	list := `{"jsonrpc":"2.0","id":7,"method":"tools/list"}`
	cases := []struct {
		name     string
		method   string
		session  string
		body     string
		wantCode int
		wantBody string // exact; "" with wantList false means empty
		wantList bool
	}{
		{"tools/list with the session", http.MethodPost, id, list, http.StatusOK, "", true},
		{"unknown session", http.MethodPost, strings.Repeat("f", 32), list, http.StatusNotFound, "", false},
		{"missing session", http.MethodPost, "", list, http.StatusBadRequest, "", false},
		{"notification", http.MethodPost, id, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, http.StatusAccepted, "", false},
		{"GET", http.MethodGet, id, "", http.StatusMethodNotAllowed, "", false},
		{"DELETE", http.MethodDelete, id, "", http.StatusMethodNotAllowed, "", false},
		{"65 KiB body", http.MethodPost, id, `{"x":"` + strings.Repeat("a", 65<<10) + `"}`, http.StatusRequestEntityTooLarge, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(f.h, tc.method, "/mcp", map[string]string{headerSession: tc.session}, tc.body)
			if rec.Code != tc.wantCode {
				t.Fatalf("code %d, want %d (%q)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantList {
				srv := mcp.New(serverName, "test", tools.All(f.chat))
				want := `{"jsonrpc":"2.0","id":7,"result":` + string(srv.ToolListBytes()) + `}`
				if rec.Body.String() != want {
					t.Fatalf("body %q, want %q", rec.Body.String(), want)
				}
				if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
					t.Fatalf("content type %q", ct)
				}
				return
			}
			if rec.Body.String() != tc.wantBody {
				t.Fatalf("body %q, want %q", rec.Body.String(), tc.wantBody)
			}
		})
	}
	t.Run("initialize answers json with a session id", func(t *testing.T) {
		rec := do(f.h, http.MethodPost, "/mcp", nil, initializeBody)
		want := `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},` +
			`"serverInfo":{"name":"mairc","version":"test"}}}`
		if rec.Code != http.StatusOK || rec.Body.String() != want || rec.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("got %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
		}
		if got := rec.Header().Get(headerSession); got == "" || got == id {
			t.Fatalf("session id %q", got)
		}
	})
}

func TestHookDelivery(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"by pid", `{"pids":[999,4242],"event":"PostToolUse"}`},
		{"by seat", `{"pids":[999],"seat":"tmux:%2","event":"PostToolUse"}`},
		{"by name", `{"name":"bob","event":"PostToolUse"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, testSettings())
			a := initialize(t, f.h, "tmux:%1", 4141)
			b := initialize(t, f.h, "tmux:%2", 4242)
			if got := call(t, f.h, a, "irc_name", `{"name":"alice"}`); got != "you are alice" {
				t.Fatal(got)
			}
			if got := call(t, f.h, b, "irc_name", `{"name":"bob"}`); got != "you are bob" {
				t.Fatal(got)
			}
			// B's first hook loads its state; nothing is due yet.
			if rec := hook(f.h, tc.body); rec.Code != http.StatusNoContent {
				t.Fatalf("before: %d %q", rec.Code, rec.Body.String())
			}
			if got := call(t, f.h, a, "irc_send", `{"text":"hello all"}`); got != "sent" {
				t.Fatal(got)
			}
			if got := call(t, f.h, a, "irc_send", `{"text":"psst","to":"bob"}`); got != "sent" {
				t.Fatal(got)
			}
			rec := hook(f.h, tc.body)
			if rec.Code != http.StatusOK || rec.Body.String() != "pm alice: psst\nroom alice: hello all" {
				t.Fatalf("got %d %q", rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
				t.Fatalf("content type %q", ct)
			}
			if rec := hook(f.h, tc.body); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
				t.Fatalf("again: %d %q", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHookResolution(t *testing.T) {
	f := newFixture(t, testSettings())
	// Two agents started by one parent process (pid 7) are ambiguous; each also has a pid of its own.
	x := initialize(t, f.srv, "seat-x", 7)
	y := initialize(t, f.srv, "seat-y", 7)
	z := initialize(t, f.srv, "seat-z", 8)
	initialize(t, f.srv, "", 9)
	for id, name := range map[string]string{x: "xavier", y: "yvonne", z: "zelda"} {
		if got := call(t, f.srv, id, "irc_name", `{"name":"`+name+`"}`); got != "you are "+name {
			t.Fatal(got)
		}
	}
	cases := []struct {
		name string
		req  hookRequest
		want string
	}{
		{"explicit name wins", hookRequest{Name: "nora", Pids: []int{8}, Seat: "seat-x"}, "nora"},
		{"first known pid", hookRequest{Pids: []int{1000, 8}, Seat: "seat-x"}, "zelda"},
		{"ambiguous pid falls through to the next pid", hookRequest{Pids: []int{7, 8}}, "zelda"},
		{"ambiguous pid falls through to the seat", hookRequest{Pids: []int{7}, Seat: "seat-y"}, "yvonne"},
		{"unnamed session pid falls through to the seat", hookRequest{Pids: []int{9}, Seat: "seat-x"}, "xavier"},
		{"nothing known", hookRequest{Pids: []int{7, 9}, Seat: "seat-none"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := f.srv.resolve(t.Context(), tc.req)
			if err != nil || got != tc.want {
				t.Fatalf("got %q %v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestHookUnnamed(t *testing.T) {
	f := newFixture(t, testSettings())
	cases := []struct {
		name     string
		body     string
		wantCode int
		wantBody string
	}{
		{"no body", ``, http.StatusNoContent, ""},
		{"empty object", `{}`, http.StatusNoContent, ""},
		{"other event", `{"pids":[5],"seat":"s","event":"PostToolUse"}`, http.StatusNoContent, ""},
		{"session start", `{"pids":[5],"seat":"s","event":"SessionStart"}`, http.StatusOK, "irc: call irc_name to join"},
		{"session start again", `{"event":"SessionStart"}`, http.StatusOK, "irc: call irc_name to join"},
		{"bad json", `{"pids":"x"`, http.StatusNoContent, ""},
		{"65 KiB body", `{"seat":"` + strings.Repeat("a", 65<<10) + `"}`, http.StatusRequestEntityTooLarge, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := hook(f.h, tc.body)
			if rec.Code != tc.wantCode || rec.Body.String() != tc.wantBody {
				t.Fatalf("got %d %q, want %d %q", rec.Code, rec.Body.String(), tc.wantCode, tc.wantBody)
			}
		})
	}
}

// TestHookEmptyTouchesNoDatabase closes the store under a warmed-up agent: the empty path still answers 204 and
// logs nothing, so it never reached the database.
func TestHookEmptyTouchesNoDatabase(t *testing.T) {
	f := newFixture(t, testSettings())
	b := initialize(t, f.srv, "seat-b", 4242)
	if got := call(t, f.srv, b, "irc_name", `{"name":"bob"}`); got != "you are bob" {
		t.Fatal(got)
	}
	for _, body := range []string{`{"name":"bob"}`, `{"pids":[4242]}`} {
		if rec := hook(f.srv, body); rec.Code != http.StatusNoContent {
			t.Fatalf("warm-up: %d", rec.Code)
		}
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"name":"bob"}`, `{"pids":[4242]}`, `{"pids":[1]}`, `{"event":"SessionStart"}`} {
		rec := hook(f.srv, body)
		if rec.Code == http.StatusOK && rec.Body.String() == joinLine {
			continue
		}
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s: %d %q", body, rec.Code, rec.Body.String())
		}
	}
	if len(*f.logs) != 0 {
		t.Fatalf("logged %q: the empty path touched the database", *f.logs)
	}
	// Control: a lookup that needs the database fails and is logged, still silently.
	// (A seat no live session is attached from: one that is resolves from memory since bead mairc-648.)
	if rec := hook(f.srv, `{"seat":"seat-elsewhere"}`); rec.Code != http.StatusNoContent || len(*f.logs) != 1 {
		t.Fatalf("control: %d, logs %q", rec.Code, *f.logs)
	}
}

func TestSeatRemembersName(t *testing.T) {
	f := newFixture(t, testSettings())
	first := initialize(t, f.srv, "tmux:%5", 0)
	if got := call(t, f.srv, first, "irc_name", `{"name":"carol"}`); got != "you are carol" {
		t.Fatal(got)
	}
	cases := []struct {
		name, seat string
		pid        int
		wantName   string
	}{
		{"same seat", "tmux:%5", 5555, "carol"},
		{"other seat", "tmux:%6", 6666, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := initialize(t, f.srv, tc.seat, tc.pid)
			f.srv.mu.Lock()
			got := f.srv.sessions[id].Name
			f.srv.mu.Unlock()
			if got != tc.wantName {
				t.Fatalf("Session.Name %q, want %q", got, tc.wantName)
			}
			// The pid index learned the name at initialize, without a tool call.
			if name := f.srv.nameForPids(identity.HostOf(tc.seat), []int{tc.pid}); name != tc.wantName {
				t.Fatalf("pid resolves to %q, want %q", name, tc.wantName)
			}
		})
	}
}

func TestReload(t *testing.T) {
	f := newFixture(t, testSettings())
	id := initialize(t, f.h, "", 0)
	if got := call(t, f.h, id, "irc_name", `{"name":"dave"}`); got != "you are dave" {
		t.Fatal(got)
	}
	steps := []struct {
		name     string
		file     string
		wantCode int
		wantBody string
	}{
		{"valid file applies", `{"max_public_len": 5, "public_cooldown_s": 0}`, http.StatusNoContent, ""},
		{"invalid file keeps the old value", `{"max_public_len": 0}`, http.StatusBadRequest, "max_public_len: must be between 1 and 999"},
		{"unknown key keeps the old value", `{"max_public_lenx": 9}`, http.StatusBadRequest, "unknown setting: max_public_lenx"},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			if err := os.WriteFile(f.paths.Config, []byte(st.file), 0o600); err != nil {
				t.Fatal(err)
			}
			rec := do(f.h, http.MethodPost, "/v1/reload", nil, "")
			if rec.Code != st.wantCode || rec.Body.String() != st.wantBody {
				t.Fatalf("got %d %q, want %d %q", rec.Code, rec.Body.String(), st.wantCode, st.wantBody)
			}
			if got := call(t, f.h, id, "irc_send", `{"text":"sixsix"}`); got != "too long 6/5" {
				t.Fatalf("send: %q", got)
			}
			if got := call(t, f.h, id, "irc_send", `{"text":"five5"}`); got != "sent" {
				t.Fatalf("send: %q", got)
			}
		})
	}
}

func TestStatus(t *testing.T) {
	f := newFixture(t, testSettings())
	a := initialize(t, f.h, "", 0)
	initialize(t, f.h, "", 0)
	if got := call(t, f.h, a, "irc_name", `{"name":"erin"}`); got != "you are erin" {
		t.Fatal(got)
	}
	f.clock.add(90*time.Second + 400*time.Millisecond)
	cases := []struct {
		name, method string
		wantCode     int
		wantBody     string
	}{
		{"GET", http.MethodGet, http.StatusOK, `{"version":"test","uptime_s":90,"agents":1,"sessions":2}`},
		{"POST", http.MethodPost, http.StatusMethodNotAllowed, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(f.h, tc.method, "/v1/status", nil, "")
			if rec.Code != tc.wantCode || rec.Body.String() != tc.wantBody {
				t.Fatalf("got %d %q, want %d %q", rec.Code, rec.Body.String(), tc.wantCode, tc.wantBody)
			}
			if tc.wantCode == http.StatusOK && rec.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("content type %q", rec.Header().Get("Content-Type"))
			}
		})
	}
}

// TestProtocolDoc checks that docs/protocol.md documents exactly the endpoints this handler serves, with their
// status codes and headers.
func TestProtocolDoc(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "protocol.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	var endpoints []string
	for line := range strings.SplitSeq(doc, "\n") {
		if strings.HasPrefix(line, "### `") {
			endpoints = append(endpoints, line)
		}
	}
	want := []string{
		"### `GET /healthz`",
		"### `POST /mcp`",
		"### `POST /v1/hook`",
		"### `POST /v1/reload`",
		"### `GET /v1/status`",
		"### `GET /v1/watch`", // added by bead mairc-648
	}
	if strings.Join(endpoints, "\n") != strings.Join(want, "\n") {
		t.Fatalf("endpoint headings %q, want %q", endpoints, want)
	}
	for _, s := range []string{
		"Authorization: Bearer", headerSession, headerSeat, headerPid, headerHookSession, "hook_session",
		"`200`", "`202`", "`204`", "`400`", "`401`", "`404`", "`405`", "`413`",
		"`" + joinLine + "`", `{"version":"...","uptime_s":N,"agents":N,"sessions":N}`,
		// The hook body gained `session` and `claimed` (bead mairc-seat-0fk).
		`{"pids":[int,...],"seat":"...","name":"...","event":"...","session":"...","claimed":"..."}`,
	} {
		if !strings.Contains(doc, s) {
			t.Errorf("docs/protocol.md does not mention %q", s)
		}
	}
}

func BenchmarkHookEmpty(b *testing.B) {
	f := newFixture(b, testSettings())
	id := initialize(b, f.h, "seat-b", 4242)
	if got := call(b, f.h, id, "irc_name", `{"name":"bob"}`); got != "you are bob" {
		b.Fatal(got)
	}
	body := []byte(`{"pids":[4242],"seat":"seat-b","event":"PostToolUse"}`)
	b.ReportAllocs()
	for b.Loop() {
		req := httptest.NewRequestWithContext(b.Context(), http.MethodPost, "/v1/hook", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			b.Fatal(rec.Code)
		}
	}
}
