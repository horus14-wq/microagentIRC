# Compatibility with five more coding CLIs — **UNTESTED**

Would `mairc` work with Pi, Goose, Cursor CLI, GitHub Copilot CLI and Amp? This file records what each CLI's own
documentation says, and nothing else. **Every row below is UNTESTED**: no CLI was installed for mairc, no MCP server
was registered with one, no hook was run, and no model call was made. The tested CLIs (Claude Code, Codex, Gemini,
Grok, Devin) are in [compat.md](compat.md); this file is separate so that one is not disturbed.

Documentation read **2026-11-17**; each section carries the URLs it rests on. These tools change fast: re-read before
relying on a cell.

Host check, 2026-11-17 (the one thing here that was run):

| Command | Result |
|---|---|
| `command -v pi` | `~/.local/bin/pi`, `pi --version` = **0.8.0** (built 2026-10-13). So Pi *is* installed on this host; `pi --help` was read for its MCP config precedence. Nothing else about Pi was exercised. |
| `command -v goose` | `~/go/bin/goose`, `goose --version` = **v3.30.1**, `goose DRIVER DBSTRING [OPTIONS] COMMAND` — the database-migration tool, **not** Block's goose agent. |
| `command -v cursor-agent copilot amp` | nothing. |

## What mairc needs from a host CLI

The same four things [compat.md](compat.md) records, so the rows below can be judged against them:

1. **An MCP client** over stdio (`mairc stdio`) or Streamable HTTP (the daemon at `127.0.0.1:4747/mcp`, `Authorization:
   Bearer <token>` from `$MAIRC_HOME/token`).
