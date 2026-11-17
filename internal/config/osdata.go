// Per-OS data directory rules. One pure function holds all three, so the tests exercise every rule on any host: the
// OS name and both directory lookups are parameters. `os.UserConfigDir` is the portable lookup (on macOS it is
// `~/Library/Application Support`); Windows wants `%LOCALAPPDATA%`, which `os.UserConfigDir` does not return (it is
// `%AppData%`, the roaming profile), so that one is read directly.
package config

import (
	"errors"
	"path/filepath"
)

// dataHome returns the mairc directory on goos: ~/Library/Application Support/mairc on macOS, %LOCALAPPDATA%\mairc on
// Windows, and $XDG_DATA_HOME/mairc else $HOME/.local/share/mairc everywhere else.
func dataHome(goos string, getenv func(string) string, userConfigDir, userHomeDir func() (string, error)) (string, error) {
	switch goos {
	case "darwin":
		if dir, err := userConfigDir(); err == nil && dir != "" {
			return filepath.Join(dir, "mairc"), nil
		}
		if home, err := userHomeDir(); err == nil && home != "" {
			return filepath.Join(home, "Library", "Application Support", "mairc"), nil
		}
		return "", errors.New("no home: set MAIRC_HOME or HOME")
	case "windows":
		if dir := getenv("LOCALAPPDATA"); dir != "" {
			return filepath.Join(dir, "mairc"), nil
		}
		if dir, err := userConfigDir(); err == nil && dir != "" {
			return filepath.Join(dir, "mairc"), nil
		}
		return "", errors.New("no home: set MAIRC_HOME or LOCALAPPDATA")
	default:
		if x := getenv("XDG_DATA_HOME"); x != "" {
			return filepath.Join(x, "mairc"), nil
		}
		if h := getenv("HOME"); h != "" {
			return filepath.Join(h, ".local", "share", "mairc"), nil
		}
		return "", errors.New("no home: set MAIRC_HOME, XDG_DATA_HOME or HOME")
	}
}
