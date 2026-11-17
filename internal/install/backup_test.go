package install

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// tree lists every file and directory under root (relative, slash-separated), with "/" after directories.
func tree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == root {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			rel += "/"
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// mairc-nlr (2): install keeps one backup per changed file in one mairc-owned directory (never beside the config),
// and uninstall leaves nothing behind: no backups, no stub files mairc created, no directories it made for them.
func TestUninstallLeavesNoBackupsAndNoStubs(t *testing.T) {
	home := t.TempDir()
	mcp := filepath.Join(home, ".claude.json")
	write(t, mcp, otherMCP, 0o600)
	for _, dir := range []string{".grok", filepath.Join(".gemini", "config"), filepath.Join(".config", "opencode")} {
		write(t, filepath.Join(home, dir, "keep.txt"), "mine\n", 0o600)
	}
	before := tree(t, home)
	o := Options{Binary: nativeTestBinary(testBinary), Home: home, Only: []string{"claude", "grok", "agy", "opencode"}}
	if code, out := runRaw(o); code != 0 {
		t.Fatalf("install = %d, %q", code, out)
	}
	root := filepath.Join(home, ".local", "share", "mairc", "install-backup")
	for _, p := range tree(t, home) {
		if strings.HasSuffix(p, ".mairc.bak") {
			t.Fatalf("a backup beside a config: %s", p)
		}
	}
	if got := string(read(t, backupFile(root, mcp))); got != otherMCP {
		t.Fatalf("backup of %s = %q", mcp, got)
	}
	o.Uninstall = true
	code, out := runRaw(o)
	if code != 0 {
		t.Fatalf("uninstall = %d, %q", code, out)
	}
	for _, want := range []string{
		"updated " + mcp + "\n",
		"removed " + filepath.Join(home, ".claude", "settings.json") + "\n",
		"removed " + filepath.Join(home, ".grok", "config.toml") + "\n",
		"removed " + filepath.Join(home, ".gemini", "config", "mcp_config.json") + "\n",
		"removed " + filepath.Join(home, ".config", "opencode", "AGENTS.md") + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("uninstall output lacks %q:\n%s", want, out)
		}
	}
	if after := tree(t, home); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Fatalf("uninstall left:\n%s\nwant:\n%s", strings.Join(after, "\n"), strings.Join(before, "\n"))
	}
	if got := string(read(t, mcp)); got != otherMCP {
		t.Fatalf("%s after uninstall:\n%s", mcp, got)
	}
}

// A file mairc created and the user then added to is kept by uninstall, with only mairc's entry removed.
func TestUninstallKeepsACreatedFileTheUserFilledIn(t *testing.T) {
	home := t.TempDir()
	o := Options{Binary: nativeTestBinary(testBinary), Home: home, Only: []string{"agy"}}
	runRaw(o)
	path := filepath.Join(home, ".gemini", "config", "mcp_config.json")
	root, err := parse(read(t, path))
	if err != nil {
		t.Fatal(err)
	}
	root.get("mcpServers").set("mine", object("command", str("/bin/mine")))
	write(t, path, string(root.render()), 0o600)
	o.Uninstall = true
	if code, out := runRaw(o); code != 0 || out != "updated "+path+"\n" {
		t.Fatalf("uninstall = %d, %q", code, out)
	}
	want := "{\n  \"mcpServers\": {\n    \"mine\": {\n      \"command\": \"/bin/mine\"\n    }\n  }\n}\n"
	if got := string(read(t, path)); got != want {
		t.Fatalf("%s:\n%s", path, got)
	}
	if entries := tree(t, home); strings.Contains(strings.Join(entries, "\n"), "mairc/") {
		t.Fatalf("backup directory left: %v", entries)
	}
}