2. **A hook** that runs a command with JSON on stdin and whose text reaches the model — that is what `mairc hook`
   prints (plain text, or Claude Code's `hookSpecificOutput.additionalContext` JSON). Zero output when nothing is new.
3. **A seat**: mairc maps the hook's ancestor PID chain to the CLI process it is running beside, plus `$MAIRC_NAME` /
   `--name`. A hook that does not run as a child of the CLI's process cannot be placed this way.
4. **Instruction files**, for the "call `irc_name` to join" convention and the room etiquette.

## Summary

| CLI | MCP config (path, key) | stdio | Streamable HTTP + headers | Hook mechanism + events | Hook stdout reaches model? | Seat id from a hook | Instruction file | Verdict |
|---|---|---|---|---|---|---|---|---|
| **Pi** 0.8.0 [DOC][BIN] | `~/.pi/agent/mcp.json` (user), `.pi/mcp.json` (project, after trust), `.agents/mcp.json`, `--mcp-config`; key `mcpServers` | yes: `command`/`args`/`env`/`cwd` | yes: `url`/`headers`/`oauth`; `type` optional (`stdio`, `http`, `streamable-http`), **`sse` rejected** | none: no command hooks; TypeScript **extensions** subscribe to lifecycle events (`session_start`, `input`, tool events) and can `pi.exec()` | not through a command hook; an extension can inject (`pi.sendMessage`, `pi.appendEntry`) | extension runs **inside** the Pi process, so the PID chain is exact | `AGENTS.md` and `CLAUDE.md` discovery (`--no-context-files` turns it off) | MCP **should work** (`mairc install --cli pi` writing `mcp.json`); hook delivery needs a small extension → **no-hook fallback** until one exists |
| **Goose** (Block) | `~/.config/goose/config.yaml`, key `extensions.<name>` | yes: `type: stdio`, `cmd`/`args`/`envs` | yes: `type: streamable_http` + `uri` (+ `headers`, OAuth `client_id`) | hooks per the Open Plugins spec: `~/.agents/plugins/<p>/hooks/hooks.json` or `<project>/.agents/plugins/…`; events `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, … , matcher is a regex | **no**: only `PreToolUse`/`Stop` decisions are read; every other event is observation-only and stdout is a decision channel | stdin JSON `session_id`, `event`, `message`, `cwd`; goose runs the hook with `sh -c` (child of goose, so a PID chain exists) | `.goosehints` (global `~/.config/goose/.goosehints` + per-directory), and `AGENTS.md` read by default | MCP **should work**; hook delivery **not feasible** → **no-hook fallback** |
| **Cursor CLI** (`agent`) | shares the editor config: `~/.cursor/mcp.json` (user), `<project>/.cursor/mcp.json`; key `mcpServers` | yes: `command`/`args`/`env` | yes: `url` + `headers` (SSE also) | `hooks.json` at `~/.cursor/hooks.json` and `<project>/.cursor/hooks.json` (also enterprise/team/plugin); events `sessionStart`, `beforeSubmitPrompt`, `afterAgentResponse`, `preToolUse`, `postToolUse`, …; JSON in and out; it can also load **Claude Code hooks** | likely yes: responses can carry `user_message` / `agent_message`, which Cursor concatenates across sources [DOC] — the exact delivery per event is not stated in one place [UNV] | stdin JSON carries a conversation id and `cwd`; the hook runs as a child process of the CLI | `AGENTS.md` and `.cursor/rules` | MCP **should work** (`mairc install --cli cursor` writing `mcp.json`); hook plausible with a Cursor-shaped output formatter → **should work, unverified** |
| **GitHub Copilot CLI** (`copilot`) | `~/.copilot/mcp-config.json` (or `$COPILOT_HOME/mcp-config.json`), `copilot mcp add …`; key `mcpServers` | yes: `command`/`args`/`env` | yes: `--transport http`/`sse`, `url` + `headers` | `~/.copilot/hooks/<any>.json` (or `$COPILOT_HOME/hooks/`), repo `.github/hooks/`; events `sessionStart`, `userPromptSubmitted`, `postToolUse`, `agentStop`, `notification`, … | partly: `sessionStart` **does** inject `additionalContext` into the model; `userPromptSubmitted` output from a config file is **dropped**; `postToolUse` can inject | VS Code-compatible payload carries `hook_event_name` + `session_id`; camelCase form carries `sessionId`; child process of the CLI | `AGENTS.md`, `CLAUDE.md`, `GEMINI.md`, `.github/copilot-instructions.md`, `*.instructions.md` | MCP **should work**; session-start delivery works, per-prompt delivery does not → **partial, unverified** |
| **Amp** (`amp`) | `<user settings>` `~/.config/amp/settings.json` key `amp.mcpServers` (`.amp/settings.json` workspace overrides), or `mcp.json` inside a skill; `amp mcp add` | yes: `command`/`args`/`env` | yes: `url` + `headers` (SSE and HTTP; OAuth automatic) | none: TypeScript/JS **plugins** with events (`session.start`, `agent.start`, `tool.call`, `tool.result`, `agent.end`), from `.amp/plugins/` or `~/.config/amp/plugins/` | not from a command; a plugin can `amp.registerTool`, call tools and show UI — no documented way to inject text into the model's context | plugin runs inside the Amp process; `event.thread.id` names the thread | `AGENTS.md` | MCP **should work**; hook delivery **not feasible without a plugin** → **no-hook fallback** |

Legend as in [compat.md](compat.md): **[DOC]** official documentation, **[BIN]** the installed binary (only Pi here),
**[UNV]** unverified, with the reason. Everything in this file is UNTESTED in the mairc sense: no mairc code was run
against any of these CLIs.

## Pi — superseded: the Rust port is supported, see compat.md "Pi (Rust port)"; this section described the TypeScript Pi, which is not supported

Sources (read 2026-11-17): <https://docs.pi.dev/latest/mcp> (MCP Servers), <https://docs.pi.dev/latest/extensions>
(Extensions), <https://docs.pi.dev/latest/cli> (CLI reference), <https://pi.dev/docs> (the docs index;
`pi.dev/latest/...` redirects to `docs.pi.dev/latest/...`). Binary: `pi 0.8.0`, `pi --help` on this host.

(a) **MCP configuration.** `~/.pi/agent/mcp.json` for user servers, `.pi/mcp.json` for project servers (read only
after project trust), `.agents/mcp.json` is also discovered, and `--mcp-config <path>` overrides all of them (the
installed binary lists the precedence in that order). Format is the usual `{"mcpServers": {...}}`; stdio servers use
`command`, `args`, `env`, `cwd`; HTTP servers use `url`, `headers`, `oauth`. Pi says plainly: "Pi connects to Model
Context Protocol servers over stdio or streamable HTTP", and "The legacy SSE transport is not supported." An entry
with a `command` is stdio and one with a `url` is streamable HTTP; `type` is optional and `sse` is rejected. `pi mcp
add`, `pi mcp list`, `pi mcp remove` manage the same files. Tools are exposed as `mcp__<server>__<tool>`, with
`exposure`/`toolExposure` controlling what reaches the model, and a `description` that is also what tool search ranks.

(b) **Hooks and whether their output reaches the model.** Pi has **no command hooks**: nothing in its settings runs an
external program per prompt or per tool the way Claude Code's `hooks` do. What it has is **extensions** — TypeScript
modules loaded from `~/.pi/agent/extensions/` or `.pi/extensions/` (or `-e`), which subscribe to lifecycle events
(`session_start`, `input`, `agent_start`, tool events, and so on), can register tools and commands, and can run
processes with `pi.exec(command, args)`. So `mairc hook`'s output could only reach the model through a small Pi
extension that runs the hook and appends a message (`pi.sendMessage`, `pi.appendEntry`); nothing in Pi consumes the
plain `irc:` text on its own. Until such an extension exists, Pi works in mairc's **no-hook fallback**: the agent calls
the `irc_*` MCP tools when it decides to.

(c) **How a hook would identify its seat.** Trivially, and better than most: an extension is not a separate process,
it runs inside Pi, so `process.pid` is Pi's pid and mairc's ancestor-PID seat mapping is exact. A CLI-side extension
would pass `process.pid` (and `$MAIRC_NAME` or a configured name) to the daemon's `/v1/hook`.

(d) **Instruction files.** `AGENTS.md` and `CLAUDE.md` are discovered as context files (`--no-context-files` disables
that); prompt templates, skills and packages are separate mechanisms.

(e) **Verdict.** `should work with mairc install --cli pi` for the MCP half, and Pi is the easiest of the five to wire
because the installed binary takes an extra config file by flag. Hook delivery needs an extension, so today it is
`works through the no-hook fallback`. Note the correction to the bead's premise: Pi 0.8.0 **does** speak MCP natively
over stdio and streamable HTTP; the "no MCP in the core, extensions only" reports the bead cites are older than this
binary. Nothing here was run.

## Goose (Block) — **UNTESTED**

Sources (read 2026-11-17): <https://goose-docs.ai/docs/getting-started/using-extensions/> (extensions and the
`config.yaml` entry shape), <https://goose-docs.ai/docs/guides/context-engineering/hooks> (hooks),
<https://goose-docs.ai/docs/guides/context-engineering/using-goosehints> (hints and `AGENTS.md`). The old
`block.github.io/goose` path answers 404 and points at `goose-docs.ai`.

(a) **MCP configuration.** `~/.config/goose/config.yaml`, key `extensions`, one entry per server: `type: stdio` with
`cmd`, `args`, `envs`, `timeout`, or `type: streamable_http` with `uri` (note: `uri`, not `url`), `headers`,
`timeout`, and optional OAuth `client_id`/`client_secret_key`/`scopes`. `goose configure` (interactive) and
`goose mcp <name>` are the CLI routes; there are also `goose://extension?...` deeplinks. stdio and
`streamable_http` are both supported, so both mairc transports fit; a Bearer token goes in `headers`.

(b) **Hooks and whether their output reaches the model.** goose implements the **Open Plugins hooks
specification**: a plugin directory with `hooks/hooks.json` under `~/.agents/plugins/<plugin>/` or
`<project>/.agents/plugins/<plugin>/`, commands run with `sh -c`, payload JSON on stdin (`event`, `session_id`,
`matcher_context`, `message` on `UserPromptSubmit`, `tool_name`/`tool_input` on tool events, `working_dir`), `matcher`
a **regex**, `timeout` in seconds, `${PLUGIN_ROOT}` available. Events: `SessionStart`, `SessionEnd`, `Stop`,
`UserPromptSubmit`, `PreToolUse`, `PreToolUseResult`, `PostToolUse`, `PostToolUseFailure`, `BeforeReadFile`,
`AfterFileEdit`, `BeforeShellExecution`, `AfterShellExecution`. **Output does not reach the model.** The docs are
explicit that most events are observation-only and that stdout is the **decision channel** for the two events that can
block (`PreToolUse`, `Stop`): exit 2 with a reason on stderr denies, `{"decision":"block"}` on stdout denies, exit 0
with empty stdout allows, and "a stray log line on stdout … read as no decision". So `mairc hook`'s `irc:` text would
be swallowed — worse, it would be *parsed* as a decision and discarded.

(c) **How a hook would identify its seat.** The payload carries goose's `session_id` and `event` (not
`hook_event_name`), so mairc would need `--event` per rule rather than reading the event from stdin; the hook runs as
a `sh -c` child of goose, so the ancestor-PID mapping still finds the CLI process, and `$MAIRC_NAME` (or a fixed name
per seat) would have to be set in the plugin's env.

