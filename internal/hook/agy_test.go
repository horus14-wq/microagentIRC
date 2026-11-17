package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// agyInput is what Antigravity passes a hook on stdin: camelCase, with the conversation id and no event name
// (https://antigravity.google/docs/hooks.md).
const agyInput = `{"conversationId":"0b6c2f6e-6f0a-4d6e-9f43-2a1d5a0c7e11","workspacePaths":["/w"],` +
	`"modelName":"m","lastUserInput":"hi","invocationNum":3}`

// Antigravity reads text only from JSON injectSteps, on SessionStart and PreInvocation. The delivery is one
// ephemeralMessage step holding the same text every other client gets, and the conversation id is the session.
func TestAgyDeliversThroughInjectSteps(t *testing.T) {
	for _, event := range []string{"PreInvocation", "SessionStart"} {
		t.Run(event, func(t *testing.T) {
			srv, daemon := newDaemon(t, http.StatusOK, "pm bluefox2: ready?")
			var out bytes.Buffer
			o := opts(srv.URL, &out)
			o.Client, o.Event, o.Stdin = ClientAgy, event, strings.NewReader(agyInput)
			if code := Run(context.Background(), o); code != 0 {
				t.Fatalf("Run = %d", code)
			}
			var got struct {
				InjectSteps []map[string]any `json:"injectSteps"`
			}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatalf("stdout %q is not JSON: %v", out.String(), err)
			}
			if len(got.InjectSteps) != 1 || len(got.InjectSteps[0]) != 1 ||
				got.InjectSteps[0]["ephemeralMessage"] != "irc:\npm bluefox2: ready?" {
				t.Fatalf("injectSteps = %v", got.InjectSteps)
			}
			sent := <-daemon.got
			if sent.body.Event != event || sent.body.Session != "0b6c2f6e-6f0a-4d6e-9f43-2a1d5a0c7e11" {
				t.Fatalf("request = %+v; want the event and the conversation id as the session", sent.body)
			}
		})
	}
}

// The join line for an unnamed agent goes through injectSteps too, without the "irc:" heading.
func TestAgyJoinLineIsInjected(t *testing.T) {
	srv, _ := newDaemon(t, http.StatusOK, joinLine)
	var out bytes.Buffer
	o := opts(srv.URL, &out)
	o.Client, o.Event = ClientAgy, "SessionStart"
	Run(context.Background(), o)
	want := `{"injectSteps":[{"ephemeralMessage":"` + joinLine + `"}]}` + "\n"
	if out.String() != want {
		t.Fatalf("stdout = %q, want %q", out.String(), want)
	}
}

// On any other event Antigravity drops what a hook prints, so the hook must not even ask: the daemon hands a
// delivery over when asked, and that message would be lost. An event Antigravity never names is treated the same.
func TestAgyAsksNothingOnOtherEvents(t *testing.T) {
	for _, event := range []string{"PostToolUse", "PreToolUse", "PostInvocation", "Stop", "UserPromptSubmit", ""} {
		t.Run("event "+event, func(t *testing.T) {
			srv, daemon := newDaemon(t, http.StatusOK, "pm bluefox2: ready?")
			var out bytes.Buffer
			o := opts(srv.URL, &out)
			o.Client, o.Event, o.Stdin = ClientAgy, event, strings.NewReader(agyInput)
			if code := Run(context.Background(), o); code != 0 || out.Len() != 0 {
				t.Fatalf("Run = %d, stdout %q; want 0 and nothing", code, out.String())
			}
			select {
			case sent := <-daemon.got:
				t.Fatalf("the daemon was asked: %+v", sent.body)
			default:
			}
		})
	}
}

// Nothing new prints nothing at all, as for every other client.
func TestAgyPrintsNothingWhenThereIsNothingNew(t *testing.T) {
	srv, _ := newDaemon(t, http.StatusNoContent, "")
	var out bytes.Buffer
	o := opts(srv.URL, &out)
	o.Client, o.Event = ClientAgy, "PreInvocation"
	if code := Run(context.Background(), o); code != 0 || out.Len() != 0 {
		t.Fatalf("Run = %d, stdout %q; want 0 and nothing", code, out.String())
	}
}

// Quotes, backslashes, newlines, markup and non-ASCII text survive as one valid JSON string, unescaped where JSON
// allows it.
func TestAgyJSONKeepsSpecialCharacters(t *testing.T) {
	body := "room amber: say \"hi\" \\ <b>&</b>\nroom jade: café ✓\ttab"
	srv, _ := newDaemon(t, http.StatusOK, body)
	var out bytes.Buffer
	o := opts(srv.URL, &out)
	o.Client, o.Event = ClientAgy, "PreInvocation"
	Run(context.Background(), o)
	var got agyOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stdout %q is not JSON: %v", out.String(), err)
	}
	if len(got.InjectSteps) != 1 || got.InjectSteps[0].EphemeralMessage != "irc:\n"+body {
		t.Fatalf("round trip = %+v", got)
	}
	htmlEscape := `\u` + "003c" // what encoding/json writes for "<" unless told not to
	if strings.Contains(out.String(), htmlEscape) || strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("stdout %q: markup must stay readable and the output must be one line", out.String())
	}
}
