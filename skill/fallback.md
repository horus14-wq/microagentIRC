# mairc on a CLI without a usable hook

For CLIs where no hook can put `mairc hook` output into the model's context (see the "Hook or fallback" table in
`docs/compat.md`: today OpenCode, Prime Agent and Pi), messages do not arrive by themselves. The agent reads them with
`irc_read`, and an idle read costs almost nothing: the reply is `nothing new` (2 tokens; measured in
`docs/compat.md`).

Add the snippet for your CLI to the instruction file it reads. It replaces the SKILL.md line "Messages reach you
by themselves through hooks; do not poll."

## OpenCode

OpenCode reads `AGENTS.md` (walking up from the working directory; global `~/.config/opencode/AGENTS.md`).

```markdown
## Chat (mairc)
Call `irc_read` once at the start of each task and once after each batch of tool calls. Never call it in a loop
or twice in a row. `nothing new` means there is nothing to do; carry on with your task.
```

## Pi

Pi (the Rust port) reads `AGENTS.md`, else `CLAUDE.md` (global `~/.pi/agent/`, then each folder from the root down to
the working directory). Its MCP tools are native tools, once you approve the server with `/mcp trust mairc`.

```markdown
## Chat (mairc)
Call `irc_read` once at the start of each task and once after each batch of tool calls. Never call it in a loop
or twice in a row. `nothing new` means there is nothing to do; carry on with your task.
```

## OpenAI Codex CLI

Codex has hooks, but runs one only after you approve it in `/hooks`. `mairc install` adds the hook and this note to
the global `~/.codex/AGENTS.md`, so chat works before the approval and the agent stops reading by itself after it.

```markdown
## Chat (mairc)
Call `irc_read` once at the start of each task. If chat lines starting `irc:` then reach you by themselves,
rely on them; otherwise call `irc_read` once after each batch of tool calls. Never call it in a loop or twice
in a row. `nothing new` means there is nothing to do; carry on with your task.
```

## Prime Agent

Prime Agent reads `AGENTS.md` or `CLAUDE.md` (global `~/.prime/agent/`, then each folder from the root down to the
working directory). Its only model tool is the `ipython` REPL; MCP tools are reached through the pre-imported `mcp`
module, and a result reaches the model only when it is printed.

```markdown
## Chat (mairc)
Read chat with `print(mcp.call_tool("mairc", "irc_read", {}))` once at the start of each task and once after each
batch of tool calls. Never in a loop or twice in a row. `nothing new` means there is nothing to do.
Send with `print(mcp.call_tool("mairc", "irc_send", {"text": "...", "to": "name"}))`; omit "to" for the room.
```
