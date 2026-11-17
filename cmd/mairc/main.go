// Command mairc is microagentIRC: a tiny chat system for coding agents, served as an MCP server.
// This file only parses the command line and calls the packages; it adds no logic of its own beyond flags,
// environment, signals and starting the daemon in the background.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/horus14-wq/microagentIRC/internal/config"
	"github.com/horus14-wq/microagentIRC/internal/daemon"
	"github.com/horus14-wq/microagentIRC/internal/doctor"
	"github.com/horus14-wq/microagentIRC/internal/hook"
	"github.com/horus14-wq/microagentIRC/internal/identity"
	"github.com/horus14-wq/microagentIRC/internal/install"
	"github.com/horus14-wq/microagentIRC/internal/shim"
)

// version is the release; overridable with -ldflags "-X main.version=...".
var version = "0.1.0"

const usageLine = "usage: mairc daemon|stdio|hook|config|install|status|doctor|version"

// controlTimeout bounds `mairc status` and the reload `mairc config set` asks for.
const controlTimeout = time.Second

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr))
}

// run dispatches one command line and returns the exit code. The hook is matched first and does nothing else
// before its own work: it runs after every tool call of every agent.
func run(args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, usageLine)
		return 2
	}
	switch args[0] {
	case "hook":
		return runHook(args[1:], getenv, stdin, stdout)
	case "version":
		if !noArgs("version", args[1:], stderr) {
			return 2
		}
		_, _ = fmt.Fprintln(stdout, version)
		return 0
	case "daemon":
		if !noArgs("daemon", args[1:], stderr) {
			return 2
		}
		return runDaemon(getenv, stderr)
	case "stdio":
		if !noArgs("stdio", args[1:], stderr) {
			return 2
		}
		return runStdio(getenv, stdin, stdout)
	case "config":
		return runConfig(args[1:], getenv, stdout, stderr)
	case "install":
		return runInstall(args[1:], stdin, stdout, stderr)
	case "status":
		if !noArgs("status", args[1:], stderr) {
			return 2
		}
		return runStatus(getenv, stdout)
	case "doctor":
		return runDoctor(args[1:], getenv, stdin, stdout, stderr)
	default:
		_, _ = fmt.Fprintln(stderr, usageLine)
		return 2
	}
}

// noArgs parses a subcommand that takes no flags or arguments, printing its usage on anything else.
func noArgs(name string, args []string, stderr io.Writer) bool {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if fs.Parse(args) != nil || fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "usage: mairc %s\n", name)
		return false
	}
	return true
}

// runHook is the hot path: resolve paths, read config.json and the token, compute seat and ancestors, call
// hook.Run. Any problem, including a bad flag, prints nothing on either stream and exits 0.
func runHook(args []string, getenv func(string) string, stdin io.Reader, stdout io.Writer) int {
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	client := fs.String("client", "plain", "")
	event := fs.String("event", "", "")
	name := fs.String("name", "", "")
	if fs.Parse(args) != nil || fs.NArg() > 0 || (*client != "claude" && *client != "plain") {
		return 0
	}
	if *name == "" {
		*name = getenv("MAIRC_NAME")
	}
	p, err := config.ResolvePaths(getenv)
	if err != nil {
		return 0
	}
	settings, err := config.Load(p.Config)
	if err != nil {
		return 0
	}
	token, err := config.ReadToken(p.Token)
	if err != nil {
		return 0
	}
	var in io.Reader
	if !isTerminal(stdin) {
		in = stdin
	}
	return hook.Run(context.Background(), hook.Options{
		BaseURL: "http://" + settings.Listen,
		Token:   token,
		Timeout: time.Duration(settings.HookTimeoutMS) * time.Millisecond,
		Client:  *client,
		Event:   *event,
		Name:    *name,
		Seat:    identity.HostSeat(identity.Seat(getenv, identity.ControllingTTY, os.Getwd), os.Hostname),
		PIDs:    identity.Ancestors(os.Getpid(), 8, identity.ParentOf),
		Stdin:   in,
		Stdout:  stdout,
		Host:    hook.HostFrom(getenv),
		Session: identity.SessionFromEnv(getenv),
	})
}

// isTerminal reports whether r is a terminal; the hook never reads one.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok || f == nil {
		return r == nil
	}
	info, err := f.Stat()
	return err != nil || info.Mode()&os.ModeCharDevice != 0
}

