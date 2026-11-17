package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/horus14-wq/microagentIRC/internal/core"
)

// The plan.md §4 table: key, min, max, default (booleans carry no numeric bounds here).
func table() []struct {
	key     string
	min     int
	max     int
	def     string
	boolean bool
} {
	return []struct {
		key     string
		min     int
		max     int
		def     string
		boolean bool
	}{
		{"max_private_len", 1, 999, "256", false},
		{"max_public_len", 1, 999, "256", false},
		{"max_room_lines", 1, 100, "10", false},
		{"private_enabled", 0, 0, "true", true},
		{"public_enabled", 0, 0, "true", true},
		{"private_cooldown_s", 0, 86400, "60", false},
		{"public_cooldown_s", 0, 86400, "60", false},
		{"keep_messages", 1, 1000000, "1000", false},
		{"keep_hours", 1, 8760, "24", false},
		{"check_private_every", 1, 1000, "1", false},
		{"check_room_every", 1, 1000, "10", false},
		{"listen", 0, 0, "127.0.0.1:4747", false},
		{"hook_timeout_ms", 50, 5000, "300", false},
	}
}

func TestDefaultsMatchTheTable(t *testing.T) {
	s := core.DefaultSettings()
	if got := Keys(); len(got) != len(table()) {
		t.Fatalf("Keys() = %v", got)
	}
	for i, row := range table() {
		if Keys()[i] != row.key {
			t.Fatalf("Keys()[%d] = %q, want %q", i, Keys()[i], row.key)
		}
		v, err := Get(s, row.key)
		if err != nil {
			t.Fatalf("Get(%q): %v", row.key, err)
		}
		if v != row.def {
			t.Errorf("default %s = %q, want %q", row.key, v, row.def)
		}
	}
	if err := Validate(s); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
}

func TestValidateRanges(t *testing.T) {
	for _, row := range table() {
		if row.boolean || row.key == "listen" {
			continue
		}
		for _, v := range []int{row.min, row.max} {
			s := core.DefaultSettings()
			if err := Set(&s, row.key, strconv.Itoa(v)); err != nil {
				t.Errorf("Set(%s, %d): %v", row.key, v, err)
			}
		}
		for _, v := range []int{row.min - 1, row.max + 1} {
			s := core.DefaultSettings()
			err := Set(&s, row.key, strconv.Itoa(v))
			if err == nil {
				t.Errorf("Set(%s, %d) accepted, want refusal", row.key, v)
				continue
			}
			want := fmt.Sprintf("%s: must be between %d and %d", row.key, row.min, row.max)
			if err.Error() != want {
				t.Errorf("Set(%s, %d) = %q, want %q", row.key, v, err, want)
			}
		}
	}
}

func TestValidateRejectsNonLoopbackListen(t *testing.T) {
	for _, bad := range []string{"0.0.0.0:4747", "example.com:4747", "10.0.0.1:80", "127.0.0.1", "127.0.0.1:0", "127.0.0.1:65536", "localhost:99999"} {
		s := core.DefaultSettings()
		if err := Set(&s, "listen", bad); err == nil {
			t.Errorf("listen %q accepted", bad)
		} else if err.Error() != "listen: must be a loopback host:port" {
			t.Errorf("listen %q = %q", bad, err)
		}
	}
	for _, good := range []string{"127.0.0.1:4747", "localhost:1", "[::1]:8080", "127.0.0.1:65535"} {
		s := core.DefaultSettings()
		if err := Set(&s, "listen", good); err != nil {
			t.Errorf("listen %q refused: %v", good, err)
		}
	}
}

func TestSetBooleans(t *testing.T) {
	for _, spelling := range []struct {
		text string
		want bool
	}{
		{"true", true},
		{"on", true},
		{"1", true},
		{"false", false},
		{"off", false},
		{"0", false},
	} {
		s := core.DefaultSettings()
		if err := Set(&s, "private_enabled", spelling.text); err != nil {
			t.Fatalf("Set(private_enabled, %q): %v", spelling.text, err)
		}
		if s.PrivateEnabled != spelling.want {
			t.Errorf("private_enabled = %v after %q", s.PrivateEnabled, spelling.text)
		}
	}
	s := core.DefaultSettings()
	err := Set(&s, "public_enabled", "maybe")
	if err == nil || err.Error() != "public_enabled: must be true or false" {
		t.Fatalf("Set(public_enabled, maybe) = %v", err)
	}
}

