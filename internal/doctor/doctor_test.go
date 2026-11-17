package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/horus14-wq/microagentIRC/internal/install"
)

// fake is a status for one CLI: an existing binary unless path is overridden.
func fake(t *testing.T, name string, mcp, hook, fallback bool) install.Status {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "mairc")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700); err != nil { // #nosec G306 -- an executable stub
		t.Fatal(err)
	}
	s := install.Status{Name: name, Detected: true, MCPCommand: bin, MCPPath: "/cfg/" + name + ".json"}
	s.MCPPresent = mcp
	if hook {
		s.HookPath, s.HookPresent, s.HookCommand = "/cfg/"+name+"-hooks.json", true, bin+" hook --client claude"
	}
	if fallback {
		s.FallbackPath, s.FallbackPresent = "/cfg/"+name+"-AGENTS.md", true
	}
	return s
}

func healthyDaemon() Probes {
	return Probes{
		Health: func() error { return nil },
		Status: func() (int, int, error) { return 2, 3, nil },
		Hook:   func(string) (string, error) { return "", nil },
	}
}

// runOne runs the doctor over one CLI and returns its output and exit code.
func runOne(t *testing.T, s install.Status, p Probes) (string, int) {
	t.Helper()
	var b strings.Builder
	code := Run(Options{Out: &b, CLIs: []install.Status{s}, Probes: p, Daemon: "http://127.0.0.1:9"})
	return b.String(), code
}

