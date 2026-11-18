//go:build windows

package main

import (
	"os/exec"
	"testing"
)

// mairc-vq8: the daemon must leave a kill-on-close job the agent CLI runs under, or it dies when the CLI does.
// CREATE_BREAKAWAY_FROM_JOB is the flag that does it; DETACHED_PROCESS alone does not escape a job object.
func TestDetachRequestsJobBreakaway(t *testing.T) {
	cmd := exec.Command("cmd")
	detach(cmd)
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&breakawayFromJob == 0 {
		t.Fatalf("detach CreationFlags = %#x, want CREATE_BREAKAWAY_FROM_JOB set", cmd.SysProcAttr.CreationFlags)
	}
}
