// Package doctor implements `mairc doctor`: it checks what `mairc install` set up — the daemon, each configured CLI's
// MCP entry and hook — and prints one line per check with a plain-language fix. `--live` additionally runs a CLI once
// headless, because only that shows whether a tool call actually reaches the daemon; it is opt-in since it spends the
// user's tokens, and the caller asks before it runs.
//
// Nothing here decides anything about the daemon or the CLIs itself: the probes are functions, so every failure
// message has a test that does not need a daemon, a CLI or a network.
package doctor

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/horus14-wq/microagentIRC/internal/install"
)

// Probes are the four things the doctor asks the outside world. Production fills them in cmd/mairc.
type Probes struct {
	// Health asks the daemon for /healthz; nil error means it answers.
	Health func() error
	// Status asks the daemon for /v1/status with the token; a non-nil error means the token was refused or the
	// daemon did not answer (Health has already told those apart).
	Status func() (sessions, agents int, err error)
	// Hook runs a recorded hook command with an empty stdin from the way mairc's hook would be invoked, returning
	// what it printed. err covers a command that cannot start or exits non-zero.
	Hook func(command string) (string, error)
	// Live runs one headless prompt through a CLI. reached reports whether the daemon saw the session the run opened;
	// err covers a CLI that could not run at all.
	Live func(cli string) (reached bool, err error)
}

// Options is one doctor run.
type Options struct {
	Out io.Writer
	// Daemon is the base URL the doctor probed, for the line that names it (e.g. "http://127.0.0.1:4747").
	Daemon string
	CLIs   []install.Status // the install table's view of this machine, from install.Statuses
	Probes Probes
	// Live turns on the headless run for the CLIs whose static checks passed. Spend is asked once per CLI before its
	// run and must return true to proceed; nil means no live run happens.
	Live  bool
	Spend func(cli string) bool
	// LivePrompt is the one short prompt sent by a live run; empty means defaultPrompt.
	LivePrompt string
}

// Prompt is the one short prompt a live run sends: the cheapest possible proof that the MCP server is reachable and
// answering, one tool call, whose arrival the daemon records as an MCP session.
func Prompt() string { return defaultPrompt }

// defaultPrompt asks for the cheapest possible proof that the MCP server is reachable and answering: one tool call,
// whose arrival the daemon itself records as an MCP session.
const defaultPrompt = "Call the mairc tool irc_who once, then stop. Do not read or edit any file."

// Run prints one line per check and returns the process exit code: 0 only when everything checked is healthy.
func Run(o Options) int {
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	var problems, configured, found int
	for _, c := range o.CLIs {
		if c.Detected {
			found++
		}
		if c.MCPPresent || c.HookPresent || c.FallbackPresent {
			configured++
		}
	}
	_, _ = fmt.Fprintf(out, "mairc doctor: %s\n", plural(found, "CLI"))
	if !daemon(out, o.Probes, o.Daemon) {
		problems++
	}
	var live []install.Status
	for _, c := range o.CLIs {
		ok, line := cli(c, o.Probes)
		_, _ = fmt.Fprintln(out, line)
		if !ok {
			problems++
		} else if c.MCPPresent {
			live = append(live, c)
		}
	}
	if configured == 0 && found > 0 {
		_, _ = fmt.Fprintln(out, "nothing is set up yet — run `mairc install`")
	}
	if o.Live {
		problems += liveRuns(out, o, live)
	} else if len(live) > 0 {
		_, _ = fmt.Fprintln(out, "not checked: whether a tool call reaches the daemon — `mairc doctor --live` runs each CLI once (spends your tokens)")
	}
	switch problems {
	case 0:
		_, _ = fmt.Fprintln(out, "everything checked is healthy")
	case 1:
		_, _ = fmt.Fprintln(out, "1 problem: the fix is on its line above")
	default:
		_, _ = fmt.Fprintf(out, "%d problems: the fix is on each line above\n", problems)
	}
	return exitCode(problems)
}

// exitCode is 0 only when everything checked is healthy.
func exitCode(problems int) int {
	if problems > 0 {
		return 1
	}
	return 0
}

// daemon prints the daemon's line and reports whether it is healthy: reachable, and accepting the token.
func daemon(out io.Writer, p Probes, at string) bool {
	if p.Health == nil {
		_, _ = fmt.Fprintln(out, "daemon: not checked by this build")
		return true
	}
	if err := p.Health(); err != nil {
		_, _ = fmt.Fprintf(out, "daemon: not reachable at %s — start it with `mairc daemon`, or just run any mairc command\n", daemonAt(at))
		return false
	}
	sessions, agents, err := status(p)
	if err != nil {
		_, _ = fmt.Fprintln(out, "daemon: the token was rejected — check $MAIRC_HOME/token; if it is wrong, restart the daemon so it writes a new one")
		return false
	}
	_, _ = fmt.Fprintf(out, "daemon: ok (%s, %s)\n", plural(sessions, "session"), plural(agents, "agent"))
	return true
}

func status(p Probes) (int, int, error) {
	if p.Status == nil {
		return 0, 0, nil
	}
	return p.Status()
}

// daemonAt is the URL shown in the daemon line: the one that was probed, else the default.
func daemonAt(at string) string {
	if at == "" {
		return "127.0.0.1:4747"
	}
	return at
}

