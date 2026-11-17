package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/horus14-wq/microagentIRC/internal/core"
	"github.com/horus14-wq/microagentIRC/internal/identity"
	"github.com/horus14-wq/microagentIRC/internal/mcp"
	"github.com/horus14-wq/microagentIRC/internal/names"
	"github.com/horus14-wq/microagentIRC/internal/tools"
)

// seatSettings: no cooldown between private messages, and no room lines in the way.
func seatSettings() core.Settings {
	s := core.DefaultSettings()
	s.PrivateCooldownS = 0
	s.CheckRoomEvery = 1000
	return s
}

// boundFixture is a fixture whose server stores hook-session bindings in the fixture's database.
func boundFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t, seatSettings())
	if err := f.srv.useBindings(t.Context(), f.store); err != nil {
		t.Fatal(err)
	}
	return f
}

// join opens a session with the shim headers given and names it.
func join(t *testing.T, f *fixture, name, seat string, pid int, hookSession string) string {
	t.Helper()
	headers := map[string]string{}
	if seat != "" {
		headers[headerSeat] = seat
	}
	if pid != 0 {
		headers[headerPid] = strconv.Itoa(pid)
	}
	if hookSession != "" {
		headers[headerHookSession] = hookSession
	}
	rec := do(f.srv, http.MethodPost, "/mcp", headers, initializeBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("initialize: %d", rec.Code)
	}
	id := rec.Header().Get(headerSession)
	if name != "" {
		if got := call(t, f.srv, id, "irc_name", `{"name":"`+name+`"}`); got != "you are "+name {
			t.Fatalf("irc_name: %q", got)
		}
	}
	return id
}

// pm sends a private message from one session to a name.
func pm(t *testing.T, f *fixture, from, to, text string) {
	t.Helper()
	if got := call(t, f.srv, from, "irc_send", `{"text":"`+text+`","to":"`+to+`"}`); got != "sent" {
		t.Fatalf("irc_send to %s: %q", to, got)
	}
}

// delivered runs one hook request and returns what it delivered ("" for a 204).
func delivered(t *testing.T, f *fixture, body string) string {
	t.Helper()
	rec := hook(f.srv, body)
	switch rec.Code {
	case http.StatusNoContent:
		return ""
	case http.StatusOK:
		return rec.Body.String()
	}
	t.Fatalf("hook %s: %d", body, rec.Code)
	return ""
}

func TestAHookSessionIsLearnedAndThenResolvesByItself(t *testing.T) {
	f := boundFixture(t)
	fox := join(t, f, "fox", "@pc/tmux:%1", 100, "")
	dog := join(t, f, "dog", "@pc/tmux:%2", 200, "")

	// Resolved by process id the first time; the daemon learns the session.
	pm(t, f, dog, "fox", "one")
	if got := delivered(t, f, `{"pids":[100],"seat":"@pc/tmux:%1","session":"sess-fox"}`); got != "pm dog: one" {
		t.Fatalf("by process id: %q", got)
	}
	// From now on the session alone is enough: the seat and the process chain match nothing.
	pm(t, f, dog, "fox", "two")
	if got := delivered(t, f, `{"pids":[999],"seat":"@elsewhere/cwd:/x","session":"sess-fox"}`); got != "pm dog: two" {
		t.Fatalf("by session alone: %q", got)
	}
	// Learned from the seat as well.
	pm(t, f, fox, "dog", "three")
	if got := delivered(t, f, `{"seat":"@pc/tmux:%2","session":"sess-dog"}`); got != "pm fox: three" {
		t.Fatalf("by seat: %q", got)
	}
	pm(t, f, fox, "dog", "four")
	if got := delivered(t, f, `{"session":"sess-dog"}`); got != "pm fox: four" {
		t.Fatalf("dog by session alone: %q", got)
	}
	// A bound session is not re-bound by a process id or a seat that points at someone else: the binding wins.
	pm(t, f, dog, "fox", "five")
	pm(t, f, fox, "dog", "six")
	if got := delivered(t, f, `{"pids":[200],"seat":"@pc/tmux:%2","session":"sess-fox"}`); got != "pm dog: five" {
		t.Fatalf("a bound session keeps its name: %q", got)
	}
	// An invalid session id is ignored, not trusted: resolution falls back to the seat.
	if got := delivered(t, f, `{"seat":"@pc/tmux:%2","session":"bad id"}`); got != "pm fox: six" {
		t.Fatalf("invalid session id: %q", got)
	}

	stored, err := f.store.HookSessions(t.Context())
	if err != nil || len(stored) != 2 || stored["sess-fox"] != "fox" || stored["sess-dog"] != "dog" {
		t.Fatalf("stored bindings: %v (%v)", stored, err)
	}
}

