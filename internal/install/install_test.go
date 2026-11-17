package install

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testBinary = "/opt/mairc/bin/mairc"

func golden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "install", name)) // #nosec G304 -- fixed test vector
	if err != nil {
		t.Fatal(err)
	}
	return nativeGolden(b)
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- a file inside the test's temporary directory
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// run runs Claude Code's row, as `mairc install --only claude`: these tests must not depend on which CLIs the host
// running them has installed (mairc-x2y made detection the default).
func run(o Options) (int, string) {
	var out bytes.Buffer
	o.Binary = nativeTestBinary(o.Binary)
	o.Out = &out
	if len(o.Only) == 0 && !o.Print {
		o.Only = []string{"claude"}
	}
	return Run(o), out.String()
}

type scopeCase struct {
	scope            string
	mcpGold, setGold string
	opts             func(home, project string) Options
	mcpPath, setPath func(home, project string) string
}

func scopes() []scopeCase {
	return []scopeCase{
		{
			scope: "user", mcpGold: "user-claude.json", setGold: "user-settings.json",
			opts:    func(home, _ string) Options { return Options{Binary: testBinary, Home: home} },
			mcpPath: func(home, _ string) string { return filepath.Join(home, ".claude.json") },
			setPath: func(home, _ string) string { return filepath.Join(home, ".claude", "settings.json") },
		},
		{
			scope: "project", mcpGold: "project-mcp.json", setGold: "project-settings.json",
			opts: func(home, project string) Options {
				return Options{Binary: testBinary, Scope: "project", Home: home, Project: project}
			},
			mcpPath: func(_, project string) string { return filepath.Join(project, ".mcp.json") },
			setPath: func(_, project string) string { return filepath.Join(project, ".claude", "settings.json") },
		},
	}
}

func TestFreshInstallMatchesGoldenFilesForBothScopes(t *testing.T) {
	for _, sc := range scopes() {
		t.Run(sc.scope, func(t *testing.T) {
			home, project := t.TempDir(), t.TempDir()
			code, out := run(sc.opts(home, project))
			mcp, set := sc.mcpPath(home, project), sc.setPath(home, project)
			if code != 0 || out != "updated "+mcp+"\nupdated "+set+"\n" {
				t.Fatalf("Run = %d, out %q", code, out)
			}
			if got := read(t, mcp); !bytes.Equal(got, golden(t, sc.mcpGold)) {
				t.Fatalf("%s:\n%s", mcp, got)
			}
			if got := read(t, set); !bytes.Equal(got, golden(t, sc.setGold)) {
				t.Fatalf("%s:\n%s", set, got)
			}
			for _, p := range []string{mcp, set} {
				info, err := os.Stat(p)
				if err != nil || info.Mode().Perm() != nativeFileMode(0o600) {
					t.Fatalf("%s mode = %v, %v; want 0600", p, info.Mode().Perm(), err)
				}
				if _, err := os.Stat(p + ".mairc.bak"); err == nil {
					t.Fatalf("a created file needs no backup: %s.mairc.bak exists", p)
				}
			}
		})
	}
}

// Files with other servers, other hooks on the same events and other top-level keys keep all of it, in order;
// ours is added once; uninstall restores the original bytes.
const otherMCP = `{
  "theme": "dark",
  "mcpServers": {
    "other": {
      "command": "/usr/bin/other",
      "args": [
        "serve"
      ]
    }
  },
  "zeta": [
    1,
    2.50
  ]
}
`

const otherSettings = `{
  "model": "opus",
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Edit|Write",
        "hooks": [
          {
            "type": "command",
            "command": "/usr/local/bin/lint <files> & \"check\""
          }
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "notify-send done"
          }
        ]
      }
    ]
  },
  "permissions": {
    "allow": []
  }
}
`

