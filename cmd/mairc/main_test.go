package main

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const testToken = "test-token-0123"

// envOf turns a map into a getenv function; nothing reads the real environment.
func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// home makes a MAIRC_HOME whose config points at listen, with a token file when withToken is set.
func home(t *testing.T, listen string, withToken bool) string {
	t.Helper()
	dir := t.TempDir()
	if listen != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"listen":"`+listen+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if withToken {
		if err := os.WriteFile(filepath.Join(dir, "token"), []byte(testToken+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// refused returns a loopback address nothing listens on.
func refused(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// fakeDaemon serves the daemon endpoints the command uses and records who asked.
type fakeDaemon struct {
	mu      sync.Mutex
	calls   []string
	auth    []string
	reload  int    // status for /v1/reload
	status  string // body for /v1/status
	hook    string // body for /v1/hook (200), or "" for 204
	mcpBody string // body for /mcp (200)
}

func (f *fakeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	_, _ = io.Copy(io.Discard, r.Body)
	switch r.URL.Path {
	case "/healthz":
		_, _ = io.WriteString(w, "ok")
	case "/v1/reload":
		w.WriteHeader(f.reload)
	case "/v1/status":
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, f.status)
	case "/v1/hook":
		if f.hook == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, f.hook)
	case "/mcp":
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "s-1")
		_, _ = io.WriteString(w, f.mcpBody)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeDaemon) seen(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, c := range f.calls {
		if c == call && f.auth[i] == "Bearer "+testToken {
			return true
		}
	}
	return false
}

func startFake(t *testing.T, f *fakeDaemon) string {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func runWith(args []string, env map[string]string, stdin io.Reader) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(args, envOf(env), stdin, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestUsageAndExitCodes(t *testing.T) {
	env := map[string]string{"MAIRC_HOME": t.TempDir()}
	cases := []struct {
		name           string
		args           []string
		code           int
		stdout, stderr string
	}{
		{"no argument", nil, 2, "", usageLine + "\n"},
		{"unknown command", []string{"chat"}, 2, "", usageLine + "\n"},
		{"version", []string{"version"}, 0, version + "\n", ""},
		{"version with an argument", []string{"version", "x"}, 2, "", "usage: mairc version\n"},
		{"daemon with a flag", []string{"daemon", "--fast"}, 2, "", "usage: mairc daemon\n"},
		{"stdio with an argument", []string{"stdio", "x"}, 2, "", "usage: mairc stdio\n"},
		{"status with a flag", []string{"status", "-v"}, 2, "", "usage: mairc status\n"},
		// mairc-x2y: the install usage line names the new flags (it replaces the cli bead's line).
		{"install with an unknown flag", []string{"install", "--global"}, 2, "", installUsage + "\n"},
		{"install with an argument", []string{"install", "now"}, 2, "", installUsage + "\n"},
		{"install --user and --project together", []string{"install", "--user", "--project"}, 2, "", installUsage + "\n"},
		{"hook with a bad flag", []string{"hook", "--bogus"}, 0, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := runWith(tc.args, env, nil)
			if code != tc.code || out != tc.stdout || errOut != tc.stderr {
				t.Fatalf("run(%q) = %d, stdout %q, stderr %q; want %d, %q, %q",
					tc.args, code, out, errOut, tc.code, tc.stdout, tc.stderr)
			}
		})
	}
}

// An unknown --only name stops before any file is read (it changes nothing in the real home).
func TestInstallOnlyRejectsAnUnknownCLI(t *testing.T) {
	code, out, _ := runWith([]string{"install", "--only", "claude,vim", "--yes"}, map[string]string{}, nil)
	if code != 2 || !strings.HasPrefix(out, "usage: unknown CLI vim (supported: ") {
		t.Fatalf("install --only claude,vim = %d, stdout %q", code, out)
	}
}

// The yes/no question: only y or yes proceeds; anything else, end of input included, is no.
func TestAskAcceptsOnlyYes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{{"y\n", true}, {"YES\n", true}, {"n\n", false}, {"\n", false}, {"", false}, {"yep\n", false}} {
		var out bytes.Buffer
		if got := ask(strings.NewReader(tc.in), &out, []string{"/h/.claude.json"}); got != tc.want {
			t.Fatalf("ask(%q) = %v", tc.in, got)
		}
		if out.String() != "mairc will change:\n  /h/.claude.json\nProceed? [y/N] " {
			t.Fatalf("prompt = %q", out.String())
		}
	}
}

// --print is the one install path a test may take: it changes no file.
func TestInstallPrintWiresTheBinary(t *testing.T) {
	code, out, errOut := runWith([]string{"install", "--print"}, map[string]string{}, nil)
	exe, _ := os.Executable()
	exe, _ = filepath.Abs(exe)
	if code != 0 || errOut != "" || !strings.Contains(out, "MCP (stdio): "+exe+" stdio\n") {
		t.Fatalf("install --print = %d, stderr %q, stdout:\n%s", code, errOut, out)
	}
}

func TestConfigGetWorks(t *testing.T) {
	env := map[string]string{"MAIRC_HOME": home(t, "127.0.0.1:4999", false)}
	code, out, errOut := runWith([]string{"config", "get", "listen"}, env, nil)
	if code != 0 || errOut != "" || out != "127.0.0.1:4999\n" {
		t.Fatalf("config get listen = %d, stdout %q, stderr %q", code, out, errOut)
	}
}

func TestHookIsSilentOnEveryProblem(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"no home at all", []string{"hook"}, map[string]string{}},
		{"no token", []string{"hook"}, map[string]string{"MAIRC_HOME": t.TempDir()}},
		{"bad flag", []string{"hook", "--nope"}, map[string]string{"MAIRC_HOME": t.TempDir()}},
		{"unknown client", []string{"hook", "--client", "vim"}, map[string]string{"MAIRC_HOME": t.TempDir()}},
		{"stray argument", []string{"hook", "extra"}, map[string]string{"MAIRC_HOME": t.TempDir()}},
		{"daemon not running", []string{"hook"}, map[string]string{"MAIRC_HOME": home(t, refused(t), true)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := runWith(tc.args, tc.env, strings.NewReader(`{"hook_event_name":"PostToolUse"}`))
			if code != 0 || out != "" || errOut != "" {
				t.Fatalf("hook = %d, stdout %q, stderr %q; want 0 and nothing", code, out, errOut)
			}
		})
	}
}

