package doctor

// LiveCommand returns the argument list that runs a CLI once, headless, with the prompt in its place. ok=false for a
// CLI with no recorded command: the doctor then says "not checked" rather than guess at flags that spend the
// user's tokens.
//
// A live run has to let the model call one MCP tool without a person approving it, so each command carries the
// flag its CLI needs for that. The doctor runs it in an empty temporary folder, so that even a CLI told to approve
// everything has nothing of the user's in front of it.
//
// Where each command comes from. "run" means it was run for real against a mairc daemon and the tool call arrived
// (17 Nov 2026, the versions named); the others are from docs/compat.md and not yet run by the doctor:
//   - claude (2.1.326, run): print mode, the cheapest model, and only mairc's tools allowed.
//   - codex (0.169.0, run): `codex exec` outside a git repository needs --skip-git-repo-check, and an MCP tool call
//     needs approvals off.
//   - grok (1.0.88, run): the prompt follows -p; --always-approve lets the tool call through.
//   - devin (3000.13.4, run): -p with a permission mode that asks nothing, and workspace trust off for the
//     empty folder.
//   - opencode (2.3.0, run): `opencode run`.
//   - prime-agent (0.10.0, run with an explicit provider and model): -p with the user's own default model.
func LiveCommand(cli, prompt string) ([]string, bool) {
	switch cli {
	case "claude":
		// The prompt follows -p: --allowedTools takes a list and would take a prompt placed after it as a tool.
		return []string{"claude", "-p", prompt, "--model", "haiku", "--allowedTools", "mcp__mairc"}, true
	case "codex":
		return []string{
			"codex", "exec", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", prompt,
		}, true
	case "grok":
		return []string{"grok", "-p", prompt, "--always-approve"}, true
	case "devin":
		// Devin refuses a folder it does not trust; the run is in a fresh empty one.
		return []string{
			"devin", "--respect-workspace-trust", "false", "--permission-mode", "dangerous", "-p", prompt,
		}, true
	case "opencode":
		return []string{"opencode", "run", prompt}, true
	case "prime-agent":
		return []string{"prime-agent", "-p", prompt}, true
	}
	return nil, false
}