(d) **Instruction files.** `.goosehints`: a global file at `~/.config/goose/.goosehints`, plus per-directory files
loaded hierarchically; `AGENTS.md` is read by default too, and `CONTEXT_FILE_NAMES` can add other names. Hints go into
the system prompt on every request (the Developer extension must be enabled).

(e) **Verdict.** MCP half: `should work with mairc install --cli goose` (write `~/.config/goose/config.yaml`
`extensions.mairc` with `type: stdio`, `cmd: <mairc>`, `args: [stdio]`). Hook half: `not feasible` — goose's hook
stdout is a decision channel, not context — so `works through the no-hook fallback`. Nothing here was run; the `goose`
on this host is the database-migration tool.

## Cursor CLI — **UNTESTED**

Sources (read 2026-11-17): <https://cursor.com/docs/mcp> (MCP and `mcp.json`), <https://cursor.com/docs/cli/mcp>
(CLI MCP commands), <https://cursor.com/docs/cli/headless> (print mode), <https://cursor.com/docs/hooks> (hooks; the
`/docs/agent/third-party-hooks` page resolves to the same content), <https://cursor.com/docs/rules> (rules and
`AGENTS.md`).

(a) **MCP configuration.** The CLI "uses the same configuration as the editor": `<project>/.cursor/mcp.json` for
project servers and `~/.cursor/mcp.json` for user servers, key `mcpServers`, with `command`/`args`/`env` for stdio,
`url` + `headers` (+ static OAuth `auth`) for Streamable HTTP, SSE also supported. `agent mcp list` (interactive) and
`agent mcp list-tools <id>` inspect; `agent mcp enable`/`disable` keep a local approved list; `agent --approve-mcps`
skips approval prompts. Headless runs (`agent -p`) reject anything that would need approval unless `--force` is
passed, which matters for MCP tools in scripts.

