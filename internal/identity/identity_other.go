//go:build !linux && !darwin && !windows

package identity

// On a system mairc has no process-fact reader for, both answers are "unknown" rather than a guess: Seat then falls
// through to the working directory, and Ancestors stops at the first step. This build must compile: go build ./...
// is checked for every GOOS in the release.
func ControllingTTY() (string, bool) {
	return "", false
}

// ParentOf answers ok=false on a system with no process-fact reader.
func ParentOf(int) (int, bool) {
	return 0, false
}