func runDaemon(getenv func(string) string, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()
	p, err := config.ResolvePaths(getenv)
	if err == nil {
		err = daemon.Run(ctx, daemon.Options{Paths: p, Version: version})
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// runStdio runs the shim. On a first run the token does not exist until the daemon has created it, and the shim
// takes the token as it starts, so a missing token starts the daemon here and waits briefly for the file.
func runStdio(getenv func(string) string, stdin io.Reader, stdout io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()
	p, err := config.ResolvePaths(getenv)
	if err != nil {
		return 0
	}
	settings, _ := config.Load(p.Config)
	start := func() error { return startDaemon(p) }
	token, err := config.ReadToken(p.Token)
	if err != nil {
		_ = start()
		token = waitForToken(ctx, p.Token)
	}
	_ = shim.Run(ctx, shim.Options{
		BaseURL:     "http://" + settings.Listen,
		Token:       token,
		Seat:        identity.HostSeat(identity.Seat(getenv, identity.ControllingTTY, os.Getwd), os.Hostname),
		AgentPID:    os.Getppid(),
		Session:     identity.SessionFromEnv(getenv),
		In:          stdin,
		Out:         stdout,
		StartDaemon: start,
	})
	return 0
}

// waitForToken polls for the token file for up to 3 s (the shim's own wait for /healthz) and returns "" if it
// never appears; the shim then answers each request with its daemon-unavailable error.
func waitForToken(ctx context.Context, path string) string {
	for waited := time.Duration(0); waited < 3*time.Second; waited += 50 * time.Millisecond {
		if token, err := config.ReadToken(path); err == nil {
			return token
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ""
		case <-timer.C:
		}
	}
	return ""
}

// startDaemon starts `<this binary> daemon` detached: its own session, stdin from /dev/null, stdout and stderr
// appended to the log, the environment inherited. It does not wait for it. A second daemon started at the same
// time finds the address held by a healthy daemon and exits 0.
func startDaemon(p config.Paths) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := config.EnsureHome(p); err != nil {
		return err
	}
	logFile, err := os.OpenFile(p.Log, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer func() { _ = devNull.Close() }()
	// A detached daemon must outlive this command, so its context is never cancelled.
	cmd := exec.CommandContext(context.Background(), exe, "daemon") // #nosec G204 -- this binary, re-executed
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devNull, logFile, logFile
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func runConfig(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	p, err := config.ResolvePaths(getenv)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return config.Run(args, p, stdout, stderr, func() error { return reload(p) })
}

// errNotRunning is the reload error when the daemon does not answer.
var errNotRunning = errors.New("daemon not running")

// reload asks the daemon to reread config.json: POST /v1/reload with the token, 1 s.
func reload(p config.Paths) error {
	settings, err := config.Load(p.Config)
	if err != nil {
		return err
	}
	token, err := config.ReadToken(p.Token)
	if err != nil {
		return errNotRunning
	}
	ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+settings.Listen+"/v1/reload", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := controlClient().Do(req)
	if err != nil {
		return errNotRunning
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if msg := strings.TrimSpace(string(body)); msg != "" {
		return errors.New(msg)
	}
	return fmt.Errorf("daemon answered %d", resp.StatusCode)
}

func controlClient() *http.Client {
	return &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
}

// installUsage is `mairc install`'s usage line.
const installUsage = "usage: mairc install [--only cli,...] [--user|--project] [--dry-run] [--yes] [--uninstall] [--print]"

// runInstall sets mairc up for every supported coding CLI found on this machine (or those --only names), after one
// yes/no question unless --yes or --dry-run.
func runInstall(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	scope := fs.String("scope", "user", "")
	user := fs.Bool("user", false, "")
	project := fs.Bool("project", false, "")
	only := fs.String("only", "", "")
	dryRun := fs.Bool("dry-run", false, "")
	yes := fs.Bool("yes", false, "")
	printOnly := fs.Bool("print", false, "")
	uninstall := fs.Bool("uninstall", false, "")
	if fs.Parse(args) != nil || fs.NArg() > 0 || (*user && *project) {
		_, _ = fmt.Fprintln(stderr, installUsage)
		return 2
	}
	switch {
	case *user:
		*scope = "user"
	case *project:
		*scope = "project"
	}
	var onlyList []string
	for _, name := range strings.Split(*only, ",") {
		if name = strings.TrimSpace(name); name != "" {
			onlyList = append(onlyList, name)
		}
	}
	var confirm func([]string) bool
	if !*yes {
		confirm = func(files []string) bool { return ask(stdin, stdout, files) }
	}
	binary, err := os.Executable()
	if err == nil {
		binary, err = filepath.Abs(binary)
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	// Every CLI config records the binary's absolute path, so a binary in a temporary or Downloads
	// directory disappears with the next cleanup and breaks every entry. install asks PrepareBinary
	// once its selection validates: offer a copy to a stable location first; --yes accepts without
	// asking, --dry-run warns but writes nothing.
	prepare := func(binary string) (string, error) {
		if !unstableDir(binary, home) {
			return binary, nil
		}
		stable := stableBinaryPath(home)
		switch {
		case *dryRun:
			_, _ = fmt.Fprintf(stdout, "%s sits in a temporary or Downloads directory; it would be copied to %s first\n", binary, stable)
			return stable, nil // the preview shows the path the real install records
		case *yes || askYesNo(stdin, stdout, fmt.Sprintf("%s sits in a temporary or Downloads directory; the recorded path disappears with the next cleanup.\nCopy mairc to %s first? [y/N] ", binary, stable)):
			if err := copyFile(binary, stable, 0o755); err != nil {
				return "", err
			}
			_, _ = fmt.Fprintf(stdout, "copied %s to %s\n", binary, stable)
			return stable, nil
		default:
			_, _ = fmt.Fprintf(stdout, "warning: every CLI config will record %s — install again from a stable path\n", binary)
		}
		return binary, nil
	}
	return install.Run(install.Options{
		Binary: binary, Scope: *scope, Home: home, Project: cwd,
		Print: *printOnly, Uninstall: *uninstall, Out: stdout,
		Only: onlyList, DryRun: *dryRun, Confirm: confirm, LookPath: exec.LookPath, GOOS: runtime.GOOS,
		PrepareBinary: prepare,
	})
}

// ask lists the files about to change and reads one yes/no answer; anything but y or yes (end of input included)
// is no.
func ask(stdin io.Reader, stdout io.Writer, files []string) bool {
	_, _ = fmt.Fprintln(stdout, "mairc will change:")
	for _, f := range files {
		_, _ = fmt.Fprintf(stdout, "  %s\n", f)
	}
	return askYesNo(stdin, stdout, "Proceed? [y/N] ")
}

// askYesNo prints prompt and reads one yes/no answer; anything but y or yes (end of input included) is no.
func askYesNo(stdin io.Reader, stdout io.Writer, prompt string) bool {
	_, _ = fmt.Fprint(stdout, prompt)
	if stdin == nil {
		return false
	}
	line, _ := bufio.NewReader(stdin).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

// unstableDir reports whether path sits in a directory a cleanup may empty: the system temporary
// directory or the user's Downloads folder.
func unstableDir(path, home string) bool {
	dir := filepath.Dir(path)
	for _, unstable := range []string{os.TempDir(), filepath.Join(home, "Downloads")} {
		if unstable == "" {
			continue
		}
		if abs, err := filepath.Abs(unstable); err == nil &&
			(dir == abs || strings.HasPrefix(dir, abs+string(os.PathSeparator))) {
			return true
		}
	}
	return false
}

// copyFile copies src to dst, creating dst's directory and giving dst mode.
func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	b, err := os.ReadFile(src) // #nosec G304 -- the running binary's own path
	if err != nil {
		return err
	}
	// The same binary is already there (a second install): nothing to copy.
	if old, err := os.ReadFile(dst); err == nil && bytes.Equal(old, b) { // #nosec G304 -- stableBinaryPath
		return nil
	}
	// Write beside the target and rename over it. Writing into the target itself fails with "text file busy" while
	// an agent's shim or the daemon runs from it; a rename replaces the name and leaves the running copy alone.
	tmp := dst + ".new"
	if err := os.WriteFile(tmp, b, mode); err != nil { // #nosec G703 -- beside stableBinaryPath, not input
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("%w (is mairc running from %s? stop the agents that use it and install again)", err, dst)
	}
	return nil
}

// daemonStatus is the body of GET /v1/status.
type daemonStatus struct {
	Version  string `json:"version"`
	UptimeS  int64  `json:"uptime_s"`
	Agents   int    `json:"agents"`
	Sessions int    `json:"sessions"`
}

func runStatus(getenv func(string) string, stdout io.Writer) int {
	st, ok := fetchStatus(getenv)
	if !ok {
		_, _ = fmt.Fprintln(stdout, "not running")
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "running %s, %d agents, %d sessions, up %s\n",
		st.Version, st.Agents, st.Sessions, time.Duration(st.UptimeS)*time.Second)
	return 0
}

// fetchStatus asks GET /v1/status with the token, 1 s; any failure is "not running".
func fetchStatus(getenv func(string) string) (daemonStatus, bool) {
	var st daemonStatus
	p, err := config.ResolvePaths(getenv)
	if err != nil {
		return st, false
	}
	settings, err := config.Load(p.Config)
	if err != nil {
		return st, false
	}
	token, err := config.ReadToken(p.Token)
	if err != nil {
		return st, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+settings.Listen+"/v1/status", nil)
	if err != nil {
		return st, false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := controlClient().Do(req)
	if err != nil {
		return st, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&st) != nil {
		return st, false
	}
	return st, true
}

// doctorTimeout bounds one live CLI run: a CLI that has to think for longer is a CLI the user is watching anyway.
const doctorTimeout = 3 * time.Minute

// runDoctor checks what `mairc install` set up: the daemon, each configured CLI's MCP entry and hook, and with --live
// whether a tool call actually reaches the daemon. Exit 0 only when everything checked is healthy.
func runDoctor(args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	live := fs.Bool("live", false, "")
	assumeYes := fs.Bool("yes", false, "")
	scope := fs.String("scope", "user", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		_, _ = fmt.Fprintln(stderr, "usage: mairc doctor [--live] [--yes] [--scope user|project]")
		return 2
	}
	if *scope != "user" && *scope != "project" {
		_, _ = fmt.Fprintln(stderr, "usage: --scope must be user or project")
		return 2
	}
	p, err := config.ResolvePaths(getenv)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	settings, err := config.Load(p.Config)
	if err != nil {
		_, _ = fmt.Fprintf(stdout, "config: %v — fix %s\n", err, p.Config)
		return 1
	}
	settings.Listen = listenOr(settings.Listen, getenv)
	token, _ := config.ReadToken(p.Token)
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	self, _ := os.Executable()
	statuses := install.Statuses(install.Options{
		Binary: self, Scope: *scope, Home: home, Project: cwd, GOOS: runtime.GOOS, LookPath: exec.LookPath,
	})
	base := "http://" + settings.Listen
	probes := doctor.Probes{
		Health: func() error { return daemonHealth(base) },
		Status: func() (int, int, error) { return daemonCounts(base, token) },
		Hook:   runHookCommand,
	}
	var confirm func(string) bool
	if *live {
		probes.Live = func(cli string) (bool, error) { return liveRun(base, token, cli) }
		confirm = askSpend(stdin, stdout, *assumeYes)
	}
	return doctor.Run(doctor.Options{Out: stdout, Daemon: base, CLIs: statuses, Probes: probes, Live: *live, Spend: confirm})
}

// listenOr falls back to the default listen address when the settings file does not name one.
func listenOr(listen string, getenv func(string) string) string {
	if listen != "" {
		return listen
	}
	if addr := getenv("MAIRC_LISTEN"); addr != "" {
		return addr
	}
	return "127.0.0.1:4747"
}

// daemonHealth asks /healthz: the daemon writes "ok" there once it is serving.
func daemonHealth(base string) error {
	ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := controlClient().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		return fmt.Errorf("%s answered %d", base+"/healthz", resp.StatusCode)
	}
	return nil
}

// daemonCounts asks /v1/status with the token: a 401 or 403 means the token the doctor read is not the daemon's.
func daemonCounts(base, token string) (int, int, error) {
	var st daemonStatus
	ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/status", nil)
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := controlClient().Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return 0, 0, err
	}
	return st.Sessions, st.Agents, nil
}

// runHookCommand runs a recorded hook command the way its CLI would: no arguments, empty stdin, output captured. The
// doctor is looking for two things — that it starts at all, and that it prints nothing when nothing is new.
func runHookCommand(command string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), hookCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command) // #nosec G204 -- the command is what the user's CLI config already runs
	cmd.Stdin = strings.NewReader("")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, io.Discard
	if err := cmd.Run(); err != nil {
		return out.String(), err
	}
	return out.String(), nil
}

// hookCommandTimeout bounds one hook run; the hook's own budget is a few seconds.
const hookCommandTimeout = 15 * time.Second

// liveRun runs a CLI once, headless, and reports whether the daemon saw the MCP session that run opened.
func liveRun(base, token, cli string) (bool, error) {
	args, ok := doctor.LiveCommand(cli)
	if !ok {
		return false, fmt.Errorf("mairc doctor has no headless command recorded for %s", cli)
	}
	before, _, err := daemonCounts(base, token)
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), doctorTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], append(args[1:], doctor.Prompt())...)
	cmd.Stdin = nil
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("%s: %w", firstLineOf(out.String()), err)
	}
	after, _, err := daemonCounts(base, token)
	if err != nil {
		return false, err
	}
	return after > before, nil
}

// firstLineOf is the CLI's first output line, for an error that has to say what happened.
func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// askSpend returns the confirmation asked before each live run: the run spends the user's tokens, so it is said out
// loud first, and --yes answers for every CLI.
func askSpend(stdin io.Reader, out io.Writer, yes bool) func(string) bool {
	r := bufio.NewReader(stdin)
	return func(cli string) bool {
		if yes {
			return true
		}
		_, _ = fmt.Fprintf(out, "run %s once to check delivery? It calls the mairc tool once and spends your tokens [y/N] ", cli)
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			return false
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return true
		}
		return false
	}
}
