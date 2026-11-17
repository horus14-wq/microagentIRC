package identity

// procRecord is one row of a process table: a process id, its recorded parent's id, and when the process started (0
// when unknown). Windows builds the table from a process snapshot; the resolver below is plain Go so it is tested on
// every platform.
type procRecord struct {
	pid     uint32
	ppid    uint32
	started uint64
}

// windowsSystemPID is the Windows "System" process; pid 0 is the idle process. Neither is an ancestor worth naming.
const windowsSystemPID = 4

// parentIn returns pid's parent from a process table, with the checks Windows needs because, unlike Linux, it does
// not reparent an orphan: the recorded parent id stays after that parent exits and may later be reused by an
// unrelated process. So the answer is ok=false when pid is not in the table, when the recorded parent is the idle
// or System process, when that parent is no longer in the table (it exited), or when the process now holding that
// id started after pid did (the id was reused). Any of these ends an ancestor walk, exactly as an unreadable /proc
// entry does on Linux.
func parentIn(table []procRecord, pid int) (int, bool) {
	if pid <= 0 || pid > int(^uint32(0)) {
		return 0, false
	}
	child, ok := find(table, uint32(pid))
	if !ok || child.ppid == 0 || child.ppid == windowsSystemPID || child.ppid == child.pid {
		return 0, false
	}
	parent, ok := find(table, child.ppid)
	if !ok {
		return 0, false
	}
	if parent.started != 0 && child.started != 0 && parent.started > child.started {
		return 0, false
	}
	return int(child.ppid), true
}

func find(table []procRecord, pid uint32) (procRecord, bool) {
	for _, r := range table {
		if r.pid == pid {
			return r, true
		}
	}
	return procRecord{}, false
}
