package install

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// manyCLIsHome is a home directory as found on a machine with all seven supported CLIs in daily use: every file
// already holds the user's own servers, hooks, permissions, comments and instructions. Paths are relative to home,
// slash-separated. No seed contains the word mairc, so any mairc byte left after uninstall is the installer's.
func manyCLIsHome() map[string]string {
	return map[string]string{
		".claude.json": `{
  "numStartups": 412,
  "theme": "dark",
  "mcpServers": {
    "agent-mail": {
      "type": "http",
      "url": "http://127.0.0.1:8765/mcp/",
      "headers": {
        "Authorization": "Bearer abc\u0026def"
      }
    },
    "lsp": {"command": "/usr/local/bin/agent-lsp", "args": ["--stdio"], "env": {"RUST_LOG": "warn"}}
  },
  "projects": {
    "/home/u/src/app": {
      "allowedTools": [],
      "hasTrustDialogAccepted": true
    }
  }
}
`,
		".claude/settings.json": `{
    "model": "opus",
    "permissions": {
        "allow": ["Bash(git status:*)", "Read(~/notes/**)"],
        "deny": ["Bash(rm -rf:*)"]
    },
    "env": {"EDITOR": "vim"},
    "hooks": {
        "PostToolUse": [
            {
                "matcher": "Edit|Write",
                "hooks": [{"type": "command", "command": "gofumpt -w \"$FILE\"", "timeout": 30}]
            }
        ],
        "Stop": [{"hooks": [{"type": "command", "command": "notify-send done"}]}]
    }
}
`,
		".claude/CLAUDE.md": "# House rules\nPrefer small diffs.\n",
		".codex/config.toml": `# Codex settings, edited by hand
model = "gpt-5.5-codex"
approval_policy = "on-request"

[mcp_servers.lsp]
command = "/usr/local/bin/agent-lsp" # the language server bridge
args = ["--stdio"]

[projects."/home/u/src/app"]
trust_level = "trusted"
`,
		".codex/hooks.json": "{\"hooks\":{\"Stop\":[{\"hooks\":[{\"type\":\"command\",\"command\":\"say done\"}]}]}}\n",
		".codex/AGENTS.md":  "# Codex instructions\n\nRun the tests before you commit.\n",
		".grok/config.toml": "# Grok Build\n[ui]\ntheme = \"midnight\"\n\n[mcp_servers.agent-lsp]\ncommand = \"/usr/local/bin/agent-lsp\"\nargs = [\"--stdio\"]\nenabled = true\n",
		".grok/AGENTS.md":   "Answer in English.\n",
		".config/devin/mcp_config.json": "{\n\t\"mcpServers\": {\n\t\t\"github\": {\n\t\t\t\"command\": \"gh-mcp\",\n" +
			"\t\t\t\"args\": [],\n\t\t\t\"env\": {\"GH_HOST\": \"github.com\"}\n\t\t}\n\t}\n}\n",
		".config/opencode/opencode.json": `{
  "$schema": "https://opencode.ai/config.json",
  "theme": "tokyonight",
  "mcp": {
    "docs": {"type": "remote", "url": "https://docs.example.com/mcp", "enabled": true}
  },
  "permission": {"edit": "ask"}
}
`,
		".config/opencode/AGENTS.md": "# OpenCode\n\n- Keep answers short.\n- Never push.\n",
		".prime/agent/settings.json": `{"defaultModel":"deepseek","mcpServers":{"fs":{"type":"stdio","command":"fs-mcp","args":["/home/u"]}}}`,
		".prime/agent/AGENTS.md":     "My Prime rules.\n",
		".gemini/config/mcp_config.json": `{
  "mcpServers": {
    "search": {
      "command": "search-mcp",
      "args": []
    }
  }
}
`,
		".gemini/settings.json": "{\"theme\": \"Default\"}\n",
	}
}

func seedHome(t *testing.T, files map[string]string) string {
	t.Helper()
	home := t.TempDir()
	for rel, content := range files {
		write(t, filepath.Join(home, filepath.FromSlash(rel)), content, 0o600)
	}
	return home
}

