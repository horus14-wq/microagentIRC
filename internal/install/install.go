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
	mcpPath, settingsPath := paths(o, scope)
	files := []*target{
		{path: mcpPath, apply: mcpEdit(o.Binary, o.Uninstall)},
		{path: settingsPath, apply: hooksEdit(o.Binary, o.Uninstall)},
	}
	// Read and check every file before changing any: one unparsable file leaves both untouched.
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
	changed := 0
	for _, f := range files {
		edited, ok := f.apply(f.root)
		if !ok {
			_, _ = fmt.Fprintf(out, "cannot parse %s: fix it or use --print\n", f.path)
			return 1
		}
		if !edited {
			continue
		}
		if err := f.store(); err != nil {
			_, _ = fmt.Fprintf(out, "cannot write %s\n", f.path)
			return 1
		}
		_, _ = fmt.Fprintf(out, "updated %s\n", f.path)
		changed++
	}
	if changed == 0 && !o.Uninstall {
		_, _ = fmt.Fprintln(out, "already installed")
	}
	return 0
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

func paths(o Options, scope string) (mcp, settings string) {
	if scope == "project" {
		return filepath.Join(o.Project, ".mcp.json"), filepath.Join(o.Project, ".claude", "settings.json")
	}
	return filepath.Join(o.Home, ".claude.json"), filepath.Join(o.Home, ".claude", "settings.json")
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

// mcpEdit adds, updates or removes mcpServers.mairc.
func mcpEdit(binary string, uninstall bool) func(root *node) (bool, bool) {
	return func(root *node) (bool, bool) {
		if uninstall {
			servers := root.get("mcpServers")
			if servers == nil || servers.kind != objectNode || !servers.del("mairc") {
				return false, true
			}
			if len(servers.keys) == 0 {
				root.del("mcpServers")
			}
			return true, true
		}
		servers, ok := root.child("mcpServers", objectNode)
		if !ok {
			return false, false
		}
		existing := servers.get("mairc")
		if existing == nil || existing.kind != objectNode {
			servers.set("mairc", object("command", str(binary), "args", array(str("stdio"))))
			return true, true
		}
		// Ours already: update the command and args in place, keeping anything else the user added (env, ...).
		edited := false
		for _, kv := range []struct {
			key   string
			value *node
		}{{"command", str(binary)}, {"args", array(str("stdio"))}} {
			if !kv.value.equal(existing.get(kv.key)) {
				existing.set(kv.key, kv.value)
				edited = true
			}
		}
		return edited, true
	}
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

// hooksEdit adds, updates or removes mairc's hook handler on each hook event.
func hooksEdit(binary string, uninstall bool) func(root *node) (bool, bool) {
	return func(root *node) (bool, bool) {
		if uninstall {
			return removeHooks(root), true
		}
		hooks, ok := root.child("hooks", objectNode)
		if !ok {
			return false, false
		}
		want := hookHandler(binary)
		edited := false
		for _, event := range hookEvents() {
			groups, ok := hooks.child(event, arrayNode)
			if !ok {
				return false, false
			}
			found := false
			for _, group := range groups.items {
				handlers := group.get("hooks")
				if handlers == nil || handlers.kind != arrayNode {
					continue
				}
				for i, h := range handlers.items {
					if !isOurHook(h) {
						continue
					}
					found = true
					if !h.equal(want) {
						handlers.items[i] = hookHandler(binary)
						edited = true
					}
				}
			}
			if !found {
				groups.items = append(groups.items, object("hooks", array(hookHandler(binary))))
				edited = true
			}
		}
		return edited, true
	}
}

// removeHooks removes every mairc handler, then each group, event array and the hooks object that only it emptied.
func removeHooks(root *node) bool {
	hooks := root.get("hooks")
	if hooks == nil || hooks.kind != objectNode {
		return false
	}
	removed := false
	for _, event := range append([]string(nil), hooks.keys...) {
		groups := hooks.get(event)
		if groups == nil || groups.kind != arrayNode {
			continue
		}
		removedHere := false
		kept := groups.items[:0]
		for _, group := range groups.items {
			handlers := group.get("hooks")
			if handlers == nil || handlers.kind != arrayNode {
				kept = append(kept, group)
				continue
			}
			removedInGroup := false
			keptHandlers := handlers.items[:0]
			for _, h := range handlers.items {
				if isOurHook(h) {
					removedInGroup = true
					continue
				}
				keptHandlers = append(keptHandlers, h)
			}
			handlers.items = keptHandlers
			if removedInGroup {
				removed, removedHere = true, true
				if len(keptHandlers) == 0 {
					continue // the group only held ours
				}
			}
			kept = append(kept, group)
		}
		groups.items = kept
		if removedHere && len(kept) == 0 {
			hooks.del(event)
		}
	}
	if removed && len(hooks.keys) == 0 {
		root.del("hooks")
	}
	return removed
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
