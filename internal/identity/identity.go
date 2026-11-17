// Package identity answers two small facts about the process mairc runs in, used to tell which
// agent a shim or a hook belongs to without relying on any particular agent CLI: the seat (where
// the agent sits) and the ancestor process ids.
package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// maxSeat is the byte limit for a seat; longer values are cut and suffixed with "~" plus the
// first 8 hex characters of their SHA-256 so two long values stay distinct.
const maxSeat = 200

// Seat returns where this agent sits, the first that applies:
//
//	"env:<v>"  when $MAIRC_SEAT is v (non-empty)
//	"tmux:<v>" when $TMUX_PANE is v (non-empty), e.g. "tmux:%12"
//	"tty:<p>"  when the controlling terminal's path is p, e.g. "tty:/dev/pts/7"
//	"cwd:<d>"  the working directory otherwise
//
// getenv is os.Getenv, tty is ControllingTTY and cwd is os.Getwd in production.
func Seat(getenv func(string) string, tty func() (string, bool), cwd func() (string, error)) string {
	var seat string
	switch {
	case getenv("MAIRC_SEAT") != "":
		seat = "env:" + getenv("MAIRC_SEAT")
	case getenv("TMUX_PANE") != "":
		seat = "tmux:" + getenv("TMUX_PANE")
	default:
		if p, ok := tty(); ok {
			seat = "tty:" + p
			break
		}
		d, err := cwd()
		if err != nil {
			d = ""
		}
		seat = "cwd:" + d
	}
	if len(seat) <= maxSeat {
		return seat
	}
	sum := sha256.Sum256([]byte(seat))
	suffix := "~" + hex.EncodeToString(sum[:4])
	return seat[:maxSeat-len(suffix)] + suffix
}

// ControllingTTY returns the path of the process's controlling terminal, read from the symlinks
// /proc/self/fd/0, /proc/self/fd/1 and /proc/self/fd/2 in that order: the first that points at
// /dev/pts/N or /dev/ttyN. It returns ok=false when none does (pipes, files, no /proc).
func ControllingTTY() (string, bool) {
	for _, fd := range []string{"0", "1", "2"} {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", fd))
		if err != nil {
			continue
		}
		if isTTY(target) {
			return target, true
		}
	}
	return "", false
}

func isTTY(path string) bool {
	if strings.HasPrefix(path, "/dev/tty") {
		return true
	}
	return strings.HasPrefix(path, "/dev/pts/")
}

// Ancestors returns the process ids of this process's ancestors, nearest first (parent,
// grandparent, ...), at most limit, stopping at pid 1 (not included). parentOf returns a
// process's parent pid; ok=false ends the walk. A process whose parent is itself ends the walk.
// In production self is os.Getpid() and parentOf is ParentOf.
func Ancestors(self int, limit int, parentOf func(pid int) (ppid int, ok bool)) []int {
	if limit <= 0 {
		return nil
	}
	var out []int
	for pid := self; len(out) < limit; {
		ppid, ok := parentOf(pid)
		if !ok || ppid <= 1 || ppid == pid {
			return out
		}
		out = append(out, ppid)
		pid = ppid
	}
	return out
}

// ParentOf reads a process's parent pid from /proc/<pid>/stat on Linux. On other systems, or
// when the file cannot be read or parsed, it returns ok=false.
func ParentOf(pid int) (int, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	return parseStatPpid(string(raw))
}

// parseStatPpid reads the ppid field out of a /proc/<pid>/stat line. The file has the form
// "pid (comm) state ppid ..." and comm may contain spaces and parentheses, so the ppid is the
// second space-separated field after the last ")".
func parseStatPpid(stat string) (int, bool) {
	i := strings.LastIndex(stat, ")")
	if i < 0 {
		return 0, false
	}
	fields := strings.Fields(stat[i+1:])
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil || ppid < 0 {
		return 0, false
	}
	return ppid, true
}
