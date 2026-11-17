package install

import (
	"bytes"
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
	o.Out = &out
	return Run(o), out.String()
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
		`+       "command": "` + testBinary + `",` + "\n",
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
	const fixture = "{\n  \"keep\": {\n    \"a\": 1\n  }\n}\n"
	for _, c := range clis() {
		for _, scope := range []string{"user", "project"} {
			t.Run(c.name+"/"+scope, func(t *testing.T) {
				home, project := t.TempDir(), t.TempDir()
				o := Options{Binary: testBinary, Scope: scope, Home: home, Project: project, Only: []string{c.name}}
				files := c.files(scope, o)
				for _, f := range files {
					write(t, f.path, fixture, 0o600)
				}
				if code, out := runRaw(o); code != 0 || strings.Count(out, "updated ") != len(files) {
					t.Fatalf("install = %d, out %q", code, out)
				}
				for _, f := range files {
					got := string(read(t, f.path))
					if !strings.HasPrefix(got, "{\n  \"keep\": {\n    \"a\": 1\n  },\n") || !strings.Contains(got, testBinary) {
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
					if got := string(read(t, f.path)); got != fixture {
						t.Fatalf("uninstall did not restore %s:\n%s", f.path, got)
					}
				}
			})
		}
	}
}

// The MCP entry each new row writes into a fresh file, exactly.
func TestNewRowsWriteTheirDocumentedEntry(t *testing.T) {
	stdio := "{\n  \"mcpServers\": {\n    \"mairc\": {\n      \"command\": \"" + testBinary + "\",\n      \"args\": [\n        \"stdio\"\n      ]\n    }\n  }\n}\n"
	typed := "{\n  \"mcpServers\": {\n    \"mairc\": {\n      \"type\": \"stdio\",\n      \"command\": \"" + testBinary + "\",\n      \"args\": [\n        \"stdio\"\n      ]\n    }\n  }\n}\n"
	cases := []struct {
		cli, goos, rel, want string
	}{
		{"devin", "linux", filepath.Join(".config", "devin", "mcp_config.json"), stdio},
		{"devin", "windows", filepath.Join("AppData", "Roaming", "devin", "mcp_config.json"), stdio},
		{"prime-agent", "linux", filepath.Join(".prime", "agent", "settings.json"), typed},
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
