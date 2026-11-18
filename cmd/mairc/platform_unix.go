//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// shutdownSignals are the signals a daemon or the stdio shim stops cleanly on: SIGINT from a terminal and SIGTERM
// from a service manager.
func shutdownSignals() []os.Signal { return []os.Signal{os.Interrupt, syscall.SIGTERM} }

// detach puts the started daemon in its own session, so it survives the shell that started it and owns no terminal.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// startDetached starts the daemon command; there is no job-object equivalent to fall back from.
func startDetached(newCmd func() *exec.Cmd) (*exec.Cmd, error) {
	cmd := newCmd()
	return cmd, cmd.Start()
}

// stableBinaryPath is where `mairc install` copies a binary that sits in a temporary or Downloads
// directory: the user's bin directory, on PATH for the usual shells.
func stableBinaryPath(home string) string {
	return filepath.Join(home, ".local", "bin", "mairc")
}
