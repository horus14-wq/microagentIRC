package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lookPathOf finds exactly the named binaries.
func lookPathOf(found ...string) func(string) (string, error) {
	return func(file string) (string, error) {
		for _, f := range found {
			if f == file {
				return "/usr/bin/" + file, nil
			}
		}
		return "", errors.New("not found")
	}
}

func runRaw(o Options) (int, string) {
	var out bytes.Buffer
	o.Binary = nativeTestBinary(o.Binary) // an absolute path on this OS (C:\opt\...\mairc.exe on Windows)
	o.Out = &out
	return Run(o), out.String()
}

// jbin is the native test binary as it appears inside a JSON string (backslashes escaped on Windows).
func jbin() string {
	quoted, _ := json.Marshal(nativeTestBinary(testBinary))
	return strings.Trim(string(quoted), `"`)
}

func TestDetectionSetsUpWhatIsPresentAndSkipsTheRest(t *testing.T) {
	cases := []struct {
		name     string
		lookPath func(string) (string, error)
		dir      string // created under home before the run
		wantSet  bool
	}{
		{"binary on PATH", lookPathOf("claude"), "", true},
		{"config directory only", lookPathOf(), ".claude", true},
		{"neither", lookPathOf(), "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if tc.dir != "" {
				if err := os.MkdirAll(filepath.Join(home, tc.dir), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			code, out := runRaw(Options{Binary: testBinary, Home: home, LookPath: tc.lookPath})
			mcp := filepath.Join(home, ".claude.json")
			_, statErr := os.Stat(mcp)
			if code != 0 || (statErr == nil) != tc.wantSet {
				t.Fatalf("Run = %d, out %q, %s written: %v", code, out, mcp, statErr == nil)
			}
			if !tc.wantSet {
				var want strings.Builder
				for _, c := range clis() {
					want.WriteString(c.name + ": not found, skipped\n")
				}
				want.WriteString("already installed\n")
				if out != want.String() {
					t.Fatalf("out = %q, want %q", out, want.String())
				}
			}
		})
	}
}

func TestOnlyNamesAnUnknownCLI(t *testing.T) {
	code, out := runRaw(Options{Binary: testBinary, Home: t.TempDir(), Only: []string{"claude", "vim"}})
	if code != 2 || out != "usage: unknown CLI vim (supported: "+cliNames()+")\n" {
		t.Fatalf("Run = %d, out %q", code, out)
	}
}