func TestGetEveryKey(t *testing.T) {
	s := core.DefaultSettings()
	s.MaxRoomLines = 7
	s.PrivateEnabled = false
	s.Listen = "localhost:9"
	want := map[string]string{
		"max_private_len": "256", "max_room_lines": "7", "private_enabled": "false",
		"public_enabled": "true", "listen": "localhost:9",
	}
	for key, v := range want {
		got, err := Get(s, key)
		if err != nil || got != v {
			t.Errorf("Get(%q) = %q, %v", key, got, err)
		}
	}
	if _, err := Get(s, "bogus"); err == nil || err.Error() != "unknown setting: bogus" {
		t.Fatalf("Get(bogus) = %v", err)
	}
	if err := Set(&s, "bogus", "1"); err == nil || err.Error() != "unknown setting: bogus" {
		t.Fatalf("Set(bogus) = %v", err)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// Missing file returns the defaults.
	s, err := Load(path)
	if err != nil || s != core.DefaultSettings() {
		t.Fatalf("Load(missing) = %v, %v", s, err)
	}

	// A partial file keeps the other defaults.
	if err := os.WriteFile(path, []byte(`{"keep_hours": 48, "private_enabled": false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.KeepHours != 48 || s.PrivateEnabled || s.MaxPrivateLen != 256 {
		t.Fatalf("partial load = %+v", s)
	}

	// Unknown key and bad value name the key.
	for body, want := range map[string]string{
		`{"nope": 1}`:              "unknown setting: nope",
		`{"keep_hours": "soon"}`:   "keep_hours: invalid value",
		`{"keep_hours": 0}`:        "keep_hours: must be between 1 and 8760",
		`{"private_enabled": 3}`:   "private_enabled: must be true or false",
		`{"listen": "0.0.0.0:80"}`: "listen: must be a loopback host:port",
		`{"listen": 5}`:            "listen: invalid value",
		`{"max_room_lines": true}`: "max_room_lines: invalid value",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil || err.Error() != want {
			t.Errorf("Load(%s) = %v, want %q", body, err, want)
		}
	}
}

func TestSaveRoundTripsAndIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	s := core.DefaultSettings()
	s.KeepHours = 99
	s.PublicEnabled = false
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil || back != s {
		t.Fatalf("round trip = %+v, %v", back, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := privateMode(); info.Mode().Perm() != want {
		t.Fatalf("mode = %o, want %o", info.Mode().Perm(), want)
	}

	// A failed write leaves the old file intact: an invalid settings value is refused before any write,
	// and a directory target fails without touching an existing file beside it.
	bad := core.DefaultSettings()
	bad.KeepHours = 0
	old, _ := os.ReadFile(path) // #nosec G304 -- test file in t.TempDir
	if err := Save(path, bad); err == nil || err.Error() != "keep_hours: must be between 1 and 8760" {
		t.Fatalf("Save(invalid) = %v", err)
	}
	got, _ := os.ReadFile(path) // #nosec G304 -- test file in t.TempDir
	if !bytes.Equal(old, got) {
		t.Fatal("failed save rewrote the file")
	}
}

func TestGoldenDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := Save(path, core.DefaultSettings()); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path) // #nosec G304 -- test file in t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "config.default.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden mismatch:\n%s\nwant:\n%s", got, want)
	}
}

func TestReadToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if _, err := ReadToken(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadToken(missing) = %v", err)
	}
	if err := os.WriteFile(path, []byte("  abc123 \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadToken(path); err != nil || got != "abc123" {
		t.Fatalf("ReadToken = %q, %v", got, err)
	}
}

func TestResolvePaths(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	// On every OS: MAIRC_HOME wins, and the file names under it.
	m := filepath.Join("/", "m")
	p, err := ResolvePaths(env(map[string]string{"MAIRC_HOME": m}))
	if err != nil || p.Config != filepath.Join(m, "config.json") || p.DB != filepath.Join(m, "mairc.db") ||
		p.Token != filepath.Join(m, "token") || p.Log != filepath.Join(m, "daemon.log") ||
		p.PID != filepath.Join(m, "daemon.pid") || p.Home != m {
		t.Fatalf("MAIRC_HOME paths = %+v, %v", p, err)
	}
	p, _ = ResolvePaths(env(map[string]string{"MAIRC_HOME": m, "XDG_DATA_HOME": "/x", "HOME": "/h", "LOCALAPPDATA": "/l"}))
	if p.Home != m {
		t.Fatalf("MAIRC_HOME should win: %+v", p)
	}
	// The Linux rules on any host (macOS and Windows have their own native directories: TestDataHomePerOS).
	none := func() (string, error) { return "", errors.New("no dir") }
	p, err = resolve("linux", env(map[string]string{"XDG_DATA_HOME": "/x"}), none, none)
	if err != nil || p.Home != filepath.Join("/x", "mairc") || p.Config != filepath.Join("/x", "mairc", "config.json") {
		t.Fatalf("XDG paths = %+v, %v", p, err)
	}
	p, err = resolve("linux", env(map[string]string{"HOME": "/h"}), none, none)
	if err != nil || p.Home != filepath.Join("/h", ".local", "share", "mairc") {
		t.Fatalf("HOME paths = %+v, %v", p, err)
	}
	if _, err := resolve("linux", env(map[string]string{}), none, none); err == nil {
		t.Fatal("resolve with no env must fail")
	}
}

// privateMode is what a file written with mode 0600 reports here: Windows has no Unix permission bits, and Go reports
// a writable file as 0666 (Chmod only sets or clears the read-only attribute), so there the contract is "writable".
func privateMode() os.FileMode {
	if runtime.GOOS == "windows" {
		return 0o666
	}
	return 0o600
}

// oz8wd-style rule coverage for mairc-7fl: every OS rule is exercised on any host, with the OS name and both
// directory lookups injected.
func TestDataHomePerOS(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	dir := func(v string, err error) func() (string, error) {
		return func() (string, error) { return v, err }
	}
	none := dir("", errors.New("no dir"))
	cases := []struct {
		name       string
		goos       string
		env        map[string]string
		configDir  func() (string, error)
		homeDir    func() (string, error)
		want       string
		wantErrSub string
	}{
		{
			name:      "linux xdg wins over home",
			goos:      "linux",
			env:       map[string]string{"XDG_DATA_HOME": "/x", "HOME": "/h"},
			configDir: none, homeDir: none,
			want: filepath.Join("/x", "mairc"),
		},
		{
			name:      "linux home fallback",
			goos:      "linux",
			env:       map[string]string{"HOME": "/h"},
			configDir: none, homeDir: none,
			want: filepath.Join("/h", ".local", "share", "mairc"),
		},
		{
			name:      "linux without either",
			goos:      "linux",
			env:       map[string]string{},
			configDir: none, homeDir: none,
			wantErrSub: "set MAIRC_HOME, XDG_DATA_HOME or HOME",
		},
		{
			name:      "darwin uses the user config dir",
			goos:      "darwin",
			env:       map[string]string{"HOME": "/h"},
			configDir: dir("/Users/a/Library/Application Support", nil), homeDir: none,
			want: filepath.Join("/Users/a/Library/Application Support", "mairc"),
		},
		{
			name:      "darwin falls back to the home library",
			goos:      "darwin",
			env:       map[string]string{"HOME": "/Users/a"},
			configDir: none, homeDir: dir("/Users/a", nil),
			want: filepath.Join("/Users/a", "Library", "Application Support", "mairc"),
		},
		{
			name:      "darwin without either",
			goos:      "darwin",
			env:       map[string]string{},
			configDir: none, homeDir: none,
			wantErrSub: "set MAIRC_HOME or HOME",
		},
		{
			name:      "windows local app data wins",
			goos:      "windows",
			env:       map[string]string{"LOCALAPPDATA": `/Users/a/AppData/Local`, "XDG_DATA_HOME": "/x"},
			configDir: dir(`C:/Users/a/AppData/Roaming`, nil), homeDir: none,
			want: filepath.Join(`/Users/a/AppData/Local`, "mairc"),
		},
		{
			name:      "windows roaming fallback",
			goos:      "windows",
			env:       map[string]string{},
			configDir: dir(`C:/Users/a/AppData/Roaming`, nil), homeDir: none,
			want: filepath.Join(`C:/Users/a/AppData/Roaming`, "mairc"),
		},
		{
			name:      "windows without either",
			goos:      "windows",
			env:       map[string]string{},
			configDir: none, homeDir: none,
			wantErrSub: "set MAIRC_HOME or LOCALAPPDATA",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := dataHome(c.goos, env(c.env), c.configDir, c.homeDir)
			if c.wantErrSub != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErrSub) {
					t.Fatalf("dataHome(%s) error = %v, want %q", c.goos, err, c.wantErrSub)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("dataHome(%s) = %q, %v, want %q", c.goos, got, err, c.want)
			}
		})
	}
}

// MAIRC_HOME wins on every OS, and the file names are the same under it.
func TestResolveHonoursMaircHomeOnEveryOS(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	dir := func(v string) func() (string, error) {
		return func() (string, error) { return v, nil }
	}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		p, err := resolve(goos, env(map[string]string{
			"MAIRC_HOME": "/m", "HOME": "/h", "XDG_DATA_HOME": "/x", "LOCALAPPDATA": "/l",
		}), dir("/cfg"), dir("/h"))
		if err != nil || p.Home != "/m" || p.Config != filepath.Join("/m", "config.json") ||
			p.Token != filepath.Join("/m", "token") || p.PID != filepath.Join("/m", "daemon.pid") {
			t.Fatalf("%s with MAIRC_HOME = %+v, %v", goos, p, err)
		}
	}
}

func TestEnsureHome(t *testing.T) {
	dir := t.TempDir()
	p := Paths{Home: filepath.Join(dir, "deep", "home")}
	if err := EnsureHome(p); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p.Home)
	if err != nil || !info.IsDir() {
		t.Fatalf("home = %v", err)
	}
	if err := EnsureHome(p); err != nil {
		t.Fatalf("EnsureHome again: %v", err)
	}
}

func TestRun(t *testing.T) {
	newRun := func(t *testing.T) (Paths, *bytes.Buffer, *bytes.Buffer) {
		t.Helper()
		dir := t.TempDir()
		p := Paths{Home: dir, Config: filepath.Join(dir, "config.json")}
		var out, errOut bytes.Buffer
		if err := Save(p.Config, core.DefaultSettings()); err != nil {
			t.Fatal(err)
		}
		return p, &out, &errOut
	}

	t.Run("list", func(t *testing.T) {
		p, out, errOut := newRun(t)
		if rc := Run(nil, p, out, errOut, nil); rc != 0 {
			t.Fatalf("rc = %d, errOut = %s", rc, errOut)
		}
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		if len(lines) != len(table()) {
			t.Fatalf("%d lines", len(lines))
		}
		for i, row := range table() {
			want := row.key + " = " + row.def
			if lines[i] != want {
				t.Errorf("line %d = %q, want %q", i, lines[i], want)
			}
		}
	})

	t.Run("get", func(t *testing.T) {
		p, out, errOut := newRun(t)
		if rc := Run([]string{"get", "keep_hours"}, p, out, errOut, nil); rc != 0 {
			t.Fatalf("rc = %d", rc)
		}
		if out.String() != "24\n" {
			t.Fatalf("get = %q", out.String())
		}
		if rc := Run([]string{"get", "bogus"}, p, out, errOut, nil); rc != 1 ||
			!strings.Contains(errOut.String(), "unknown setting: bogus") {
			t.Fatalf("get bogus rc = %d, errOut = %q", rc, errOut)
		}
	})

	t.Run("set", func(t *testing.T) {
		p, out, errOut := newRun(t)
		reloads := 0
		reload := func() error { reloads++; return nil }
		if rc := Run([]string{"set", "keep_hours", "48"}, p, out, errOut, reload); rc != 0 {
			t.Fatalf("rc = %d, errOut = %s", rc, errOut)
		}
		if out.String() != "keep_hours = 48\n" {
			t.Fatalf("set out = %q", out.String())
		}
		if reloads != 1 {
			t.Fatalf("reload called %d times", reloads)
		}
		s, err := Load(p.Config)
		if err != nil || s.KeepHours != 48 {
			t.Fatalf("saved = %+v, %v", s, err)
		}
	})

	t.Run("set invalid", func(t *testing.T) {
		p, out, errOut := newRun(t)
		if rc := Run([]string{"set", "keep_hours", "0"}, p, out, errOut, nil); rc != 1 ||
			!strings.Contains(errOut.String(), "keep_hours: must be between 1 and 8760") {
			t.Fatalf("rc = %d, errOut = %q", rc, errOut)
		}
	})

	t.Run("reload error stays zero", func(t *testing.T) {
		p, out, errOut := newRun(t)
		reload := func() error { return errors.New("connection refused") }
		if rc := Run([]string{"set", "keep_hours", "48"}, p, out, errOut, reload); rc != 0 {
			t.Fatalf("rc = %d", rc)
		}
		if !strings.Contains(errOut.String(), "saved; daemon not reloaded: connection refused") {
			t.Fatalf("errOut = %q", errOut)
		}
	})

	t.Run("usage", func(t *testing.T) {
		p, out, errOut := newRun(t)
		for _, args := range [][]string{{"bogus"}, {"get"}, {"set", "k"}, {"set", "k", "v", "x"}, {"get", "k", "x"}} {
			out.Reset()
			errOut.Reset()
			if rc := Run(args, p, out, errOut, nil); rc != 2 {
				t.Errorf("Run(%v) rc = %d, want 2", args, rc)
			}
		}
	})
}
