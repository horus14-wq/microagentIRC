package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeDaemon answers POST /v1/hook with a fixed status and body and records the last request.
type fakeDaemon struct {
	status int
	body   string
	got    chan captured
}

type captured struct {
	method, path, auth, contentType string
	body                            request
}

func (f *fakeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req request
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &req)
	select {
	case f.got <- captured{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), req}:
	default:
	}
	if f.status == http.StatusOK {
		w.Header().Set("Content-Type", "text/plain")
	}
	w.WriteHeader(f.status)
	_, _ = io.WriteString(w, f.body)
}

func newDaemon(t *testing.T, status int, body string) (*httptest.Server, *fakeDaemon) {
	t.Helper()
	f := &fakeDaemon{status: status, body: body, got: make(chan captured, 1)}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return srv, f
}

func opts(baseURL string, stdout io.Writer) Options {
	return Options{
		BaseURL: baseURL,
		Token:   "tok-123",
		Timeout: 2 * time.Second,
		Client:  "plain",
		Name:    "greenfox1",
		Seat:    "seat-a",
		PIDs:    []int{4321, 4000, 1},
		Stdout:  stdout,
	}
}

func TestRequestCarriesTokenBodyAndEvent(t *testing.T) {
	cases := []struct {
		name  string
		event string
		stdin string
		want  string
	}{
		{"event from stdin JSON", "", `{"hook_event_name":"PostToolUse","tool_name":"Bash"}`, "PostToolUse"},
		{"--event wins over stdin", "SessionStart", `{"hook_event_name":"PostToolUse"}`, "SessionStart"},
		{"no event anywhere", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, f := newDaemon(t, http.StatusNoContent, "")
			o := opts(srv.URL, &bytes.Buffer{})
			o.Event = tc.event
			o.Stdin = strings.NewReader(tc.stdin)
			if code := Run(context.Background(), o); code != 0 {
				t.Fatalf("Run = %d, want 0", code)
			}
			got := <-f.got
			if got.method != http.MethodPost || got.path != "/v1/hook" {
				t.Fatalf("request = %s %s, want POST /v1/hook", got.method, got.path)
			}
			if got.auth != "Bearer tok-123" {
				t.Fatalf("Authorization = %q", got.auth)
			}
			if got.contentType != "application/json" {
				t.Fatalf("Content-Type = %q", got.contentType)
			}
			want := request{PIDs: []int{4321, 4000, 1}, Seat: "seat-a", Name: "greenfox1", Event: tc.want}
			if got.body.Seat != want.Seat || got.body.Name != want.Name || got.body.Event != want.Event ||
				len(got.body.PIDs) != 3 || got.body.PIDs[0] != 4321 || got.body.PIDs[2] != 1 {
				t.Fatalf("body = %+v, want %+v", got.body, want)
			}
		})
	}
}

func TestNilPIDsAreSentAsAnEmptyList(t *testing.T) {
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	o := opts(srv.URL, &bytes.Buffer{})
	o.PIDs = nil
	Run(context.Background(), o)
	if !bytes.Contains(raw, []byte(`"pids":[]`)) {
		t.Fatalf("body = %s, want pids []", raw)
	}
}

func refusedURL(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + ln.Addr().String()
	_ = ln.Close()
	return url
}

func TestZeroOutputWhenNothingIsDelivered(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"204 no content", http.StatusNoContent, ""},
		{"500", http.StatusInternalServerError, "boom"},
		{"401", http.StatusUnauthorized, "unauthorized"},
		{"empty 200", http.StatusOK, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newDaemon(t, tc.status, tc.body)
			var out bytes.Buffer
			if code := Run(context.Background(), opts(srv.URL, &out)); code != 0 || out.Len() != 0 {
				t.Fatalf("Run = %d, stdout %q; want 0 and nothing", code, out.String())
			}
		})
	}
	t.Run("connection refused", func(t *testing.T) {
		var out bytes.Buffer
		if code := Run(context.Background(), opts(refusedURL(t), &out)); code != 0 || out.Len() != 0 {
			t.Fatalf("Run = %d, stdout %q; want 0 and nothing", code, out.String())
		}
	})
	t.Run("malformed base URL", func(t *testing.T) {
		var out bytes.Buffer
		if code := Run(context.Background(), opts("http://[::1", &out)); code != 0 || out.Len() != 0 {
			t.Fatalf("Run = %d, stdout %q; want 0 and nothing", code, out.String())
		}
	})
}

