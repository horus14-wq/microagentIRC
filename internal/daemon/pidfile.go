package daemon

import (
	"os"
	"strconv"
	"strings"

	"github.com/horus14-wq/microagentIRC/internal/identity"
)

// removeStalePIDFile deletes daemon.pid when it exists but does not name a live process — the file a daemon
// killed outright leaves behind (a Windows job object closing with its agent CLI, a power cut). Whoever reads
// the file treats such a pid as stale; the daemon removes it on every start it can, since the file is about to
// name this process anyway. A missing file or one naming a live process is left alone.
func removeStalePIDFile(path string, logf func(string, ...any)) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the daemon's own pid file
	if err != nil {
		return
	}
	pid, perr := strconv.Atoi(strings.TrimSpace(string(raw)))
	switch {
	case perr != nil:
		_ = os.Remove(path)
		logf("removed stale daemon.pid")
	case pid <= 0 || !identity.PIDAlive(pid):
		_ = os.Remove(path)
		logf("removed stale daemon.pid (pid %d)", pid)
	}
}