func TestDryRunShowsTheDiffAndWritesNothing(t *testing.T) {
	home := t.TempDir()
	mcp := filepath.Join(home, ".claude.json")
	write(t, mcp, otherMCP, 0o600)
	code, out := runRaw(Options{Binary: testBinary, Home: home, Only: []string{"claude"}, DryRun: true})
	if code != 0 {
		t.Fatalf("Run = %d, out %q", code, out)
	}
	if got := string(read(t, mcp)); got != otherMCP {
		t.Fatalf("--dry-run changed %s", mcp)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); err == nil {
		t.Fatal("--dry-run created the settings file")
	}
	for _, want := range []string{
		"would update " + mcp + "\n",
		`+     "mairc": {` + "\n",
		`+       "command": "` + jbin() + `",` + "\n",
		"would update " + filepath.Join(home, ".claude", "settings.json") + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"theme"`) {
		t.Fatalf("the diff shows unchanged lines:\n%s", out)
	}
}

func TestDecliningTheConfirmationWritesNothing(t *testing.T) {
	home := t.TempDir()
	var asked []string
	code, out := runRaw(Options{
		Binary: testBinary, Home: home, Only: []string{"claude"},
		Confirm: func(files []string) bool { asked = files; return false },
	})
	if code != 1 || out != "nothing changed\n" {
		t.Fatalf("Run = %d, out %q", code, out)
	}
	want := []string{filepath.Join(home, ".claude.json"), filepath.Join(home, ".claude", "settings.json")}
	if strings.Join(asked, "|") != strings.Join(want, "|") {
		t.Fatalf("asked about %v, want %v", asked, want)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("declined install wrote %v", entries)
	}
	// Nothing to change: no question is asked.
	runRaw(Options{Binary: testBinary, Home: home, Only: []string{"claude"}})
	called := false
	code, out = runRaw(Options{
		Binary: testBinary, Home: home, Only: []string{"claude"},
		Confirm: func([]string) bool { called = true; return false },
	})
	if code != 0 || out != "already installed\n" || called {
		t.Fatalf("second run = %d, out %q, asked %v", code, out, called)
	}
}

// The JSON writer on its own: an entry at a nested key path is added, updated in place (keeping the user's own
// keys), and removed together with the containers only it filled.
func TestJSONWriterNestedEntry(t *testing.T) {
	spec := fileSpec{format: "json", server: &entrySpec{
		keyPath: []string{"mcp", "servers"}, name: "mairc",
		value: func(b string) *node { return object("type", str("local"), "command", array(str(b), str("stdio"))) },
	}}
	root, err := parse([]byte(`{"a": 1, "mcp": {"servers": {"x": {"type": "remote"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if edited, ok := applyJSON(root, spec, "/bin/mairc", false); !edited || !ok {
		t.Fatalf("add = %v, %v", edited, ok)
	}
	root.get("mcp").get("servers").get("mairc").set("env", object("K", str("v")))
	if edited, ok := applyJSON(root, spec, "/new/mairc", false); !edited || !ok {
		t.Fatalf("update = %v, %v", edited, ok)
	}
	got := string(root.bytes())
	if !strings.Contains(got, `"/new/mairc"`) || !strings.Contains(got, `"K": "v"`) {
		t.Fatalf("update lost the user's key or the new path:\n%s", got)
	}
	if edited, _ := applyJSON(root, spec, "/new/mairc", false); edited {
		t.Fatal("a second identical run changed something")
	}
	if edited, _ := applyJSON(root, spec, "", true); !edited {
		t.Fatal("uninstall removed nothing")
	}
	want := "{\n  \"a\": 1,\n  \"mcp\": {\n    \"servers\": {\n      \"x\": {\n        \"type\": \"remote\"\n      }\n    }\n  }\n}\n"
	if got := string(root.bytes()); got != want {
		t.Fatalf("after uninstall:\n%s\nwant:\n%s", got, want)
	}
	only, _ := parse([]byte(`{}`))
	applyJSON(only, spec, "/bin/mairc", false)
	applyJSON(only, spec, "", true)
	if got := string(only.bytes()); got != "{}\n" {
		t.Fatalf("containers only we filled were not removed: %s", got)
	}
	bad, _ := parse([]byte(`{"mcp": []}`))
	if _, ok := applyJSON(bad, spec, "/bin/mairc", false); ok {
		t.Fatal("a non-object on the key path must refuse")
	}
}

// Every row, both scopes: the files it names keep other content, gain mairc once, a second run changes nothing, and
// uninstall restores the original bytes.
func TestEveryRowMergesIdempotentlyAndUninstallsExactly(t *testing.T) {
	// One fixture per format, and the prefix the merged file must still start with.
	fixtures := map[string]struct{ content, keptPrefix string }{
		"json":  {"{\n  \"keep\": {\n    \"a\": 1\n  }\n}\n", "{\n  \"keep\": {\n    \"a\": 1\n  },\n"},
		"block": {"model = \"o4\"\n\n[mcp_servers.other]\ncommand = \"other\"\n", "model = \"o4\"\n\n[mcp_servers.other]\ncommand = \"other\"\n\n"},
	}
	for _, c := range clis() {
		for _, scope := range []string{"user", "project"} {
			t.Run(c.name+"/"+scope, func(t *testing.T) {
				home, project := t.TempDir(), t.TempDir()
				o := Options{Binary: testBinary, Scope: scope, Home: home, Project: project, Only: []string{c.name}}
				files := c.files(scope, o)
				if len(files) == 0 {
					return // nothing to merge in this scope; the skip line is pinned by the row's own test
				}
				for _, f := range files {
					write(t, f.path, fixtures[f.format].content, 0o600)
				}
				if code, out := runRaw(o); code != 0 || strings.Count(out, "updated ") != len(files) {
					t.Fatalf("install = %d, out %q", code, out)
				}
				for _, f := range files {
					got := string(read(t, f.path))
					mark := jbin() // block files: their marker is the evidence (below)
					if f.block != nil {
						mark = f.block.begin
					}
					if !strings.HasPrefix(got, fixtures[f.format].keptPrefix) || !strings.Contains(got, mark) {
						t.Fatalf("%s lost the fixture or lacks mairc:\n%s", f.path, got)
					}
				}
				if code, out := runRaw(o); code != 0 || out != "already installed\n" {
					t.Fatalf("second install = %d, out %q", code, out)
				}
				o.Uninstall = true
				if code, _ := runRaw(o); code != 0 {
					t.Fatalf("uninstall = %d", code)
				}
				for _, f := range files {
					if got := string(read(t, f.path)); got != fixtures[f.format].content {
						t.Fatalf("uninstall did not restore %s:\n%s", f.path, got)
					}
				}
			})
		}
	}
}

// The MCP entry each new row writes into a fresh file, exactly.
func TestNewRowsWriteTheirDocumentedEntry(t *testing.T) {
	stdio := "{\n  \"mcpServers\": {\n    \"mairc\": {\n      \"command\": \"" + jbin() + "\",\n      \"args\": [\n        \"stdio\"\n      ]\n    }\n  }\n}\n"
	typed := "{\n  \"mcpServers\": {\n    \"mairc\": {\n      \"type\": \"stdio\",\n      \"command\": \"" + jbin() + "\",\n      \"args\": [\n        \"stdio\"\n      ]\n    }\n  }\n}\n"
	cases := []struct {
		cli, goos, rel, want string
	}{
		{"devin", "linux", filepath.Join(".config", "devin", "mcp_config.json"), stdio},
		{"devin", "windows", filepath.Join("AppData", "Roaming", "devin", "mcp_config.json"), stdio},
		{"prime-agent", "linux", filepath.Join(".prime", "agent", "settings.json"), typed},
		{"pi", "linux", filepath.Join(".pi", "agent", "mcp.json"), stdio},
	}
	for _, tc := range cases {
		t.Run(tc.cli+"/"+tc.goos, func(t *testing.T) {
			home := t.TempDir()
			if code, out := runRaw(Options{Binary: testBinary, Home: home, Only: []string{tc.cli}, GOOS: tc.goos}); code != 0 {
				t.Fatalf("install = %d, out %q", code, out)
			}
			if got := string(read(t, filepath.Join(home, tc.rel))); got != tc.want {
				t.Fatalf("%s:\n%s\nwant:\n%s", tc.rel, got, tc.want)
			}
		})
	}
}

func TestBlockWriter(t *testing.T) {
	spec := tomlServerBlock()
	begin, end := tomlMarkers()
	block := func(bin string) string { return begin + "\n" + tomlMCPServer(bin) + end + "\n" }

	// A fresh file is exactly the block.
	out, edited, problem := applyBlock(nil, spec, "/opt/mairc/bin/mairc", false)
	want := begin + "\n[mcp_servers.mairc]\ncommand = \"/opt/mairc/bin/mairc\"\nargs = [\"stdio\"]\n" + end + "\n"
	if !edited || problem != "" || string(out) != want {
		t.Fatalf("fresh = %q, %v, %q", out, edited, problem)
	}

	// A new binary path replaces the block in place; content after it is kept.
	before := "a = 1\n\n" + block("/old/mairc") + "[after]\nb = 2\n"
	out, edited, _ = applyBlock([]byte(before), spec, "/new/mairc", false)
	if !edited || string(out) != "a = 1\n\n"+block("/new/mairc")+"[after]\nb = 2\n" {
		t.Fatalf("update in place = %q", out)
	}
	out, _, _ = applyBlock(out, spec, "", true)
	if string(out) != "a = 1\n[after]\nb = 2\n" {
		t.Fatalf("uninstall from the middle = %q", out)
	}

	// A CLI that rewrites its own file can put its tables inside mairc's markers (Grok 1.0.88 wrote [marketplace]
	// before the end marker on its first run). Those lines are not mairc's: a re-install keeps them, after the
	// block, and an uninstall keeps them too. Before, both deleted them with the block.
	theirs := "[marketplace]\nofficial = true\n\n[[marketplace.sources]]\ngit = \"https://example.com/m.git\"\n"
	rewritten := "a = 1\n\n" + begin + "\n" + tomlMCPServer("/old/mairc") + "\n" + theirs + end + "\n[after]\nb = 2\n"
	out, edited, problem = applyBlock([]byte(rewritten), spec, "/new/mairc", false)
	if !edited || problem != "" || string(out) != "a = 1\n\n"+block("/new/mairc")+theirs+"[after]\nb = 2\n" {
		t.Fatalf("re-install over a rewritten file = %q (%q)", out, problem)
	}
	if again, edited, _ := applyBlock(out, spec, "/new/mairc", false); edited || string(again) != string(out) {
		t.Fatalf("the repaired file is not stable: %q", again)
	}
	out, edited, _ = applyBlock([]byte(rewritten), spec, "", true)
	if !edited || string(out) != "a = 1\n"+theirs+"[after]\nb = 2\n" {
		t.Fatalf("uninstall from a rewritten file = %q", out)
	}
	// Their table first, mairc's after it, both inside the markers.
	reordered := begin + "\n" + theirs + tomlMCPServer("/old/mairc") + end + "\n"
	out, _, _ = applyBlock([]byte(reordered), spec, "", true)
	if string(out) != theirs {
		t.Fatalf("uninstall with their table first = %q", out)
	}

	// Refusals: a hand-written table of ours, and a begin marker without an end.
	for name, raw := range map[string]string{
		"hand-written table": "[mcp_servers.mairc]\ncommand = \"x\"\n",
		"unterminated block": "a = 1\n" + begin + "\n[mcp_servers.mairc]\n",
	} {
		if _, _, problem := applyBlock([]byte(raw), spec, testBinary, false); problem == "" {
			t.Fatalf("%s: no refusal", name)
		}
	}

	// A Windows path is a valid TOML basic string.
	if got := tomlString(`C:\Program Files\mairc\mairc.exe`); got != `"C:\\Program Files\\mairc\\mairc.exe"` {
		t.Fatalf("tomlString = %s", got)
	}
	if got := tomlString("a\"b\x01"); got != `"a\"b\u0001"` {
		t.Fatalf("tomlString escapes = %s", got)
	}

	// The one documented inexact case: a file without a final newline gets one.
	out, _, _ = applyBlock([]byte("a = 1"), spec, testBinary, false)
	out, _, _ = applyBlock(out, spec, "", true)
	if string(out) != "a = 1\n" {
		t.Fatalf("no-newline file after uninstall = %q", out)
	}
}

// The TOML rows write the block, exactly, into a fresh file.
func TestTOMLRowsWriteTheBlock(t *testing.T) {
	begin, end := tomlMarkers()
	want := begin + "\n" + tomlMCPServer(nativeTestBinary(testBinary)) + end + "\n"
	for _, tc := range []struct{ cli, rel string }{
		{"codex", filepath.Join(".codex", "config.toml")},
		{"grok", filepath.Join(".grok", "config.toml")},
	} {
		home := t.TempDir()
		if code, out := runRaw(Options{Binary: testBinary, Home: home, Only: []string{tc.cli}}); code != 0 {
			t.Fatalf("%s install = %d, %q", tc.cli, code, out)
		}
		if got := string(read(t, filepath.Join(home, tc.rel))); got != want {
			t.Fatalf("%s:\n%s", tc.rel, got)
		}
	}
}

// The fallback snippets the installer writes are the ones skill/fallback.md documents, verbatim.
func TestFallbackSnippetsMatchSkill(t *testing.T) {
	skill := string(read(t, filepath.Join("..", "..", "skill", "fallback.md")))
	for name, snippet := range map[string]string{
		"opencode": openCodeFallback, "prime-agent": primeAgentFallback, "codex": codexFallback,
	} {
		if !strings.Contains(skill, "```markdown\n"+snippet+"```") {
			t.Fatalf("the %s snippet is not in skill/fallback.md verbatim:\n%s", name, snippet)
		}
	}
}

// At user scope the no-hook CLIs get the fallback snippet in their own instruction file; the block is removed again
// by uninstall, and a project scope never writes a shared AGENTS.md.
func TestNoHookRowsWriteTheFallbackSnippet(t *testing.T) {
	for _, tc := range []struct{ cli, rel, snippet string }{
		{"opencode", filepath.Join(".config", "opencode", "AGENTS.md"), openCodeFallback},
		{"prime-agent", filepath.Join(".prime", "agent", "AGENTS.md"), primeAgentFallback},
		{"pi", filepath.Join(".pi", "agent", "AGENTS.md"), openCodeFallback},
	} {
		home, project := t.TempDir(), t.TempDir()
		agents := filepath.Join(home, tc.rel)
		write(t, agents, "# My rules\nBe brief.\n", 0o600)
		if code, out := runRaw(Options{Binary: testBinary, Home: home, Only: []string{tc.cli}}); code != 0 {
			t.Fatalf("%s install = %d, %q", tc.cli, code, out)
		}
		got := string(read(t, agents))
		if !strings.HasPrefix(got, "# My rules\nBe brief.\n\n<!-- mairc: begin") || !strings.Contains(got, tc.snippet) {
			t.Fatalf("%s:\n%s", agents, got)
		}
		runRaw(Options{Binary: testBinary, Home: home, Only: []string{tc.cli}, Uninstall: true})
		if got := string(read(t, agents)); got != "# My rules\nBe brief.\n" {
			t.Fatalf("%s after uninstall: %q", agents, got)
		}
		runRaw(Options{Binary: testBinary, Scope: "project", Home: home, Project: project, Only: []string{tc.cli}})
		if _, err := os.Stat(filepath.Join(project, "AGENTS.md")); err == nil {
			t.Fatalf("%s project scope wrote a shared AGENTS.md", tc.cli)
		}
	}
}

// OpenCode's entry, and its .jsonc file preferred when it exists.
func TestOpenCodeRow(t *testing.T) {
	home := t.TempDir()
	if code, out := runRaw(Options{Binary: testBinary, Home: home, Only: []string{"opencode"}}); code != 0 {
		t.Fatalf("install = %d, %q", code, out)
	}
	want := "{\n  \"mcp\": {\n    \"mairc\": {\n      \"type\": \"local\",\n      \"command\": [\n        \"" + jbin() +
		"\",\n        \"stdio\"\n      ]\n    }\n  }\n}\n"
	if got := string(read(t, filepath.Join(home, ".config", "opencode", "opencode.json"))); got != want {
		t.Fatalf("opencode.json:\n%s", got)
	}
	withJSONC := t.TempDir()
	jsonc := filepath.Join(withJSONC, ".config", "opencode", "opencode.jsonc")
	write(t, jsonc, "{\n  // my comment\n  \"theme\": \"dark\"\n}\n", 0o600)
	code, out := runRaw(Options{Binary: testBinary, Home: withJSONC, Only: []string{"opencode"}})
	if code != 1 || !strings.Contains(out, "cannot parse "+jsonc) {
		t.Fatalf("a commented .jsonc must be refused, got %d %q", code, out)
	}
	if got := string(read(t, jsonc)); !strings.Contains(got, "// my comment") {
		t.Fatal("the commented .jsonc was rewritten")
	}
}

// agyOtherHooks is a hooks.json that already holds someone else's named hook, in a house style of its own.
const agyOtherHooks = "{\n\t\"guard\": {\n\t\t\"enabled\": true,\n\t\t\"PreToolUse\": [\n\t\t\t{\n\t\t\t\t\"matcher\": \"run_command\"," +
	"\n\t\t\t\t\"hooks\": [{\"type\": \"command\", \"command\": \"/opt/guard.py\", \"timeout\": 6}]\n\t\t\t}\n\t\t]\n\t}\n}\n"

// Antigravity hooks (mairc-2b3): install adds one named hook, "mairc", to ~/.gemini/config/hooks.json with a bare
// handler list on SessionStart and PreInvocation, each command naming its event. Another tool's hook in the file
// is kept byte for byte, a second install changes nothing, and uninstall puts the file back exactly.
func TestAntigravityHooks(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".gemini", "config", "hooks.json")
	write(t, path, agyOtherHooks, 0o600)
	o := Options{Binary: testBinary, Home: home, Only: []string{"agy"}}
	if code, out := runRaw(o); code != 0 {
		t.Fatalf("install = %d, %q", code, out)
	}
	installed := string(read(t, path))
	if !strings.HasPrefix(installed, strings.TrimSuffix(agyOtherHooks, "\n}\n")) {
		t.Fatalf("the other hook was not kept as written:\n%s", installed)
	}
	var got map[string]struct {
		Enabled       bool `json:"enabled"`
		SessionStart  []map[string]any
		PreInvocation []map[string]any
		PostToolUse   []map[string]any
	}
	if err := json.Unmarshal([]byte(installed), &got); err != nil {
		t.Fatalf("hooks.json is not JSON: %v\n%s", err, installed)
	}
	ours := got["mairc"]
	for event, handlers := range map[string][]map[string]any{
		"SessionStart": ours.SessionStart, "PreInvocation": ours.PreInvocation,
	} {
		want := testBinary + " hook --client agy --event " + event
		if len(handlers) != 1 || handlers[0]["type"] != "command" || handlers[0]["command"] != want ||
			handlers[0]["timeout"] != float64(5) || len(handlers[0]) != 3 {
			t.Fatalf("%s handlers = %v, want one command %q", event, handlers, want)
		}
	}
	if !ours.Enabled || ours.PostToolUse != nil || len(got) != 2 {
		t.Fatalf("mairc hook = %+v in %d hooks", ours, len(got))
	}
	if code, out := runRaw(o); code != 0 || strings.Contains(out, "hooks.json") {
		t.Fatalf("second install = %d, %q; want hooks.json unchanged", code, out)
	}
	if again := string(read(t, path)); again != installed {
		t.Fatalf("second install changed the file:\n%s", again)
	}
	// Status (mairc doctor) reports the named hook as agy's hook, not as a second MCP entry.
	for _, s := range Statuses(Options{Scope: "user", Home: home, Only: []string{"agy"}}) {
		wantMCP := filepath.Join(home, ".gemini", "config", "mcp_config.json")
		if s.HookPath != path || !s.HookPresent || s.MCPPath != wantMCP || !s.MCPPresent ||
			s.HookCommand != testBinary+" hook --client agy --event SessionStart" {
			t.Fatalf("status = %+v", s)
		}
	}
	o.Uninstall = true
	if code, out := runRaw(o); code != 0 {
		t.Fatalf("uninstall = %d, %q", code, out)
	}
	if after := string(read(t, path)); after != agyOtherHooks {
		t.Fatalf("after uninstall:\n%s", after)
	}
}

// Antigravity: the file agy's own "mcp add" writes, exactly; and project scope says it is skipped.
func TestAntigravityRow(t *testing.T) {
	home := t.TempDir()
	if code, out := runRaw(Options{Binary: testBinary, Home: home, Only: []string{"agy"}}); code != 0 {
		t.Fatalf("install = %d, %q", code, out)
	}
	want := "{\n  \"mcpServers\": {\n    \"mairc\": {\n      \"command\": \"" + jbin() +
		"\",\n      \"args\": [\n        \"stdio\"\n      ]\n    }\n  }\n}\n"
	if got := string(read(t, filepath.Join(home, ".gemini", "config", "mcp_config.json"))); got != want {
		t.Fatalf("mcp_config.json:\n%s", got)
	}
	code, out := runRaw(Options{Binary: testBinary, Scope: "project", Home: home, Project: t.TempDir(), Only: []string{"agy"}})
	if code != 0 || !strings.HasPrefix(out, "agy: no project-scope configuration, skipped\n") {
		t.Fatalf("project scope = %d, %q", code, out)
	}
}

// Codex gets both delivery paths: the hook (Claude Code's format, the same bytes the Claude row writes into an empty
// settings.json) with one line telling the user to approve it, and the self-limiting irc_read note in its global
// AGENTS.md for until then. The approval line appears only when the hook file changed; uninstall removes all three.
func TestCodexGetsTheHookTheApprovalLineAndTheNote(t *testing.T) {
	home := t.TempDir()
	o := Options{Binary: testBinary, Home: home, Only: []string{"codex"}}
	code, out := runRaw(o)
	if code != 0 || strings.Count(out, "codex: approve the mairc hook once: start codex and run /hooks\n") != 1 {
		t.Fatalf("install = %d, %q", code, out)
	}
	claudeHome := t.TempDir()
	runRaw(Options{Binary: testBinary, Home: claudeHome, Only: []string{"claude"}})
	hooks := filepath.Join(home, ".codex", "hooks.json")
	if got, want := string(read(t, hooks)), string(read(t, filepath.Join(claudeHome, ".claude", "settings.json"))); got != want {
		t.Fatalf("hooks.json:\n%s\nwant the Claude hook entries:\n%s", got, want)
	}
	if got := string(read(t, filepath.Join(home, ".codex", "AGENTS.md"))); !strings.Contains(got, codexFallback) {
		t.Fatalf("AGENTS.md:\n%s", got)
	}
	if code, out := runRaw(o); code != 0 || out != "already installed\n" {
		t.Fatalf("second install = %d, %q (no approval line when nothing changed)", code, out)
	}
	o.Uninstall = true
	if code, out := runRaw(o); code != 0 || strings.Contains(out, "approve") {
		t.Fatalf("uninstall = %d, %q", code, out)
	}
	if _, err := os.Stat(hooks); err == nil {
		t.Fatal("hooks.json, which mairc created, is still there after uninstall")
	}
	project := t.TempDir()
	runRaw(Options{Binary: testBinary, Scope: "project", Home: t.TempDir(), Project: project, Only: []string{"codex"}})
	if _, err := os.Stat(filepath.Join(project, ".codex", "hooks.json")); err != nil {
		t.Fatalf("project scope hooks.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, ".codex", "AGENTS.md")); err == nil {
		t.Fatal("project scope wrote an AGENTS.md")
	}
}

// A machine with Grok or Devin and no Claude Code still gets delivery: each has mairc's hook in a file of its own,
// and does not depend on the one in Claude Code's settings. Before, these two rows wrote the MCP server only.
func TestGrokAndDevinGetTheirOwnHookWithoutClaudeCode(t *testing.T) {
	// hookedEvents returns the events on which the file runs mairc's hook, under the object at keyPath.
	hookedEvents := func(t *testing.T, path string, keyPath []string) []string {
		t.Helper()
		root, err := parse(read(t, path))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		hooks := walkKeys(root, keyPath)
		var events []string
		for i, event := range hooks.keys {
			for _, group := range hooks.items[i].items {
				for _, handler := range group.get("hooks").items {
					if isOurHook(handler) {
						events = append(events, event)
					}
				}
			}
		}
		return events
	}

	home := t.TempDir()
	o := Options{Binary: testBinary, Home: home, Only: []string{"grok", "devin"}}
	code, out := runRaw(o)
	if code != 0 || strings.Count(out, "grok: hooks run only in folders Grok trusts") != 1 {
		t.Fatalf("install = %d, %q", code, out)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); err == nil {
		t.Fatal("installing Grok and Devin wrote Claude Code files")
	}
	grokHooks := filepath.Join(home, ".grok", "hooks", "mairc.json")
	if got := hookedEvents(t, grokHooks, []string{"hooks"}); len(got) != 1 || got[0] != "PostToolUse" {
		t.Fatalf("Grok's hook file runs mairc's hook on %v, want PostToolUse only", got)
	}
	devinConfig := filepath.Join(home, ".config", "devin", "config.json")
	if got := hookedEvents(t, devinConfig, []string{"hooks"}); strings.Join(got, ",") != strings.Join(hookEvents(), ",") {
		t.Fatalf("Devin's config runs mairc's hook on %v, want %v", got, hookEvents())
	}
	for _, s := range Statuses(o) {
		if !s.HookPresent {
			t.Fatalf("%s: doctor sees no hook: %+v", s.Name, s)
		}
	}
	if code, out := runRaw(o); code != 0 || out != "already installed\n" {
		t.Fatalf("second install = %d, %q", code, out)
	}
	o.Uninstall = true
	if code, out := runRaw(o); code != 0 {
		t.Fatalf("uninstall = %d, %q", code, out)
	}
	for _, path := range []string{grokHooks, filepath.Dir(grokHooks), devinConfig} {
		if _, err := os.Stat(path); err == nil {
			t.Fatalf("%s, which mairc created, is still there after uninstall", path)
		}
	}

	// Project scope: Devin's hooks file is the hooks object itself.
	project := t.TempDir()
	runRaw(Options{Binary: testBinary, Scope: "project", Home: t.TempDir(), Project: project, Only: []string{"grok", "devin"}})
	if got := hookedEvents(t, filepath.Join(project, ".devin", "hooks.v1.json"), nil); len(got) != len(hookEvents()) {
		t.Fatalf("project .devin/hooks.v1.json runs mairc's hook on %v", got)
	}
	if got := hookedEvents(t, filepath.Join(project, ".grok", "hooks", "mairc.json"), []string{"hooks"}); len(got) != 1 {
		t.Fatalf("project .grok/hooks/mairc.json runs mairc's hook on %v", got)
	}
}

// A config that is a symlink (dotfile managers; Devin's global AGENTS.md links to Codex's on some machines) is changed
// where it lives: the link stays a link, the target gains mairc's entry, the backup is the target's.
func TestSymlinkedConfigStaysALink(t *testing.T) {
	home, store := t.TempDir(), t.TempDir()
	target := filepath.Join(store, "claude.json")
	write(t, target, "{\n  \"keep\": 1\n}\n", 0o600)
	link := filepath.Join(home, ".claude.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if code, out := runRaw(Options{Binary: testBinary, Home: home, Only: []string{"claude"}}); code != 0 {
		t.Fatalf("install = %d, %q", code, out)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced: %v %v", info, err)
	}
	if got := string(read(t, target)); !strings.Contains(got, jbin()) || !strings.HasPrefix(got, "{\n  \"keep\": 1,\n") {
		t.Fatalf("target:\n%s", got)
	}
	if got := string(read(t, backupFile(backupRoot(Options{Home: home}), link))); got != "{\n  \"keep\": 1\n}\n" {
		t.Fatalf("backup of the target = %q", got)
	}
}

// Pi spawns no MCP server the user has not acknowledged (/mcp trust): install names that step, which it cannot take.
func TestPiInstallNamesTheTrustStep(t *testing.T) {
	home := t.TempDir()
	code, out := runRaw(Options{Binary: testBinary, Home: home, Only: []string{"pi"}})
	if code != 0 || !strings.Contains(out, "pi: approve the mairc MCP server once: start pi and run /mcp trust mairc\n") {
		t.Fatalf("install --only pi = %d:\n%s", code, out)
	}
}
