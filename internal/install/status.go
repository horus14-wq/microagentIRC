package install

import (
	"errors"
	"io/fs"
	"os"
	"strings"
)

// Status is one supported CLI as `mairc doctor` finds it on this machine: what the installer would have written, and
// what is actually there now. Everything in it is read-only; nothing is created, changed or installed.
type Status struct {
	Name     string // the CLI's name in the table (clis.go)
	Detected bool   // its binary is on PATH, or its configuration directory exists under home
	Binary   string // its binary on PATH ("" when only a configuration directory was found)

	// MCP: the file that should carry mairc's server entry for this scope.
	MCPPath    string // "" when the CLI has no MCP file for this scope
	MCPPresent bool   // mairc's entry is in it
	MCPCommand string // the command the entry runs ("" when absent): what the CLI will execute
	// Hook: the file that should carry mairc's hook, for the CLIs whose hook output reaches the model.
	HookPath    string
	HookPresent bool
	HookCommand string // the command the hook runs, as recorded ("" when absent)
	// HookNotice is a step only the user can take before the CLI runs the hook (e.g. approving it in the CLI);
	// mairc cannot check it from outside. "" when there is none.
	HookNotice string
	// Fallback: the instruction file that carries the "read chat yourself" note for a CLI with no hook.
	FallbackPath    string
	FallbackPresent bool

	Problem string // the first file that could not be read or parsed, and why
}

// Statuses reports each supported CLI for a scope, in the table's order: with o.Only set, exactly the named ones;
// otherwise every CLI that is detected or already configured (an unconfigured, undetected one is left out).
func Statuses(o Options) []Status {
	scope := o.Scope
	if scope == "" {
		scope = "user"
	}
	var out []Status
	for _, c := range clis() {
		if len(o.Only) > 0 && !containsName(o.Only, c.name) {
			continue
		}
		s := statusOf(c, scope, o)
		if len(o.Only) == 0 && !s.Detected && !s.configured() {
			continue
		}
		out = append(out, s)
	}
	return out
}

// configured reports whether mairc wrote anything for this CLI.
func (s Status) configured() bool { return s.MCPPresent || s.HookPresent || s.FallbackPresent }

// statusOf reads one CLI's files.
func statusOf(c cliSpec, scope string, o Options) Status {
	s := Status{Name: c.name, Detected: detect(c, o), Binary: binaryOnPath(o.LookPath, c.binaries)}
	for _, f := range c.files(scope, o) {
		raw, err := os.ReadFile(f.path) // #nosec G304 -- the CLI's own configuration file, named by the table
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			if s.Problem == "" {
				s.Problem = f.path + ": " + err.Error()
			}
			continue
		}
		switch {
		case f.server != nil && f.namedHook:
			if s.HookPath == "" {
				s.HookPath, s.HookNotice = f.path, f.notice
			}
			if len(raw) == 0 {
				continue
			}
			root, perr := parse(raw)
			if perr != nil || root.kind != objectNode {
				if s.Problem == "" {
					s.Problem = f.path + ": not a JSON object"
				}
				continue
			}
			if command, ok := namedHookCommand(walkKeys(root, f.server.keyPath).get(f.server.name)); ok {
				s.HookPresent, s.HookCommand = true, command
			}
		case f.server != nil:
			if s.MCPPath == "" {
				s.MCPPath = f.path
			}
			if len(raw) == 0 {
				continue
			}
			root, perr := parse(raw)
			if perr != nil || root.kind != objectNode {
				if s.Problem == "" {
					s.Problem = f.path + ": not a JSON object"
				}
				continue
			}
			entry := walkKeys(root, f.server.keyPath).get(f.server.name)
			if command, ok := serverCommand(entry); ok {
				s.MCPPresent, s.MCPCommand = true, command
			}
		case f.hooks != nil:
			if s.HookPath == "" {
				s.HookPath, s.HookNotice = f.path, f.notice
			}
			if len(raw) == 0 {
				continue
			}
			root, perr := parse(raw)
			if perr != nil || root.kind != objectNode {
				if s.Problem == "" {
					s.Problem = f.path + ": not a JSON object"
				}
				continue
			}
			if command, ok := hookCommandIn(root, f.hooks); ok {
				s.HookPresent, s.HookCommand = true, command
			}
		case f.block != nil && f.block.plugin:
			// A plugin file is the CLI's hook: it runs `mairc hook` itself, so there is no command to run from outside.
			if s.HookPath == "" {
				s.HookPath = f.path
			}
			if present, _ := blockIn(raw, f.block); present {
				s.HookPresent = true
			}
		case f.block != nil:
			instruction := strings.HasPrefix(f.block.begin, "<!--") // the markdown marker; TOML uses "# ..."
			if instruction {
				if s.FallbackPath == "" {
					s.FallbackPath = f.path
				}
			} else if s.MCPPath == "" {
				s.MCPPath = f.path
			}
			present, command := blockIn(raw, f.block)
			switch {
			case instruction && present:
				s.FallbackPresent = true
			case !instruction && present:
				s.MCPPresent = true
				if s.MCPCommand == "" {
					s.MCPCommand = command
				}
			}
		}
	}
	return s
}

