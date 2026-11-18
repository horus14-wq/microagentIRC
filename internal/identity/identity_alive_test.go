package identity

import (
	"math"
	"os"
	"os/exec"
	"testing"
)

// deadPID runs a process that exits at once and returns its pid, which no live process holds afterwards.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$") // #nosec G204 G702 -- the test binary itself, matching no test
	if err := cmd.Run(); err != nil {
		t.Skipf("cannot spawn a helper process: %v", err)
	}
	return cmd.Process.Pid
}

func TestPIDAlive(t *testing.T) {
	if !PIDAlive(os.Getpid()) {
		t.Error("own pid reported dead")
	}
	if PIDAlive(deadPID(t)) {
		t.Error("a reaped process reported alive")
	}
	for _, pid := range []int{0, -1, math.MaxInt32} {
		if PIDAlive(pid) {
			t.Errorf("pid %d reported alive", pid)
		}
	}
}
