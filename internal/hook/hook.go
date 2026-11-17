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
	"slices"
	"strings"
	"time"

	"github.com/horus14-wq/microagentIRC/internal/identity"
	"github.com/horus14-wq/microagentIRC/internal/names"
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
	Session string        // the CLI session id from the environment (identity.SessionFromEnv); stdin's wins

	claimed string // the name read from an irc_name result on stdin; set by Run
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

// request is the body of POST /v1/hook. Session and Claimed are left out when empty, so an agent CLI that gives
// the hook neither sends exactly what it sent before they existed.
type request struct {
	PIDs    []int  `json:"pids"`
	Seat    string `json:"seat"`
	Name    string `json:"name"`
	Event   string `json:"event"`
	Session string `json:"session,omitempty"`
	Claimed string `json:"claimed,omitempty"`
}

// Run performs one hook check. It always returns 0: a hook must never fail the agent's turn.
func Run(ctx context.Context, o Options) int {
	in := readStdin(o.Stdin)
	event := o.Event
	if event == "" {
		event = in.event
	}
	if !hostDelivers(o.Host, event) {
		return 0
	}
	// The session id the agent CLI passed on stdin wins over the one in the environment: stdin is this event's.
	if in.session != "" {
		o.Session = in.session
	}
	o.claimed = in.claimed
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

// stdinFacts is what the hook takes from the JSON the agent CLI passes on stdin.
type stdinFacts struct {
	event   string // hook_event_name
	session string // the CLI's session id, when valid
	claimed string // the name the agent was just given by irc_name, when this event follows that call
}

// readStdin reads at most maxStdin bytes and returns what a JSON object there says; every field is "" for anything
// else (nil, empty, not JSON, not an object, a field of another type, a read error). A nil reader is not read, so
// the hook never waits on a terminal.
//
// Field names, from docs/compat.md (read from each CLI's documentation or a recorded hook input):
//   - the session id: `session_id` (Claude Code, Codex, Devin, and Grok's Claude-style alias), `sessionId`
//     (Grok, Gemini CLI), `conversationId` (Antigravity);
//   - after a tool call (`PostToolUse`): `tool_name` and `tool_response` (Claude Code, Codex, Devin, Grok's alias).
func readStdin(stdin io.Reader) stdinFacts {
	if stdin == nil {
		return stdinFacts{}
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, maxStdin))
	if err != nil || len(raw) == 0 {
		return stdinFacts{}
	}
	var v struct {
		HookEventName  string          `json:"hook_event_name"`
		SessionID      string          `json:"session_id"`
		SessionIDCamel string          `json:"sessionId"`
		ConversationID string          `json:"conversationId"`
		ToolName       string          `json:"tool_name"`
		ToolResponse   json.RawMessage `json:"tool_response"`
	}
	if json.Unmarshal(raw, &v) != nil {
		// One field of an unexpected type must not cost the others: read each on its own.
		var loose map[string]json.RawMessage
		if json.Unmarshal(raw, &loose) != nil {
			return stdinFacts{}
		}
		text := func(key string) string {
			var s string
			_ = json.Unmarshal(loose[key], &s)
			return s
		}
		v.HookEventName, v.SessionID = text("hook_event_name"), text("session_id")
		v.SessionIDCamel, v.ConversationID = text("sessionId"), text("conversationId")
		v.ToolName, v.ToolResponse = text("tool_name"), loose["tool_response"]
	}
	facts := stdinFacts{event: v.HookEventName}
	for _, id := range []string{v.SessionID, v.SessionIDCamel, v.ConversationID} {
		if identity.ValidSession(id) {
			facts.session = id
			break
		}
	}
	facts.claimed = claimedName(v.ToolName, v.ToolResponse)
	return facts
}

// namePrefix is the start of irc_name's result: "you are <name>".
const namePrefix = "you are "

// claimedName returns the name irc_name just gave the agent, when this hook event follows that tool call: the tool
// name is mairc's irc_name (isNameTool: agent CLIs prefix MCP tools, for example "mcp__mairc__irc_name") and its
// result holds a text that is exactly "you are <name>" with a valid name. Anything else is "". The daemon decides
// whether the claim is this agent's to make.
//
// The result's shape differs by CLI (a string, an object with a text or output field, a list of content blocks), so
// every string in it is looked at, to a small depth.
func claimedName(tool string, response json.RawMessage) string {
	if !isNameTool(tool) || len(response) == 0 {
		return ""
	}
	var v any
	if json.Unmarshal(response, &v) != nil {
		return ""
	}
	return findName(v, 4)
}

// isNameTool reports whether the tool name an agent CLI passed is mairc's irc_name: exactly "irc_name", or
// "irc_name" after a prefix that names the mairc server and ends in a separator ("mcp__mairc__irc_name",
// "mairc.irc_name", "mairc-irc_name"). Another server's tool that merely ends in the same letters is not.
func isNameTool(tool string) bool {
	const name = "irc_name"
	prefix, ok := strings.CutSuffix(tool, name)
	if !ok {
		return false
	}
	if prefix == "" {
		return true
	}
	last := prefix[len(prefix)-1]
	separated := last == '_' || last == '.' || last == '-' || last == '/' || last == ':'
	return separated && strings.Contains(strings.ToLower(prefix), "mairc")
}

func findName(v any, depth int) string {
	switch x := v.(type) {
	case string:
		if name, ok := strings.CutPrefix(strings.TrimSpace(x), namePrefix); ok && names.Valid(name) {
			return name
		}
	case []any:
		if depth == 0 {
			return ""
		}
		for _, item := range x {
			if name := findName(item, depth-1); name != "" {
				return name
			}
		}
	case map[string]any:
		if depth == 0 {
			return ""
		}
		// A fixed order, so the same input always gives the same answer.
		keys := make([]string, 0, len(x))
		for key := range x {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			if name := findName(x[key], depth-1); name != "" {
				return name
			}
		}
	}
	return ""
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
	payload, err := json.Marshal(request{
		PIDs: pids, Seat: o.Seat, Name: o.Name, Event: event, Session: o.Session, Claimed: o.claimed,
	})
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
