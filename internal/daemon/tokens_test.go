package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/horus14-wq/microagentIRC/internal/config"
)

// as sends one request with the given bearer token.
func as(h http.Handler, token, method, path string, headers map[string]string, body string) *httptest.ResponseRecorder {
	all := map[string]string{"Authorization": "Bearer " + token}
	for k, v := range headers {
		all[k] = v
	}
	return do(h, method, path, all, body)
}

// machineFixture is a daemon that accepts the hub token and one machine token, for the machine "pc2".
func machineFixture(t *testing.T) (f *fixture, machineToken string) {
	t.Helper()
	f = boundFixture(t)
	machineToken = strings.Repeat("ab", 32)
	f.srv.setTokens(testToken, map[string]string{config.HashToken(machineToken): "pc2"})
	return f, machineToken
}

// attachAs opens a session with a token and a seat and returns the status and the session id.
func attachAs(f *fixture, token, seat string) (int, string) {
	headers := map[string]string{}
	if seat != "" {
		headers[headerSeat] = seat
	}
	rec := as(f.srv, token, http.MethodPost, "/mcp", headers, initializeBody)
	return rec.Code, rec.Header().Get(headerSession)
}

// callAs calls a tool in a session with a token and returns the status and the text.
func callAs(f *fixture, token, id, tool, args string) (int, string) {
	body := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"` + tool + `","arguments":` + args + `}}`
	rec := as(f.srv, token, http.MethodPost, "/mcp", map[string]string{headerSession: id}, body)
	// The tool's answer is the one text block of the result; on anything else the raw body is returned.
	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &resp) == nil && len(resp.Result.Content) == 1 {
		return rec.Code, resp.Result.Content[0].Text
	}
	return rec.Code, rec.Body.String()
}

// A machine token opens sessions only for seats on its own machine, and nobody else's token can use them.
func TestAMachineTokenActsOnlyForItsOwnMachine(t *testing.T) {
	f, pc2 := machineFixture(t)
	for _, tc := range []struct {
		name, token, seat string
		want              int
	}{
		{"its own machine", pc2, "@pc2/tmux:%1", http.StatusOK},
		{"another machine's seat", pc2, "@pc1/tmux:%1", http.StatusForbidden},
		{"a seat without a machine", pc2, "tmux:%1", http.StatusForbidden},
		{"no seat", pc2, "", http.StatusForbidden},
		{"the hub token, any seat", testToken, "@pc1/tmux:%1", http.StatusOK},
		{"the hub token, no seat", testToken, "", http.StatusOK},
		{"an unknown token", strings.Repeat("cd", 32), "@pc2/tmux:%1", http.StatusUnauthorized},
		{"an empty token", "", "@pc2/tmux:%1", http.StatusUnauthorized},
	} {
		if got, _ := attachAs(f, tc.token, tc.seat); got != tc.want {
			t.Errorf("%s: initialize answered %d, want %d", tc.name, got, tc.want)
		}
	}

	_, spoke := attachAs(f, pc2, "@pc2/tmux:%1")
	_, hub := attachAs(f, testToken, "@pc1/tmux:%1")
	if code, text := callAs(f, pc2, spoke, "irc_name", `{"name":"owl"}`); code != http.StatusOK || text != "you are owl" {
		t.Fatalf("the machine's own session: %d %q", code, text)
	}
	if code, text := callAs(f, testToken, hub, "irc_name", `{"name":"fox"}`); code != http.StatusOK || text != "you are fox" {
		t.Fatalf("the hub's session: %d %q", code, text)
	}
	// A session answers only to the token that opened it.
	if code, _ := callAs(f, pc2, hub, "irc_who", `{}`); code != http.StatusNotFound {
		t.Errorf("the hub's session used with the machine token: %d, want 404", code)
	}
	if code, _ := callAs(f, testToken, spoke, "irc_who", `{}`); code != http.StatusNotFound {
		t.Errorf("the machine's session used with the hub token: %d, want 404", code)
	}
	if rec := as(f.srv, pc2, http.MethodGet, "/v1/watch", map[string]string{headerSession: hub}, ""); rec.Code != http.StatusNotFound {
		t.Errorf("watching the hub's session with the machine token: %d, want 404", rec.Code)
	}
	// The machine can see the daemon's status; settings and tokens are the hub's to reload.
	if rec := as(f.srv, pc2, http.MethodGet, "/v1/status", nil, ""); rec.Code != http.StatusOK {
		t.Errorf("status with the machine token: %d", rec.Code)
	}
	if rec := as(f.srv, pc2, http.MethodPost, "/v1/reload", nil, ""); rec.Code != http.StatusForbidden {
		t.Errorf("reload with the machine token: %d, want 403", rec.Code)
	}
	// A name on another machine cannot be taken from here: the name is bound to its seat.
	if _, text := callAs(f, pc2, spoke, "irc_name", `{"name":"fox"}`); text != "name taken" {
		t.Errorf("taking the hub agent's name answered %q", text)
	}
}

// A machine's hook is given only that machine's agents' messages, however it asks.
func TestAMachineTokensHookReadsOnlyItsOwnAgents(t *testing.T) {
	f, pc2 := machineFixture(t)
	_, spoke := attachAs(f, pc2, "@pc2/tmux:%1")
	_, hub := attachAs(f, testToken, "@pc1/tmux:%1")
	callAs(f, pc2, spoke, "irc_name", `{"name":"owl"}`)
	callAs(f, testToken, hub, "irc_name", `{"name":"fox"}`)
	// fox's hook, on the hub, teaches the daemon fox's CLI session.
	if rec := as(f.srv, testToken, http.MethodPost, "/v1/hook", nil, `{"seat":"@pc1/tmux:%1","session":"cli-fox"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("fox's own hook: %d", rec.Code)
	}
	if _, text := callAs(f, pc2, spoke, "irc_send", `{"text":"for fox","to":"fox"}`); text != "sent" {
		t.Fatal(text)
	}
	if _, text := callAs(f, testToken, hub, "irc_send", `{"text":"for owl","to":"owl"}`); text != "sent" {
		t.Fatal(text)
	}
	hook := func(body string) (int, string) {
		rec := as(f.srv, pc2, http.MethodPost, "/v1/hook", nil, body)
		return rec.Code, rec.Body.String()
	}
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"another machine's seat", `{"seat":"@pc1/tmux:%1"}`, http.StatusForbidden},
		{"no seat", `{}`, http.StatusForbidden},
		{"naming an agent outright", `{"seat":"@pc2/tmux:%1","name":"fox"}`, http.StatusForbidden},
		{"even its own agent by name", `{"seat":"@pc2/tmux:%1","name":"owl"}`, http.StatusForbidden},
		// Its own seat, but the CLI session of an agent on the hub: resolved, then found to be another machine's.
		{"another machine's CLI session", `{"seat":"@pc2/cwd:/x","session":"cli-fox"}`, http.StatusNoContent},
	} {
		if code, body := hook(tc.body); code != tc.want || strings.Contains(body, "for fox") {
			t.Errorf("%s: %d %q, want %d and nothing of fox's", tc.name, code, body, tc.want)
		}
	}
	// fox's message is still there for fox, and owl's hook gets owl's.
	if rec := as(f.srv, testToken, http.MethodPost, "/v1/hook", nil, `{"session":"cli-fox"}`); rec.Body.String() != "pm owl: for fox" {
		t.Errorf("fox's hook: %d %q", rec.Code, rec.Body.String())
	}
	if code, body := hook(`{"seat":"@pc2/tmux:%1","session":"cli-owl"}`); code != http.StatusOK || body != "pm fox: for owl" {
		t.Errorf("owl's hook: %d %q", code, body)
	}
}