// snapshot reads every file under root (relative, slash-separated) into a map.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	for _, rel := range tree(t, root) {
		if !strings.HasSuffix(rel, "/") {
			files[rel] = string(read(t, filepath.Join(root, filepath.FromSlash(rel))))
		}
	}
	return files
}

// withoutMairc returns the JSON document's meaning (keys sorted, whitespace dropped) with mairc's entries taken out:
// the MCP entry under mcpServers or mcp, and every hook handler that runs `mairc hook`, with the groups and events
// only it filled.
func withoutMairc(t *testing.T, raw string) string {
	t.Helper()
	root, err := parse([]byte(raw))
	if err != nil {
		t.Fatalf("not JSON:\n%s", raw)
	}
	for _, key := range []string{"mcpServers", "mcp"} {
		if servers := root.get(key); servers != nil {
			servers.del("mairc")
		}
	}
	removeHooks(root, &hookSpec{keyPath: []string{"hooks"}, ours: isOurHook})
	var v any
	if err := json.Unmarshal(root.bytes(), &v); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(canonical)
}

// The release gate (mairc-nlr): `mairc install` on a machine with all seven CLIs and real user content. Install
// succeeds; a second run is byte-identical; the user's content is intact; one corrupt file blocks only its own CLI;
// uninstall leaves no mairc bytes, no stub files and no backups.
func TestInstallOnAMachineWithManyCLIs(t *testing.T) {
	seed := manyCLIsHome()
	home := seedHome(t, seed)
	treeBefore := tree(t, home)
	o := Options{Binary: nativeTestBinary(testBinary), Home: home} // detection by configuration directory

	code, out := runRaw(o)
	if code != 0 {
		t.Fatalf("install = %d:\n%s", code, out)
	}
	if strings.Contains(out, "skipped") {
		t.Fatalf("a CLI was not set up:\n%s", out)
	}
	installed := snapshot(t, home)
	for _, c := range clis() {
		for _, f := range c.files("user", o) {
			rel, _ := filepath.Rel(home, f.path)
			if _, ok := installed[filepath.ToSlash(rel)]; !ok {
				t.Fatalf("%s: %s was not written", c.name, rel)
			}
		}
	}
	// What doctor reads back: every CLI has its server entry, and its hook or fallback note where it gets one.
	for _, s := range Statuses(o) {
		if !s.Detected || !s.MCPPresent || (s.HookPath != "") != s.HookPresent ||
			(s.FallbackPath != "") != s.FallbackPresent || s.Problem != "" {
			t.Fatalf("%s after install: %+v", s.Name, s)
		}
	}
	if n := len(Statuses(o)); n != len(clis()) {
		t.Fatalf("%d CLIs detected, want %d", n, len(clis()))
	}

	// User content is intact: JSON files mean the same once mairc's entries are taken out; text files still start
	// with every byte the user had, and only mairc's block follows.
	for rel, before := range seed {
		after := installed[rel]
		switch {
		case strings.HasSuffix(rel, ".json"):
			if withoutMairc(t, after) != withoutMairc(t, before) {
				t.Fatalf("%s lost user content:\n%s\nwas:\n%s", rel, after, before)
			}
		case !strings.HasPrefix(after, before):
			t.Fatalf("%s lost user content:\n%s\nwas:\n%s", rel, after, before)
		}
	}

	// Grok and Devin also run the hooks in ~/.claude/settings.json. mairc's hook must be there once per event and in
	// no other file either of them reads, or one message would be asked for (and paid for) twice.
	assertOneHookPerEvent(t, home, installed)

	// A second run is byte-identical, backups included.
	if code, out := runRaw(o); code != 0 || out != "already installed\n" {
		t.Fatalf("second install = %d:\n%s", code, out)
	}
	again := snapshot(t, home)
	if len(again) != len(installed) {
		t.Fatalf("the second run added or removed files: %d, then %d", len(installed), len(again))
	}
	for rel, content := range installed {
		if again[rel] != content {
			t.Fatalf("the second run changed %s", rel)
		}
	}

	// Uninstall gives back the home exactly as seeded: same files and folders, same bytes.
	o.Uninstall = true
	if code, out := runRaw(o); code != 0 {
		t.Fatalf("uninstall = %d:\n%s", code, out)
	}
	if after := tree(t, home); strings.Join(after, "\n") != strings.Join(treeBefore, "\n") {
		t.Fatalf("uninstall left:\n%s\nwant:\n%s", strings.Join(after, "\n"), strings.Join(treeBefore, "\n"))
	}
	for rel, content := range snapshot(t, home) {
		if content != seed[rel] {
			t.Fatalf("%s after uninstall:\n%s\nwant:\n%s", rel, content, seed[rel])
		}
		if strings.Contains(content, "mairc") {
			t.Fatalf("%s still holds mairc bytes:\n%s", rel, content)
		}
	}
}

