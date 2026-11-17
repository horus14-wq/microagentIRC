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
	"slices"
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
	// GOOS selects per-OS configuration paths (runtime.GOOS in production; "" means linux rules).
	GOOS string
	// PrepareBinary, when non-nil, runs once after the selection validates and before any file is
	// written, and returns the binary path to record. The caller uses it to move a temporary or
	// Downloads binary somewhere stable first; nil records Binary as given.
	PrepareBinary func(binary string) (string, error)
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
	if o.PrepareBinary != nil && !o.Uninstall {
		b, err := o.PrepareBinary(o.Binary)
		if err != nil {
			_, _ = fmt.Fprintln(out, err)
			return 1
		}
		o.Binary = b
	}
	// Each CLI is read, checked and edited in memory before anything is written. A CLI with a file that cannot be
	// read, parsed or merged is skipped whole, with one line naming the file; the others go ahead.
	var ready, failed []*cliRun
	backups := backupRoot(o)
	for _, c := range selected {
		specs := c.files(scope, o)
		if len(specs) == 0 {
			_, _ = fmt.Fprintf(out, "%s: no %s-scope configuration, skipped\n", c.name, scope)
			continue
		}
		run := &cliRun{name: c.name}
		for _, f := range specs {
			run.files = append(run.files, &target{path: f.path, spec: f, backups: backups})
		}
		if problem := run.prepare(o.Binary, o.Uninstall); problem != "" {
			_, _ = fmt.Fprintf(out, "%s: skipped, %s\n", c.name, problem)
			failed = append(failed, run)
			continue
		}
		ready = append(ready, run)
	}
	var changed []*target
	for _, run := range ready {
		changed = append(changed, run.changed...)
	}
	if o.DryRun {
		for _, f := range changed {
			if f.remove {
				_, _ = fmt.Fprintf(out, "would remove %s\n", f.path)
				continue
			}
			_, _ = fmt.Fprintf(out, "would update %s\n%s", f.path, shortDiff(f.raw, f.out))
		}
	} else if len(changed) > 0 && o.Confirm != nil && !o.Confirm(paths(changed)) {
		_, _ = fmt.Fprintln(out, "nothing changed")
		return 1
	}
	if !o.DryRun {
		var stored []*cliRun
		for _, run := range ready {
			if f, err := run.store(out, o.Uninstall); err != nil {
				_, _ = fmt.Fprintf(out, "%s: cannot write %s\n", run.name, f.path)
				failed = append(failed, run)
				continue
			}
			stored = append(stored, run)
		}
		ready = stored
	}
	if len(failed) > 0 {
		_, _ = fmt.Fprintln(out, summary(ready, failed, o))
		return 1
	}
	if len(changed) == 0 && !o.Uninstall {
		_, _ = fmt.Fprintln(out, "already installed")
	}
	return 0
}

// A cliRun is one CLI's files in one run: all of them are written, or none.
type cliRun struct {
	name    string
	files   []*target
	changed []*target
}

// prepare loads and edits every file of the CLI in memory. problem is the one-line reason the CLI is skipped.
func (r *cliRun) prepare(binary string, uninstall bool) (problem string) {
	for _, f := range r.files {
		if err := f.load(); err != nil {
			if errors.Is(err, errNotJSON) {
				return fmt.Sprintf("cannot parse %s: fix it or use --print", f.path)
			}
			return "cannot read " + f.path
		}
	}
	for _, f := range r.files {
		edited, problem := f.apply(binary, uninstall)
		if problem != "" {
			return problem
		}
		// A file mairc created that uninstall leaves empty is a stub: it is removed, not written back.
		if uninstall && f.existed && f.empty() && f.created() {
			f.remove, edited = true, true
		}
		if edited {
			r.changed = append(r.changed, f)
		}
	}
	return ""
}