// Revoking a machine's token puts it out at once: the token is refused and its sessions are gone. A reload reads
// the tokens again, so a rotated hub token replaces the old one without a restart.
func TestTokensAreReloadedAndARevokedMachineIsOut(t *testing.T) {
	f, pc2 := machineFixture(t)
	_, spoke := attachAs(f, pc2, "@pc2/tmux:%1")
	_, hub := attachAs(f, testToken, "@pc1/tmux:%1")

	hubToken, machines := testToken, map[string]string{config.HashToken(pc2): "pc2"}
	var loadErr error
	f.srv.loadTokens = func() (string, map[string]string, error) { return hubToken, machines, loadErr }

	// Revoke pc2: after the reload its token is unknown and its session does not exist, the hub's is untouched.
	machines = map[string]string{}
	if rec := as(f.srv, testToken, http.MethodPost, "/v1/reload", nil, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("reload: %d %q", rec.Code, rec.Body.String())
	}
	if code, _ := callAs(f, pc2, spoke, "irc_who", `{}`); code != http.StatusUnauthorized {
		t.Errorf("the revoked token: %d, want 401", code)
	}
	f.srv.mu.Lock()
	_, spokeLives := f.srv.sessions[spoke]
	_, hubLives := f.srv.sessions[hub]
	f.srv.mu.Unlock()
	if spokeLives || !hubLives {
		t.Errorf("after the revoke: the machine's session lives %v, the hub's %v", spokeLives, hubLives)
	}

	// Rotate the hub token: the reload is asked with the old one, which then stops working.
	hubToken = strings.Repeat("ef", 32)
	if rec := as(f.srv, testToken, http.MethodPost, "/v1/reload", nil, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("reload for the rotation: %d", rec.Code)
	}
	if rec := as(f.srv, testToken, http.MethodGet, "/v1/status", nil, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("the old hub token: %d, want 401", rec.Code)
	}
	if code, _ := callAs(f, hubToken, hub, "irc_who", `{}`); code != http.StatusOK {
		t.Errorf("the new hub token with the session opened under the old one: %d, want 200", code)
	}
	// Token files that cannot be read keep the tokens in force.
	loadErr = errors.New("machines.json: unexpected end of JSON input")
	if rec := as(f.srv, hubToken, http.MethodPost, "/v1/reload", nil, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("reload with unreadable tokens: %d, want 400", rec.Code)
	}
	if rec := as(f.srv, hubToken, http.MethodGet, "/v1/status", nil, ""); rec.Code != http.StatusOK {
		t.Errorf("after a failed reload the token in force: %d", rec.Code)
	}
}

