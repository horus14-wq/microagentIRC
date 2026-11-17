package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// hookJSON runs `mairc hook` for the seat with the JSON an agent CLI would pass on stdin and returns what it
// printed.
func (w *world) hookJSON(seat, stdin string) string {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(w.t.Context(), wait)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "hook")
	cmd.Env = w.env("MAIRC_SEAT=" + seat)
	cmd.Stdin = strings.NewReader(stdin)
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	out, err := cmd.Output()
	if err != nil || errOut.Len() != 0 {
		w.t.Fatalf("hook: %v, stderr %q", err, errOut.String())
	}
	return string(out)
}

// post sends one authorized request straight to the daemon, for the cases a real shim or hook on this machine
// cannot produce: another machine's name, or an older client's request.
func (w *world) post(path string, headers map[string]string, body string) (status int, header http.Header, text string) {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(w.t.Context(), wait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.base+path, strings.NewReader(body))
	if err != nil {
		w.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+w.token())
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		w.t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, string(raw)
}

// remote is an agent whose shim headers the test writes itself.
type remote struct {
	w  *world
	id string
}

func (w *world) remoteAgent(seat string, pid int, name string) *remote {
	w.t.Helper()
	headers := map[string]string{"X-Mairc-Pid": strconv.Itoa(pid)}
	if seat != "" {
		headers["X-Mairc-Seat"] = seat
	}
	status, header, _ := w.post("/mcp", headers,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	if status != http.StatusOK {
		w.t.Fatalf("initialize: %d", status)
	}
	r := &remote{w: w, id: header.Get("Mcp-Session-Id")}
	if got := r.call("irc_name", map[string]any{"name": name}); got != "you are "+name {
		w.t.Fatalf("irc_name: %q", got)
	}
	return r
}

func (r *remote) call(tool string, args map[string]any) string {
	r.w.t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	if err != nil {
		r.w.t.Fatal(err)
	}
	status, _, text := r.w.post("/mcp", map[string]string{"Mcp-Session-Id": r.id}, string(body))
	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if status != http.StatusOK || json.Unmarshal([]byte(text), &resp) != nil || len(resp.Result.Content) != 1 {
		r.w.t.Fatalf("%s: %d %q", tool, status, text)
	}
	return resp.Result.Content[0].Text
}

// hookBody posts a hook request and returns what was delivered ("" for a 204).
func (w *world) hookBody(body string) string {
	w.t.Helper()
	status, _, text := w.post("/v1/hook", nil, body)
	if status != http.StatusOK && status != http.StatusNoContent {
		w.t.Fatalf("hook %s: %d", body, status)
	}
	return text
}

// Three agents in one folder with no terminal to tell them apart and one parent process: their seats and process
// chains are identical. Each hook has only its agent CLI's session id and, after irc_name, that call's result.
func TestAgentsThatShareASeatAndAParentAreToldApartByTheirSession(t *testing.T) {
	w := newWorld(t, nil)
	const seat = "shared-folder"
	agents := map[string]*agent{}
	for _, name := range []string{"redfox", "bluedog", "greenowl"} {
		a := w.newAgent(seat)
		if got := a.ok("irc_name", map[string]any{"name": name}); got != "you are "+name {
			t.Fatalf("irc_name: %q", got)
		}
		agents[name] = a
		// The hook event after the call: the CLI passes the tool's name and result with its session id.
		after := `{"hook_event_name":"PostToolUse","session_id":"cli-` + name +
			`","tool_name":"mcp__mairc__irc_name","tool_response":[{"type":"text","text":"you are ` + name + `"}]}`
		if out := w.hookJSON(seat, after); out != "" {
			t.Fatalf("%s: nothing is due yet, the hook printed %q", name, out)
		}
	}
	agents["redfox"].ok("irc_send", map[string]any{"text": "for dog", "to": "bluedog"})
	agents["redfox"].ok("irc_send", map[string]any{"text": "for owl", "to": "greenowl"})
	agents["bluedog"].ok("irc_send", map[string]any{"text": "for fox", "to": "redfox"})
	for _, tc := range []struct{ name, want string }{
		{"greenowl", "irc:\npm redfox: for owl\n"},
		{"redfox", "irc:\npm bluedog: for fox\n"},
		{"bluedog", "irc:\npm redfox: for dog\n"},
	} {
		event := `{"hook_event_name":"PostToolUse","session_id":"cli-` + tc.name + `","tool_name":"Bash"}`
		if out := w.hookJSON(seat, event); out != tc.want {
			t.Fatalf("%s's hook printed %q, want %q", tc.name, out, tc.want)
		}
		// Delivered once, and to nobody else.
		if out := w.hookJSON(seat, event); out != "" {
			t.Fatalf("%s's hook printed %q the second time", tc.name, out)
		}
	}
}

func TestASessionSurvivesAMovedSeatAndADaemonRestart(t *testing.T) {
	w := newWorld(t, nil)
	a := w.newAgent("seat-a")
	b := w.newAgent("seat-b")
	nameA, nameB := a.join(), b.join()
	event := `{"hook_event_name":"PostToolUse","session_id":"cli-b"}`

	// Resolved by its seat the first time; the daemon learns the session.
	a.ok("irc_send", map[string]any{"text": "one", "to": nameB})
	if out := w.hookJSON("seat-b", event); out != "irc:\npm "+nameA+": one\n" {
		t.Fatalf("by seat: %q", out)
	}
	// The seat changes (another pane, another folder): the session still finds the agent.
	a.ok("irc_send", map[string]any{"text": "two", "to": nameB})
	if out := w.hookJSON("seat-moved", event); out != "irc:\npm "+nameA+": two\n" {
		t.Fatalf("after the seat moved: %q", out)
	}
	// Control: the moved seat alone, without the session, is nobody.
	a.ok("irc_send", map[string]any{"text": "three", "to": nameB})
	if out := w.hookJSON("seat-moved", `{"hook_event_name":"PostToolUse"}`); out != "" {
		t.Fatalf("the moved seat without a session printed %q", out)
	}

	w.stopDaemon()
	w.startDaemon()
	// No shim has attached since the restart, so no process id is known, and the seat matches nothing: only the
	// stored binding can resolve this.
	if out := w.hookJSON("seat-moved", event); out != "irc:\npm "+nameA+": three\n" {
		t.Fatalf("after the restart: %q", out)
	}
}

func TestTwoMachinesWithTheSameSeatAndProcessIDStayApart(t *testing.T) {
	w := newWorld(t, nil)
	one := w.remoteAgent("@pc-one/tmux:%1", 4242, "redfox")
	two := w.remoteAgent("@pc-two/tmux:%1", 4242, "bluedog")
	if got := one.call("irc_send", map[string]any{"text": "to two", "to": "bluedog"}); got != "sent" {
		t.Fatal(got)
	}
	if got := two.call("irc_send", map[string]any{"text": "to one", "to": "redfox"}); got != "sent" {
		t.Fatal(got)
	}
	// By process id, with a seat that is remembered for nobody: the machine alone decides.
	if got := w.hookBody(`{"pids":[4242],"seat":"@pc-two/cwd:/elsewhere"}`); got != "pm redfox: to two" {
		t.Fatalf("pc-two by process id: %q", got)
	}
	if got := w.hookBody(`{"pids":[4242],"seat":"@pc-one/cwd:/elsewhere"}`); got != "pm bluedog: to one" {
		t.Fatalf("pc-one by process id: %q", got)
	}
	// By seat: the same pane id on each machine is a different seat.
	if got := one.call("irc_send", map[string]any{"text": "again", "to": "bluedog"}); got != "sent" {
		t.Fatal(got)
	}
	if got := w.hookBody(`{"seat":"@pc-one/tmux:%1"}`); got != "" {
		t.Fatalf("pc-one's pane received pc-two's message: %q", got)
	}
	if got := w.hookBody(`{"seat":"@pc-two/tmux:%1"}`); got != "pm redfox: again" {
		t.Fatalf("pc-two by seat: %q", got)
	}
}

// A shim and a hook from before sessions and machines existed: no X-Mairc-Session, a seat without a machine, a
// body with only the four old fields.
func TestAnOlderClientIsServedAsBefore(t *testing.T) {
	w := newWorld(t, nil)
	old := w.remoteAgent("tmux:%7", 5151, "redfox")
	other := w.remoteAgent("tmux:%8", 6161, "bluedog")
	for _, tc := range []struct{ how, body string }{
		{"process id", `{"pids":[9,5151,1],"seat":"cwd:/nowhere","name":"","event":"PostToolUse"}`},
		{"seat", `{"pids":[],"seat":"tmux:%7","name":"","event":"PostToolUse"}`},
		{"name", `{"pids":[],"seat":"","name":"redfox","event":"PostToolUse"}`},
	} {
		if got := other.call("irc_send", map[string]any{"text": tc.how, "to": "redfox"}); got != "sent" {
			t.Fatal(got)
		}
		if got := w.hookBody(tc.body); got != "pm bluedog: "+tc.how {
			t.Fatalf("by %s: %q", tc.how, got)
		}
	}
	if got := w.hookBody(`{"pids":[77],"seat":"tmux:%99","name":"","event":"SessionStart"}`); got != "irc: call irc_name to join" {
		t.Fatalf("an unnamed old client at session start: %q", got)
	}
	_ = old
}

// The private cooldown is per recipient: with the default 60 seconds in force, an agent that has just written to
// one agent can write to another at once, but not to the first again; and the room has a cooldown of its own.
func TestThePrivateCooldownIsPerRecipient(t *testing.T) {
	w := newWorld(t, map[string]any{"private_cooldown_s": 60, "public_cooldown_s": 60})
	a, b, c := w.newAgent("seat-a"), w.newAgent("seat-b"), w.newAgent("seat-c")
	a.join()
	nameB, nameC := b.join(), c.join()
	cooldown := regexp.MustCompile(`^cooldown \d+s$`)
	refused := func(args map[string]any) string {
		t.Helper()
		text, isError := a.call("irc_send", args)
		if !isError {
			t.Fatalf("irc_send %v succeeded with %q", args, text)
		}
		return text
	}
	a.ok("irc_send", map[string]any{"text": "to b", "to": nameB})
	a.ok("irc_send", map[string]any{"text": "to c", "to": nameC}) // another agent: no wait
	if got := refused(map[string]any{"text": "to b again", "to": nameB}); !cooldown.MatchString(got) {
		t.Fatalf("a second private message to the same agent: %q", got)
	}
	if got := refused(map[string]any{"text": "to both", "to": nameB + "," + nameC}); !cooldown.MatchString(got) {
		t.Fatalf("a private message to two agents on cooldown: %q", got)
	}
	// The room is counted apart: the private messages did not start its cooldown, and one room message does.
	a.ok("irc_send", map[string]any{"text": "room one"})
	if got := refused(map[string]any{"text": "room two"}); !cooldown.MatchString(got) {
		t.Fatalf("a second room message: %q", got)
	}
	// Each side has its own cooldown: b can answer a at once.
	b.ok("irc_send", map[string]any{"text": "reply", "to": a.name})
	// What arrived: one message each, nothing from the refused sends.
	if out, _, _ := w.hook("seat-b", "--client", "plain", "--event", "PostToolUse"); out != "irc:\npm "+a.name+": to b\n" {
		t.Fatalf("b's hook printed %q", out)
	}
	if out, _, _ := w.hook("seat-c", "--client", "plain", "--event", "PostToolUse"); out != "irc:\npm "+a.name+": to c\n" {
		t.Fatalf("c's hook printed %q", out)
	}
}

// Three agents start in one seat and let irc_name choose. Each gets its own name, keeps it across a daemon
// restart, and the first one's name is free again for an agent that starts there after it has gone.
func TestAgentsInOneSeatEachGetAndKeepTheirOwnName(t *testing.T) {
	w := newWorld(t, nil)
	const seat = "shared-folder"
	agents := []*agent{w.newAgent(seat), w.newAgent(seat), w.newAgent(seat)}
	seen := map[string]bool{}
	for _, a := range agents {
		name := a.join()
		if seen[name] {
			t.Fatalf("two agents in one seat were both named %s", name)
		}
		seen[name] = true
	}

	w.stopDaemon()
	w.startDaemon()
	// In reverse order, so that the agent that had the seat first is not the first to come back.
	for i := len(agents) - 1; i >= 0; i-- {
		a := agents[i]
		a.ok("irc_send", map[string]any{"text": "back"})
		if got := a.ok("irc_name", nil); got != "you are "+a.name {
			t.Fatalf("after the restart %s was told %q", a.name, got)
		}
	}

	// The first agent leaves; whoever starts in the seat next is that agent again.
	first := agents[0]
	first.close()
	w.until("the first agent's session is dropped", func() bool {
		late := w.newAgent(seat)
		defer late.close()
		got, isErr := late.call("irc_name", nil)
		return !isErr && got == "you are "+first.name
	})
}

// mairc runs one `mairc` command in the world's home and returns what it printed and its exit code.
func (w *world) mairc(args ...string) (string, int) {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(w.t.Context(), wait)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = w.env()
	out, err := cmd.CombinedOutput()
	code := 0
	if exit := (*exec.ExitError)(nil); errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		w.t.Fatalf("mairc %v: %v", args, err)
	}
	return string(out), code
}

