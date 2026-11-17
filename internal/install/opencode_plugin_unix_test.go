//go:build unix

package install

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// mairc-ub6: the OpenCode plugin itself, run by Node with a fake OpenCode context and a fake `mairc` binary. It adds
// what the hook printed to a tool's result once; adds nothing when nothing is waiting or the hook fails; never
// delivers on mairc's own tools except irc_name (whose result the hook needs to bind the session); and at a session's
// first model request adds the hook's lines to the system prompt, once per session.
func TestOpenCodePluginDeliversWhatTheHookPrints(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; the OpenCode plugin is plain JavaScript run by OpenCode's Node runtime")
	}
	dir := t.TempDir()
	out, fail, calls := filepath.Join(dir, "out"), filepath.Join(dir, "fail"), filepath.Join(dir, "calls")
	fake := filepath.Join(dir, "mairc")
	script := "#!/bin/sh\n" +
		"{ cat; echo; } >> '" + calls + "'\n" +
		"[ -e '" + fail + "' ] && exit 1\n" +
		"[ -e '" + out + "' ] && cat '" + out + "'\n" +
		"exit 0\n"
	write(t, fake, script, 0o700)
	plugin := filepath.Join(dir, "mairc.mjs")
	write(t, plugin, openCodePlugin().body(fake), 0o600)

	cmd := exec.CommandContext(t.Context(), node, filepath.Join("testdata", "opencode_plugin_harness.mjs"), plugin, out, fail)
	raw, err := cmd.Output()
	if err != nil {
		var stderr []byte
		if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
			stderr = ee.Stderr
		}
		t.Fatalf("harness: %v\n%s", err, stderr)
	}
	var got struct {
		ID                                               string
		Hooks                                            []string
		Delivered, Inner, Named, Nothing, Failed, System []string
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("harness output %q: %v", raw, err)
	}
	waiting := "irc:\npm alice: hi"
	for _, c := range []struct {
		what      string
		got, want []string
	}{
		{"hooks", got.Hooks, []string{"session.context", "tool.execute.after"}},
		{"a tool call with mail waiting", got.Delivered, []string{"tool output", waiting}},
		{"mairc's own tool inside the code tool", got.Inner, []string{"tool output"}},
		{"irc_name", got.Named, []string{"tool output", waiting}},
		{"nothing waiting", got.Nothing, []string{"tool output"}},
		{"a failing hook", got.Failed, []string{"tool output"}},
		{"session start, twice", got.System, []string{"base", "irc:\npm alice: lost"}},
	} {
		if !slices.Equal(c.got, c.want) {
			t.Errorf("%s: content %q, want %q", c.what, c.got, c.want)
		}
	}
	if got.ID != "mairc" {
		t.Errorf("plugin id %q", got.ID)
	}
	// The hook saw OpenCode's session id and the tool's result, and was not run for mairc_irc_who.
	runs := strings.Split(strings.TrimSpace(string(read(t, calls))), "\n")
	if len(runs) != 5 {
		t.Fatalf("hook ran %d times, want 5 (bash, irc_name, bash, bash, session start):\n%s", len(runs), strings.Join(runs, "\n"))
	}
	var named map[string]any
	if err := json.Unmarshal([]byte(runs[1]), &named); err != nil {
		t.Fatal(err)
	}
	if named["tool_name"] != "mairc_irc_name" || named["session_id"] != "ses_test1" || named["hook_event_name"] != "PostToolUse" ||
		named["tool_response"] == nil {
		t.Errorf("irc_name hook input %v", named)
	}
	if !strings.Contains(runs[4], `"hook_event_name":"SessionStart"`) || !strings.Contains(runs[4], `"session_id":"ses_test2"`) {
		t.Errorf("session start hook input %s", runs[4])
	}
}

// mairc-ub6: install writes the plugin into OpenCode's plugins directory with the binary's path, a second run changes
// nothing, the status reports it as OpenCode's hook, and uninstall removes the file it created.
func TestOpenCodeInstallWritesAndRemovesThePlugin(t *testing.T) {
	home := t.TempDir()
	o := Options{Binary: testBinary, Home: home, Only: []string{"opencode"}}
	if code, out := runRaw(o); code != 0 {
		t.Fatalf("install = %d:\n%s", code, out)
	}
	path := filepath.Join(home, ".config", "opencode", "plugins", "mairc.js")
	first := string(read(t, path))
	if !strings.HasPrefix(first, "// mairc: begin") || !strings.Contains(first, `const MAIRC = "`+testBinary+`";`) ||
		!strings.HasSuffix(first, "// mairc: end\n") {
		t.Fatalf("plugin file:\n%s", first)
	}
	if code, out := runRaw(o); code != 0 || string(read(t, path)) != first {
		t.Fatalf("second install = %d, changed the plugin:\n%s", code, out)
	}
	s := statusFor(t, Options{Scope: "user", Home: home, Only: []string{"opencode"}}, "opencode")
	if !s.HookPresent || s.HookPath != path || s.HookCommand != "" {
		t.Errorf("status hook = %v %q %q", s.HookPresent, s.HookPath, s.HookCommand)
	}
	o.Uninstall = true
	if code, out := runRaw(o); code != 0 {
		t.Fatalf("uninstall = %d:\n%s", code, out)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("uninstall left %s: %v", path, err)
	}
}
