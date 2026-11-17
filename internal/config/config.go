// Package config holds the settings of mairc: where its files live, the defaults, validation, loading and saving
// config.json, and the `mairc config` subcommand's logic.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/horus14-wq/microagentIRC/internal/core"
)

// Paths are the files of one mairc installation.
type Paths struct {
	Home, Config, DB, Token, Log, PID string
}

// ResolvePaths returns the paths under $MAIRC_HOME, else the per-OS data directory of dataHome. File names:
// config.json, mairc.db, token, daemon.log, daemon.pid. getenv is os.Getenv in production.
func ResolvePaths(getenv func(string) string) (Paths, error) {
	return resolve(runtime.GOOS, getenv, os.UserConfigDir, os.UserHomeDir)
}

// resolve is ResolvePaths with the OS name and the two directory lookups injected, so every rule is testable on any
// host. MAIRC_HOME wins on every OS.
func resolve(goos string, getenv func(string) string, userConfigDir, userHomeDir func() (string, error)) (Paths, error) {
	home := getenv("MAIRC_HOME")
	if home == "" {
		var err error
		if home, err = dataHome(goos, getenv, userConfigDir, userHomeDir); err != nil {
			return Paths{}, err
		}
	}
	return Paths{
		Home:   home,
		Config: filepath.Join(home, "config.json"),
		DB:     filepath.Join(home, "mairc.db"),
		Token:  filepath.Join(home, "token"),
		Log:    filepath.Join(home, "daemon.log"),
		PID:    filepath.Join(home, "daemon.pid"),
	}, nil
}

// EnsureHome creates Home with mode 0700 if missing.
func EnsureHome(p Paths) error {
	return os.MkdirAll(p.Home, 0o700)
}

// Load reads the settings file. A missing file returns the defaults and no error.
// Keys absent from the file keep their defaults. An unknown key or an invalid value is an error
// naming the key.
func Load(path string) (core.Settings, error) {
	s := core.DefaultSettings()
	raw, err := os.ReadFile(path) // #nosec G304 -- path is the caller-provided file; reading it is the function's contract
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return s, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return s, fmt.Errorf("config: %w", err)
	}
	for key, value := range fields {
		if !known(key) {
			return s, fmt.Errorf("unknown setting: %s", key)
		}
		if err := setJSON(&s, key, value); err != nil {
			return s, err
		}
	}
	return s, Validate(s)
}

// Save writes the settings as indented JSON, atomically (temp file in the same directory, fsync,
// rename), mode 0600. On Windows the mode bits are not an access control: the file is private because it lives in
// the per-user data directory (see ResolvePaths).
func Save(path string, s core.Settings) error {
	if err := Validate(s); err != nil {
		return err
	}
	body, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "config-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if _, err := tmp.Write(append(body, '\n')); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// A numeric or boolean setting's bounds; kind picks the error text.
type field struct {
	min, max int
	boolean  bool
	listen   bool
	set      func(*core.Settings, int)
	get      func(*core.Settings) string
}

// The settings table of plan.md §4, in Keys() order. A function, not a package-level variable:
// nothing mutable may live at package scope.
func fields() map[string]field {
	return map[string]field{
		"max_private_len":     {min: 1, max: 999, set: func(s *core.Settings, v int) { s.MaxPrivateLen = v }, get: func(s *core.Settings) string { return strconv.Itoa(s.MaxPrivateLen) }},
		"max_public_len":      {min: 1, max: 999, set: func(s *core.Settings, v int) { s.MaxPublicLen = v }, get: func(s *core.Settings) string { return strconv.Itoa(s.MaxPublicLen) }},
		"max_room_lines":      {min: 1, max: 100, set: func(s *core.Settings, v int) { s.MaxRoomLines = v }, get: func(s *core.Settings) string { return strconv.Itoa(s.MaxRoomLines) }},
		"private_enabled":     {boolean: true, set: func(s *core.Settings, v int) { s.PrivateEnabled = v != 0 }, get: func(s *core.Settings) string { return strconv.FormatBool(s.PrivateEnabled) }},
		"public_enabled":      {boolean: true, set: func(s *core.Settings, v int) { s.PublicEnabled = v != 0 }, get: func(s *core.Settings) string { return strconv.FormatBool(s.PublicEnabled) }},
		"private_cooldown_s":  {min: 0, max: 86400, set: func(s *core.Settings, v int) { s.PrivateCooldownS = v }, get: func(s *core.Settings) string { return strconv.Itoa(s.PrivateCooldownS) }},
		"public_cooldown_s":   {min: 0, max: 86400, set: func(s *core.Settings, v int) { s.PublicCooldownS = v }, get: func(s *core.Settings) string { return strconv.Itoa(s.PublicCooldownS) }},
		"keep_messages":       {min: 1, max: 1000000, set: func(s *core.Settings, v int) { s.KeepMessages = v }, get: func(s *core.Settings) string { return strconv.Itoa(s.KeepMessages) }},
		"keep_hours":          {min: 1, max: 8760, set: func(s *core.Settings, v int) { s.KeepHours = v }, get: func(s *core.Settings) string { return strconv.Itoa(s.KeepHours) }},
		"check_private_every": {min: 1, max: 1000, set: func(s *core.Settings, v int) { s.CheckPrivateEvery = v }, get: func(s *core.Settings) string { return strconv.Itoa(s.CheckPrivateEvery) }},
		"check_room_every":    {min: 1, max: 1000, set: func(s *core.Settings, v int) { s.CheckRoomEvery = v }, get: func(s *core.Settings) string { return strconv.Itoa(s.CheckRoomEvery) }},
		"listen":              {listen: true, get: func(s *core.Settings) string { return s.Listen }},
		"hook_timeout_ms":     {min: 50, max: 5000, set: func(s *core.Settings, v int) { s.HookTimeoutMS = v }, get: func(s *core.Settings) string { return strconv.Itoa(s.HookTimeoutMS) }},
	}
}

func known(key string) bool {
	_, ok := fields()[key]
	return ok
}

// setJSON decodes one JSON value strictly into its field; the error names the key.
func setJSON(s *core.Settings, key string, raw json.RawMessage) error {
	f := fields()[key]
	if f.listen {
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return fmt.Errorf("%s: invalid value", key)
		}
		if !loopback(v) {
			return errors.New("listen: must be a loopback host:port")
		}
		s.Listen = v
		return nil
	}
	if f.boolean {
		var v bool
		if err := json.Unmarshal(raw, &v); err != nil {
			return fmt.Errorf("%s: must be true or false", key)
		}
		f.set(s, map[bool]int{true: 1, false: 0}[v])
		return nil
	}
	var v int
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("%s: invalid value", key)
	}
	if v < f.min || v > f.max {
		return fmt.Errorf("%s: must be between %d and %d", key, f.min, f.max)
	}
	f.set(s, v)
	return nil
}

