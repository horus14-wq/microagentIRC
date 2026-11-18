//go:build windows

package identity

import "syscall"

// PIDAlive reports whether a process with pid exists: OpenProcess succeeds for a live process and fails for a
// dead or nonexistent one. PROCESS_QUERY_LIMITED_INFORMATION is granted for other users' processes too.
func PIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	_ = syscall.CloseHandle(h)
	return true
}
