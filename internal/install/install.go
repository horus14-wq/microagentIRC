// Package install implements `mairc install`: it registers mairc's MCP server and hooks with Claude Code, merging
// into the user's or the project's files without overwriting anything else, and prints the two snippets for other
// agent CLIs.
//
// File locations and JSON shapes were checked against the Claude Code documentation on 17 Nov 2026
// (https://code.claude.com/docs/en/mcp and https://code.claude.com/docs/en/hooks; docs.claude.com redirects there):
//   - MCP servers: user scope in ~/.claude.json under the top-level "mcpServers"; project scope in <project>/.mcp.json
//     under "mcpServers". An entry with a "command" and no "type" is a stdio server.
//   - Hooks: ~/.claude/settings.json (user) and <project>/.claude/settings.json (project), under
//     "hooks": {"<Event>": [{"matcher"?: ..., "hooks": [{"type": "command", "command": ..., "timeout": <seconds>}]}]}.
//     A group without "matcher" fires on every occurrence of the event.
package install

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Options is everything one install, uninstall or print needs. The cli bead parses the flags.
type Options struct {
	Binary    string // absolute path of the mairc binary to reference
	Scope     string // "user" (default) or "project"
	Home      string // the user's home directory
	Project   string // the project directory (for scope "project")
	Print     bool   // --print: change nothing, write the snippets to Out
	Uninstall bool   // --uninstall: remove what install added
	Out       io.Writer

	// Only names the CLIs to set up (--only); empty means every supported CLI detected on this machine.
	Only []string
	// DryRun prints what would change, as a short diff per file, and writes nothing (--dry-run).
	DryRun bool
	// Confirm is asked once, with the files about to change, before anything is written; nil means yes (--yes).
	Confirm func(files []string) bool
	// LookPath finds a CLI's binary for detection (exec.LookPath in production); nil detects by config directory only.
	LookPath func(file string) (string, error)
}

// hookEvents returns the Claude Code events mairc's hook runs on, in the order they are written.
func hookEvents() []string {
	return []string{"SessionStart", "UserPromptSubmit", "PostToolUse"}
}

// hookTimeoutSeconds is the timeout Claude Code gives the hook command.
const hookTimeoutSeconds = "5"

// hookHandler is one fresh copy of mairc's hook handler.
func hookHandler(binary string) *node {
	if windowsBinary(binary) {
		// Exec form bypasses cmd, PowerShell and Git Bash tokenization (checked 17 Nov 2026):
		// https://code.claude.com/docs/en/hooks#exec-form-and-shell-form
		return object("type", str("command"), "command", str(binary),
			"args", array(str("hook"), str("--client"), str("claude")), "timeout", num(hookTimeoutSeconds))
	}
	return object("type", str("command"), "command", str(hookCommand(binary)), "timeout", num(hookTimeoutSeconds))
}