// A daemon that answers after the deadline: nothing is printed and Run returns well within the timeout's bound.
// The handler blocks on a channel the test releases afterwards; the test itself never sleeps.
func TestTimeoutIsHonouredAndSilent(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-time.After(2 * time.Second):
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "too late")
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})
	o := opts(srv.URL, &bytes.Buffer{})
	var out bytes.Buffer
	o.Stdout = &out
	o.Timeout = 100 * time.Millisecond
	start := time.Now()
	code := Run(context.Background(), o)
	elapsed := time.Since(start)
	if code != 0 || out.Len() != 0 {
		t.Fatalf("Run = %d, stdout %q; want 0 and nothing", code, out.String())
	}
	if elapsed >= 400*time.Millisecond {
		t.Fatalf("Run took %v with a 100 ms timeout, want < 400 ms", elapsed)
	}
}

func TestGoldenVectors(t *testing.T) {
	cases := []struct {
		vector, client, event string
	}{
		{"plain", "plain", "PostToolUse"},
		{"claude-UserPromptSubmit", "claude", "UserPromptSubmit"},
		{"claude-SessionStart", "claude", "SessionStart"},
		{"claude-PostToolUse", "claude", "PostToolUse"},
		{"join-line", "claude", "SessionStart"},
	}
	dir := filepath.Join("..", "..", "testdata", "vectors", "hook")
	for _, tc := range cases {
		t.Run(tc.vector, func(t *testing.T) {
			in, err := os.ReadFile(filepath.Join(dir, tc.vector+".in.txt"))
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join(dir, tc.vector+".out.txt"))
			if err != nil {
				t.Fatal(err)
			}
			srv, _ := newDaemon(t, http.StatusOK, string(in))
			var out bytes.Buffer
			o := opts(srv.URL, &out)
			o.Client = tc.client
			o.Event = tc.event
			if code := Run(context.Background(), o); code != 0 {
				t.Fatalf("Run = %d", code)
			}
			if !bytes.Equal(out.Bytes(), want) {
				t.Fatalf("stdout:\n%q\nwant:\n%q", out.String(), want)
			}
		})
	}
}

// Quotes, a backslash, `<` and non-ASCII text survive the Claude JSON encoding unchanged (HTML escaping is off).
func TestClaudeJSONKeepsSpecialCharacters(t *testing.T) {
	body := "say \"hi\" \\ <b> grüße 你好"
	srv, _ := newDaemon(t, http.StatusOK, body)
	var out bytes.Buffer
	o := opts(srv.URL, &out)
	o.Client = "claude"
	o.Event = "PostToolUse"
	Run(context.Background(), o)
	line := out.String()
	if strings.Count(line, "\n") != 1 || !strings.HasSuffix(line, "\n") {
		t.Fatalf("want exactly one line of JSON, got %q", line)
	}
	if escaped := "\\" + "u003c"; strings.Contains(line, escaped) || !strings.Contains(line, "<b>") {
		t.Fatalf("HTML escaping is on: %q", line)
	}
	var decoded claudeOutput
	if err := json.Unmarshal([]byte(line), &decoded); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if decoded.HookSpecificOutput.HookEventName != "PostToolUse" ||
		decoded.HookSpecificOutput.AdditionalContext != "irc:\n"+body {
		t.Fatalf("decoded = %+v", decoded)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestOddStdinDoesNotBreakAnything(t *testing.T) {
	cases := []struct {
		name  string
		stdin io.Reader
	}{
		{"nil stdin", nil},
		{"empty", strings.NewReader("")},
		{"not JSON", strings.NewReader("hello there")},
		{"JSON array", strings.NewReader(`["hook_event_name"]`)},
		{"non-string event", strings.NewReader(`{"hook_event_name":42}`)},
		{"1 MiB blob", bytes.NewReader(bytes.Repeat([]byte("x"), 1<<20))},
		{"read error", failingReader{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, f := newDaemon(t, http.StatusOK, "hello")
			var out bytes.Buffer
			o := opts(srv.URL, &out)
			o.Stdin = tc.stdin
			if code := Run(context.Background(), o); code != 0 {
				t.Fatalf("Run = %d", code)
			}
			if got := <-f.got; got.body.Event != "" {
				t.Fatalf("event = %q, want empty", got.body.Event)
			}
			if out.String() != "irc:\nhello\n" {
				t.Fatalf("stdout = %q", out.String())
			}
		})
	}
}

func BenchmarkRunEmpty(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	o := Options{
		BaseURL: srv.URL, Token: "t", Timeout: time.Second, Client: "claude", Event: "PostToolUse",
		Seat: "s", PIDs: []int{1}, Stdout: io.Discard,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Run(context.Background(), o)
	}
}