// store writes the CLI's changed files and reports each; it stops at the first file it cannot write.
// After a whole uninstall, the CLI's backups are dropped: nothing of mairc's is left.
func (r *cliRun) store(out io.Writer, uninstall bool) (*target, error) {
	for _, f := range r.changed {
		if f.remove {
			if err := f.removeStub(); err != nil {
				return f, err
			}
			_, _ = fmt.Fprintf(out, "removed %s\n", f.path)
			continue
		}
		if err := f.store(!uninstall); err != nil {
			return f, err
		}
		_, _ = fmt.Fprintf(out, "updated %s\n", f.path)
		if f.spec.notice != "" && !uninstall {
			_, _ = fmt.Fprintln(out, f.spec.notice)
		}
	}
	if uninstall {
		for _, f := range r.files {
			f.dropBackup()
		}
	}
	return nil, nil
}

// summary is the closing line of a run in which some CLI was skipped.
func summary(done, failed []*cliRun, o Options) string {
	verb := map[[2]bool]string{
		{false, false}: "set up", {true, false}: "removed from",
		{false, true}: "would set up", {true, true}: "would remove from",
	}[[2]bool{o.Uninstall, o.DryRun}]
	names := func(runs []*cliRun) string {
		if len(runs) == 0 {
			return "none"
		}
		list := make([]string, 0, len(runs))
		for _, r := range runs {
			list = append(list, r.name)
		}
		return strings.Join(list, ", ")
	}
	sorted := make([]*cliRun, 0, len(failed))
	for _, c := range clis() {
		for _, r := range failed {
			if r.name == c.name {
				sorted = append(sorted, r)
			}
		}
	}
	return fmt.Sprintf("summary: %s %s; skipped %s", verb, names(done), names(sorted))
}

// CheckNames returns the usage error install gives for the first name the table does not know, or "" when every
// name is a supported CLI (for commands such as doctor that take the same --only list).
func CheckNames(names []string) string {
	for _, name := range names {
		if !slices.ContainsFunc(clis(), func(c cliSpec) bool { return c.name == name }) {
			return fmt.Sprintf("usage: unknown CLI %s (supported: %s)", name, cliNames())
		}
	}
	return ""
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
	spec    fileSpec
	root    *node  // the parsed document, for the JSON format
	raw     []byte // the file as read (empty when missing)
	out     []byte // the file as it will be written
	existed bool
	mode    fs.FileMode
	dest    string // where the bytes are written: path with symlinks resolved (dotfile managers link configs)
	backups string // mairc's backup directory ("" for none; see backupRoot)
	remove  bool   // uninstall: the file is a stub mairc created, and is deleted
}

// load reads the file. A missing file is empty (an empty object for JSON); a JSON file that is not a JSON object is
// errNotJSON. Text formats are kept as bytes: their writer never re-encodes the user's content.
func (t *target) load() error {
	t.dest = t.path
	info, err := os.Stat(t.path)
	if errors.Is(err, fs.ErrNotExist) {
		t.mode = 0o600
		if t.spec.format == "json" {
			t.root = &node{kind: objectNode}
		}
		return nil
	}
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(t.path)
	if err != nil {
		return err
	}
	if t.spec.format == "json" {
		root, err := parse(raw)
		if err != nil {
			return err
		}
		if root.kind != objectNode {
			return errNotJSON
		}
		t.root = root
	}
	t.raw, t.existed, t.mode = raw, true, info.Mode().Perm()
	// A symlinked config is changed where it lives: renaming over the link would replace it with a plain file.
	resolved, err := filepath.EvalSymlinks(t.path)
	if err != nil {
		return err
	}
	t.dest = resolved
	return nil
}

