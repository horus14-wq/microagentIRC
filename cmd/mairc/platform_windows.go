//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// Windows process-creation flags (Win32 "Process Creation Flags"). The syscall package exports only some of them.
const (
	detachedProcess       = 0x00000008 // the child gets no console, so the parent's console closing sends it nothing
	createNewProcessGroup = 0x00000200 // a Ctrl+C aimed at the parent's group does not reach the child
	breakawayFromJob      = 0x01000000 // CREATE_BREAKAWAY_FROM_JOB: leave a kill-on-close job the agent CLI runs under
)

// shutdownSignals are the signals a daemon or the stdio shim stops cleanly on. Windows delivers no SIGTERM to a console
// application; os.Interrupt is Ctrl+C, which is also what a service manager's stop reaches through the console control
// handler.
func shutdownSignals() []os.Signal { return []os.Signal{os.Interrupt} }

// detach starts the daemon without a console of its own and in its own process group, so closing the console that
// started it does not stop it; HideWindow keeps the console window off the desktop. CREATE_BREAKAWAY_FROM_JOB lets
// the daemon leave a job object the agent CLI runs under — agent harnesses and terminals increasingly place their
// children in kill-on-close jobs, which DETACHED_PROCESS alone does not escape (mairc-vq8).
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | createNewProcessGroup | breakawayFromJob,
		HideWindow:    true,
	}
}

// startDetached starts a fresh daemon command and returns it started. When the job does not allow breaking away,
// CreateProcess fails and the daemon is retried inside the job: one that dies with its CLI is the bug this
// prevents, and a job that forbids breakaway is no worse off than before the flag existed.
func startDetached(newCmd func() *exec.Cmd) (*exec.Cmd, error) {
	cmd := newCmd()
	if err := cmd.Start(); err == nil {
		return cmd, nil
	}
	cmd = newCmd()
	cmd.SysProcAttr.CreationFlags &^= breakawayFromJob
	return cmd, cmd.Start()
}

// stableBinaryPath is where `mairc install` copies a binary that sits in a temporary or Downloads
// directory: a bin directory under the user's profile.
func stableBinaryPath(home string) string {
	return filepath.Join(home, "bin", "mairc.exe")
}