// Validate checks every field against its range.
func Validate(s core.Settings) error {
	if !loopback(s.Listen) {
		return errors.New("listen: must be a loopback host:port")
	}
	for key, f := range fields() {
		if f.listen || f.boolean {
			continue
		}
		v, _ := strconv.Atoi(f.get(&s))
		if v < f.min || v > f.max {
			return fmt.Errorf("%s: must be between %d and %d", key, f.min, f.max)
		}
	}
	return nil
}

// loopback reports whether value is host:port with a loopback host and a port in 1–65535.
func loopback(value string) bool {
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return false
	}
	switch host {
	case "127.0.0.1", "::1", "localhost":
	default:
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535
}

// Keys lists the setting keys in the order of the plan.md §4 table.
func Keys() []string {
	return []string{
		"max_private_len",
		"max_public_len",
		"max_room_lines",
		"private_enabled",
		"public_enabled",
		"private_cooldown_s",
		"public_cooldown_s",
		"keep_messages",
		"keep_hours",
		"check_private_every",
		"check_room_every",
		"listen",
		"hook_timeout_ms",
	}
}

// Get returns one setting as text ("256", "true", "127.0.0.1:4747").
func Get(s core.Settings, key string) (string, error) {
	f, ok := fields()[key]
	if !ok {
		return "", fmt.Errorf("unknown setting: %s", key)
	}
	return f.get(&s), nil
}

// Set parses value for key, stores it in s and validates the result. Booleans accept
// true/false/on/off/1/0.
func Set(s *core.Settings, key, value string) error {
	f, ok := fields()[key]
	if !ok {
		return fmt.Errorf("unknown setting: %s", key)
	}
	if f.listen {
		if !loopback(value) {
			return errors.New("listen: must be a loopback host:port")
		}
		s.Listen = value
		return Validate(*s)
	}
	if f.boolean {
		v, ok := map[string]int{
			"true": 1, "on": 1, "1": 1,
			"false": 0, "off": 0, "0": 0,
		}[strings.ToLower(value)]
		if !ok {
			return fmt.Errorf("%s: must be true or false", key)
		}
		f.set(s, v)
		return Validate(*s)
	}
	v, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("%s: must be between %d and %d", key, f.min, f.max)
	}
	if v < f.min || v > f.max {
		return fmt.Errorf("%s: must be between %d and %d", key, f.min, f.max)
	}
	f.set(s, v)
	return Validate(*s)
}

// ReadToken returns the trimmed content of the token file.
func ReadToken(path string) (string, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- path is the caller-provided token file; reading it is the function's contract
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

// emit writes a line, ignoring the error: the caller checks the exit code, not the pipe.
func emit(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

// Run implements `mairc config`: with no arguments it prints every "key = value" line in Keys()
// order; `get <key>` prints the value; `set <key> <value>` loads, sets, saves and prints
// "key = value". It writes to out and errOut and returns the process exit code (0 ok, 1 error,
// 2 usage). After a successful set it calls reload(), which the caller wires to the daemon; a
// reload error is printed to errOut as "saved; daemon not reloaded: <err>" and still returns 0.
func Run(args []string, p Paths, out, errOut io.Writer, reload func() error) int {
	s, err := Load(p.Config)
	if err != nil {
		emit(errOut, "%s\n", err)
		return 1
	}
	switch {
	case len(args) == 0:
		for _, key := range Keys() {
			v, _ := Get(s, key)
			emit(out, "%s = %s\n", key, v)
		}
		return 0
	case args[0] == "get" && len(args) == 2:
		v, err := Get(s, args[1])
		if err != nil {
			emit(errOut, "%s\n", err)
			return 1
		}
		emit(out, "%s\n", v)
		return 0
	case args[0] == "set" && len(args) == 3:
		if err := Set(&s, args[1], args[2]); err != nil {
			emit(errOut, "%s\n", err)
			return 1
		}
		if err := Save(p.Config, s); err != nil {
			emit(errOut, "%s\n", err)
			return 1
		}
		v, _ := Get(s, args[1])
		emit(out, "%s = %s\n", args[1], v)
		if reload != nil {
			if err := reload(); err != nil {
				emit(errOut, "saved; daemon not reloaded: %s\n", err)
			}
		}
		return 0
	default:
		emit(errOut, "usage: mairc config [get <key> | set <key> <value>]\n")
		return 2
	}
}
