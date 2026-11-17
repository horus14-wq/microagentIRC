// Package e2e runs the real mairc binary: a daemon, stdio shims as agents, and hook runs. Nothing is faked.
// Each test gets its own home directory and its own loopback port. There are no sleeps: waiting is polling with
// a deadline.
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// binary is the mairc binary built once for the whole run (TestMain).
var binary string //nolint:gochecknoglobals // set once in TestMain, before any test runs

const wait = 10 * time.Second

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	dir, err := os.MkdirTemp("", "mairc-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	binary = filepath.Join(dir, "mairc")
	if runtime.GOOS == "windows" {
		binary += ".exe" // Windows runs only files with an executable extension
	}
	build := exec.CommandContext(context.Background(), "go", "build", "-o", binary, "../cmd/mairc")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: build failed: %v\n%s", err, out)
		return 1
	}
	return m.Run()
}

// world is one installation: a home directory, a port and (usually) a running daemon.
type world struct {
	t      *testing.T
	home   string
	base   string
	daemon *exec.Cmd
}

// newWorld writes the settings (cooldowns off unless set) and starts the daemon.
func newWorld(t *testing.T, settings map[string]any) *world {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{"listen": addr, "private_cooldown_s": 0, "public_cooldown_s": 0}
	for key, value := range settings {
		config[key] = value
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, home: t.TempDir(), base: "http://" + addr}
	if err := os.WriteFile(filepath.Join(w.home, "config.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	w.startDaemon()
	t.Cleanup(w.stopDaemon)
	return w
}

func (w *world) env(extra ...string) []string {
	env := make([]string, 0, len(extra)+4)
	for _, kv := range os.Environ() {
		// The test's own terminal or tmux pane must not leak into an agent's seat or name.
		if strings.HasPrefix(kv, "MAIRC_") || strings.HasPrefix(kv, "TMUX") {
			continue
		}
		env = append(env, kv)
	}
	return append(append(env, "MAIRC_HOME="+w.home), extra...)
}

func (w *world) healthy() bool {
	ctx, cancel := context.WithTimeout(w.t.Context(), 500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.base+"/healthz", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

// until polls cond until it holds or the deadline passes.
func (w *world) until(what string, cond func() bool) {
	w.t.Helper()
	deadline := time.Now().Add(wait)
	for !cond() {
		if time.Now().After(deadline) {
			w.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond) //nolint:forbidigo // polling interval, not a wait for an outcome
	}
}

func (w *world) startDaemon() {
	w.t.Helper()
	w.daemon = exec.CommandContext(context.Background(), binary, "daemon")
	w.daemon.Env = w.env()
	ownGroup(w.daemon)
	var stderr bytes.Buffer
	w.daemon.Stderr = &stderr
	if err := w.daemon.Start(); err != nil {
		w.t.Fatal(err)
	}
	w.until("the daemon to answer /healthz", w.healthy)
}

// stopDaemon asks the daemon to stop (SIGTERM; a console break on Windows) and requires a clean exit within three
// seconds.
func (w *world) stopDaemon() {
	w.t.Helper()
	if w.daemon == nil {
		return
	}
	cmd := w.daemon
	w.daemon = nil
	if err := askToStop(cmd.Process); err != nil {
		w.t.Errorf("signal the daemon: %v", err)
		return
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			w.t.Errorf("the daemon did not exit cleanly: %v", err)
		}
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		w.t.Errorf("the daemon did not stop within 3 s of SIGTERM")
	}
	w.until("the daemon to stop answering", func() bool { return !w.healthy() })
}

func (w *world) token() string {
	w.t.Helper()
	raw, err := os.ReadFile(filepath.Join(w.home, "token"))
	if err != nil {
		w.t.Fatal(err)
	}
	return strings.TrimSpace(string(raw))
}

// agent is one `mairc stdio` process: an agent's MCP server.
type agent struct {
	w     *world
	seat  string
	cmd   *exec.Cmd
	in    io.WriteCloser
	lines chan []byte
	next  int
	name  string
}

// newAgent starts a shim for the seat and performs the MCP handshake.
func (w *world) newAgent(seat string) *agent {
	w.t.Helper()
	a := &agent{w: w, seat: seat, lines: make(chan []byte, 16)}
	a.cmd = exec.CommandContext(context.Background(), binary, "stdio")
	a.cmd.Env = w.env("MAIRC_SEAT=" + seat)
	var err error
	if a.in, err = a.cmd.StdinPipe(); err != nil {
		w.t.Fatal(err)
	}
	out, err := a.cmd.StdoutPipe()
	if err != nil {
		w.t.Fatal(err)
	}
	if err := a.cmd.Start(); err != nil {
		w.t.Fatal(err)
	}
	go func() {
		reader := bufio.NewReaderSize(out, 1<<20)
		for {
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 {
				a.lines <- line
			}
			if err != nil {
				close(a.lines)
				return
			}
		}
	}()
	w.t.Cleanup(a.close)
	a.request("initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "e2e", "version": "0"},
	})
	a.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return a
}

// close ends the shim the way an agent CLI does: it closes the server's stdin.
func (a *agent) close() {
	if a.cmd == nil {
		return
	}
	_ = a.in.Close()
	_ = a.cmd.Wait()
	a.cmd = nil
}

func (a *agent) send(msg map[string]any) {
	a.w.t.Helper()
	raw, err := json.Marshal(msg)
	if err != nil {
		a.w.t.Fatal(err)
	}
	if _, err := a.in.Write(append(raw, '\n')); err != nil {
		a.w.t.Fatal(err)
	}
}

// request sends one JSON-RPC request and returns its result object.
func (a *agent) request(method string, params any) json.RawMessage {
	a.w.t.Helper()
	a.next++
	a.send(map[string]any{"jsonrpc": "2.0", "id": a.next, "method": method, "params": params})
	select {
	case line, ok := <-a.lines:
		if !ok {
			a.w.t.Fatalf("%s: the shim closed its output", method)
		}
		var resp struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(line, &resp); err != nil {
			a.w.t.Fatalf("%s: not a JSON line: %q", method, line)
		}
		if resp.ID != a.next || resp.Error != nil {
			a.w.t.Fatalf("%s: unexpected response %s", method, line)
		}
		return resp.Result
	case <-time.After(wait):
		a.w.t.Fatalf("%s: no response within %s", method, wait)
		return nil
	}
}

// call runs one tool and returns its text and whether it is an error result.
func (a *agent) call(tool string, args map[string]any) (string, bool) {
	a.w.t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	raw := a.request("tools/call", map[string]any{"name": tool, "arguments": args})
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || len(result.Content) != 1 || result.Content[0].Type != "text" {
		a.w.t.Fatalf("%s: unexpected tool result %s", tool, raw)
	}
	return result.Content[0].Text, result.IsError
}

// ok runs a tool that must succeed and returns its text.
func (a *agent) ok(tool string, args map[string]any) string {
	a.w.t.Helper()
	text, isError := a.call(tool, args)
	if isError {
		a.w.t.Fatalf("%s %v: error result %q", tool, args, text)
	}
	return text
}

// join gives the agent a generated name.
func (a *agent) join() string {
	a.w.t.Helper()
	text := a.ok("irc_name", nil)
	name, found := strings.CutPrefix(text, "you are ")
	if !found || !regexp.MustCompile(`^[a-z]+[0-9]+$`).MatchString(name) {
		a.w.t.Fatalf("irc_name answered %q", text)
	}
	a.name = name
	return name
}

// hook runs `mairc hook` for the seat and returns what it printed and its exit code.
func (w *world) hook(seat string, args ...string) (stdout, stderr string, code int) {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(w.t.Context(), wait)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, append([]string{"hook"}, args...)...)
	cmd.Env = w.env("MAIRC_SEAT=" + seat)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		code = exit.ExitCode()
	default:
		w.t.Fatalf("hook: %v", err)
	}
	return out.String(), errOut.String(), code
}