(b) **Hooks and whether their output reaches the model.** `hooks.json`, at `~/.cursor/hooks.json` (user),
`<project>/.cursor/hooks.json` (project, checked in), and enterprise/team/plugin levels with a documented precedence
order; the CLI runs the same agent loop, so the same hooks apply. Hooks are spawned processes speaking JSON both ways,
with events `sessionStart`, `sessionEnd`, `preToolUse`, `postToolUse`, `postToolUseFailure`, `beforeShellExecution`,
`afterShellExecution`, `beforeMCPExecution`, `afterMCPExecution`, `beforeReadFile`, `afterFileEdit`,
`beforeSubmitPrompt`, `preCompact`, `stop`, `afterAgentResponse`, `afterAgentThought`, plus Tab and workspace events.
Configuration is `{"version": 1, "hooks": {"<event>": [{"command": "…"}]}}`, run from the project root (project) or
`~/.cursor/` (user), reloaded automatically. Model visibility: the merge rules say `user_message` and `agent_message`
values are **concatenated** across sources, which is the JSON a hook returns for the conversation — i.e. text a hook
emits is meant to enter the chat, unlike goose's decision-only channel. [UNV] because the docs state the merge rule
without spelling out, event by event, which of the two fields is shown to the model versus to the user. A notable
detail: "Cursor supports loading hooks from third-party tools like Claude Code", and Cursor is the only one of these
five that documents reading another tool's hook format.

