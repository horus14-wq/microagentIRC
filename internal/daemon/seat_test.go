package daemon

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/horus14-wq/microagentIRC/internal/core"
	"github.com/horus14-wq/microagentIRC/internal/mcp"
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
	if got := delivered(t, f, `{"session":"hs-fox-2","claimed":"fox"}`); got != "" {
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
