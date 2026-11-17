//go:build !windows

package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuotedHookExecutesTheLiteralPOSIXPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a b ' $HOME & c")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "mairc")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(binary, 0o700); err != nil { //nolint:gosec // This private test fixture must be executable to verify literal hook-path execution.
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "sh")
	cmd.Stdin = strings.NewReader(hookCommand(binary) + "\n")
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != "hook\n--client\nclaude\n" {
		t.Fatalf("literal path execution: %q, %v", out, err)
	}
}