// Run installs, uninstalls or prints. It returns the process exit code (0 ok, 1 error, 2 usage).
func Run(o Options) int {
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	scope := o.Scope
	if scope == "" {
		scope = "user"
	}
	if msg := usage(o, scope); msg != "" {
		_, _ = fmt.Fprintln(out, msg)
		return 2
	}
	if o.Print {
		printSnippets(out, o.Binary)
		return 0
	}
	selected, skipped, msg := selectCLIs(o)
	if msg != "" {
		_, _ = fmt.Fprintln(out, msg)
		return 2
	}
	for _, name := range skipped {
		_, _ = fmt.Fprintf(out, "%s: not found, skipped\n", name)
	}
	var files []*target
	for _, c := range selected {
		for _, f := range c.files(scope, o) {
			f := f
			files = append(files, &target{path: f.path, apply: func(root *node) (bool, bool) {
				return applyJSON(root, f, o.Binary, o.Uninstall)
			}})
		}
	}
	// Read, check and edit every file in memory before writing any: one unparsable file leaves all untouched.
	for _, f := range files {
		if err := f.load(); err != nil {
			if errors.Is(err, errNotJSON) {
				_, _ = fmt.Fprintf(out, "cannot parse %s: fix it or use --print\n", f.path)
			} else {
				_, _ = fmt.Fprintf(out, "cannot read %s\n", f.path)
			}
			return 1
		}
	}
	var changed []*target
	for _, f := range files {
		edited, ok := f.apply(f.root)
		if !ok {
			_, _ = fmt.Fprintf(out, "cannot parse %s: fix it or use --print\n", f.path)
			return 1
		}
		if edited {
			changed = append(changed, f)
		}
	}
	if o.DryRun {
		for _, f := range changed {
			_, _ = fmt.Fprintf(out, "would update %s\n%s", f.path, shortDiff(f.raw, f.root.bytes()))
		}
	} else if len(changed) > 0 && o.Confirm != nil && !o.Confirm(paths(changed)) {
		_, _ = fmt.Fprintln(out, "nothing changed")
		return 1
	}
	if !o.DryRun {
		for _, f := range changed {
			if err := f.store(); err != nil {
				_, _ = fmt.Fprintf(out, "cannot write %s\n", f.path)
				return 1
			}
			_, _ = fmt.Fprintf(out, "updated %s\n", f.path)
		}
	}
	if len(changed) == 0 && !o.Uninstall {
		_, _ = fmt.Fprintln(out, "already installed")
	}
	return 0
}

// selectCLIs returns the CLIs to set up: those named by --only (an unknown name is a usage error), else every
// supported CLI detected on this machine, with the names of the ones not found.
func selectCLIs(o Options) (selected []cliSpec, skipped []string, usageMsg string) {
	if len(o.Only) > 0 {
		for _, name := range o.Only {
			found := false
			for _, c := range clis() {
				if c.name == name {
					selected, found = append(selected, c), true
				}
			}
			if !found {
				return nil, nil, fmt.Sprintf("usage: unknown CLI %s (supported: %s)", name, cliNames())
			}
		}
		return selected, nil, ""
	}
	for _, c := range clis() {
		if detect(c, o) {
			selected = append(selected, c)
		} else {
			skipped = append(skipped, c.name)
		}
	}
	return selected, skipped, ""
}

func paths(targets []*target) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, t.path)
	}
	return out
}

// shortDiff shows the changed region between before and after: the lines after their common head and before their
// common tail, at most maxDiffLines of each side.
func shortDiff(before, after []byte) string {
	const maxDiffLines = 40
	a, b := splitLines(before), splitLines(after)
	head := 0
	for head < len(a) && head < len(b) && a[head] == b[head] {
		head++
	}
	tail := 0
	for tail < len(a)-head && tail < len(b)-head && a[len(a)-1-tail] == b[len(b)-1-tail] {
		tail++
	}
	var s strings.Builder
	for side, lines := range [][]string{a[head : len(a)-tail], b[head : len(b)-tail]} {
		mark := "- "
		if side == 1 {
			mark = "+ "
		}
		for i, line := range lines {
			if i == maxDiffLines {
				fmt.Fprintf(&s, "%s… (%d more lines)\n", mark, len(lines)-maxDiffLines)
				break
			}
			s.WriteString(mark + line + "\n")
		}
	}
	return s.String()
}

func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func usage(o Options, scope string) string {
	switch {
	case o.Print && o.Uninstall:
		return "usage: --print and --uninstall cannot be combined"
	case !filepath.IsAbs(o.Binary):
		return "usage: the mairc binary must be an absolute path"
	case scope != "user" && scope != "project":
		return "usage: scope must be user or project"
	case !o.Print && scope == "user" && o.Home == "":
		return "usage: the home directory is unknown"
	case !o.Print && scope == "project" && o.Project == "":
		return "usage: scope project needs a project directory"
	}
	return ""
}

// target is one file the installer may change.
type target struct {
	path    string
	apply   func(root *node) (edited, ok bool)
	root    *node
	raw     []byte
	existed bool
	mode    fs.FileMode
}

