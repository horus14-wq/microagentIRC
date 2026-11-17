//go:build !windows

package identity

import "testing"

func TestParseStatPpid(t *testing.T) {
	if ppid, ok := parseStatPpid("1234 (bash) S 77 1234 0 0"); !ok || ppid != 77 {
		t.Fatalf("bash = %d %v", ppid, ok)
	}
	if ppid, ok := parseStatPpid("1234 (my (odd) name) R 77 1234 0 0"); !ok || ppid != 77 {
		t.Fatalf("odd name = %d %v", ppid, ok)
	}
	for _, bad := range []string{"", "1234 bash S 77", "1234 (bash)", "1234 (bash) S", "x (y) S zzz"} {
		if _, ok := parseStatPpid(bad); ok {
			t.Errorf("parseStatPpid(%q) ok, want not ok", bad)
		}
	}
}

func TestControllingTTYSmoke(t *testing.T) {
	// Whatever the test environment is, the function must not fail or hang; the value, when ok,
	// names a real tty path.
	if p, ok := ControllingTTY(); ok && !isTTY(p) {
		t.Fatalf("ControllingTTY = %q, not a tty path", p)
	}
}
