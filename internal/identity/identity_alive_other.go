//go:build !unix && !windows

package identity

// PIDAlive reports false where the platform has no process-existence check mairc can make: callers must treat
// every pid as possibly live rather than risk calling a running daemon stale.
func PIDAlive(int) bool { return false }
