package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeFile puts content at path, creating its directory.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// statusFor returns the status of one CLI by name, or fails.
func statusFor(t *testing.T, o Options, name string) Status {
	t.Helper()
	for _, s := range Statuses(o) {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no status for %s in %+v", name, Statuses(o))
	return Status{}
}

func TestStatusesReportsWhatInstallWrote(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	bin := filepath.Join(home, "bin", "mairc")
	writeFile(t, bin, "#!/bin/sh\n")
	// Claude Code user scope: the MCP entry and the hook mairc writes.
	writeFile(t, filepath.Join(home, ".claude.json"), `{"mcpServers":{"mairc":{"command":"`+bin+`","args":["stdio"]}}}`)
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"`+
		bin+` hook --client claude","timeout":5}]}]}}`)
	o := Options{Binary: bin, Scope: "user", Home: home, Project: project, GOOS: "linux"}
	claude := statusFor(t, o, "claude")
	switch {
	case !claude.MCPPresent || claude.MCPCommand != bin:
		t.Errorf("claude MCP = %v %q, want present with %q", claude.MCPPresent, claude.MCPCommand, bin)
	case !claude.HookPresent || claude.HookCommand != bin+" hook --client claude":
		t.Errorf("claude hook = %v %q", claude.HookPresent, claude.HookCommand)
	case claude.MCPPath != filepath.Join(home, ".claude.json"):
		t.Errorf("claude MCP path = %q", claude.MCPPath)
	}
}

func TestStatusesSeesAnUnconfiguredCLI(t *testing.T) {
	home := t.TempDir()
	// Codex's config directory exists (so it is detected) and holds no mairc block.
	writeFile(t, filepath.Join(home, ".codex", "config.toml"), "model = \"gpt-5\"\n")
	o := Options{Scope: "user", Home: home}
	codex := statusFor(t, o, "codex")
	if codex.Detected != true || codex.MCPPresent {
		t.Fatalf("codex status = %+v, want detected and not configured", codex)
	}
	if codex.MCPPath != filepath.Join(home, ".codex", "config.toml") {
		t.Errorf("codex MCP path = %q", codex.MCPPath)
	}
}

func TestStatusesReadsTheTOMLBlockCommand(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "mairc")
	begin, end := tomlMarkers()
	writeFile(t, filepath.Join(home, ".codex", "config.toml"),
		"model = \"x\"\n\n"+begin+"\n[mcp_servers.mairc]\ncommand = \""+bin+"\"\nargs = [\"stdio\"]\n"+end+"\n")
	codex := statusFor(t, Options{Scope: "user", Home: home}, "codex")
	if !codex.MCPPresent || codex.MCPCommand != bin {
		t.Fatalf("codex = %v %q, want present with %q", codex.MCPPresent, codex.MCPCommand, bin)
	}
}

func TestStatusesSeesTheFallbackInstructionFile(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".prime", "agent", "AGENTS.md"),
		"# mine\n\n<!-- mairc: begin (managed by `mairc install`; edits inside are replaced) -->\n## Chat (mairc)\n<!-- mairc: end -->\n")
	prime := statusFor(t, Options{Scope: "user", Home: home}, "prime-agent")
	if !prime.FallbackPresent || prime.FallbackPath != filepath.Join(home, ".prime", "agent", "AGENTS.md") {
		t.Fatalf("prime-agent fallback = %v %q", prime.FallbackPresent, prime.FallbackPath)
	}
	if prime.HookPath != "" {
		t.Errorf("prime-agent has a hook path %q, but it has no hook", prime.HookPath)
	}
}

func TestStatusesReportsAnUnparsableConfig(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "opencode", "opencode.json"), "{ not json")
	s := statusFor(t, Options{Scope: "user", Home: home}, "opencode")
	if s.Problem == "" || s.MCPPresent {
		t.Fatalf("opencode = %+v, want a problem and no entry", s)
	}
}

func TestStatusesHonoursOnlyAndLeavesUndetectedCLIsOut(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".codex", "config.toml"), "model = \"x\"\n")
	only := Statuses(Options{Scope: "user", Home: home, Only: []string{"codex"}})
	if len(only) != 1 || only[0].Name != "codex" {
		t.Fatalf("--only codex = %+v", only)
	}
	// Every detected CLI is reported, and nothing that neither exists nor is configured.
	for _, s := range Statuses(Options{Scope: "user", Home: home}) {
		if !s.Detected && !s.MCPPresent && !s.HookPresent && !s.FallbackPresent {
			t.Errorf("%s reported although it is neither detected nor configured", s.Name)
		}
	}
}

func TestStatusesFindsAProjectScopeFile(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	bin := filepath.Join(home, "mairc")
	writeFile(t, filepath.Join(project, ".mcp.json"), `{"mcpServers":{"mairc":{"command":"`+bin+`","args":["stdio"]}}}`)
	s := Statuses(Options{Scope: "project", Home: home, Project: project, Only: []string{"claude"}})[0]
	if s.MCPPath != filepath.Join(project, ".mcp.json") || !s.MCPPresent {
		t.Fatalf("project claude = %+v", s)
	}
}

func TestStatusesReadsOpenCodeCommandArray(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "mairc")
	body, err := json.Marshal(map[string]any{"mcp": map[string]any{
		"mairc": map[string]any{"type": "local", "command": []string{bin, "stdio"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, ".config", "opencode", "opencode.json"), string(body))
	s := statusFor(t, Options{Scope: "user", Home: home}, "opencode")
	if !s.MCPPresent || s.MCPCommand != bin {
		t.Fatalf("opencode = %v %q, want present with %q", s.MCPPresent, s.MCPCommand, bin)
	}
}