// apply runs the file's format writer and sets out. problem is a one-line refusal; nothing may be written then.
func (t *target) apply(binary string, uninstall bool) (edited bool, problem string) {
	switch t.spec.format {
	case "json":
		edited, ok := applyJSON(t.root, t.spec, binary, uninstall)
		if !ok {
			return false, fmt.Sprintf("cannot parse %s: fix it or use --print", t.path)
		}
		t.out = t.root.render()
		return edited, ""
	case "block":
		out, edited, problem := applyBlock(t.raw, t.spec.block, binary, uninstall)
		if problem != "" {
			return false, fmt.Sprintf("%s %s", t.path, problem)
		}
		t.out = out
		return edited, ""
	}
	return false, fmt.Sprintf("cannot parse %s: unknown format %s", t.path, t.spec.format)
}

// store writes atomically: a temporary file in the same directory, the original mode (0600 for a new file), and a
// rename over the target. A symlinked file is written at its resolved destination (dest), so the link stays a link.
// With keep (install), the first change of an existing file first copies the original into mairc's backup directory,
// and a file mairc creates is noted there, with the directories it made for it, so uninstall can remove it again.
func (t *target) store(keep bool) error {
	dest := t.destination()
	dir := filepath.Dir(dest)
	var made string
	if !t.existed {
		made = topMissing(dir)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	if keep && t.backups != "" {
		var err error
		switch {
		case !t.existed:
			err = t.writeBackup(t.path+createdSuffix, []byte(made), 0o600)
		case !t.created():
			err = t.writeBackup(t.path, t.raw, t.mode)
		}
		if err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dest)+".mairc-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(t.out)
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
		werr = os.Rename(tmpName, dest)
	}
	if werr != nil {
		_ = os.Remove(tmpName)
		return werr
	}
	return nil
}

func (t *target) destination() string {
	if t.dest == "" {
		return t.path
	}
	return t.dest
}

// createdSuffix marks, in the backup directory, a file mairc created; its content is the first directory mairc made
// for it ("" when the directory was there).
const createdSuffix = ".created"

// created reports whether mairc created this file (and the user has not had it backed up as their own since).
func (t *target) created() bool {
	if t.backups == "" {
		return false
	}
	_, err := os.Stat(backupFile(t.backups, t.path) + createdSuffix)
	return err == nil
}

// empty reports whether the file as it will be written holds nothing: an empty JSON object, or only whitespace.
func (t *target) empty() bool {
	if t.spec.format == "json" {
		return t.root != nil && t.root.kind == objectNode && len(t.root.keys) == 0
	}
	return strings.TrimSpace(string(t.out)) == ""
}