// walkKeys follows a key path through objects; a missing step is an empty object, so every lookup is safe.
func walkKeys(root *node, path []string) *node {
	cur := root
	for _, key := range path {
		next := cur.get(key)
		if next == nil || next.kind != objectNode {
			return &node{kind: objectNode}
		}
		cur = next
	}
	return cur
}

// serverCommand returns the command an MCP entry runs: the "command" value, or the first of "args" when the entry is
// an OpenCode-style {"type":"local","command":[...]} array.
func serverCommand(entry *node) (string, bool) {
	if entry == nil || entry.kind != objectNode {
		return "", false
	}
	if command := entry.get("command"); command != nil {
		if s, ok := command.stringValue(); ok {
			return s, true
		}
		if command.kind == arrayNode && len(command.items) > 0 {
			return command.items[0].stringValue()
		}
	}
	return "", false
}

// namedHookCommand returns the command of the first handler in a named hook entry (Antigravity: an object whose
// event keys hold bare handler lists), in the order the events are written.
func namedHookCommand(entry *node) (string, bool) {
	if entry == nil || entry.kind != objectNode {
		return "", false
	}
	for _, handlers := range entry.items {
		if handlers.kind != arrayNode {
			continue
		}
		for _, handler := range handlers.items {
			if handler.kind != objectNode {
				continue
			}
			if command, ok := handler.get("command").stringValue(); ok {
				return command, true
			}
		}
	}
	return "", false
}

// hookCommandIn returns the command of the first handler under any of the spec's events that mairc owns.
func hookCommandIn(root *node, spec *hookSpec) (string, bool) {
	hooks := walkKeys(root, spec.keyPath)
	for _, event := range spec.events {
		groups := hooks.get(event)
		if groups == nil || groups.kind != arrayNode {
			continue
		}
		for _, group := range groups.items {
			handlers := group.get("hooks")
			if handlers == nil || handlers.kind != arrayNode {
				continue
			}
			for _, handler := range handlers.items {
				if spec.ours(handler) {
					return recordedCommand(handler), true
				}
			}
		}
	}
	return "", false
}

// recordedCommand spells a hook handler back into the command line it runs: the exec form's command plus args, or the
// shell form's command as written.
func recordedCommand(handler *node) string {
	command, _ := handler.get("command").stringValue()
	if args := handler.get("args"); args != nil && args.kind == arrayNode {
		parts := []string{command}
		for _, arg := range args.items {
			if s, ok := arg.stringValue(); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, " ")
	}
	return command
}

// blockIn reports whether the file holds the spec's block, and for a TOML server block the command it runs.
func blockIn(raw []byte, spec *blockSpec) (bool, string) {
	if len(raw) == 0 {
		return false, ""
	}
	lines := strings.Split(string(raw), "\n")
	begin := markerPrefix(spec.begin)
	inside := false
	for _, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(trimmed, spec.end):
			inside = false
		case inside:
			if value, found := strings.CutPrefix(strings.TrimSpace(trimmed), "command = "); found {
				// The installer escapes the path (tomlString): a Windows path's backslashes are doubled in the file.
				command, _ := tomlUnquote(value)
				return true, command
			}
		case strings.HasPrefix(trimmed, begin):
			inside = true
		}
	}
	return strings.Contains(string(raw), spec.end), ""
}

func containsName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// binaryOnPath returns the path of the first of names found on PATH.
func binaryOnPath(look func(string) (string, error), names []string) string {
	if look == nil {
		return ""
	}
	for _, name := range names {
		if path, err := look(name); err == nil {
			return path
		}
	}
	return ""
}