// One corrupt file (Devin's JSON) on the same machine: Devin is skipped with one line naming it, the six other CLIs
// are set up, and the run ends with the summary and exit 1.
func TestInstallOnAMachineWithManyCLIsAndOneCorruptFile(t *testing.T) {
	seed := manyCLIsHome()
	const corrupt = "{\n\t\"mcpServers\": {\n\t\t\"github\": {\"command\": \"gh-mcp\",,}\n"
	seed[".config/devin/mcp_config.json"] = corrupt
	home := seedHome(t, seed)
	o := Options{Binary: nativeTestBinary(testBinary), Home: home}
	code, out := runRaw(o)
	devin := filepath.Join(home, ".config", "devin", "mcp_config.json")
	if !strings.Contains(out, "devin: skipped, cannot parse "+devin+": fix it or use --print\n") || code != 1 ||
		!strings.HasSuffix(out, "summary: set up claude, codex, grok, opencode, prime-agent, agy; skipped devin\n") {
		t.Fatalf("install = %d:\n%s", code, out)
	}
	if got := string(read(t, devin)); got != corrupt {
		t.Fatalf("the corrupt file was changed:\n%s", got)
	}
	for _, c := range clis() {
		if c.name == "devin" {
			continue
		}
		for _, f := range c.files("user", o) {
			if !strings.Contains(out, "updated "+f.path+"\n") {
				t.Fatalf("%s: %s was not set up:\n%s", c.name, f.path, out)
			}
		}
	}
}

// assertOneHookPerEvent checks that mairc's hook runs once per event for every CLI: once in Claude Code's settings
// (which Grok and Devin also run), once in Codex's own hooks.json (which only Codex runs), and nowhere else.
func assertOneHookPerEvent(t *testing.T, home string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		if strings.Contains(rel, "install-backup/") {
			continue
		}
		perEvent := map[string]int{}
		if strings.HasSuffix(rel, ".json") {
			root, err := parse([]byte(content))
			if err != nil {
				t.Fatalf("%s: %v", rel, err)
			}
			hooks := walkKeys(root, []string{"hooks"})
			for i, event := range hooks.keys {
				for _, group := range hooks.items[i].items {
					handlers := group.get("hooks")
					if handlers == nil {
						continue
					}
					for _, handler := range handlers.items {
						if isOurHook(handler) {
							perEvent[event]++
						}
					}
				}
			}
		} else if strings.Contains(content, "[[hooks") && strings.Contains(content, "mairc") {
			t.Fatalf("%s: a TOML hook table next to mairc's entry", rel)
		}
		want := 0
		if rel == ".claude/settings.json" || rel == ".codex/hooks.json" {
			want = 1
		}
		for _, event := range hookEvents() {
			if perEvent[event] != want {
				t.Fatalf("%s: mairc's hook on %s %d times, want %d", rel, event, perEvent[event], want)
			}
		}
		if len(perEvent) > len(hookEvents()) {
			t.Fatalf("%s: mairc's hook on other events: %v", rel, perEvent)
		}
	}
	if !bytes.Contains(read(t, filepath.Join(home, ".claude", "settings.json")), []byte(`"SessionStart"`)) {
		t.Fatal("Claude Code's settings carry no hook")
	}
}
