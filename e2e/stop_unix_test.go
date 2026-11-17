//go:build !windows

package e2e

import (
	"os"
	"os/exec"
	"syscall"
)

// ownGroup prepares a daemon start; nothing is needed where SIGTERM can be sent to one process.
func ownGroup(*exec.Cmd) {}

// askToStop is the clean-stop request the daemon handles: SIGTERM.
func askToStop(p *os.Process) error { return p.Signal(syscall.SIGTERM) }
