package identity

import (
	"os"
	"strings"
	"testing"
)

func noTTY() (string, bool)  { return "", false }
func yesTTY() (string, bool) { return "/dev/pts/7", true }
func okCWD() (string, error) { return "/home/agent/work", nil }

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestSeatSourcesAndPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		tty  func() (string, bool)
		cwd  func() (string, error)
		want string
	}{
		{"env wins", map[string]string{"MAIRC_SEAT": "bench3", "TMUX_PANE": "%12"}, yesTTY, okCWD, "env:bench3"},
		{"tmux second", map[string]string{"TMUX_PANE": "%12"}, yesTTY, okCWD, "tmux:%12"},
		{"tty third", map[string]string{}, yesTTY, okCWD, "tty:/dev/pts/7"},
		{"cwd last", map[string]string{}, noTTY, okCWD, "cwd:/home/agent/work"},
		{"empty env skipped", map[string]string{"MAIRC_SEAT": "", "TMUX_PANE": "%1"}, noTTY, okCWD, "tmux:%1"},
		{"tty failure falls to cwd", map[string]string{}, noTTY, okCWD, "cwd:/home/agent/work"},
	} {
		if got := Seat(envOf(tc.env), tc.tty, tc.cwd); got != tc.want {
			t.Errorf("%s: Seat = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSeatLongValueIsCutWithHash(t *testing.T) {
	long := "/home/agent/" + strings.Repeat("deep", 125) // > 500 bytes
	other := "/home/agent/" + strings.Repeat("peep", 125)
	cwdLong := func() (string, error) { return long, nil }
	cwdOther := func() (string, error) { return other, nil }

	a := Seat(envOf(nil), noTTY, cwdLong)
	b := Seat(envOf(nil), noTTY, cwdOther)
	if len(a) > maxSeat {
		t.Fatalf("len = %d, want <= %d", len(a), maxSeat)
	}
	if !strings.HasPrefix(a, "cwd:") || !strings.Contains(a, "~") {
		t.Fatalf("seat %q lost prefix or hash marker", a[:60])
	}
	if a == b {
		t.Fatal("two different long directories gave the same seat")
	}
	if a[:len(a)-9] != "cwd:"+long[:maxSeat-4-9] {
		t.Fatalf("cut point wrong: %q", a[:40])
	}
}

func TestAncestors(t *testing.T) {
	// A chain pid -> pid-1 down to 1.
	chain := func(pid int) (int, bool) {
		if pid <= 1 {
			return 0, false
		}
		return pid - 1, true
	}
	if got := Ancestors(13, 8, chain); len(got) != 8 || got[0] != 12 || got[7] != 5 {
		t.Fatalf("max 8 of chain of 12 = %v", got)
	}
	if got := Ancestors(4, 8, chain); len(got) != 2 || got[0] != 3 || got[1] != 2 {
		t.Fatalf("walk reaching pid 1 = %v, want [3 2] (1 not included)", got)
	}
	broken := func(pid int) (int, bool) {
		if pid == 10 {
			return 0, false
		}
		return pid - 1, true
	}
	if got := Ancestors(12, 8, broken); len(got) != 2 || got[0] != 11 || got[1] != 10 {
		t.Fatalf("broken link = %v, want [11 10]", got)
	}
	cycle := func(pid int) (int, bool) { return pid, true }
	if got := Ancestors(9, 8, cycle); len(got) != 0 {
		t.Fatalf("self-parent cycle = %v, want []", got)
	}
	if got := Ancestors(9, 0, chain); got != nil {
		t.Fatalf("max 0 = %v", got)
	}
}

func TestParentOfLive(t *testing.T) {
	// On every platform: Linux reads /proc, Windows takes a process snapshot.
	ppid, ok := ParentOf(os.Getpid())
	if !ok {
		t.Skip("no process table on this system")
	}
	if ppid != os.Getppid() {
		t.Fatalf("ParentOf(self) = %d, want %d", ppid, os.Getppid())
	}
}

// A Devin seat inherits CLAUDE_* variables from a Claude Code parent (docs/compat.md, Devin: "do not trust CLAUDE_* to
// identify a CLI"). The seat never reads them, so a Devin pane and a Claude Code pane stay distinct seats.
func TestSeatIgnoresInheritedClaudeVariables(t *testing.T) {
	noTTY := func() (string, bool) { return "", false }
	cwd := func() (string, error) { return "/w", nil }
	for _, base := range []map[string]string{{"TMUX_PANE": "%9"}, {}} {
		inherited := map[string]string{"CLAUDE_CODE_SESSION_ID": "claude-parent", "CLAUDE_PID": "4242", "DEVIN_PROJECT_DIR": "/w"}
		for k, v := range base {
			inherited[k] = v
		}
		want := Seat(func(k string) string { return base[k] }, noTTY, cwd)
		if got := Seat(func(k string) string { return inherited[k] }, noTTY, cwd); got != want {
			t.Fatalf("seat with inherited CLAUDE_* = %q, want %q", got, want)
		}
	}
}
