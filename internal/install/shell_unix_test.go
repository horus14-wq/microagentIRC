//go:build !windows

package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestQuotedHookExecutesTheLiteralPOSIXPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a b ' $HOME & c")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "mairc")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sh", "-c", hookCommand(binary)).CombinedOutput()
	if err != nil || string(out) != "hook\n--client\nclaude\n" {
		t.Fatalf("literal path execution: %q, %v", out, err)
	}
}
