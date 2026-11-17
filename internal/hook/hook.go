// Package hook implements `mairc hook`, the program an agent CLI runs after each tool call. It asks the daemon
// whether anything is waiting for this agent. When nothing is, it prints nothing and exits 0, so the model spends
// zero tokens; when something is, it prints only the new lines, in the form the agent CLI accepts.
//
// The hook is silent on every failure (no stdout, no stderr), never blocks past its deadline, never starts the daemon
// and never retries: a hook must never fail or slow the agent's turn.
package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxStdin bounds how much of the agent CLI's hook JSON is read to find the event name.
const maxStdin = 64 << 10

// joinLine is the daemon's one line for an unnamed agent at session start. It is printed as it is.
const joinLine = "irc: call irc_name to join"

// Options is everything one hook check needs. The caller (`cmd/mairc`) resolves the settings, token and identity.
type Options struct {
	BaseURL string        // e.g. "http://127.0.0.1:4747"
	Token   string        // the daemon's bearer token
	Timeout time.Duration // the whole request's deadline, from Settings.HookTimeoutMS
	Client  string        // "claude" or "plain"
	Event   string        // --event; empty means: take hook_event_name from the stdin JSON
	Name    string        // --name or $MAIRC_NAME; may be empty
	Seat    string        // the agent's seat
	PIDs    []int         // ancestor process ids, nearest first
	Stdin   io.Reader     // the hook JSON from the agent CLI; may be nil
	Stdout  io.Writer     // where the delivery is printed
	Host    string        // the CLI actually running the hook (HostFrom): "", HostGrok or HostDevin
}

// The CLIs that run Claude Code's hooks but read hook output differently (docs/compat.md).
const (
	HostGrok  = "grok"
	HostDevin = "devin"
)

// HostFrom names the CLI running the hook from the environment it sets for hooks: Grok sets GROK_HOOK_EVENT for every
// hook it runs, Devin sets DEVIN_PROJECT_DIR (docs/compat.md). CLAUDE_* variables are not used: Devin seats inherit
// them from a Claude Code parent. GROK_HOOK_EVENT wins because it is set per hook run, not inherited by a shell.
func HostFrom(getenv func(string) string) string {
	switch {
	case getenv("GROK_HOOK_EVENT") != "":
		return HostGrok
	case getenv("DEVIN_PROJECT_DIR") != "":
		return HostDevin
	}
	return ""
}

// hostDelivers reports whether the host CLI puts this event's hook output in front of the model. When it does not,
// the hook must not ask the daemon: the daemon hands a delivery over when asked, and a dropped delivery is a lost
// message.
//   - Grok: only PreToolUse/PostToolUse JSON additionalContext; UserPromptSubmit stdout is discarded, SessionStart
//     output is a user-only systemMessage (docs/compat.md, Grok (c)).
//   - Devin: JSON additionalContext on SessionStart, UserPromptSubmit and PostToolUse; plain stdout is dropped
//     (docs/compat.md, Devin (c), live test).
func hostDelivers(host, event string) bool {
	switch host {
	case HostGrok:
		return event == "PostToolUse" || event == "PreToolUse"
	case HostDevin:
		return event == "SessionStart" || event == "UserPromptSubmit" || event == "PostToolUse"
	}
	return true
}

// request is the body of POST /v1/hook.
type request struct {
	PIDs  []int  `json:"pids"`
	Seat  string `json:"seat"`
	Name  string `json:"name"`
	Event string `json:"event"`
}

// Run performs one hook check. It always returns 0: a hook must never fail the agent's turn.
func Run(ctx context.Context, o Options) int {
	event := o.Event
	if event == "" {
		event = eventFromStdin(o.Stdin)
	}
	if !hostDelivers(o.Host, event) {
		return 0
	}
	body, ok := ask(ctx, o, event)
	if !ok || o.Stdout == nil {
		return 0
	}
	client := o.Client
	if o.Host != "" {
		client = "json" // Grok and Devin read only the JSON form, on every event they deliver
	}
	_, _ = io.WriteString(o.Stdout, format(client, event, body))
	return 0
}

// eventFromStdin reads at most maxStdin bytes and returns the string hook_event_name of a JSON object, or "" for
// anything else (nil, empty, not JSON, not an object, a non-string name, a read error). A nil reader is not read, so
// the hook never waits on a terminal.
func eventFromStdin(stdin io.Reader) string {
	if stdin == nil {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, maxStdin))
	if err != nil || len(raw) == 0 {
		return ""
	}
	var v struct {
		HookEventName string `json:"hook_event_name"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	return v.HookEventName
}

// ask sends one POST /v1/hook under one total deadline (connection, request and body) and returns the body of a 200
// that has one. Every other outcome (204, any other status, an empty body, a timeout, a refused connection, any
// error) is "nothing to deliver".
func ask(ctx context.Context, o Options, event string) (string, bool) {
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	pids := o.PIDs
	if pids == nil {
		pids = []int{}
	}
	payload, err := json.Marshal(request{PIDs: pids, Seat: o.Seat, Name: o.Name, Event: event})
	if err != nil {
		return "", false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(o.BaseURL, "/")+"/v1/hook", bytes.NewReader(payload))
	if err != nil {
		return "", false
	}
	req.Header.Set("Authorization", "Bearer "+o.Token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil || len(body) == 0 {
		return "", false
	}
	return string(body), true
}

// claudeOutput is the JSON Claude Code reads from a hook's stdout for events other than UserPromptSubmit and
// SessionStart.
type claudeOutput struct {
	HookSpecificOutput claudeSpecific `json:"hookSpecificOutput"`
}

type claudeSpecific struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// format renders a delivery for the agent CLI.
//
// Claude Code hooks (https://code.claude.com/docs/en/hooks; docs.claude.com redirects there), verified 17 Nov 2026:
//   - "UserPromptSubmit, UserPromptExpansion, SessionStart, and PostModelSwitch: Claude Code adds plain-text stdout as
//     context that Claude can see and act on." This bead prints plain text for UserPromptSubmit and SessionStart.
//   - Other events (PostToolUse and the like): context reaches the model through
//     `{"hookSpecificOutput":{"hookEventName":"<event>","additionalContext":"..."}}`; the object requires
//     hookEventName set to the event name. additionalContext is accepted for every event above too, so
//     UserPromptExpansion and PostModelSwitch, which get the JSON form here, still deliver.
//
// client "json" (Grok and Devin, set by Run) is the JSON form on every event, the join line included: those CLIs
// drop plain stdout.
func format(client, event, body string) string {
	text := "irc:\n" + body
	if body == joinLine {
		if client != "json" {
			return body + "\n"
		}
		text = body
	}
	if client == "json" || client == "claude" && event != "UserPromptSubmit" && event != "SessionStart" {
		var out bytes.Buffer
		enc := json.NewEncoder(&out)
		enc.SetEscapeHTML(false)
		if enc.Encode(claudeOutput{HookSpecificOutput: claudeSpecific{
			HookEventName: event, AdditionalContext: text,
		}}) != nil {
			return ""
		}
		return out.String()
	}
	return text + "\n"
}
