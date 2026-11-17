package daemon

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"testing"
)

// initializeAs attaches a shim that reported the agent CLI session hookSession.
func initializeAs(t *testing.T, h http.Handler, seat string, pid int, hookSession string) string {
	t.Helper()
	headers := map[string]string{headerSeat: seat, headerPid: strconv.Itoa(pid), headerHookSession: hookSession}
	rec := do(h, http.MethodPost, "/mcp", headers, initializeBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("initialize: %d %q", rec.Code, rec.Body.String())
	}
	return rec.Header().Get(headerSession)
}

// mairc-byd: Prime Agent has no hooks; the daemon's bridge hands each named Prime Agent session what a hook would get,
// once, with `prime-agent send`. Nothing waiting sends nothing; an unnamed session or one not under Prime Agent is
// never a target.
func TestTheBridgeDeliversToAPrimeAgentAgentOnce(t *testing.T) {
	f := newFixture(t, testSettings())
	primo := initializeAs(t, f.srv, "seat-p", 600, "prime-agent:abc123")
	if got := call(t, f.srv, primo, "irc_name", `{"name":"primo"}`); got != "you are primo" {
		t.Fatal(got)
	}
	initializeAs(t, f.srv, "seat-u", 602, "prime-agent:unnamed9") // a Prime Agent agent with no name yet
	quinn := initialize(t, f.srv, "seat-q", 601)
	if got := call(t, f.srv, quinn, "irc_name", `{"name":"quinn"}`); got != "you are quinn" {
		t.Fatal(got)
	}
	type sent struct{ agent, text string }
	var sends []sent
	send := func(_ context.Context, agent, text string) error {
		sends = append(sends, sent{agent, text})
		return nil
	}

	f.srv.bridgeOnce(t.Context(), send)
	if len(sends) != 0 {
		t.Fatalf("nothing waiting, yet sent %v", sends)
	}
	if got := call(t, f.srv, quinn, "irc_send", `{"text":"hello","to":"primo"}`); got != "sent" {
		t.Fatal(got)
	}
	f.srv.bridgeOnce(t.Context(), send)
	want := []sent{{"abc123", "irc:\npm quinn: hello"}}
	if !slices.Equal(sends, want) {
		t.Fatalf("sent %v, want %v", sends, want)
	}
	f.srv.bridgeOnce(t.Context(), send)
	if !slices.Equal(sends, want) {
		t.Fatalf("delivered twice: %v", sends)
	}
	if targets := f.srv.bridgeTargets(); len(targets) != 1 || targets["primo"] != "abc123" {
		t.Errorf("targets %v, want only primo", targets)
	}
}