// silent requires a hook run that printed nothing anywhere and exited 0.
func (w *world) silent(seat string, args ...string) {
	w.t.Helper()
	if out, errOut, code := w.hook(seat, args...); out != "" || errOut != "" || code != 0 {
		w.t.Fatalf("hook must be silent: stdout %q stderr %q exit %d", out, errOut, code)
	}
}

func TestRoundTrip(t *testing.T) {
	w := newWorld(t, nil)
	a, b := w.newAgent("seat-a"), w.newAgent("seat-b")

	listed := a.request("tools/list", map[string]any{})
	var tools struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(listed, &tools); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	if got := strings.Join(names, " "); got != "irc_name irc_send irc_read irc_who" {
		t.Fatalf("tools: %s", got)
	}
	if len(listed) > 1200 {
		t.Fatalf("tools/list is %d bytes, the budget is 1200", len(listed))
	}

	nameA, nameB := a.join(), b.join()
	if nameA == nameB {
		t.Fatalf("both agents were named %s", nameA)
	}
	if got := a.ok("irc_send", map[string]any{"text": "hello room"}); got != "sent" {
		t.Fatalf("room send answered %q", got)
	}
	if got := a.ok("irc_send", map[string]any{"text": "hi", "to": nameB}); got != "sent" {
		t.Fatalf("private send answered %q", got)
	}
	out, errOut, code := w.hook("seat-b", "--client", "plain")
	if want := "irc:\npm " + nameA + ": hi\n"; out != want || errOut != "" || code != 0 {
		t.Fatalf("B's hook: stdout %q stderr %q exit %d, want %q", out, errOut, code, want)
	}
	w.silent("seat-b", "--client", "plain")
	if who := a.ok("irc_who", nil); !strings.Contains(who, nameA) || !strings.Contains(who, nameB) {
		t.Fatalf("irc_who: %q", who)
	}
}

