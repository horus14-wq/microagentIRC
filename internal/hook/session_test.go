package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sent runs one hook check against a fake daemon and returns the request it made.
func sent(t *testing.T, o Options, stdin string) request {
	t.Helper()
	srv, daemon := newDaemon(t, http.StatusNoContent, "")
	o.BaseURL = srv.URL
	if stdin != "" {
		o.Stdin = strings.NewReader(stdin)
	}
	var out bytes.Buffer
	o.Stdout = &out
	if code := Run(context.Background(), o); code != 0 || out.Len() != 0 {
		t.Fatalf("Run = %d with output %q; a 204 is silent", code, out.String())
	}
	select {
	case got := <-daemon.got:
		return got.body
	default:
		t.Fatal("the hook made no request")
		return request{}
	}
}

func TestSessionIDComesFromStdinThenTheEnvironment(t *testing.T) {
	base := opts("", nil)
	tests := []struct {
		name    string
		env     string // Options.Session, what the caller read from the environment
		stdin   string
		want    string
		wantEvt string
	}{
		{"none", "", `{"hook_event_name":"PostToolUse"}`, "", "PostToolUse"},
		{"session_id", "", `{"hook_event_name":"PostToolUse","session_id":"s-snake"}`, "s-snake", "PostToolUse"},
		{"sessionId", "", `{"hook_event_name":"PostToolUse","sessionId":"s-camel"}`, "s-camel", "PostToolUse"},
		{"conversationId", "", `{"conversationId":"s-conv"}`, "s-conv", ""},
		{"session_id is tried first", "", `{"session_id":"first","sessionId":"second"}`, "first", ""},
		{"the environment alone", "env-1", ``, "env-1", ""},
		{"stdin wins over the environment", "env-1", `{"session_id":"stdin-1"}`, "stdin-1", ""},
		{"an invalid stdin id falls back to the environment", "env-1", `{"session_id":"has space"}`, "env-1", ""},
		{"an invalid id with no fallback is dropped", "", `{"session_id":"bad\u0001id"}`, "", ""},
		{"a field of another type costs only itself", "", `{"hook_event_name":"Stop","session_id":7,"sessionId":"ok"}`, "ok", "Stop"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := base
			o.Session = tc.env
			got := sent(t, o, tc.stdin)
			if got.Session != tc.want || got.Event != tc.wantEvt {
				t.Fatalf("session %q event %q, want %q and %q", got.Session, got.Event, tc.want, tc.wantEvt)
			}
		})
	}
}

func TestTheNameIsClaimedOnlyFromAnIRCNameResult(t *testing.T) {
	post := func(tool, response string) string {
		return `{"hook_event_name":"PostToolUse","session_id":"s-1","tool_name":"` + tool + `","tool_response":` +
			response + `}`
	}
	tests := []struct {
		name  string
		stdin string
		want  string
	}{
		{"a plain string result", post("mcp__mairc__irc_name", `"you are greenfox1"`), "greenfox1"},
		{"content blocks", post("mcp__mairc__irc_name", `[{"type":"text","text":"you are reddog7"}]`), "reddog7"},
		{"an object with an output field", post("irc_name", `{"success":true,"output":"you are teal-owl_2","error":null}`), "teal-owl_2"},
		{"nested content", post("mairc.irc_name", `{"content":[{"type":"text","text":"you are fox"}],"isError":false}`), "fox"},
		{"surrounding whitespace", post("irc_name", `"  you are greenfox1\n"`), "greenfox1"},
		{"another tool is never a claim", post("mcp__mairc__irc_send", `"you are greenfox1"`), ""},
		{"an error result is not a claim", post("irc_name", `"name taken"`), ""},
		{"more text is not the exact form", post("irc_name", `"you are greenfox1 now"`), ""},
		{"an invalid name", post("irc_name", `"you are Room"`), ""},
		{"a reserved name", post("irc_name", `"you are room"`), ""},
		{"no result", `{"hook_event_name":"PostToolUse","tool_name":"irc_name"}`, ""},
		{"a result that is not JSON text", post("irc_name", `12`), ""},
		{"too deep to be a tool result", post("irc_name", `[[[[[["you are greenfox1"]]]]]]`), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sent(t, opts("", nil), tc.stdin).Claimed; got != tc.want {
				t.Fatalf("claimed %q, want %q", got, tc.want)
			}
		})
	}
}

// An agent CLI that passes neither a session id nor a tool result sends exactly the body it sent before those
// fields existed.
func TestTheRequestIsUnchangedWithoutTheNewFacts(t *testing.T) {
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	o := opts(srv.URL, io.Discard)
	o.Stdin = strings.NewReader(`{"hook_event_name":"PostToolUse"}`)
	Run(context.Background(), o)
	if want := `{"pids":[4321,4000,1],"seat":"seat-a","name":"greenfox1","event":"PostToolUse"}`; string(raw) != want {
		t.Fatalf("body %s\nwant %s", raw, want)
	}

	o = opts(srv.URL, io.Discard)
	o.Name = ""
	o.Stdin = strings.NewReader(
		`{"hook_event_name":"PostToolUse","sessionId":"s-9","tool_name":"mcp__mairc__irc_name","tool_response":"you are fox"}`)
	Run(context.Background(), o)
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["session"] != "s-9" || got["claimed"] != "fox" {
		t.Fatalf("with a session and an irc_name result the body carries both: %s", raw)
	}
}