// removeStub deletes a file mairc created, and the directories mairc made for it once they are empty.
func (t *target) removeStub() error {
	made, _ := os.ReadFile(backupFile(t.backups, t.path) + createdSuffix) // #nosec G304 -- inside mairc's backup directory
	if err := os.Remove(t.destination()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if len(made) > 0 {
		pruneDirs(filepath.Dir(t.path), string(made))
	}
	return nil
}

// writeBackup writes content once at the backup of name; an existing backup is never overwritten.
func (t *target) writeBackup(name string, content []byte, mode fs.FileMode) error {
	path := backupFile(t.backups, name)
	dir := filepath.Dir(path)
	if made := topMissing(t.backups); made != "" {
		if err := os.MkdirAll(t.backups, 0o700); err != nil {
			return err
		}
		// The directories made for the backup directory itself, so the last uninstall removes them too.
		if err := backup(filepath.Join(t.backups, madeFile), []byte(made), 0o600); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return backup(path, content, mode)
}

// madeFile, in the backup directory, names the first directory install made to hold it.
const madeFile = "made"

// dropBackup removes the file's backup and created mark, then every directory of the backup tree that is now empty,
// the backup directory itself (and those made for it) included.
func (t *target) dropBackup() {
	if t.backups == "" {
		return
	}
	path := backupFile(t.backups, t.path)
	_ = os.Remove(path)
	_ = os.Remove(path + createdSuffix)
	pruneDirs(filepath.Dir(path), t.backups)
	entries, err := os.ReadDir(t.backups)
	if err != nil || len(entries) != 1 || entries[0].Name() != madeFile {
		return
	}
	made, err := os.ReadFile(filepath.Join(t.backups, madeFile)) // #nosec G304 -- inside mairc's backup directory
	if err != nil || os.Remove(filepath.Join(t.backups, madeFile)) != nil {
		return
	}
	pruneDirs(t.backups, string(made))
}

// pruneDirs removes dir and its parents while they are empty, up to and including stop, which must contain dir.
func pruneDirs(dir, stop string) {
	if rel, err := filepath.Rel(stop, dir); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return
	}
	for {
		if os.Remove(dir) != nil || dir == stop {
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

// topMissing returns the outermost directory of dir's path that does not exist yet ("" when dir exists).
func topMissing(dir string) string {
	top := ""
	for {
		if _, err := os.Stat(dir); err == nil {
			return top
		}
		top = dir
		parent := filepath.Dir(dir)
		if parent == dir {
			return top
		}
		dir = parent
	}
}

// backupRoot is mairc's own directory for install backups, under the user's home: mairc's default data directory
// on each OS (internal/config) plus install-backup. "" without a home: then nothing is backed up.
func backupRoot(o Options) string {
	if o.Home == "" {
		return ""
	}
	switch o.GOOS {
	case "darwin":
		return filepath.Join(o.Home, "Library", "Application Support", "mairc", "install-backup")
	case "windows":
		return filepath.Join(o.Home, "AppData", "Local", "mairc", "install-backup")
	}
	return filepath.Join(o.Home, ".local", "share", "mairc", "install-backup")
}

// backup writes the original content once; an existing backup is never overwritten.
func backup(path string, raw []byte, mode fs.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode) // #nosec G304 -- inside mairc's backup directory
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
	return shellWord(binary) + " hook --client claude"
}

// shellWord is binary as one POSIX shell word: unchanged when it holds only safe characters, single-quoted
// otherwise.
func shellWord(binary string) string {
	for _, c := range binary {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			strings.ContainsRune("/_-.:+", c) {
			continue
		}
		return "'" + strings.ReplaceAll(binary, "'", "'\"'\"'") + "'"
	}
	return binary
}

// agyHookEvents are the Antigravity events whose hook output reaches the model at a moment mairc wants
// (internal/hook: agyDelivers), in the order they are written.
func agyHookEvents() []string {
	return []string{"SessionStart", "PreInvocation"}
}

// agyHooks is mairc's named hook in Antigravity's hooks.json (https://antigravity.google/docs/hooks.md, read
// 17 Nov 2026): {"mairc": {"enabled": true, "<Event>": [{"type": "command", "command": "...", "timeout": N}]}}.
// SessionStart and PreInvocation take a bare handler list (no matcher). Each command names its event, because the
// documented hook input does not. The whole entry is mairc's: install rewrites these keys and keeps any other key
// the user added to it; uninstall removes the entry.
func agyHooks(binary string) *node {
	events := agyHookEvents()
	pairs := make([]any, 0, 2+2*len(events))
	pairs = append(pairs, "enabled", num("true"))
	for _, event := range events {
		command := shellWord(binary) + " hook --client agy --event " + event
		if windowsBinary(binary) {
			command = `"` + binary + `" hook --client agy --event ` + event
		}
		pairs = append(pairs, event, array(object(
			"type", str("command"), "command", str(command), "timeout", num(hookTimeoutSeconds))))
	}
	return object(pairs...)
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

// backupFile is where the backup of path (a configured path, as the CLI table names it) is kept under root: the path's own directories below root, with a Windows
// volume (C: or \\server\share) as one plain directory name.
func backupFile(root, path string) string {
	volume := filepath.VolumeName(path)
	name := strings.Map(func(r rune) rune {
		if r == ':' || r == '\\' || r == '/' {
			return '_'
		}
		return r
	}, strings.TrimLeft(volume, `\/`))
	return filepath.Join(root, name, path[len(volume):])
}