func TestHookDeliversThroughTheDaemon(t *testing.T) {
	f := &fakeDaemon{hook: "#main greenfox1: hi"}
	env := map[string]string{"MAIRC_HOME": home(t, startFake(t, f), true), "MAIRC_NAME": "bluecat2"}
	code, out, errOut := runWith([]string{"hook", "--client", "claude", "--event", "SessionStart"}, env, nil)
	if code != 0 || errOut != "" || out != "irc:\n#main greenfox1: hi\n" {
		t.Fatalf("hook = %d, stdout %q, stderr %q", code, out, errOut)
	}
	if !f.seen("POST /v1/hook") {
		t.Fatalf("the hook did not reach the daemon with the token: %v", f.calls)
	}
}

// The Claude Code hook entry run by Grok: on UserPromptSubmit (output discarded by Grok) the daemon is not asked, so
// the message stays unread; on PostToolUse it is delivered as JSON.
func TestHookUnderGrokConsumesOnlyWhatGrokDelivers(t *testing.T) {
	f := &fakeDaemon{hook: "#main greenfox1: hi"}
	env := map[string]string{
		"MAIRC_HOME": home(t, startFake(t, f), true), "MAIRC_NAME": "bluecat2", "GROK_HOOK_EVENT": "UserPromptSubmit",
	}
	code, out, errOut := runWith([]string{"hook", "--client", "claude", "--event", "UserPromptSubmit"}, env, nil)
	if code != 0 || out != "" || errOut != "" || f.seen("POST /v1/hook") {
		t.Fatalf("UserPromptSubmit under Grok = %d, stdout %q, stderr %q, calls %v", code, out, errOut, f.calls)
	}
	env["GROK_HOOK_EVENT"] = "PostToolUse"
	code, out, _ = runWith([]string{"hook", "--client", "claude", "--event", "PostToolUse"}, env, nil)
	if code != 0 || !strings.Contains(out, `"additionalContext":"irc:\n#main greenfox1: hi"`) {
		t.Fatalf("PostToolUse under Grok = %d, stdout %q", code, out)
	}
}

func TestConfigSetReloadsTheDaemon(t *testing.T) {
	f := &fakeDaemon{reload: http.StatusNoContent}
	env := map[string]string{"MAIRC_HOME": home(t, startFake(t, f), true)}
	code, _, errOut := runWith([]string{"config", "set", "hook_timeout_ms", "400"}, env, nil)
	if code != 0 || errOut != "" || !f.seen("POST /v1/reload") {
		t.Fatalf("config set = %d, stderr %q, calls %v", code, errOut, f.calls)
	}
}

func TestConfigSetReportsADaemonThatIsDown(t *testing.T) {
	env := map[string]string{"MAIRC_HOME": home(t, refused(t), true)}
	code, _, errOut := runWith([]string{"config", "set", "hook_timeout_ms", "400"}, env, nil)
	if code != 0 || errOut != "saved; daemon not reloaded: daemon not running\n" {
		t.Fatalf("config set = %d, stderr %q", code, errOut)
	}
}

