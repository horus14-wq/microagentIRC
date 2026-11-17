package shim

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	watches chan string // the session id of each watch the shim opened
	up      atomic.Bool
}

func newFakeDaemon(t *testing.T, handler func(reqSeen, *http.Request) (int, string, http.Header)) (*fakeDaemon, *httptest.Server) {
	d := &fakeDaemon{t: t, handler: handler, seen: make(chan reqSeen, 32), watches: make(chan string, 32)}
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
		if r.URL.Path == watchPath {
			// The shim's watch (bead mairc-648) is not one of the scripted /mcp exchanges: it is counted apart
			// and held open until the shim goes away, as the daemon holds it.
			d.watches <- r.Header.Get("Mcp-Session-Id")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
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

// The shim announces a watch on initialize, opens one for each session it gets, and sends the seat the daemon put
// in force when it initializes again after a daemon restart.
func TestTheShimWatchesItsSessionAndKeepsTheSeatItWasGiven(t *testing.T) {
	var inits atomic.Int32
	var failed atomic.Bool
	initSeats := make(chan string, 4)
	announced := make(chan string, 4)
	d, srv := newFakeDaemon(t, func(s reqSeen, r *http.Request) (int, string, http.Header) {
		var m map[string]json.RawMessage
		_ = json.Unmarshal([]byte(s.body), &m)
		method := ""
		_ = json.Unmarshal(m["method"], &method)
		h := http.Header{}
		if method == "initialize" {
			n := inits.Add(1)
			initSeats <- s.seat
			announced <- r.Header.Get("X-Mairc-Watch")
			h.Set("Mcp-Session-Id", "sess-"+strconv.Itoa(int(n)))
			h.Set("X-Mairc-Seat", "seat-a#0011aabb") // the daemon made the seat this session's own
			return http.StatusOK, `{"jsonrpc":"2.0","id":1,"result":{}}`, h
		}
		if failed.CompareAndSwap(false, true) {
			return http.StatusNotFound, "", h // the daemon restarted and forgot the session
		}
		return http.StatusOK, `{"jsonrpc":"2.0","id":9,"result":{"tools":[]}}`, h
	})
	// The agent CLI keeps the shim's stdin open until the watches have been seen: a watch lives as long as the shim.
	in, feed := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- runShim(Options{BaseURL: srv.URL, Token: "t", Seat: "seat-a", In: in, Out: io.Discard})
	}()
	if _, err := io.WriteString(feed, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`+"\n"+
		`{"jsonrpc":"2.0","id":9,"method":"tools/list"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = feed.Close()
		if err := <-done; err != nil {
			t.Errorf("the shim ended with %v", err)
		}
	}()
	if first, second := <-initSeats, <-initSeats; first != "seat-a" || second != "seat-a#0011aabb" {
		t.Fatalf("initialize sent seats %q then %q", first, second)
	}
	if first, second := <-announced, <-announced; first != "1" || second != "1" {
		t.Fatalf("X-Mairc-Watch on initialize: %q then %q", first, second)
	}
	for _, want := range []string{"sess-1", "sess-2"} {
		select {
		case got := <-d.watches:
			if got != want {
				t.Fatalf("a watch for session %q, want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no watch was opened for %s", want)
		}
	}
}

// A 401 means the token was changed (`mairc token rotate`): the shim reads the token file again and retries once
// with what it finds, so the agent keeps working without a restart. With the same token there it does not retry.
func TestAChangedTokenIsPickedUpOnA401(t *testing.T) {
	var posts atomic.Int32
	_, srv := newFakeDaemon(t, func(s reqSeen, _ *http.Request) (int, string, http.Header) {
		posts.Add(1)
		if s.auth != "Bearer new-token" {
			return http.StatusUnauthorized, "", nil
		}
		return http.StatusOK, `{"jsonrpc":"2.0","id":9,"result":{"tools":[]}}`, http.Header{"Mcp-Session-Id": {"sess-1"}}
	})
	var out bytes.Buffer
	reads := 0
	o := Options{
		BaseURL: srv.URL, Token: "old-token", Out: &out,
		In:        strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"tools/list"}` + "\n"),
		ReadToken: func() (string, error) { reads++; return "new-token", nil },
	}
	if err := runShim(o); err != nil {
		t.Fatal(err)
	}
	if lines := outLines(&out); len(lines) != 1 || !strings.Contains(lines[0], `"tools":[]`) || posts.Load() != 2 || reads != 1 {
		t.Fatalf("out %v, %d posts, %d token reads; want the answer after one retry", outLines(&out), posts.Load(), reads)
	}

	// The file still holds the refused token: one attempt, and the usual error.
	posts.Store(0)
	out.Reset()
	o.In = strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"tools/list"}` + "\n")
	o.ReadToken = func() (string, error) { return "old-token", nil }
	if err := runShim(o); err != nil {
		t.Fatal(err)
	}
	if lines := outLines(&out); len(lines) != 1 || !strings.Contains(lines[0], "mairc daemon unavailable") || posts.Load() != 1 {
		t.Fatalf("out %v, %d posts; want the unavailable error after one attempt", outLines(&out), posts.Load())
	}
}
