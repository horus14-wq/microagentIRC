package identity

import (
	"strconv"
	"strings"
)

// The two parses below read the text `ps` prints on darwin, and live in an untagged file so their cases run on every
// platform a build is checked on, not only on a Mac.

// parsePpid reads the number `ps -o ppid= -p <pid>` prints: a bare pid, layout left to ps (it pads and may indent).
// Anything else - empty output, a header, a word - is ok=false, which ends an ancestor walk rather than naming a
// wrong parent.
func parsePpid(out string) (int, bool) {
	field := strings.TrimSpace(out)
	if field == "" {
		return 0, false
	}
	if strings.ContainsAny(field, " \t\n") {
		return 0, false
	}
	ppid, err := strconv.Atoi(field)
	if err != nil || ppid <= 0 {
		return 0, false
	}
	return ppid, true
}

// parseTTY turns what `ps -o tty= -p <pid>` prints into the terminal's device path, which is the form a seat uses
// on every platform: "ttys001" becomes "/dev/ttys001". A process with no controlling terminal prints "??" or "?",
// and a process on the console prints "console"; both answer ok=false and "/dev/console" respectively, since a
// console session is not a terminal an agent can be reached on.
func parseTTY(out string) (string, bool) {
	field := strings.TrimSpace(out)
	switch {
	case field == "" || field == "??" || field == "?" || field == "-":
		return "", false
	case field == "console":
		return "/dev/console", true
	case strings.ContainsAny(field, " \t\n/"):
		// A path, a header or several columns is not what ps prints for one process's tty.
		return "", false
	}
	return "/dev/" + field, true
}

// parentOf asks ps for one process's parent; run is the command runner, so the glue is tested on every platform
// even though only darwin calls it with the real command.
func parentOf(pid int, run func(args ...string) (string, bool)) (int, bool) {
	if pid <= 0 {
		return 0, false
	}
	out, ok := run("-o", "ppid=", "-p", strconv.Itoa(pid))
	if !ok {
		return 0, false
	}
	return parsePpid(out)
}

// controllingTTYOf asks ps for one process's tty; run is the command runner, as in parentOf.
func controllingTTYOf(pid int, run func(args ...string) (string, bool)) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	out, ok := run("-o", "tty=", "-p", strconv.Itoa(pid))
	if !ok {
		return "", false
	}
	return parseTTY(out)
}
