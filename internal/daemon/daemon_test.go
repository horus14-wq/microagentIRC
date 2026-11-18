package daemon

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/horus14-wq/microagentIRC/internal/config"
)

// freeAddr returns a loopback address with a port that was free a moment ago.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// startRun runs a daemon in the background. Its log lines arrive on the returned channel; its result on done.
func startRun(ctx context.Context, paths config.Paths) (lines chan string, done chan error) {
	lines = make(chan string, 16)
	done = make(chan error, 1)
	o := Options{
		Paths:   paths,
		Version: "test",
		Clock:   newClock(),
		Logf:    func(format string, args ...any) { lines <- fmt.Sprintf(format, args...) },
	}
	go func() { done <- Run(ctx, o) }()
	return lines, done
}

func get(t *testing.T, url, token string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

func TestRun(t *testing.T) {
	home := t.TempDir()
	paths, err := config.ResolvePaths(func(key string) string {
		if key == "MAIRC_HOME" {
			return home
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	addr := freeAddr(t)
	if err := os.WriteFile(paths.Config, []byte(`{"listen":"`+addr+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	lines, done := startRun(ctx, paths)
	if line := <-lines; line != "start test "+addr {
		t.Fatalf("first log line %q", line)
	}

	raw, err := os.ReadFile(paths.Token)
	if err != nil {
		t.Fatal(err)
	}
	token := string(raw)
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(token) {
		t.Fatalf("token %q", token)
	}
	// 0600 where Unix permission bits exist; Windows has none and Go reports a writable file as 0666.
	private := os.FileMode(0o600)
	if runtime.GOOS == "windows" {
		private = 0o666
	}
	for _, p := range []string{paths.Token, paths.PID} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != private {
			t.Fatalf("%s: %v %v", p, fi, err)
		}
	}
	pid, err := os.ReadFile(paths.PID)
	if err != nil || strings.TrimSpace(string(pid)) == "" {
		t.Fatalf("pid file %q %v", pid, err)
	}

	steps := []struct {
		name, path, token string
		wantCode          int
		wantBody          string
	}{
		{"healthz", "/healthz", "", http.StatusOK, "ok"},
		{"status needs the token", "/v1/status", "", http.StatusUnauthorized, ""},
		{"status", "/v1/status", token, http.StatusOK, `{"version":"test","uptime_s":0,"agents":0,"sessions":0,"who_calls":0}`},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			code, body := get(t, "http://"+addr+st.path, st.token)
			if code != st.wantCode || body != st.wantBody {
				t.Fatalf("got %d %q, want %d %q", code, body, st.wantCode, st.wantBody)
			}
		})
	}

	t.Run("second run is already running", func(t *testing.T) {
		lines2, done2 := startRun(t.Context(), paths)
		if err := <-done2; err != nil {
			t.Fatalf("second Run: %v", err)
		}
		if line := <-lines2; line != "already running" {
			t.Fatalf("second Run logged %q", line)
		}
		if _, err := os.Stat(paths.PID); err != nil {
			t.Fatalf("second Run touched the pid file: %v", err)
		}
	})

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if line := <-lines; line != "stop" {
		t.Fatalf("last log line %q", line)
	}
	if _, err := os.Stat(paths.PID); !os.IsNotExist(err) {
		t.Fatalf("pid file left behind: %v", err)
	}
	if _, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr); err == nil {
		t.Fatal("still listening after stop")
	}
}

// deadPID runs a process that exits at once and returns its pid, which no live process holds afterwards.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$") // #nosec G204 G702 -- the test binary itself, matching no test
	if err := cmd.Run(); err != nil {
		t.Skipf("cannot spawn a helper process: %v", err)
	}
	return cmd.Process.Pid
}

// mairc-vq8: a daemon killed outright leaves daemon.pid naming a dead process. The next daemon removes the stale
// file on start (and says so in the log); a file naming a live process — including this test's own — is left for
// the pid write that follows.
func TestStalePIDFileIsRemovedOnStart(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content func(t *testing.T) string
		wantLog string
	}{
		{"dead pid", func(t *testing.T) string { return strconv.Itoa(deadPID(t)) + "\n" }, "removed stale daemon.pid (pid "},
		{"garbage", func(*testing.T) string { return "not a pid\n" }, "removed stale daemon.pid"},
		{"live pid", func(*testing.T) string { return strconv.Itoa(os.Getpid()) + "\n" }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			paths, err := config.ResolvePaths(func(key string) string {
				if key == "MAIRC_HOME" {
					return home
				}
				return ""
			})
			if err != nil {
				t.Fatal(err)
			}
			addr := freeAddr(t)
			if err := os.WriteFile(paths.Config, []byte(`{"listen":"`+addr+`"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(paths.PID, []byte(tc.content(t)), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			lines, done := startRun(ctx, paths)
			var got []string
			for line := range lines {
				got = append(got, line)
				if line == "start test "+addr {
					break
				}
			}
			stale := false
			for _, line := range got {
				if strings.HasPrefix(line, "removed stale daemon.pid") {
					stale = true
					if tc.wantLog != "" && !strings.HasPrefix(line, tc.wantLog) {
						t.Fatalf("stale line %q, want prefix %q", line, tc.wantLog)
					}
				}
			}
			if stale != (tc.wantLog != "") {
				t.Fatalf("log lines %q: stale removal = %v, want %v", got, stale, tc.wantLog != "")
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatalf("Run: %v", err)
			}
		})
	}
}
