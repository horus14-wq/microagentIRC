package identity

import (
	"strings"
	"testing"
)

// TestParsePpid is the darwin parent read, tested where every build checks it.
func TestParsePpid(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		want int
		ok   bool
	}{
		{"plain", "77\n", 77, true},
		{"padded", "   4242\n", 4242, true},
		{"no newline", "12", 12, true},
		{"empty", "", 0, false},
		{"blank", "  \n", 0, false},
		{"header", "PPID\n", 0, false},
		{"two columns", "77 1234\n", 0, false},
		{"zero", "0\n", 0, false},
		{"negative", "-3\n", 0, false},
		{"word", "launchd\n", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parsePpid(tc.out)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("parsePpid(%q) = %d %v, want %d %v", tc.out, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestParseTTY is the darwin seat read: ps prints the short device name, and a seat is the full path.
func TestParseTTY(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		want string
		ok   bool
	}{
		{"terminal", "ttys001\n", "/dev/ttys001", true},
		{"padded", "  ttys012\n", "/dev/ttys012", true},
		{"console", "console\n", "/dev/console", true},
		{"no terminal", "??\n", "", false},
		{"no terminal short", "?\n", "", false},
		{"dash", "-\n", "", false},
		{"empty", "", "", false},
		{"already a path", "/dev/ttys003\n", "", false},
		{"header", "TTY\n", "/dev/TTY", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseTTY(tc.out)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("parseTTY(%q) = %q %v, want %q %v", tc.out, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestParentOfReadsTheCommand proves the darwin glue asks ps the right question and keeps its answer, or gives up
// cleanly - on every platform, so the glue is not shipped untested.
func TestParentOfReadsTheCommand(t *testing.T) {
	var asked [][]string
	run := func(args ...string) (string, bool) {
		asked = append(asked, args)
		return "4242\n", true
	}
	if ppid, ok := parentOf(99, run); !ok || ppid != 4242 {
		t.Fatalf("parentOf = %d %v, want 4242 true", ppid, ok)
	}
	if want := []string{"-o", "ppid=", "-p", "99"}; len(asked) != 1 || strings.Join(asked[0], " ") != strings.Join(want, " ") {
		t.Fatalf("ps asked %v, want %v", asked, want)
	}
	for _, pid := range []int{0, -1} {
		if _, ok := parentOf(pid, run); ok {
			t.Fatalf("parentOf(%d) answered, want ok=false", pid)
		}
	}
	failing := func(...string) (string, bool) { return "", false }
	if _, ok := parentOf(7, failing); ok {
		t.Fatal("a failing ps answered")
	}
}

// TestControllingTTYReadsTheCommand is the same proof for the seat's terminal.
func TestControllingTTYReadsTheCommand(t *testing.T) {
	var asked []string
	run := func(args ...string) (string, bool) {
		asked = append([]string{}, args...)
		return "ttys007\n", true
	}
	if tty, ok := controllingTTYOf(42, run); !ok || tty != "/dev/ttys007" {
		t.Fatalf("controllingTTYOf = %q %v, want /dev/ttys007 true", tty, ok)
	}
	if want := "-o tty= -p 42"; strings.Join(asked, " ") != want {
		t.Fatalf("ps asked %q, want %q", strings.Join(asked, " "), want)
	}
	if _, ok := controllingTTYOf(0, run); ok {
		t.Fatal("pid 0 answered")
	}
	noTerminal := func(...string) (string, bool) { return "??\n", true }
	if tty, ok := controllingTTYOf(42, noTerminal); ok || tty != "" {
		t.Fatalf("a process with no terminal answered %q %v", tty, ok)
	}
}
