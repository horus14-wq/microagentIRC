//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// Windows process-creation flags (Win32 "Process Creation Flags"). The syscall package exports only some of them.
const (
	detachedProcess       = 0x00000008 // the child gets no console, so the parent's console closing sends it nothing
	createNewProcessGroup = 0x00000200 // a Ctrl+C aimed at the parent's group does not reach the child
)

// shutdownSignals are the signals a daemon or the stdio shim stops cleanly on. Windows delivers no SIGTERM to a console
// application; os.Interrupt is Ctrl+C, which is also what a service manager's stop reaches through the console control
// handler.
func shutdownSignals() []os.Signal { return []os.Signal{os.Interrupt} }

// detach starts the daemon without a console of its own and in its own process group, so closing the console that
// started it does not stop it; HideWindow keeps the console window off the desktop.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | createNewProcessGroup,
		HideWindow:    true,
	}
}