(c) **How a hook would identify its seat.** The hook is a child process of the agent, so mairc's ancestor-PID mapping
works. The payload carries a conversation/session id and `cwd`; there is no documented `hook_event_name` field, so a
Cursor formatter would pass `--event` from the rule, or lean on the Claude Code compatibility layer, which does send
`hook_event_name`.

(d) **Instruction files.** `AGENTS.md` and `.cursor/rules` are both documented as project context.

(e) **Verdict.** MCP half: `should work with mairc install --cli cursor` (write `.cursor/mcp.json` or
`~/.cursor/mcp.json` `mcpServers.mairc`; note Cursor wants the approval list or `--approve-mcps` in headless runs).
Hook half: plausible and the best of the five, but it needs a Cursor-shaped output formatter in `mairc hook`
(`{"user_message"|"agent_message": "irc:\n…"}`) instead of the plain text and the Claude JSON, and the exact
per-event delivery is unverified. Verdict: **should work, value unverified**. Nothing here was run, and Cursor CLI is
not installed on this host.

## GitHub Copilot CLI — **UNTESTED**

Sources (read 2026-11-17): <https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/add-mcp-servers>
(MCP servers and `copilot mcp add`), <https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/use-hooks>
(hook files), <https://docs.github.com/en/copilot/reference/hooks-reference> (payloads and what each event's output
can do), <https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/add-custom-instructions>
(instruction files).

(a) **MCP configuration.** `~/.copilot/mcp-config.json` (or `$COPILOT_HOME/mcp-config.json`), key `mcpServers`;
`copilot mcp add SERVER -- COMMAND [ARGS…]` for stdio, `--transport http|sse SERVER URL` for remote, with `--env`,
`--header "Header: value"`, `--tools`, `--timeout`. The interactive `/mcp` route edits the same file, and a new server
is available without a restart.

(b) **Hooks and whether their output reaches the model.** Hook files are JSON: user level `~/.copilot/hooks/*.json`
(or `$COPILOT_HOME/hooks/*.json`), repository level `.github/hooks/`. Shape `{"version": 1, "hooks": {"<event>":
[{"type": "command", "bash": "…", "powershell": "…", "cwd": "…", "env": {...}, "timeoutSec": 30}]}}`; HTTP hooks
(`type: "http"`, `url`, `headers`, `allowedEnvVars`) and prompt hooks (`sessionStart` only) also exist. Events:
`sessionStart`, `sessionEnd`, `userPromptSubmitted`, `userPromptTransformed`, `preToolUse`, `postToolUse`,
`postToolUseFailure`, `permissionRequest`, `preCompact`, `agentStop`, `errorOccurred`, `notification`. **Model
visibility is partial and documented precisely:** for `sessionStart`, "Only `additionalContext` is consumed" and
multiple hooks' contributions are joined with `\n\n` and injected into the session — so a session-start delivery
reaches the model; for `userPromptSubmitted`, a **command or HTTP config hook's output is dropped** (only SDK
programmatic hooks get `modifiedPrompt`) — so per-prompt delivery does not work from a config file; `postToolUse` can
inject `additionalContext` for the model. Hook stdout is bounded at 10 MiB.

(c) **How a hook would identify its seat.** Two payload formats: camelCase (`sessionId`, `prompt`, `cwd`) and the
VS Code-compatible PascalCase form that sends `hook_event_name`, `session_id`, … — the latter is exactly the shape
`mairc hook` already reads (`hook_event_name` from stdin). The hook is a child process of the CLI, so the ancestor-PID
seat mapping works; `$COPILOT_HOME` does not change the pid chain.

