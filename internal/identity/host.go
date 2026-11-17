package identity

import "strings"

// A seat is unique on one machine only: two machines can both have a pane "%1" or a terminal "/dev/pts/3". A
// host-qualified seat puts the machine in front: "@buildbox/tmux:%12". The leading "@" tells it apart from a seat
// written before hosts were added ("tmux:%12", "tty:/dev/pts/3"), none of which starts with "@".

const (
	hostMark    = "@"
	hostSep     = "/"
	maxHost     = 63
	unknownHost = "unknown"
	maxSession  = 200
)

// sessionEnv lists the environment variables in which agent CLIs give a hook or an MCP server the id of the
// session it runs for (docs/compat.md), in the order they are tried.
func sessionEnv() []string {
	return []string{"CLAUDE_CODE_SESSION_ID", "GEMINI_SESSION_ID", "GROK_SESSION_ID"}
}

// HostName returns the machine's name in the form seats use: lowercased, every character outside a-z, 0-9, "."
// and "-" replaced by "-", cut to 63 characters; "unknown" when the name cannot be read or is empty.
func HostName(hostname func() (string, error)) string {
	raw, err := hostname()
	if err != nil {
		return unknownHost
	}
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		if b.Len() >= maxHost {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return unknownHost
	}
	return b.String()
}

// HostSeat returns the seat qualified by the machine: "@<host>/<seat>". hostname is os.Hostname in production.
func HostSeat(seat string, hostname func() (string, error)) string {
	return hostMark + HostName(hostname) + hostSep + seat
}

// HostOf returns the machine of a host-qualified seat, or "" for a seat without one (an older client).
func HostOf(seat string) string {
	rest, ok := strings.CutPrefix(seat, hostMark)
	if !ok {
		return ""
	}
	host, _, ok := strings.Cut(rest, hostSep)
	if !ok {
		return ""
	}
	return host
}

// Legacy returns the seat without its machine: what the same place was called before hosts were added. A seat
// without a host is returned unchanged.
func Legacy(seat string) string {
	rest, ok := strings.CutPrefix(seat, hostMark)
	if !ok {
		return seat
	}
	_, legacy, ok := strings.Cut(rest, hostSep)
	if !ok {
		return seat
	}
	return legacy
}

// ValidSession reports whether s can be a CLI session id: 1 to 200 printable ASCII characters, no spaces.
func ValidSession(s string) bool {
	if s == "" || len(s) > maxSession {
		return false
	}
	for i := range len(s) {
		if s[i] <= ' ' || s[i] > '~' {
			return false
		}
	}
	return true
}

// SessionFromEnv returns the CLI session id an agent CLI put in the environment, or "" when there is none.
func SessionFromEnv(getenv func(string) string) string {
	for _, name := range sessionEnv() {
		if v := getenv(name); ValidSession(v) {
			return v
		}
	}
	return ""
}
