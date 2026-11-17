//go:build darwin

package identity

import (
	"os"
	"os/exec"
)

// macOS has no /proc: the two platform facts come from `ps`, which every macOS install ships and which answers both
// questions in one short-lived process. The texts are parsed, and the glue is tested, in the untagged ps_parse.go,
// whose cases run on every platform a build is checked on; this file is only the command and the two entry points.

// psCommand runs one `ps` query and returns its standard output.
func psCommand(args ...string) (string, bool) {
	// The flags and the argument values are fixed by the callers in ps_parse.go - a format name and a decimal pid -
	// so there is no shell and no text a caller controls; gosec flags the variable argument list itself.
	out, err := exec.Command("/bin/ps", args...).Output() //nolint:gosec // fixed flags, numeric pid, no shell
	if err != nil {
		return "", false
	}
	return string(out), true
}

// ControllingTTY returns the path of this process's controlling terminal, as /dev/ttysNNN, or ok=false when it has
// none (a pipe, a service, a detached session).
func ControllingTTY() (string, bool) {
	return controllingTTYOf(os.Getpid(), psCommand)
}

// ParentOf returns pid's parent pid, or ok=false when pid does not exist or `ps` cannot say.
func ParentOf(pid int) (int, bool) {
	return parentOf(pid, psCommand)
}
