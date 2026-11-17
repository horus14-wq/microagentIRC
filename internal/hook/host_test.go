package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The same `mairc hook --client claude` entry in ~/.claude/settings.json also runs under Grok and Devin
// (docs/compat.md, "Claude's hooks are imported by other CLIs"). The daemon hands a delivery over when it is asked,
// so the hook must ask only on events whose output reaches that CLI's model, and must print the form that CLI reads.
// Otherwise the messages are marked delivered and the model never sees them.
func TestHookAsksOnlyWhenTheHostCLIDeliversAndPrintsItsForm(t *testing.T) {
	cases := []struct {
		host, event string
		wantAsk     bool
		wantJSON    bool
	}{
		// Claude Code (no other host detected): unchanged.
		{"", "SessionStart", true, false},
		{"", "UserPromptSubmit", true, false},
		{"", "PostToolUse", true, true},
		// Grok: only PreToolUse/PostToolUse JSON additionalContext reaches the model; UserPromptSubmit stdout is
		// discarded and SessionStart becomes a user-only systemMessage [compat.md, Grok (c)].
		{HostGrok, "SessionStart", false, false},
		{HostGrok, "UserPromptSubmit", false, false},
		{HostGrok, "PostToolUse", true, true},
		// Devin: JSON additionalContext on SessionStart, UserPromptSubmit and PostToolUse; plain stdout is dropped
		// [compat.md, Devin (c), live test].
		{HostDevin, "SessionStart", true, true},
		{HostDevin, "UserPromptSubmit", true, true},
		{HostDevin, "PostToolUse", true, true},
		{HostDevin, "Stop", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.host+"/"+tc.event, func(t *testing.T) {
			srv, daemon := newDaemon(t, http.StatusOK, "pm bluecat2: hi")
			var out bytes.Buffer
			o := opts(srv.URL, &out)
			o.Client, o.Event, o.Host = "claude", tc.event, tc.host
			Run(context.Background(), o)
			asked := len(daemon.got) == 1
			if asked != tc.wantAsk {
				t.Fatalf("asked the daemon = %v, want %v (an unread message would be consumed and lost)", asked, tc.wantAsk)
			}
			if !tc.wantAsk {
				if out.Len() != 0 {
					t.Fatalf("printed %q on an event the host drops", out.String())
				}
				return
			}
			var parsed claudeOutput
			isJSON := json.Unmarshal(out.Bytes(), &parsed) == nil
			if isJSON != tc.wantJSON {
				t.Fatalf("output %q: JSON = %v, want %v", out.String(), isJSON, tc.wantJSON)
			}
			if isJSON && (parsed.HookSpecificOutput.HookEventName != tc.event ||
				parsed.HookSpecificOutput.AdditionalContext != "irc:\npm bluecat2: hi") {
				t.Fatalf("JSON = %+v", parsed)
			}
			if !isJSON && out.String() != "irc:\npm bluecat2: hi\n" {
				t.Fatalf("plain = %q", out.String())
			}
		})
	}
}

// The join nudge reaches Devin and Grok only in the JSON form as well.
func TestJoinLineUsesTheHostForm(t *testing.T) {
	for _, host := range []string{HostGrok, HostDevin} {
		srv, _ := newDaemon(t, http.StatusOK, joinLine)
		var out bytes.Buffer
		o := opts(srv.URL, &out)
		o.Client, o.Event, o.Host = "claude", "PostToolUse", host
		Run(context.Background(), o)
		if !strings.Contains(out.String(), `"additionalContext":"`+joinLine+`"`) {
			t.Fatalf("%s join line = %q", host, out.String())
		}
	}
}

// HostFrom names the CLI running the hook from the environment it sets for hooks: GROK_HOOK_EVENT is set by Grok
// for every hook it runs; DEVIN_PROJECT_DIR by Devin [compat.md]. Anything else is Claude Code or a plain client.
func TestHostFromEnvironment(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{}, ""},
		{map[string]string{"CLAUDE_CODE_SESSION_ID": "x"}, ""},
		{map[string]string{"GROK_HOOK_EVENT": "PostToolUse"}, HostGrok},
		{map[string]string{"DEVIN_PROJECT_DIR": "/w", "CLAUDE_CODE_SESSION_ID": "inherited"}, HostDevin},
		{map[string]string{"GROK_HOOK_EVENT": "PostToolUse", "DEVIN_PROJECT_DIR": "/w"}, HostGrok},
		{map[string]string{"DEVIN_PROJECT_DIR": ""}, ""},
	}
	for _, tc := range cases {
		if got := HostFrom(func(k string) string { return tc.env[k] }); got != tc.want {
			t.Fatalf("HostFrom(%v) = %q, want %q", tc.env, got, tc.want)
		}
	}
}