func TestNothingNewCostsNothing(t *testing.T) {
	w := newWorld(t, nil)
	w.newAgent("seat-b").join()
	for range 50 {
		w.silent("seat-b", "--client", "plain")
	}
	// The Claude Code form is just as silent, for both kinds of event.
	w.silent("seat-b", "--client", "claude", "--event", "PostToolUse")
	w.silent("seat-b", "--client", "claude", "--event", "UserPromptSubmit")
	// An agent that never joined gets nothing either, outside a session start.
	w.silent("seat-nobody", "--client", "plain")
}

func TestRoomLinesArriveEveryTenthEvent(t *testing.T) {
	w := newWorld(t, nil)
	a, b := w.newAgent("seat-a"), w.newAgent("seat-b")
	nameA := a.join()
	b.join()
	a.ok("irc_send", map[string]any{"text": "build is green"})
	for i := 1; i <= 9; i++ {
		if out, _, _ := w.hook("seat-b", "--client", "plain"); out != "" {
			t.Fatalf("event %d delivered %q; room lines are due at the tenth", i, out)
		}
	}
	if out, _, _ := w.hook("seat-b", "--client", "plain"); out != "irc:\nroom "+nameA+": build is green\n" {
		t.Fatalf("the tenth event printed %q", out)
	}
	// An agent's own room lines are never delivered back to it.
	b.ok("irc_send", map[string]any{"text": "thanks"})
	for range 10 {
		w.silent("seat-b", "--client", "plain")
	}
}

func TestLimitsAnswerVerbatim(t *testing.T) {
	w := newWorld(t, map[string]any{"public_cooldown_s": 60})
	a, b := w.newAgent("seat-a"), w.newAgent("seat-b")
	a.join()
	nameB := b.join()
	refused := func(args map[string]any) string {
		t.Helper()
		text, isError := a.call("irc_send", args)
		if !isError {
			t.Fatalf("irc_send %v succeeded with %q", args, text)
		}
		return text
	}
	if got := refused(map[string]any{"text": strings.Repeat("x", 257)}); got != "too long 257/256" {
		t.Fatalf("over the length limit: %q", got)
	}
	a.ok("irc_send", map[string]any{"text": "first"})
	if got := refused(map[string]any{"text": "second"}); !regexp.MustCompile(`^cooldown \d+s$`).MatchString(got) {
		t.Fatalf("inside the cooldown: %q", got)
	}
	if got := refused(map[string]any{"text": "hi", "to": "nobody"}); got != "unknown: nobody" {
		t.Fatalf("unknown recipient: %q", got)
	}
	a.ok("irc_send", map[string]any{"text": "hi", "to": nameB})

	// `mairc config set` saves the setting and reloads the running daemon.
	ctx, cancel := context.WithTimeout(t.Context(), wait)
	defer cancel()
	set := exec.CommandContext(ctx, binary, "config", "set", "private_enabled", "false")
	set.Env = w.env()
	if out, err := set.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "private_enabled = false" {
		t.Fatalf("config set: %v, output %q", err, out)
	}
	if got := refused(map[string]any{"text": "again", "to": nameB}); got != "pm off" {
		t.Fatalf("private messaging off: %q", got)
	}
}

func TestANameIsRememberedForItsSeat(t *testing.T) {
	w := newWorld(t, nil)
	b := w.newAgent("seat-b")
	name := b.join()
	b.close()
	again := w.newAgent("seat-b")
	if got := again.ok("irc_read", nil); got != "nothing new" {
		t.Fatalf("irc_read in the same seat, without irc_name: %q", got)
	}
	if got := again.ok("irc_name", nil); got != "you are "+name {
		t.Fatalf("the seat's name: %q, want %s", got, name)
	}
}

func TestADaemonRestartIsSurvived(t *testing.T) {
	w := newWorld(t, nil)
	a := w.newAgent("seat-a")
	name := a.join()
	a.ok("irc_send", map[string]any{"text": "before the restart"})
	w.stopDaemon()
	w.startDaemon()
	// The shim is still up: it initializes again by itself and the seat still has its name.
	if got := a.ok("irc_read", map[string]any{"n": 10}); got != "room "+name+": before the restart" {
		t.Fatalf("after the restart irc_read answered %q", got)
	}
	a.ok("irc_send", map[string]any{"text": "after the restart"})
}

