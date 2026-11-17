//go:build windows

package identity

import (
	"strconv"
	"syscall"
	"unsafe"
)

// processQueryLimitedInformation is PROCESS_QUERY_LIMITED_INFORMATION: enough to read a process's start time, and
// granted for other users' processes too where PROCESS_QUERY_INFORMATION is not.
const processQueryLimitedInformation = 0x1000

// ControllingTTY on Windows names the console this process is attached to: "con:<hex window handle>" from
// GetConsoleWindow. Every process started in one console window or terminal tab (each tab is its own pseudo console)
// shares it, which is what the Linux /dev/pts path means for a seat. It returns ok=false for a process with no
// console (a service, or one started detached).
func ControllingTTY() (string, bool) {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleWindow")
	if proc.Find() != nil {
		return "", false
	}
	hwnd, _, _ := proc.Call()
	if hwnd == 0 {
		return "", false
	}
	return "con:" + strconv.FormatUint(uint64(hwnd), 16), true
}

// ParentOf on Windows reads pid's parent from a process snapshot (CreateToolhelp32Snapshot). Windows does not reparent
// an orphan and reuses process ids, so the answer is checked as parentIn describes: ok=false when the recorded parent
// has exited, its id now belongs to a process that started later, or it is the idle or System process.
func ParentOf(pid int) (int, bool) {
	table, ok := processTable()
	if !ok {
		return 0, false
	}
	// Start times only for the two processes the answer depends on: one OpenProcess each, not one per process.
	for i := range table {
		if int(table[i].pid) == pid {
			table[i].started = startedAt(table[i].pid)
			for j := range table {
				if table[j].pid == table[i].ppid {
					table[j].started = startedAt(table[j].pid)
				}
			}
			break
		}
	}
	return parentIn(table, pid)
}

// processTable is every process's id and recorded parent id, from one snapshot; ok=false when no snapshot is taken.
func processTable() ([]procRecord, bool) {
	snap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, false
	}
	defer func() { _ = syscall.CloseHandle(snap) }()
	var entry syscall.ProcessEntry32
	// The API requires the caller to state the structure's size; there is no other way to obtain it.
	entry.Size = uint32(unsafe.Sizeof(entry)) //nolint:gosec // G103: Win32 ABI size field, read-only use.
	if syscall.Process32First(snap, &entry) != nil {
		return nil, false
	}
	var table []procRecord
	for {
		table = append(table, procRecord{pid: entry.ProcessID, ppid: entry.ParentProcessID})
		if syscall.Process32Next(snap, &entry) != nil {
			return table, true
		}
	}
}

// startedAt is the process's creation time in 100 ns units since 1601, or 0 when it cannot be read (exited, or access
// denied), which parentIn treats as unknown.
func startedAt(pid uint32) uint64 {
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, pid)
	if err != nil {
		return 0
	}
	defer func() { _ = syscall.CloseHandle(h) }()
	var created, exited, kernel, user syscall.Filetime
	if syscall.GetProcessTimes(h, &created, &exited, &kernel, &user) != nil {
		return 0
	}
	return uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
}