// cli prints one CLI's line and reports whether it is healthy. A CLI that is not on this machine and not configured is
// not checked at all: silence would hide a mistake, so it gets one line and counts as neither healthy nor a problem.
func cli(c install.Status, p Probes) (bool, string) {
	switch {
	case c.Problem != "":
		return false, fmt.Sprintf("%s: cannot read %s — fix or move that file, then run `mairc install --only %s`", c.Name, c.Problem, c.Name)
	case !c.Detected && !c.MCPPresent && !c.HookPresent && !c.FallbackPresent:
		return true, c.Name + ": not found"
	case !c.MCPPresent:
		return false, fmt.Sprintf("%s: no mairc MCP entry in %s — run `mairc install --only %s`", c.Name, c.MCPPath, c.Name)
	case !filepath.IsAbs(c.MCPCommand):
		return false, fmt.Sprintf("%s: the MCP entry runs %q, which is not an absolute path — run `mairc install --only %s`", c.Name, c.MCPCommand, c.Name)
	case !executable(c.MCPCommand):
		return false, fmt.Sprintf("%s: the MCP entry runs %s, which does not exist — run `mairc install --only %s`", c.Name, c.MCPCommand, c.Name)
	}
	if c.HookPath == "" {
		note := ""
		if c.FallbackPresent {
			note = fmt.Sprintf(" (read-it-yourself note in %s)", c.FallbackPath)
		}
		return true, fmt.Sprintf("%s: MCP ok%s — no hook, so mail arrives when the agent calls irc_read", c.Name, note)
	}
	if !c.HookPresent {
		return false, fmt.Sprintf("%s: no mairc hook in %s — message delivery is off; run `mairc install --only %s`", c.Name, c.HookPath, c.Name)
	}
	if c.HookCommand == "" {
		// A plugin runs `mairc hook` inside the CLI (OpenCode): there is no recorded command to run from here.
		return true, fmt.Sprintf("%s: ok — MCP in %s, chat plugin in %s", c.Name, c.MCPPath, c.HookPath)
	}
	if p.Hook == nil {
		return true, c.Name + ": MCP and hook ok (hook not run by this build)"
	}
	output, err := p.Hook(c.HookCommand)
	switch {
	case err != nil:
		return false, fmt.Sprintf("%s: the hook does not run (%v) — check `%s` by hand, then run `mairc install --only %s`", c.Name, err, c.HookCommand, c.Name)
	case strings.TrimSpace(output) != "":
		return false, fmt.Sprintf("%s: the hook printed something with nothing new (%q) — check `%s`, then run `mairc install --only %s`", c.Name, firstLine(output), c.HookCommand, c.Name)
	}
	if c.HookNotice != "" {
		// The CLI runs the hook only after a step mairc cannot see (approving it, trusting the folder): say so
		// instead of a plain ok, and name the step.
		step := strings.TrimPrefix(c.HookNotice, c.Name+": ")
		return true, fmt.Sprintf("%s: MCP and hook in place — not checkable from outside: %s", c.Name, step)
	}
	return true, fmt.Sprintf("%s: ok — MCP and hook in %s", c.Name, c.MCPPath)
}

// liveRuns runs the CLIs whose static checks passed, one at a time, asking before each. It returns how many of them
// failed.
func liveRuns(out io.Writer, o Options, cs []install.Status) int {
	if o.Spend == nil || o.Probes.Live == nil {
		_, _ = fmt.Fprintln(out, "not checked: whether a tool call reaches the daemon — this build cannot run a CLI")
		return 0
	}
	prompt := o.LivePrompt
	if prompt == "" {
		prompt = defaultPrompt
	}
	problems := 0
	for _, c := range cs {
		// A CLI the doctor has no headless command for is not a problem: it is not checked, and says so.
		if _, ok := LiveCommand(c.Name, prompt); !ok {
			_, _ = fmt.Fprintf(out, "live %s: not checked — mairc doctor has no headless command recorded for it\n", c.Name)
			continue
		}
		if !o.Spend(c.Name) {
			_, _ = fmt.Fprintf(out, "live %s: skipped (not run)\n", c.Name)
			continue
		}
		_, _ = fmt.Fprintf(out, "live %s: asking it to call irc_who once (%s)\n", c.Name, prompt)
		reached, err := o.Probes.Live(c.Name)
		switch {
		case err != nil:
			problems++
			_, _ = fmt.Fprintf(out, "live %s: the CLI did not run (%v) — check `%s` runs, then try again\n", c.Name, err, firstWord(c.MCPCommand))
		case !reached:
			problems++
			_, _ = fmt.Fprintf(out, "live %s: it ran but its tool call did not reach the daemon — check the MCP entry in %s, then run `mairc install --only %s`\n", c.Name, c.MCPPath, c.Name)
		default:
			_, _ = fmt.Fprintf(out, "live %s: the tool call reached the daemon\n", c.Name)
		}
	}
	return problems
}

// executable reports whether path names an existing file, or on Windows any existing path.
func executable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// plural renders a count with its noun, singular or not.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// firstLine is the first line of a hook's output, for the message that quotes it.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// firstWord is the command part of a recorded command line, for a message that names the program to check.
func firstWord(command string) string {
	if i := strings.IndexByte(command, ' '); i >= 0 {
		return command[:i]
	}
	return command
}