func TestWithoutADaemonTheHookIsSilentAndFast(t *testing.T) {
	w := newWorld(t, nil)
	w.newAgent("seat-b").join()
	w.stopDaemon()
	started := time.Now()
	w.silent("seat-b", "--client", "plain")
	if took := time.Since(started); took > time.Second {
		t.Fatalf("the hook took %s with no daemon; it must give up within a second", took)
	}
	if w.healthy() {
		t.Fatal("a hook must never start the daemon")
	}
}

func TestClaudeCodeHookOutput(t *testing.T) {
	w := newWorld(t, nil)
	a, b := w.newAgent("seat-a"), w.newAgent("seat-b")
	nameA := a.join()
	nameB := b.join()

	a.ok("irc_send", map[string]any{"text": `say "hi" <now>`, "to": nameB})
	out, errOut, code := w.hook("seat-b", "--client", "claude", "--event", "PostToolUse")
	if errOut != "" || code != 0 || strings.Count(out, "\n") != 1 {
		t.Fatalf("PostToolUse: stdout %q stderr %q exit %d", out, errOut, code)
	}
	var wrapped struct {
		Output struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &wrapped); err != nil {
		t.Fatalf("PostToolUse output is not JSON: %q", out)
	}
	if want := "irc:\npm " + nameA + `: say "hi" <now>`; wrapped.Output.Event != "PostToolUse" ||
		wrapped.Output.Context != want {
		t.Fatalf("PostToolUse: %+v, want context %q", wrapped.Output, want)
	}

	a.ok("irc_send", map[string]any{"text": "second", "to": nameB})
	out, _, _ = w.hook("seat-b", "--client", "claude", "--event", "UserPromptSubmit")
	if want := "irc:\npm " + nameA + ": second\n"; out != want {
		t.Fatalf("UserPromptSubmit printed %q, want %q", out, want)
	}

	// The event name also comes from the hook JSON the agent CLI passes on stdin.
	a.ok("irc_send", map[string]any{"text": "third", "to": nameB})
	ctx, cancel := context.WithTimeout(t.Context(), wait)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "hook", "--client", "claude")
	cmd.Env = w.env("MAIRC_SEAT=seat-b")
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"UserPromptSubmit","session_id":"x"}`)
	fromStdin, err := cmd.Output()
	if err != nil || string(fromStdin) != "irc:\npm "+nameA+": third\n" {
		t.Fatalf("event from stdin JSON: %q (%v)", fromStdin, err)
	}

	// An agent with no name is told once, at session start, how to join.
	if out, _, _ := w.hook("seat-new", "--client", "claude", "--event", "SessionStart"); out != "irc: call irc_name to join\n" {
		t.Fatalf("SessionStart without a name printed %q", out)
	}
}

func TestOnlyTheNewestMessagesAreKept(t *testing.T) {
	w := newWorld(t, map[string]any{"keep_messages": 5})
	a := w.newAgent("seat-a")
	name := a.join()
	for i := 1; i <= 8; i++ {
		a.ok("irc_send", map[string]any{"text": fmt.Sprintf("line %d", i)})
	}
	want := make([]string, 0, 5)
	for i := 4; i <= 8; i++ {
		want = append(want, fmt.Sprintf("room %s: line %d", name, i))
	}
	if got := a.ok("irc_read", map[string]any{"n": 100}); got != strings.Join(want, "\n") {
		t.Fatalf("irc_read after 8 sends with keep_messages=5:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
}

func TestStreamableHTTP(t *testing.T) {
	w := newWorld(t, nil)
	// post returns the status, the session id the daemon issued (if any) and the body.
	post := func(token, session, body string) (int, string, string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), wait)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.base+"/mcp", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, resp.Header.Get("Mcp-Session-Id"), string(raw)
	}
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{}}}`
	if status, _, _ := post("", "", initialize); status != http.StatusUnauthorized {
		t.Fatalf("without the token: %d, want 401", status)
	}
	status, session, body := post(w.token(), "", initialize)
	if status != http.StatusOK || session == "" || !strings.Contains(body, `"protocolVersion":"2025-06-18"`) {
		t.Fatalf("initialize: %d session %q body %s", status, session, body)
	}
	status, _, body = post(w.token(), session,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"irc_name","arguments":{"name":"webfox"}}}`)
	if status != http.StatusOK || !strings.Contains(body, `"text":"you are webfox"`) {
		t.Fatalf("irc_name over HTTP: %d %s", status, body)
	}
	// An HTTP agent has no shim: its hook names it explicitly.
	shim := w.newAgent("seat-a")
	shim.join()
	shim.ok("irc_send", map[string]any{"text": "hello over http", "to": "webfox"})
	if out, _, _ := w.hook("seat-unrelated", "--client", "plain", "--name", "webfox"); out != "irc:\npm "+shim.name+": hello over http\n" {
		t.Fatalf("hook --name printed %q", out)
	}
}