// statusAs asks the daemon's status with a token and returns the HTTP status.
func (w *world) statusAs(token string) int {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(w.t.Context(), wait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.base+"/v1/status", http.NoBody)
	if err != nil {
		w.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// `mairc token rotate` replaces the hub token while everything runs: the old token stops working at once, and an
// agent whose shim started with it keeps sending, with no restart of the daemon or of the agent.
func TestTheHubTokenIsRotatedWithoutARestart(t *testing.T) {
	w := newWorld(t, nil)
	a := w.newAgent("seat-a")
	name := a.join()
	a.ok("irc_send", map[string]any{"text": "before"})
	old := w.token()

	out, code := w.mairc("token", "rotate")
	if code != 0 || !strings.Contains(out, "in force: the running daemon has reloaded its tokens") {
		t.Fatalf("token rotate = %d:\n%s", code, out)
	}
	fresh := w.token()
	if fresh == old || len(fresh) != 64 || strings.Contains(out, fresh) || strings.Contains(out, old) {
		t.Fatalf("the token was not replaced, or a token was printed:\n%s", out)
	}
	if got := w.statusAs(old); got != http.StatusUnauthorized {
		t.Fatalf("the old token still answers %d", got)
	}
	if got := w.statusAs(fresh); got != http.StatusOK {
		t.Fatalf("the new token answers %d", got)
	}
	// The shim was started with the old token: it reads the file again when refused and carries on, same name.
	a.ok("irc_send", map[string]any{"text": "after"})
	if got := a.ok("irc_read", map[string]any{"n": 5}); got != "room "+name+": before\nroom "+name+": after" {
		t.Fatalf("after the rotation irc_read answered %q", got)
	}
	// The hook reads the token on every run.
	w.silent("seat-a", "--client", "plain", "--event", "PostToolUse")
}

// A machine gets a token of its own: it works for that machine's seats only, and revoking it puts the machine out
// at once while the hub and its agents carry on.
func TestAMachineTokenIsAddedConfinedAndRevoked(t *testing.T) {
	w := newWorld(t, nil)
	hubAgent := w.newAgent("seat-hub")
	hubAgent.join()

	out, code := w.mairc("token", "add", "spoke1")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || len(lines) < 2 || len(lines[1]) != 64 {
		t.Fatalf("token add = %d:\n%s", code, out)
	}
	spoke := lines[1]
	if list, _ := w.mairc("token", "list"); !strings.HasPrefix(list, "spoke1  since ") || strings.Contains(list, spoke) {
		t.Fatalf("token list:\n%s", list)
	}
	initialize := func(token, seat string) int {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), wait)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.base+"/mcp", strings.NewReader(
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Mairc-Seat", seat)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if got := initialize(spoke, "@spoke1/tmux:%1"); got != http.StatusOK {
		t.Fatalf("the machine token for its own seat: %d", got)
	}
	if got := initialize(spoke, "@otherbox/tmux:%1"); got != http.StatusForbidden {
		t.Fatalf("the machine token for another machine's seat: %d, want 403", got)
	}
	if got := w.statusAs(spoke); got != http.StatusOK {
		t.Fatalf("status with the machine token: %d", got)
	}

	if out, code := w.mairc("token", "revoke", "spoke1"); code != 0 || !strings.Contains(out, "in force") {
		t.Fatalf("token revoke = %d:\n%s", code, out)
	}
	if got := w.statusAs(spoke); got != http.StatusUnauthorized {
		t.Fatalf("the revoked token answers %d", got)
	}
	if out, code := w.mairc("token", "revoke", "spoke1"); code != 1 || !strings.Contains(out, "no token for machine spoke1") {
		t.Fatalf("a second revoke = %d:\n%s", code, out)
	}
	if list, _ := w.mairc("token", "list"); strings.TrimSpace(list) != "no machine tokens" {
		t.Fatalf("token list after the revoke:\n%s", list)
	}
	hubAgent.ok("irc_send", map[string]any{"text": "the hub carries on"})
	if out, code := w.mairc("token"); code != 2 || !strings.Contains(out, "usage: mairc token") {
		t.Fatalf("mairc token with no argument = %d:\n%s", code, out)
	}
}