func TestBindingsSurviveARestart(t *testing.T) {
	f := boundFixture(t)
	fox := join(t, f, "fox", "@pc/tmux:%1", 100, "")
	dog := join(t, f, "dog", "@pc/tmux:%2", 200, "")
	if got := delivered(t, f, `{"pids":[100],"seat":"@pc/cwd:/w","session":"sess-fox"}`); got != "" {
		t.Fatalf("nothing is due yet: %q", got)
	}
	pm(t, f, dog, "fox", "after the restart")
	_ = fox

	// A new daemon over the same database: no MCP session exists, so no process id or seat can resolve anything.
	restarted := newServer(f.chat, mcp.New(serverName, "test", tools.All(f.chat)), testToken, "test",
		func() error { return nil }, f.clock, func(string, ...any) {})
	if err := restarted.useBindings(t.Context(), f.store); err != nil {
		t.Fatal(err)
	}
	rec := hook(restarted, `{"pids":[4242],"seat":"@nowhere/cwd:/","session":"sess-fox"}`)
	if rec.Code != http.StatusOK || rec.Body.String() != "pm dog: after the restart" {
		t.Fatalf("after a restart the stored binding resolves the hook: %d %q", rec.Code, rec.Body.String())
	}
	// Without the stored bindings the same request resolves nobody.
	forgetful := newServer(f.chat, mcp.New(serverName, "test", tools.All(f.chat)), testToken, "test",
		func() error { return nil }, f.clock, func(string, ...any) {})
	if rec := hook(forgetful, `{"pids":[4242],"seat":"@nowhere/cwd:/","session":"sess-fox"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("control: a daemon without bindings answered %d", rec.Code)
	}
}

// Three agents whose shims report the same process id and no usable seat: nothing but the result of irc_name,
// read by each agent's own hook, can tell them apart.
func TestTheNameIsClaimedFromTheIRCNameResult(t *testing.T) {
	f := boundFixture(t)
	fox := join(t, f, "fox", "", 7, "")
	dog := join(t, f, "dog", "", 7, "")
	owl := join(t, f, "owl", "", 7, "")

	// Each agent's next hook event carries its session id and the name it was just given.
	for name, hookSession := range map[string]string{"fox": "hs-fox", "dog": "hs-dog"} {
		if got := delivered(t, f, `{"pids":[7],"session":"`+hookSession+`","claimed":"`+name+`"}`); got != "" {
			t.Fatalf("%s: nothing is due yet: %q", name, got)
		}
	}
	pm(t, f, owl, "fox", "for fox")
	pm(t, f, owl, "dog", "for dog")
	if got := delivered(t, f, `{"pids":[7],"session":"hs-dog"}`); got != "pm owl: for dog" {
		t.Fatalf("dog's hook: %q", got)
	}
	if got := delivered(t, f, `{"pids":[7],"session":"hs-fox"}`); got != "pm owl: for fox" {
		t.Fatalf("fox's hook: %q", got)
	}

	refused := []struct {
		name string
		prep func()
		body string
	}{
		{"no session id", func() {}, `{"pids":[7],"claimed":"owl"}`},
		{"a name irc_name did not just give", func() {}, `{"pids":[7],"session":"hs-x","claimed":"nobody"}`},
		{"an invalid name", func() {}, `{"pids":[7],"session":"hs-x","claimed":"Not A Name"}`},
		{"after the window", func() { f.clock.add(61 * time.Second) }, `{"pids":[7],"session":"hs-owl","claimed":"owl"}`},
	}
	pm(t, f, fox, "owl", "for owl")
	for _, tc := range refused {
		tc.prep()
		if got := delivered(t, f, tc.body); got != "" {
			t.Fatalf("%s: the claim was accepted and delivered %q", tc.name, got)
		}
	}
	// Inside the window it is accepted: owl confirms its name, and its hook claims it.
	if got := call(t, f.srv, owl, "irc_name", `{"name":"owl"}`); got != "you are owl" {
		t.Fatal(got)
	}
	f.clock.add(59 * time.Second)
	if got := delivered(t, f, `{"pids":[7],"session":"hs-owl","claimed":"owl"}`); got != "pm fox: for owl" {
		t.Fatalf("a claim inside the window: %q", got)
	}

	// A new session of the same agent takes the name over with a fresh claim; the old session no longer resolves.
	if got := call(t, f.srv, fox, "irc_name", `{"name":"fox"}`); got != "you are fox" {
		t.Fatal(got)
	}
	if got := delivered(t, f, `{"pids":[7],"session":"hs-fox-2","claimed":"fox"}`); got != "" {
		t.Fatalf("nothing is due: %q", got)
	}
	pm(t, f, dog, "fox", "to the new session")
	if got := delivered(t, f, `{"session":"hs-fox"}`); got != "" {
		t.Fatalf("the old session still resolves: %q", got)
	}
	if got := delivered(t, f, `{"session":"hs-fox-2"}`); got != "pm dog: to the new session" {
		t.Fatalf("the new session: %q", got)
	}
}

// A claim is only the caller's to make: in the minute after fox confirms its name, no other agent's hook can take
// the name (and with it fox's private messages), whatever it says it read. Regression test: before the claim was
// tied to the agent that called irc_name, each of the refused requests below was accepted and delivered fox's
// message to the wrong hook.
func TestAnotherAgentCannotClaimAFreshName(t *testing.T) {
	refused := []struct{ name, body string }{
		{"a different process chain", `{"pids":[900,1],"seat":"@pc/tmux:%3","session":"hs-b","claimed":"fox"}`},
		{"the same process id on another machine", `{"pids":[100],"seat":"@other/tmux:%1","session":"hs-b","claimed":"fox"}`},
		{"no process chain and another seat", `{"seat":"@pc/tmux:%2","session":"hs-b","claimed":"fox"}`},
		{"no process chain and no seat", `{"session":"hs-b","claimed":"fox"}`},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			f := boundFixture(t)
			fox := join(t, f, "fox", "@pc/tmux:%1", 100, "")
			dog := join(t, f, "dog", "@pc/tmux:%2", 200, "")
			if got := delivered(t, f, `{"pids":[100],"seat":"@pc/tmux:%1","session":"hs-a","claimed":"fox"}`); got != "" {
				t.Fatalf("fox's own claim: nothing is due yet, got %q", got)
			}
			pm(t, f, dog, "fox", "for fox only")
			if got := delivered(t, f, tc.body); strings.Contains(got, "for fox only") {
				t.Fatalf("another agent claimed fox and was given %q", got)
			}
			if got := delivered(t, f, `{"session":"hs-a"}`); got != "pm dog: for fox only" {
				t.Fatalf("fox's hook was given %q", got)
			}
			_ = fox
		})
	}

	// Without a process id on either side, the caller's seat is what ties the claim to it.
	f := boundFixture(t)
	fox := join(t, f, "fox", "@pc/tmux:%1", 0, "")
	dog := join(t, f, "dog", "@pc/tmux:%2", 0, "")
	if got := delivered(t, f, `{"seat":"@pc/tmux:%2","session":"hs-b","claimed":"fox"}`); got != "" {
		t.Fatalf("from another seat: %q", got)
	}
	pm(t, f, dog, "fox", "hello")
	if got := delivered(t, f, `{"pids":[55],"seat":"@pc/tmux:%1","session":"hs-a","claimed":"fox"}`); got != "pm dog: hello" {
		t.Fatalf("from the caller's seat: %q", got)
	}
	_ = fox
}

func TestProcessIDsAreMatchedPerHost(t *testing.T) {
	f := boundFixture(t)
	fox := join(t, f, "fox", "@pc1/tmux:%1", 50, "")
	dog := join(t, f, "dog", "@pc2/tmux:%1", 50, "")
	pm(t, f, dog, "fox", "to fox")
	pm(t, f, fox, "dog", "to dog")
	// The same process id on two machines: the hook's own machine decides.
	if got := delivered(t, f, `{"pids":[50],"seat":"@pc2/cwd:/z"}`); got != "pm fox: to dog" {
		t.Fatalf("pc2: %q", got)
	}
	if got := delivered(t, f, `{"pids":[50],"seat":"@pc1/cwd:/z"}`); got != "pm dog: to fox" {
		t.Fatalf("pc1: %q", got)
	}
	// A hook that names no machine matches only shims that named none.
	pm(t, f, dog, "fox", "again")
	if got := delivered(t, f, `{"pids":[50],"seat":"cwd:/z"}`); got != "" {
		t.Fatalf("no host: %q", got)
	}
	old := join(t, f, "cat", "tmux:%9", 60, "")
	pm(t, f, fox, "cat", "to an old client")
	if got := delivered(t, f, `{"pids":[60]}`); got != "pm fox: to an old client" {
		t.Fatalf("an old client's shim and hook, neither with a host: %q", got)
	}
	_ = old
}

func TestANameRememberedUnderAnOldSeatIsAdopted(t *testing.T) {
	f := boundFixture(t)
	// An older version remembered this agent under a seat without a host.
	join(t, f, "fox", "tmux:%7", 0, "")
	// The same place, as this version names it.
	id := join(t, f, "", "@pc/tmux:%7", 0, "")
	f.srv.mu.Lock()
	got := f.srv.sessions[id].Name
	f.srv.mu.Unlock()
	if got != "fox" {
		t.Fatalf("the new-form seat starts with the name %q, want fox", got)
	}
	a, ok, err := f.store.AgentBySeat(t.Context(), "@pc/tmux:%7")
	if err != nil || !ok || a.Name != "fox" {
		t.Fatalf("the name moved to the new seat: %+v ok=%v err=%v", a, ok, err)
	}
	if _, ok, _ := f.store.AgentBySeat(t.Context(), "tmux:%7"); ok {
		t.Fatal("the old seat still holds the name")
	}
	// The hook finds it by the new seat too, and a different machine with the same pane does not.
	sender := join(t, f, "dog", "@pc/tmux:%8", 0, "")
	pm(t, f, sender, "fox", "hello")
	if got := delivered(t, f, `{"seat":"@other/tmux:%7"}`); got != "" {
		t.Fatalf("another machine's pane %%7 got %q", got)
	}
	if got := delivered(t, f, `{"seat":"@pc/tmux:%7"}`); got != "pm dog: hello" {
		t.Fatalf("by the new seat: %q", got)
	}
}

func TestARenameMovesTheBinding(t *testing.T) {
	f := boundFixture(t)
	fox := join(t, f, "fox", "@pc/tmux:%1", 100, "")
	dog := join(t, f, "dog", "@pc/tmux:%2", 200, "")
	delivered(t, f, `{"pids":[100],"seat":"@pc/cwd:/w","session":"sess-fox"}`) // learned
	if got := call(t, f.srv, fox, "irc_name", `{"name":"redfox"}`); got != "you are redfox" {
		t.Fatal(got)
	}
	pm(t, f, dog, "redfox", "to the new name")
	if got := delivered(t, f, `{"session":"sess-fox"}`); got != "pm dog: to the new name" {
		t.Fatalf("the session follows the rename: %q", got)
	}
	stored, _ := f.store.HookSessions(t.Context())
	if stored["sess-fox"] != "redfox" {
		t.Fatalf("stored binding after the rename: %v", stored)
	}
}

// Two agents in one seat: the second starts with the name remembered for the seat, which is the first one's. When
// it takes its own name, the first agent's hook stays with the first agent.
func TestASecondAgentInTheSeatDoesNotTakeTheFirstOnesHook(t *testing.T) {
	f := boundFixture(t)
	fox := join(t, f, "fox", "@pc/cwd:/w", 7, "")
	if got := delivered(t, f, `{"pids":[7],"seat":"@pc/cwd:/w","session":"hs-fox","claimed":"fox"}`); got != "" {
		t.Fatalf("nothing is due: %q", got)
	}
	dog := join(t, f, "dog", "@pc/cwd:/w", 7, "") // starts as fox, then names itself dog
	if got := delivered(t, f, `{"pids":[7],"seat":"@pc/cwd:/w","session":"hs-dog","claimed":"dog"}`); got != "" {
		t.Fatalf("nothing is due: %q", got)
	}
	pm(t, f, dog, "fox", "for fox")
	pm(t, f, fox, "dog", "for dog")
	if got := delivered(t, f, `{"pids":[7],"seat":"@pc/cwd:/w","session":"hs-fox"}`); got != "pm dog: for fox" {
		t.Fatalf("fox's hook: %q", got)
	}
	if got := delivered(t, f, `{"pids":[7],"seat":"@pc/cwd:/w","session":"hs-dog"}`); got != "pm fox: for dog" {
		t.Fatalf("dog's hook: %q", got)
	}
}

// A CLI started from inside another CLI inherits the outer one's session id in its environment. Its shim reports
// that id, but the id is already bound to the outer agent and stays there.
func TestAnInheritedSessionIDDoesNotMoveTheOuterAgentsHook(t *testing.T) {
	f := boundFixture(t)
	outer := join(t, f, "fox", "@pc/tmux:%1", 100, "cli-1")
	inner := join(t, f, "dog", "@pc/tmux:%2", 200, "cli-1")
	pm(t, f, inner, "fox", "for the outer agent")
	if got := delivered(t, f, `{"session":"cli-1"}`); got != "pm dog: for the outer agent" {
		t.Fatalf("the outer agent's session: %q", got)
	}
	_ = outer
}

func TestAShimThatKnowsItsSessionBindsIt(t *testing.T) {
	f := boundFixture(t)
	join(t, f, "fox", "", 0, "cli-session-1")
	dog := join(t, f, "dog", "", 0, "")
	pm(t, f, dog, "fox", "hi")
	if got := delivered(t, f, `{"session":"cli-session-1"}`); got != "pm dog: hi" {
		t.Fatalf("a session id the shim reported: %q", got)
	}
}

func TestUnusedBindingsAreDropped(t *testing.T) {
	f := boundFixture(t)
	join(t, f, "fox", "@pc/tmux:%1", 100, "")
	join(t, f, "dog", "@pc/tmux:%2", 200, "")
	delivered(t, f, `{"pids":[100],"seat":"@pc/cwd:/w","session":"sess-fox"}`)
	delivered(t, f, `{"pids":[200],"seat":"@pc/cwd:/w","session":"sess-dog"}`)
	f.clock.add(6 * 24 * time.Hour)
	delivered(t, f, `{"session":"sess-dog"}`) // dog's session is still in use
	f.clock.add(2 * 24 * time.Hour)
	f.srv.dropIdle(t.Context(), f.clock.Now())
	stored, err := f.store.HookSessions(t.Context())
	if err != nil || len(stored) != 1 || stored["sess-dog"] != "dog" {
		t.Fatalf("after eight days, fox unused for eight and dog for two: %v (%v)", stored, err)
	}
	f.srv.mu.Lock()
	_, foxBound := f.srv.bound["sess-fox"]
	_, dogBound := f.srv.bound["sess-dog"]
	f.srv.mu.Unlock()
	if foxBound || !dogBound {
		t.Fatalf("in memory: fox bound %v, dog bound %v", foxBound, dogBound)
	}
}

// The empty path of a session-resolved hook runs no SQL: the database is closed and nothing is logged.
func TestASessionResolvedHookTouchesNoDatabaseWhenNothingIsDue(t *testing.T) {
	f := boundFixture(t)
	join(t, f, "fox", "@pc/tmux:%1", 100, "")
	delivered(t, f, `{"pids":[100],"seat":"@pc/cwd:/w","session":"sess-fox"}`) // learned and stored
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if got := delivered(t, f, `{"session":"sess-fox","seat":"@pc/cwd:/moved"}`); got != "" {
			t.Fatalf("delivered %q", got)
		}
	}
	if len(*f.logs) != 0 {
		t.Fatalf("logged %q: the empty path touched the database", *f.logs)
	}
}

// attach opens a session the way a current shim does (it announces a watch) and returns the session id and the
// seat the daemon put in force.
func attach(t *testing.T, f *fixture, seat string, pid int) (id, inForce string) {
	t.Helper()
	headers := map[string]string{headerSeat: seat, headerWatch: "1"}
	if pid != 0 {
		headers[headerPid] = strconv.Itoa(pid)
	}
	rec := do(f.srv, http.MethodPost, "/mcp", headers, initializeBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("initialize: %d", rec.Code)
	}
	return rec.Header().Get(headerSession), rec.Header().Get(headerSeat)
}

func sessionName(f *fixture, id string) string {
	f.srv.mu.Lock()
	defer f.srv.mu.Unlock()
	if sess := f.srv.sessions[id]; sess != nil {
		return sess.Name
	}
	return "<no session>"
}

// watching holds a watch open for the session until the returned function is called, which waits for the daemon
// to have dropped the session.
func watching(t *testing.T, f *fixture, id string) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int, 1)
	go func() {
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/v1/watch", http.NoBody)
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set(headerSession, id)
		rec := httptest.NewRecorder()
		f.srv.ServeHTTP(rec, req)
		done <- rec.Code
	}()
	for range 2000 {
		f.srv.mu.Lock()
		sess := f.srv.sessions[id]
		open := sess != nil && sess.watchers == 1
		f.srv.mu.Unlock()
		if open {
			return func() {
				cancel()
				if code := <-done; code != http.StatusOK {
					t.Fatalf("watch answered %d", code)
				}
			}
		}
		runtime.Gosched()
	}
	cancel()
	t.Fatal("the watch never opened")
	return nil
}

// Agents that share a seat each get their own name, also when they let irc_name choose it.
func TestASecondLiveAgentInASeatGetsItsOwnSeatAndName(t *testing.T) {
	f := boundFixture(t)
	const seat = "@pc/cwd:/w"
	first, firstSeat := attach(t, f, seat, 7)
	if firstSeat != seat {
		t.Fatalf("the first agent keeps the seat it sent, got %q", firstSeat)
	}
	firstName := strings.TrimPrefix(call(t, f.srv, first, "irc_name", `{}`), "you are ")

	second, secondSeat := attach(t, f, seat, 7)
	if identity.SharedSeat(secondSeat) != seat || secondSeat == seat {
		t.Fatalf("the second agent's seat is %q, want its own under %q", secondSeat, seat)
	}
	if got := sessionName(f, second); got != "" {
		t.Fatalf("the second agent starts with the name %q, want none", got)
	}
	secondName := strings.TrimPrefix(call(t, f.srv, second, "irc_name", `{}`), "you are ")
	if secondName == firstName || !names.Valid(secondName) {
		t.Fatalf("the second agent was named %q, the first %q", secondName, firstName)
	}
	if got := sessionName(f, first); got != firstName {
		t.Fatalf("the first agent is now %q, was %q", got, firstName)
	}

	// After a daemon restart each shim sends the seat it was given, and gets its own name back in any order.
	restarted := newServer(f.chat, mcp.New(serverName, "test", tools.All(f.chat)), testToken, "test",
		func() error { return nil }, f.clock, func(string, ...any) {})
	for _, tc := range []struct{ seat, want string }{{secondSeat, secondName}, {firstSeat, firstName}} {
		rec := do(restarted, http.MethodPost, "/mcp", map[string]string{headerSeat: tc.seat, headerWatch: "1"},
			initializeBody)
		id := rec.Header().Get(headerSession)
		restarted.mu.Lock()
		got := restarted.sessions[id].Name
		restarted.mu.Unlock()
		if got != tc.want || rec.Header().Get(headerSeat) != tc.seat {
			t.Fatalf("after a restart seat %q is %q in seat %q, want %q", tc.seat, got, rec.Header().Get(headerSeat), tc.want)
		}
	}
}

// A session counts as live only while its shim watches it (or for a moment after it attached). An agent that
// restarts in its seat therefore gets its name back, however its old shim ended.
func TestANameIsFreeAgainWhenItsWatchEnds(t *testing.T) {
	f := boundFixture(t)
	const seat = "@pc/tmux:%1"
	first, _ := attach(t, f, seat, 100)
	if got := call(t, f.srv, first, "irc_name", `{"name":"fox"}`); got != "you are fox" {
		t.Fatal(got)
	}
	stop := watching(t, f, first)
	f.clock.add(time.Hour) // long past the moment of grace: only the open watch keeps the name in use
	if id, inForce := attach(t, f, seat, 200); sessionName(f, id) != "" || inForce == seat {
		t.Fatalf("while fox is watched, another agent in its seat got name %q and seat %q", sessionName(f, id), inForce)
	}
	stop()
	if got := sessionName(f, first); got != "<no session>" {
		t.Fatalf("the session outlived its watch: %q", got)
	}
	f.clock.add(time.Hour)
	id, inForce := attach(t, f, seat, 300)
	if got := sessionName(f, id); got != "fox" || inForce != seat {
		t.Fatalf("the restarted agent is %q in seat %q, want fox in %q", got, inForce, seat)
	}

	// A shim that announced a watch and never opened one is live for ten seconds only.
	const other = "@pc/tmux:%2"
	stale, _ := attach(t, f, other, 400)
	if got := call(t, f.srv, stale, "irc_name", `{"name":"dog"}`); got != "you are dog" {
		t.Fatal(got)
	}
	f.clock.add(9 * time.Second)
	if id, _ := attach(t, f, other, 500); sessionName(f, id) != "" {
		t.Fatal("inside the ten seconds the name is in use")
	}
	f.clock.add(2 * time.Second)
	if id, _ := attach(t, f, other, 600); sessionName(f, id) != "dog" {
		t.Fatalf("after the ten seconds the name is free, got %q", sessionName(f, id))
	}
}

// A seat that agents with different names share says nothing about which of them a hook runs for.
func TestASeatSharedByDifferentNamesIsSkipped(t *testing.T) {
	f := boundFixture(t)
	const seat = "@pc/cwd:/w"
	fox, _ := attach(t, f, seat, 0)
	if got := call(t, f.srv, fox, "irc_name", `{"name":"fox"}`); got != "you are fox" {
		t.Fatal(got)
	}
	// One live agent: the seat is that agent.
	sender := join(t, f, "owl", "@pc/tmux:%9", 0, "")
	pm(t, f, sender, "fox", "one")
	if got := delivered(t, f, `{"seat":"`+seat+`"}`); got != "pm owl: one" {
		t.Fatalf("one agent in the seat: %q", got)
	}
	dog, _ := attach(t, f, seat, 0)
	if got := call(t, f.srv, dog, "irc_name", `{"name":"dog"}`); got != "you are dog" {
		t.Fatal(got)
	}
	pm(t, f, sender, "fox", "two")
	pm(t, f, sender, "dog", "three")
	if got := delivered(t, f, `{"seat":"`+seat+`","session":"hs-unknown"}`); got != "" {
		t.Fatalf("two agents in the seat, and the hook was given %q", got)
	}
	f.srv.mu.Lock()
	_, learned := f.srv.bound["hs-unknown"]
	f.srv.mu.Unlock()
	if learned {
		t.Fatal("a session was bound from an ambiguous seat")
	}
	// Each is still found by what is its own.
	if got := delivered(t, f, `{"seat":"`+seat+`","name":"dog"}`); got != "pm owl: three" {
		t.Fatalf("dog by name: %q", got)
	}
}
