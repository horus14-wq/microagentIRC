package doctor

// LiveCommand returns the argument list that runs a CLI once, headless, with a prompt appended as the last argument.
// Only the CLIs whose headless invocation is recorded in docs/compat.md have one; the rest report ok=false and the
// doctor says so rather than guessing at flags that spend the user's tokens.
//
// Each entry names the documentation line it comes from (docs/compat.md, read 2026-11-17):
//   - claude: "claude -p --model haiku --setting-sources project" (print mode, cheapest model).
//   - codex: "codex exec" (its non-interactive subcommand).
//   - opencode: "opencode run" (its non-interactive subcommand).
func LiveCommand(cli string) ([]string, bool) {
	switch cli {
	case "claude":
		return []string{"claude", "-p", "--model", "haiku"}, true
	case "codex":
		return []string{"codex", "exec"}, true
	case "opencode":
		return []string{"opencode", "run"}, true
	}
	return nil, false
}
