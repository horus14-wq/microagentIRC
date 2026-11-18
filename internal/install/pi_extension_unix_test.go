//go:build unix

package install

import (
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// mairc-cuf: the Pi extension, run by Node with a fake `pi` and a fake `mairc`. A tool result gains what the hook
// printed; nothing waiting or a failing hook leaves it unchanged (the handler returns null); session start queues the
// hook's lines for the next turn; every hook run names Pi's session with --session.
func TestPiExtensionDeliversWhatTheHookPrints(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; the Pi extension is plain JavaScript")
	}
	dir := t.TempDir()
	out, fail, calls := filepath.Join(dir, "out"), filepath.Join(dir, "fail"), filepath.Join(dir, "calls")
	fake := filepath.Join(dir, "mairc")
	write(t, fake, "#!/bin/sh\n"+
		"echo \"$*\" >> '"+calls+"'\n"+
		"[ -e '"+fail+"' ] && exit 1\n"+
		"[ -e '"+out+"' ] && cat '"+out+"'\n"+
		"exit 0\n", 0o700)
	extension := filepath.Join(dir, "mairc.mjs")
	write(t, extension, piExtension().body(fake), 0o600)

	raw, err := exec.CommandContext(t.Context(), node, filepath.Join("testdata", "pi_extension_harness.mjs"),
		extension, out, fail).Output()
	if err != nil {
		var stderr []byte
		if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
			stderr = ee.Stderr
		}
		t.Fatalf("harness: %v\n%s", err, stderr)
	}
	var got struct {
		Handlers           []string
		Delivered, Nothing []string
		Failed             []string
		Structured         map[string]any
		Messages           []struct{ Content, DeliverAs string }
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("harness output %q: %v", raw, err)
	}
	if !slices.Equal(got.Handlers, []string{"session_start", "tool_result"}) {
		t.Errorf("handlers %v", got.Handlers)
	}
	if !slices.Equal(got.Delivered, []string{"tool output", "irc:\npm alice: hi"}) {
		t.Errorf("a tool result with mail waiting: %q", got.Delivered)
	}
	if got.Nothing != nil || got.Failed != nil {
		t.Errorf("nothing waiting / failing hook changed the result: %q %q", got.Nothing, got.Failed)
	}
	if got.Structured["kept"] != float64(1) {
		t.Errorf("structuredContent was not passed through: %v", got.Structured)
	}
	if len(got.Messages) != 1 || got.Messages[0].Content != "irc:\npm alice: lost" || got.Messages[0].DeliverAs != "nextTurn" {
		t.Errorf("session start messages %+v", got.Messages)
	}
	runs := strings.Split(strings.TrimSpace(string(read(t, calls))), "\n")
	if len(runs) != 5 || runs[0] != "hook --client plain --event PostToolUse --session pi-session-1" ||
		runs[4] != "hook --client plain --event SessionStart --session pi-session-1" {
		t.Errorf("hook runs:\n%s", strings.Join(runs, "\n"))
	}
}
