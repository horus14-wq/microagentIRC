//go:build linux

package identity

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

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

// EnvOf returns the value of the environment variable name in process pid's starting environment, read from
// /proc/<pid>/environ (readable for the user's own processes). ok=false when it is unset or unreadable.
func EnvOf(pid int, name string) (string, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return "", false
	}
	return envValue(string(raw), name)
}