// load reads the file; a missing file is an empty object, a file that is not a JSON object is errNotJSON.
func (t *target) load() error {
	info, err := os.Stat(t.path)
	if errors.Is(err, fs.ErrNotExist) {
		t.root, t.mode = &node{kind: objectNode}, 0o600
		return nil
	}
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(t.path)
	if err != nil {
		return err
	}
	root, err := parse(raw)
	if err != nil {
		return err
	}
	if root.kind != objectNode {
		return errNotJSON
	}
	t.root, t.raw, t.existed, t.mode = root, raw, true, info.Mode().Perm()
	return nil
}

// store keeps a one-time backup of the original content, then writes atomically: a temporary file in the same
// directory, the original mode (0600 for a new file), and a rename over the target.
func (t *target) store() error {
	dir := filepath.Dir(t.path)
	if !t.existed {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	if t.existed {
		if err := backup(t.path+".mairc.bak", t.raw, t.mode); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(t.path)+".mairc-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(t.root.bytes())
	if werr == nil {
		werr = tmp.Chmod(t.mode)
	}
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmpName, t.path)
	}
	if werr != nil {
		_ = os.Remove(tmpName)
		return werr
	}
	return nil
}

// backup writes the original content once; an existing backup is never overwritten.
func backup(path string, raw []byte, mode fs.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode) // #nosec G304 -- <the file being changed>.mairc.bak
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	_, werr := f.Write(raw)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// hookCommand is the command Claude Code runs for each hook event.
func hookCommand(binary string) string {
	for _, c := range binary {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			strings.ContainsRune("/_-.:+", c) {
			continue
		}
		binary = "'" + strings.ReplaceAll(binary, "'", "'\"'\"'") + "'"
		break
	}
	return binary + " hook --client claude"
}

func windowsBinary(binary string) bool {
	return strings.HasPrefix(binary, `\\`) || strings.HasPrefix(binary, "//") || (len(binary) >= 3 && binary[1] == ':' &&
		(binary[2] == '\\' || binary[2] == '/'))
}

func maircBinary(binary string) bool {
	base := binary[strings.LastIndexAny(binary, `/\`)+1:]
	return base == "mairc" || strings.EqualFold(base, "mairc.exe")
}

// isOurHook reports whether a hook handler runs `<any path>/mairc hook`.
func isOurHook(h *node) bool {
	command, ok := h.get("command").stringValue()
	if !ok {
		return false
	}
	if args := h.get("args"); args != nil {
		if args.kind != arrayNode || len(args.items) == 0 {
			return false
		}
		first, _ := args.items[0].stringValue()
		return maircBinary(command) && first == "hook"
	}
	if quoted, found := strings.CutSuffix(command, " hook --client claude"); found &&
		len(quoted) >= 2 && quoted[0] == '\'' && quoted[len(quoted)-1] == '\'' {
		binary := strings.ReplaceAll(quoted[1:len(quoted)-1], "'\"'\"'", "'")
		return maircBinary(binary) && hookCommand(binary) == command
	}
	fields := strings.Fields(command)
	return len(fields) >= 2 && maircBinary(fields[0]) && fields[1] == "hook"
}

// printSnippets writes the Claude Code snippets and the generic lines for other agent CLIs; it changes no file.
func printSnippets(out io.Writer, binary string) {
	mcp := object("mcpServers", object("mairc", object("command", str(binary), "args", array(str("stdio")))))
	hooks := object()
	for _, event := range hookEvents() {
		hooks.set(event, array(object("hooks", array(hookHandler(binary)))))
	}
	settings := object("hooks", hooks)
	_, _ = fmt.Fprintln(out, "Claude Code MCP server: merge into ~/.claude.json (user) or <project>/.mcp.json (project)")
	_, _ = out.Write(mcp.bytes())
	_, _ = fmt.Fprintln(out,
		"Claude Code hooks: merge into ~/.claude/settings.json (user) or <project>/.claude/settings.json (project)")
	_, _ = out.Write(settings.bytes())
	_, _ = fmt.Fprintln(out, "Other agent CLIs:")
	_, _ = fmt.Fprintf(out, "MCP (stdio): %s stdio\n", binary)
	_, _ = fmt.Fprintf(out, "Hook (any event, zero output when nothing is new): %s hook --client plain\n", binary)
}
