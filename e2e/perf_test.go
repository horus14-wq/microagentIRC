package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestPerformance measures the targets of plan.md §11 against the real binary and logs them. It is a measurement,
// not a gate: it runs only with MAIRC_PERF=1 and never fails on a number. docs/performance.md records a run.
//
//	MAIRC_PERF=1 go test -count=1 -run TestPerformance -v ./e2e
func TestPerformance(t *testing.T) {
	if os.Getenv("MAIRC_PERF") != "1" {
		t.Skip("set MAIRC_PERF=1 to measure")
	}
	w := newWorld(t, nil)
	a, b := w.newAgent("seat-a"), w.newAgent("seat-b")
	a.join()
	b.join()

	percentile := func(samples []time.Duration, p int) time.Duration {
		slices.Sort(samples)
		return samples[min(len(samples)-1, len(samples)*p/100)]
	}

	// 1. The whole hook process, nothing to deliver.
	hook := make([]time.Duration, 0, 500)
	for range 500 {
		ctx, cancel := context.WithTimeout(t.Context(), wait)
		cmd := exec.CommandContext(ctx, binary, "hook", "--client", "plain")
		cmd.Env = w.env("MAIRC_SEAT=seat-b")
		started := time.Now()
		out, err := cmd.Output()
		hook = append(hook, time.Since(started))
		cancel()
		if err != nil || len(out) != 0 {
			t.Fatalf("empty hook run: %v, output %q", err, out)
		}
	}
	t.Logf("hook, nothing to deliver, whole process: p50 %s  p99 %s  (500 runs)", percentile(hook, 50), percentile(hook, 99))

	// 2. tools/list size.
	t.Logf("tools/list result: %d bytes", len(a.request("tools/list", map[string]any{})))

	// 3. irc_send to the room over one HTTP session, cooldown 0.
	post := func(session, body string) (string, string) {
		ctx, cancel := context.WithTimeout(t.Context(), wait)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.base+"/mcp", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+w.token())
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.Header.Get("Mcp-Session-Id"), string(raw)
	}
	session, _ := post("", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	post(session, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"irc_name","arguments":{"name":"perf"}}}`)
	sends := make([]time.Duration, 0, 2000)
	for i := range 2000 {
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"irc_send",`+
			`"arguments":{"text":"a typical line of chat, about fifty characters long %d"}}}`, i+3, i)
		started := time.Now()
		_, answer := post(session, body)
		sends = append(sends, time.Since(started))
		if !strings.Contains(answer, `"text":"sent"`) {
			t.Fatalf("send %d: %s", i, answer)
		}
	}
	t.Logf("irc_send to the room, HTTP round trip: p50 %s  p99 %s  (2000 sends)", percentile(sends, 50), percentile(sends, 99))

	// 4. Daemon memory after those messages (1000 are kept) and 60 s idle.
	rss := func() string {
		raw, err := os.ReadFile("/proc/" + strconv.Itoa(w.daemon.Process.Pid) + "/status")
		if err != nil {
			return "unknown: " + err.Error()
		}
		for line := range strings.SplitSeq(string(raw), "\n") {
			if rest, ok := strings.CutPrefix(line, "VmRSS:"); ok {
				return strings.TrimSpace(rest)
			}
		}
		return "unknown"
	}
	t.Logf("daemon resident memory right after 2000 sends: %s", rss())
	idle := time.NewTimer(60 * time.Second)
	<-idle.C
	t.Logf("daemon resident memory after 60 s idle: %s", rss())

	// 5. The address and token, for an external load run against /v1/hook (see docs/performance.md).
	t.Logf("daemon: %s  token file: %s/token  named agent seat: env:seat-b", w.base, w.home)
}