func TestStatus(t *testing.T) {
	f := &fakeDaemon{status: `{"version":"0.1.0","uptime_s":3725,"agents":2,"sessions":3}`}
	running := map[string]string{"MAIRC_HOME": home(t, startFake(t, f), true)}
	cases := []struct {
		name string
		env  map[string]string
		code int
		out  string
	}{
		{"running", running, 0, "running 0.1.0, 2 agents, 3 sessions, up 1h2m5s\n"},
		{"no daemon", map[string]string{"MAIRC_HOME": home(t, refused(t), true)}, 1, "not running\n"},
		{"never started (no token)", map[string]string{"MAIRC_HOME": t.TempDir()}, 1, "not running\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := runWith([]string{"status"}, tc.env, nil)
			if code != tc.code || out != tc.out || errOut != "" {
				t.Fatalf("status = %d, stdout %q, stderr %q", code, out, errOut)
			}
		})
	}
}

func TestStdioForwardsOneRequestEndToEnd(t *testing.T) {
	response := `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18"}}`
	f := &fakeDaemon{mcpBody: response}
	env := map[string]string{"MAIRC_HOME": home(t, startFake(t, f), true)}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n")
	code, out, errOut := runWith([]string{"stdio"}, env, in)
	if code != 0 || errOut != "" {
		t.Fatalf("stdio = %d, stderr %q", code, errOut)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 1 || lines[0] != response {
		t.Fatalf("stdout = %q, want exactly the one response line", out)
	}
	if !f.seen("POST /mcp") {
		t.Fatalf("the request did not reach /mcp with the token: %v", f.calls)
	}
}

// The binary builds without cgo and links no module but this one, modernc.org/sqlite and what sqlite itself needs.
func TestBuildLinksOnlyAllowedModules(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("the go command is not on PATH: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "mairc")
	build := exec.CommandContext(t.Context(), goBin, "build", "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("CGO_ENABLED=0 go build: %v\n%s", err, out)
	}
	info, err := exec.CommandContext(t.Context(), goBin, "version", "-m", bin).Output()
	if err != nil {
		t.Fatalf("go version -m: %v", err)
	}
	graph, err := exec.CommandContext(t.Context(), goBin, "mod", "graph").Output()
	if err != nil {
		t.Fatalf("go mod graph: %v", err)
	}
	allowed := sqliteClosure(string(graph))
	if !allowed["modernc.org/sqlite"] {
		t.Fatalf("modernc.org/sqlite is not in the module graph:\n%s", graph)
	}
	scanner := bufio.NewScanner(bytes.NewReader(info))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "dep" && !allowed[fields[1]] {
			t.Errorf("the binary links %s, which is neither this module nor modernc.org/sqlite's", fields[1])
		}
	}
}

// sqliteClosure returns modernc.org/sqlite and every module it requires, transitively, from `go mod graph`.
func sqliteClosure(graph string) map[string]bool {
	edges := map[string][]string{}
	for _, line := range strings.Split(graph, "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 {
			edges[parts[0]] = append(edges[parts[0]], parts[1])
		}
	}
	path := func(mod string) string { return strings.SplitN(mod, "@", 2)[0] }
	seen := map[string]bool{}
	var queue []string
	for from, tos := range edges {
		for _, to := range tos {
			if path(to) == "modernc.org/sqlite" && !strings.Contains(from, "@") {
				queue = append(queue, to)
			}
		}
	}
	allowed := map[string]bool{}
	for len(queue) > 0 {
		mod := queue[0]
		queue = queue[1:]
		if seen[mod] {
			continue
		}
		seen[mod] = true
		allowed[path(mod)] = true
		queue = append(queue, edges[mod]...)
	}
	return allowed
}

func TestUnstableDir(t *testing.T) {
	home := filepath.Join(os.TempDir()+"-home", "tester") // a home outside TMPDIR (TMPDIR may point anywhere)
	for _, tc := range []struct {
		path string
		want bool
	}{
		{filepath.Join(os.TempDir(), "mairc"), true},
		{filepath.Join(os.TempDir(), "x", "mairc"), true},
		{filepath.Join(home, "Downloads", "mairc"), true},
		{filepath.Join(home, "Downloads", "dl", "mairc"), true},
		{filepath.Join(home, "go", "bin", "mairc"), false},
		{filepath.Join(home, ".local", "bin", "mairc"), false},
		{filepath.Join(home, "Downloads-mirror", "mairc"), false},
	} {
		if got := unstableDir(tc.path, home); got != tc.want {
			t.Errorf("unstableDir(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestCopyFile(t *testing.T) {
	src := filepath.Join(t.TempDir(), "mairc")
	if err := os.WriteFile(src, []byte("binary-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "new", "dir", "mairc")
	if err := copyFile(src, dst, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dst) // #nosec G304 -- test file in t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "binary-bytes" {
		t.Fatalf("copied %q", b)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
}
