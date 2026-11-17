package install

import (
	"os"
	"path/filepath"
	"strings"
)

// A cliSpec is one supported coding CLI: how to tell it is on this machine and which of its configuration files
// `mairc install` changes. Adding a CLI is a new row here, not new writer code: each file names its format, and the
// format's one writer applies the entries the file asks for.
type cliSpec struct {
	name     string
	binaries []string // detected when one of these is on PATH
	dirs     []string // ... or when one of these exists under the home directory
	files    func(scope string, o Options) []fileSpec
}

// A fileSpec is one configuration file and what mairc owns in it.
type fileSpec struct {
	path   string
	format string     // "json" (the JSON writer: writer_json.go)
	server *entrySpec // the MCP server entry, or nil
	hooks  *hookSpec  // Claude-style hook entries, or nil
}

// An entrySpec is one named entry inside an object reached by keyPath, e.g. mcpServers.mairc.
type entrySpec struct {
	keyPath []string
	name    string
	value   func(binary string) *node
}

// A hookSpec is mairc's handler on each of events, in Claude-style hooks: {"<Event>": [{"hooks": [handler]}]}.
type hookSpec struct {
	keyPath []string
	events  []string
	handler func(binary string) *node
	ours    func(handler *node) bool
}

// clis returns the supported CLIs in the order they are reported.
func clis() []cliSpec {
	return []cliSpec{claudeCode()}
}

// claudeCode is Claude Code (docs/compat.md, "Claude Code"; checked against code.claude.com/docs/en/mcp and /hooks
// on 17 Nov 2026): MCP servers in ~/.claude.json (user) or <project>/.mcp.json (project) under mcpServers; hooks in
// ~/.claude/settings.json or <project>/.claude/settings.json.
func claudeCode() cliSpec {
	return cliSpec{
		name:     "claude",
		binaries: []string{"claude"},
		dirs:     []string{".claude"},
		files: func(scope string, o Options) []fileSpec {
			mcp := filepath.Join(o.Home, ".claude.json")
			settings := filepath.Join(o.Home, ".claude", "settings.json")
			if scope == "project" {
				mcp = filepath.Join(o.Project, ".mcp.json")
				settings = filepath.Join(o.Project, ".claude", "settings.json")
			}
			return []fileSpec{
				{path: mcp, format: "json", server: &entrySpec{
					keyPath: []string{"mcpServers"}, name: "mairc", value: stdioServer,
				}},
				{path: settings, format: "json", hooks: &hookSpec{
					keyPath: []string{"hooks"}, events: hookEvents(), handler: hookHandler, ours: isOurHook,
				}},
			}
		},
	}
}

// stdioServer is the MCP entry that runs the mairc stdio shim.
func stdioServer(binary string) *node {
	return object("command", str(binary), "args", array(str("stdio")))
}

// detect reports whether a CLI is on this machine: one of its binaries on PATH, or one of its configuration
// directories under home.
func detect(c cliSpec, o Options) bool {
	if o.LookPath != nil {
		for _, b := range c.binaries {
			if _, err := o.LookPath(b); err == nil {
				return true
			}
		}
	}
	for _, d := range c.dirs {
		if info, err := os.Stat(filepath.Join(o.Home, d)); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// cliNames lists the supported CLIs for a usage message.
func cliNames() string {
	all := clis()
	names := make([]string, 0, len(all))
	for _, c := range all {
		names = append(names, c.name)
	}
	return strings.Join(names, ", ")
}
