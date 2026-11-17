//go:build windows

package e2e

import (
	"os"
	"os/exec"
	"syscall"
)

const (
	createNewProcessGroup = 0x00000200 // CREATE_NEW_PROCESS_GROUP: the daemon is the root of its own group
	ctrlBreakEvent        = 1          // CTRL_BREAK_EVENT: delivered to a Go program as os.Interrupt
)

// ownGroup starts the daemon in its own process group, so a console control event can be aimed at it alone.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// askToStop is the clean-stop request the daemon handles on Windows: Windows has no SIGTERM, so it is a
// CTRL_BREAK_EVENT to the daemon's process group, which Go delivers as os.Interrupt (the daemon's shutdown signal).
func askToStop(p *os.Process) error {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GenerateConsoleCtrlEvent")
	if err := proc.Find(); err != nil {
		return err
	}
	if ok, _, err := proc.Call(ctrlBreakEvent, uintptr(p.Pid)); ok == 0 {
		return err
	}
	return nil
}