(d) **Instruction files.** `AGENTS.md`, `CLAUDE.md`, `GEMINI.md`, `.github/copilot-instructions.md`,
`$HOME/.copilot/copilot-instructions.md`, and modular `*.instructions.md` (with `applyTo`), discovered in the standard
locations; `COPILOT_CUSTOM_INSTRUCTIONS_DIRS` adds more.

(e) **Verdict.** MCP half: `should work with mairc install --cli copilot` (write `mcp-config.json`, or emit the
`copilot mcp add` command). Hook half: `sessionStart` delivery would work with an `additionalContext` formatter, and
`postToolUse` is a second route, but per-prompt delivery is not available to config hooks — so Copilot CLI would get
mairc's join message *once per session* and otherwise rely on the agent calling `irc_check`. Verdict: **partial,
unverified**. Nothing here was run.

## Amp — **UNTESTED**

Sources (read 2026-11-17): <https://ampcode.com/docs/customize/mcp> (MCP), <https://ampcode.com/docs/cli/settings>
(settings files), <https://ampcode.com/docs/customize/plugins> (plugins and events),
<https://ampcode.com/docs/customize/agents-md> (`AGENTS.md`).

(a) **MCP configuration.** Local configuration lives in settings under `amp.mcpServers`: user
`~/.config/amp/settings.json` (or `.jsonc`; `--settings-file` points elsewhere), workspace the nearest
`.amp/settings.json`/`.jsonc` searched upward from the cwd, workspace overriding user. Entries are
`command`/`args`/`env` for local servers and `url`/`headers` for remote ones, with `${VAR_NAME}` expansion; `amp mcp
add context7 -- npx -y @upstash/context7-mcp` and `amp mcp add linear https://mcp.linear.app/sse` are the CLI routes,
and `amp mcp remote` manages servers defined on ampcode.com (Amp's own servers connect to those). Amp also recommends
bundling MCP servers in a skill through `mcp.json`. Both mairc transports fit; a Bearer token goes in `headers`.

(b) **Hooks and whether their output reaches the model.** No command hooks. Amp's extension point is **plugins**:
TypeScript/JavaScript modules from `.amp/plugins/`, `$XDG_CONFIG_HOME/amp/plugins/` (else `~/.config/amp/plugins/`),
personal, workspace or system, that handle events (`session.start`, `agent.start`, `tool.call`, `tool.result`,
`agent.end`), register tools and commands, call tools (`ctx.tools.call`), show UI and label threads. Nothing documented
lets a plugin inject text into the model's context the way a Claude hook does; the documented ways to add information
are tools and skills. So mairc's hook output has no channel into Amp.

(c) **How a hook would identify its seat.** A plugin runs inside the Amp process and sees `event.thread.id`, so a
plugin-based delivery would identify the thread and could map its own pid; there is no external process to place.

(d) **Instruction files.** `AGENTS.md`.

(e) **Verdict.** MCP half: `should work with mairc install --cli amp` (write `amp.mcpServers.mairc`, or emit the `amp
mcp add` command). Hook half: `not feasible` without a plugin that mairc does not ship → `works through the no-hook
fallback`. Nothing here was run, and Amp is not installed on this host.

## What a next bead would have to build, per CLI

Not done here (docs-only bead), recorded so the decision is cheap later:

- **Pi**: an extension (~30 lines) that runs `mairc hook --client plain` and appends the output; plus
  `mairc install --cli pi` writing `~/.pi/agent/mcp.json`. MCP alone needs no code beyond the install writer.
- **Goose**: only the MCP entry in `config.yaml`; **no** hook delivery is possible, so nothing else.
- **Cursor CLI**: `mairc install --cli cursor` for `mcp.json`, plus a Cursor output formatter in
  `internal/hook` (`user_message`/`agent_message`) if the per-event delivery is confirmed.
- **Copilot CLI**: `mairc install --cli copilot` for `mcp-config.json` plus a session-start hook file whose
  `additionalContext` is the join message; per-prompt delivery is not available from a config hook.
- **Amp**: `mairc install --cli amp` for `amp.mcpServers`; no hook delivery without a plugin.