func TestMergeKeepsOtherContentInOrderAndUninstallRestoresIt(t *testing.T) {
	home := t.TempDir()
	mcp, set := filepath.Join(home, ".claude.json"), filepath.Join(home, ".claude", "settings.json")
	write(t, mcp, otherMCP, 0o644)
	write(t, set, otherSettings, 0o600)

	if code, _ := run(Options{Binary: testBinary, Home: home}); code != 0 {
		t.Fatalf("install = %d", code)
	}
	gotMCP, gotSet := string(read(t, mcp)), string(read(t, set))
	order := func(s string, words ...string) bool {
		last := -1
		for _, w := range words {
			i := strings.Index(s, w)
			if i <= last {
				return false
			}
			last = i
		}
		return true
	}
	if !order(gotMCP, `"theme"`, `"mcpServers"`, `"other"`, `"mairc"`, `"zeta"`, `2.50`) {
		t.Fatalf("mcp order lost:\n%s", gotMCP)
	}
	hookWord, countWord := nativeHookWords()
	if !order(gotSet, `"model"`, `"hooks"`, `"PostToolUse"`, `"Edit|Write"`, `lint <files> & \"check\"`,
		hookWord, `"Stop"`, `notify-send`, `"SessionStart"`, `"UserPromptSubmit"`,
		`"permissions"`) {
		t.Fatalf("settings order lost:\n%s", gotSet)
	}
	if n := strings.Count(gotSet, countWord); n != 3 {
		t.Fatalf("our hook appears %d times, want 3 (one per event)", n)
	}
	if info, _ := os.Stat(mcp); info.Mode().Perm() != nativeFileMode(0o644) {
		t.Fatalf("mode not kept: %v", info.Mode().Perm())
	}

	code, out := run(Options{Binary: testBinary, Home: home, Uninstall: true})
	if code != 0 || out != "updated "+mcp+"\nupdated "+set+"\n" {
		t.Fatalf("uninstall = %d, out %q", code, out)
	}
	if got := string(read(t, mcp)); got != otherMCP {
		t.Fatalf("uninstall did not restore %s:\n%s", mcp, got)
	}
	if got := string(read(t, set)); got != otherSettings {
		t.Fatalf("uninstall did not restore %s:\n%s", set, got)
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	home := t.TempDir()
	run(Options{Binary: testBinary, Home: home})
	mcp, set := filepath.Join(home, ".claude.json"), filepath.Join(home, ".claude", "settings.json")
	before := [][]byte{read(t, mcp), read(t, set)}
	code, out := run(Options{Binary: testBinary, Home: home})
	if code != 0 || out != "already installed\n" {
		t.Fatalf("second install = %d, out %q", code, out)
	}
	if !bytes.Equal(before[0], read(t, mcp)) || !bytes.Equal(before[1], read(t, set)) {
		t.Fatal("the second install changed a file")
	}
}

func TestANewBinaryPathUpdatesOurEntriesInPlace(t *testing.T) {
	home := t.TempDir()
	run(Options{Binary: "/old/place/mairc", Home: home})
	code, out := run(Options{Binary: testBinary, Home: home})
	mcp, set := filepath.Join(home, ".claude.json"), filepath.Join(home, ".claude", "settings.json")
	if code != 0 || out != "updated "+mcp+"\nupdated "+set+"\n" {
		t.Fatalf("update = %d, out %q", code, out)
	}
	if !bytes.Equal(read(t, mcp), golden(t, "user-claude.json")) ||
		!bytes.Equal(read(t, set), golden(t, "user-settings.json")) {
		t.Fatalf("not updated in place:\n%s\n%s", read(t, mcp), read(t, set))
	}
}

func TestUninstallOfFilesWeCreatedLeavesEmptyObjects(t *testing.T) {
	home := t.TempDir()
	run(Options{Binary: testBinary, Home: home})
	run(Options{Binary: testBinary, Home: home, Uninstall: true})
	for _, p := range []string{filepath.Join(home, ".claude.json"), filepath.Join(home, ".claude", "settings.json")} {
		if got := string(read(t, p)); got != "{}\n" {
			t.Fatalf("%s = %q, want {}", p, got)
		}
	}
	// Uninstalling again, or with no files at all, changes nothing and creates nothing.
	empty := t.TempDir()
	if code, out := run(Options{Binary: testBinary, Home: empty, Uninstall: true}); code != 0 || out != "" {
		t.Fatalf("uninstall with no files = %d, %q", code, out)
	}
	if entries, _ := os.ReadDir(empty); len(entries) != 0 {
		t.Fatalf("uninstall created files: %v", entries)
	}
}

func TestBackupIsWrittenOnceAndNeverOverwritten(t *testing.T) {
	home := t.TempDir()
	mcp := filepath.Join(home, ".claude.json")
	write(t, mcp, otherMCP, 0o600)
	run(Options{Binary: "/first/mairc", Home: home})
	if got := string(read(t, mcp+".mairc.bak")); got != otherMCP {
		t.Fatalf("backup = %q, want the original", got)
	}
	run(Options{Binary: "/second/mairc", Home: home})
	run(Options{Binary: testBinary, Home: home, Uninstall: true})
	if got := string(read(t, mcp+".mairc.bak")); got != otherMCP {
		t.Fatalf("backup was overwritten: %q", got)
	}
}

func TestInvalidJSONIsReportedAndNothingIsTouched(t *testing.T) {
	for _, bad := range []string{"{ not json", "[1, 2]", "", `{"a": 1} trailing`} {
		home := t.TempDir()
		set := filepath.Join(home, ".claude", "settings.json")
		write(t, set, bad, 0o600)
		code, out := run(Options{Binary: testBinary, Home: home})
		if code != 1 || out != "cannot parse "+set+": fix it or use --print\n" {
			t.Fatalf("%q: Run = %d, out %q", bad, code, out)
		}
		if got := string(read(t, set)); got != bad {
			t.Fatalf("%q: the invalid file was changed to %q", bad, got)
		}
		if _, err := os.Stat(filepath.Join(home, ".claude.json")); err == nil {
			t.Fatalf("%q: the other file was written although one file could not be parsed", bad)
		}
	}
}

func TestPrintMatchesGoldenAndWritesNoFile(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	code, out := run(Options{Binary: testBinary, Home: home, Project: project, Print: true})
	if code != 0 || out != string(golden(t, "print.txt")) {
		t.Fatalf("Run = %d, out:\n%s", code, out)
	}
	for _, dir := range []string{home, project} {
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Fatalf("--print wrote files in %s: %v", dir, entries)
		}
	}
}

func TestUsageErrorsReturnTwo(t *testing.T) {
	home := t.TempDir()
	cases := []struct {
		name string
		o    Options
		want string
	}{
		{"relative binary", Options{Binary: "mairc", Home: home}, "usage: the mairc binary must be an absolute path\n"},
		{"unknown scope", Options{Binary: testBinary, Home: home, Scope: "system"}, "usage: scope must be user or project\n"},
		{
			"project scope without a project",
			Options{Binary: testBinary, Home: home, Scope: "project"},
			"usage: scope project needs a project directory\n",
		},
		{"user scope without a home", Options{Binary: testBinary}, "usage: the home directory is unknown\n"},
		{
			"print and uninstall",
			Options{Binary: testBinary, Home: home, Print: true, Uninstall: true},
			"usage: --print and --uninstall cannot be combined\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code, out := run(tc.o); code != 2 || out != tc.want {
				t.Fatalf("Run = %d, out %q", code, out)
			}
		})
	}
}
