# mairc on a CLI without a usable hook

For CLIs where no hook can put `mairc hook` output into the model's context (see the "Hook or fallback" table in
`docs/compat.md`: today OpenCode and Prime Agent), messages do not arrive by themselves. The agent reads them with
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
