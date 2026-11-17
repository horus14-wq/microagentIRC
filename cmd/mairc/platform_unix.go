//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// shutdownSignals are the signals a daemon or the stdio shim stops cleanly on: SIGINT from a terminal and SIGTERM
// from a service manager.
func shutdownSignals() []os.Signal { return []os.Signal{os.Interrupt, syscall.SIGTERM} }

// detach puts the started daemon in its own session, so it survives the shell that started it and owns no terminal.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