// A shim whose session is refused initializes again and still sends the old session's id. The old session is the
// same agent's: it is retired, so the agent keeps its seat and its name. Before, the old session (still watched)
// counted as another live agent in the seat, and the agent came back without a name (found by rotating a token
// while agents ran: mairc-edl). A session id with another seat retires nothing.
func TestAShimThatInitializesAgainKeepsItsName(t *testing.T) {
	f := boundFixture(t)
	const seat = "@pc/tmux:%1"
	first, _ := attach(t, f, seat, 100)
	if got := call(t, f.srv, first, "irc_name", `{"name":"fox"}`); got != "you are fox" {
		t.Fatal(got)
	}
	stop := watching(t, f, first) // the shim is alive and watching: the session is known to be live
	defer stop()

	again := do(f.srv, http.MethodPost, "/mcp", map[string]string{
		headerSeat: seat, headerWatch: "1", headerSession: first,
	}, initializeBody)
	second := again.Header().Get(headerSession)
	if again.Code != http.StatusOK || again.Header().Get(headerSeat) != seat || sessionName(f, second) != "fox" {
		t.Fatalf("initializing again: %d, seat %q, name %q; want the same seat and fox",
			again.Code, again.Header().Get(headerSeat), sessionName(f, second))
	}
	if got := sessionName(f, first); got != "<no session>" {
		t.Fatalf("the old session is still there as %q", got)
	}

	// Another agent that somehow presents that id from another seat retires nothing and gets no name.
	other := do(f.srv, http.MethodPost, "/mcp", map[string]string{
		headerSeat: "@pc/tmux:%2", headerWatch: "1", headerSession: second,
	}, initializeBody)
	if got := sessionName(f, other.Header().Get(headerSession)); got != "" {
		t.Fatalf("a session id from another seat gave the name %q", got)
	}
	if got := sessionName(f, second); got != "fox" {
		t.Fatalf("fox's session was retired by another seat: %q", got)
	}
}
