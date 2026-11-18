//go:build unix

package identity

import (
	"errors"
	"os"
	"syscall"
)

// PIDAlive reports whether a process with pid exists. Signal 0 performs the existence check without delivering a
// signal; EPERM means it exists but belongs to another user, which still counts as alive.
func PIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