func TestHealthyCLIReportsOkAndExitsZero(t *testing.T) {
	out, code := runOne(t, fake(t, "claude", true, true, false), healthyDaemon())
	if code != 0 {
		t.Fatalf("exit = %d, want 0; output:\n%s", code, out)
	}
	for _, want := range []string{"daemon: ok (2 sessions, 3 agents)", "claude: ok — MCP and hook in /cfg/claude.json", "everything checked is healthy"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestDaemonNotReachableSaysHowToStartIt(t *testing.T) {
	p := healthyDaemon()
	p.Health = func() error { return errors.New("connection refused") }
	out, code := runOne(t, fake(t, "claude", true, true, false), p)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	want := "daemon: not reachable at http://127.0.0.1:9 — start it with `mairc daemon`, or just run any mairc command"
	if !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
	if strings.Contains(out, "daemon: ok") {
		t.Error("a failed health probe still reported ok")
	}
}

func TestRejectedTokenIsItsOwnFix(t *testing.T) {
	p := healthyDaemon()
	p.Status = func() (int, int, error) { return 0, 0, errors.New("status 401") }
	out, code := runOne(t, fake(t, "claude", true, true, false), p)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	want := "daemon: the token was rejected — check $MAIRC_HOME/token; if it is wrong, restart the daemon so it writes a new one"
	if !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
}

func TestMissingMCPEntryNamesTheInstallCommand(t *testing.T) {
	out, code := runOne(t, fake(t, "codex", false, false, false), healthyDaemon())
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	want := "codex: no mairc MCP entry in /cfg/codex.json — run `mairc install --only codex`"
	if !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
}

func TestEntryPointingAtAMissingBinaryIsAFailure(t *testing.T) {
	s := fake(t, "claude", true, true, false)
	s.MCPCommand = filepath.Join(t.TempDir(), "gone", "mairc")
	out, code := runOne(t, s, healthyDaemon())
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	want := "claude: the MCP entry runs " + s.MCPCommand + ", which does not exist — run `mairc install --only claude`"
	if !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
}

func TestRelativeEntryCommandIsAFailure(t *testing.T) {
	s := fake(t, "claude", true, true, false)
	s.MCPCommand = "mairc"
	out, _ := runOne(t, s, healthyDaemon())
	want := "claude: the MCP entry runs \"mairc\", which is not an absolute path — run `mairc install --only claude`"
	if !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
}

func TestHookThatPrintsWithNothingNewIsAFailure(t *testing.T) {
	p := healthyDaemon()
	p.Hook = func(string) (string, error) { return "irc:\nroom alice: hi\n", nil }
	out, code := runOne(t, fake(t, "claude", true, true, false), p)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(out, "claude: the hook printed something with nothing new") ||
		!strings.Contains(out, "run `mairc install --only claude`") {
		t.Errorf("output lacks the hook fix line:\n%s", out)
	}
}

func TestHookThatCannotRunNamesTheCommand(t *testing.T) {
	p := healthyDaemon()
	p.Hook = func(string) (string, error) { return "", errors.New("exit status 127") }
	out, _ := runOne(t, fake(t, "claude", true, true, false), p)
	if !strings.Contains(out, "claude: the hook does not run (exit status 127)") {
		t.Errorf("output lacks the hook failure line:\n%s", out)
	}
}

func TestMissingHookSaysDeliveryIsOff(t *testing.T) {
	// The CLI has a hook file mairc could write into, and mairc's hook is not in it.
	s := fake(t, "claude", true, false, false)
	s.HookPath = "/cfg/claude-hooks.json"
	out, code := runOne(t, s, healthyDaemon())
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	want := "claude: no mairc hook in /cfg/claude-hooks.json — message delivery is off; run `mairc install --only claude`"
	if !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
}

func TestNoHookCLIIsHealthyAndSaysWhy(t *testing.T) {
	out, code := runOne(t, fake(t, "prime-agent", true, false, true), healthyDaemon())
	if code != 0 {
		t.Fatalf("exit = %d, want 0; output:\n%s", code, out)
	}
	want := "prime-agent: MCP ok (read-it-yourself note in /cfg/prime-agent-AGENTS.md) — no hook, so mail arrives when the agent calls irc_read"
	if !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
}

func TestUnreadableConfigIsAFailure(t *testing.T) {
	s := fake(t, "opencode", true, false, true)
	s.Problem = "/cfg/opencode.json: not a JSON object"
	out, code := runOne(t, s, healthyDaemon())
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(out, "opencode: cannot read /cfg/opencode.json: not a JSON object") {
		t.Errorf("output lacks the parse failure line:\n%s", out)
	}
}

func TestUndetectedUnconfiguredCLIIsOneLineAndNoProblem(t *testing.T) {
	var b strings.Builder
	code := Run(Options{Out: &b, CLIs: []install.Status{{Name: "devin"}}, Probes: healthyDaemon()})
	if code != 0 {
		t.Fatalf("exit = %d, want 0; output:\n%s", code, b.String())
	}
	if !strings.Contains(b.String(), "devin: not found") {
		t.Errorf("output lacks the not-found line:\n%s", b.String())
	}
}

func TestLiveOffersTheCheckAndSaysItSpendsTokens(t *testing.T) {
	var b strings.Builder
	code := Run(Options{Out: &b, CLIs: []install.Status{fake(t, "claude", true, true, false)}, Probes: healthyDaemon()})
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(b.String(), "`mairc doctor --live` runs each CLI once (spends your tokens)") {
		t.Errorf("output does not offer the live check:\n%s", b.String())
	}
}

func TestLiveRunReachingTheDaemonIsHealthy(t *testing.T) {
	p := healthyDaemon()
	asked := 0
	p.Live = func(cli string) (bool, error) {
		if cli != "claude" {
			t.Fatalf("live ran %q, want claude", cli)
		}
		return true, nil
	}
	var b strings.Builder
	code := Run(Options{
		Out: &b, CLIs: []install.Status{fake(t, "claude", true, true, false)}, Probes: p, Live: true,
		Spend: func(string) bool { asked++; return true },
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0; output:\n%s", code, b.String())
	}
	if asked != 1 {
		t.Fatalf("asked %d times, want 1", asked)
	}
	if !strings.Contains(b.String(), "live claude: the tool call reached the daemon") {
		t.Errorf("output lacks the reach line:\n%s", b.String())
	}
}

func TestLiveRunThatDoesNotReachIsAFailure(t *testing.T) {
	p := healthyDaemon()
	p.Live = func(string) (bool, error) { return false, nil }
	var b strings.Builder
	code := Run(Options{
		Out: &b, CLIs: []install.Status{fake(t, "claude", true, true, false)}, Probes: p, Live: true,
		Spend: func(string) bool { return true },
	})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(b.String(), "live claude: it ran but no session reached the daemon") {
		t.Errorf("output lacks the not-reached line:\n%s", b.String())
	}
}

func TestLiveRunDeclinedIsSkippedNotAFailure(t *testing.T) {
	p := healthyDaemon()
	p.Live = func(string) (bool, error) {
		t.Fatal("live ran after the user declined")
		return false, nil
	}
	var b strings.Builder
	code := Run(Options{
		Out: &b, CLIs: []install.Status{fake(t, "claude", true, true, false)}, Probes: p, Live: true,
		Spend: func(string) bool { return false },
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0; output:\n%s", code, b.String())
	}
	if !strings.Contains(b.String(), "live claude: skipped (not run)") {
		t.Errorf("output lacks the skip line:\n%s", b.String())
	}
}

func TestLiveCommandOnlyForRecordedCLIs(t *testing.T) {
	for _, cli := range []string{"claude", "codex", "opencode"} {
		if _, ok := LiveCommand(cli); !ok {
			t.Errorf("LiveCommand(%q) has no entry", cli)
		}
	}
	if _, ok := LiveCommand("devin"); ok {
		t.Error("LiveCommand(devin) guessed a command")
	}
}
