# Coding-CLI compatibility (mairc-uxt)

How each major coding CLI loads MCP servers, runs hooks, shows hook output to the model, identifies the agent
seat, and reads project instructions — what `mairc` (stdio shim, Streamable HTTP at `127.0.0.1:4747/mcp` with a
Bearer token, and a `mairc hook` that prints nothing when nothing is new) can rely on per CLI.

Researched 2026-11-17 against the official documentation (URLs and read date in each section) **and** the
binaries installed on this host. Tags: **[DOC]** = official docs, **[BIN]** = the installed binary (help output,
config, source shipped with it, or a sandbox run), **[UNV]** = unverified, with the reason. Real CLI configuration
was only read; every experiment ran under a throwaway HOME in `/data/tmp/proudlion-compat/<cli>-sandbox/`. Model
calls: one tiny headless prompt each for Claude Code and Devin; none for the others (no credentials on this host,
or quota). These tools change fast: re-check before relying on a cell.

Five more CLIs — Pi, Goose, Cursor CLI, GitHub Copilot CLI and Amp — are in [compat-untested.md](compat-untested.md): documentation only, **no binary exercised**, every row labelled UNTESTED.

## Summary

| CLI | MCP config (path, key) | stdio | Streamable HTTP + headers | Hook mechanism + events | Hook stdout reaches model? | Seat id from a hook | Instruction file |
|---|---|---|---|---|---|---|---|
| Claude Code 2.1.326 [BIN] | user+local: `~/.claude.json` `mcpServers` (local = under `projects["<abs path>"].mcpServers`); project: `<repo>/.mcp.json` `mcpServers` [DOC][BIN] | yes (no `type`, or `"type":"stdio"`; `command`/`args`/`env`) [DOC][BIN] | yes: `"type":"http"` (alias `streamable-http`) + `url` + `headers` map, `${VAR}` expansion; `claude mcp add -t http -H "Authorization: Bearer …"` [DOC][BIN] | `hooks` in settings.json (`~/.claude/settings.json`, `.claude/settings.json`, `.claude/settings.local.json`, `--settings`); 33 events incl. SessionStart, UserPromptSubmit, PostToolUse, PostToolBatch, Stop [DOC][BIN] | plain stdout: only SessionStart / UserPromptSubmit (+UserPromptExpansion, PostModelSwitch); PostToolUse/PostToolBatch/Stop need JSON `hookSpecificOutput.additionalContext`; tested: PostToolUse plain dropped, JSON delivered [DOC][BIN] | stdin `session_id` = env `CLAUDE_CODE_SESSION_ID` (also in stdio MCP children); `CLAUDE_PID`; chain hook -> `/bin/sh -c` -> claude [DOC][BIN] | CLAUDE.md chain (managed, `~/.claude/CLAUDE.md`, `./CLAUDE.md` / `.claude/CLAUDE.md`, `CLAUDE.local.md`); AGENTS.md read natively only if no CLAUDE.md (default) [DOC] |
| OpenAI Codex CLI 0.169.0 [BIN] | `~/.codex/config.toml` (or `$CODEX_HOME/config.toml`) + trusted-project `.codex/config.toml`, table `[mcp_servers.<name>]` [DOC][BIN] | yes: `command`/`args`/`env`/`env_vars`/`cwd` [DOC][BIN] | yes: `url` + `bearer_token_env_var` (Authorization: Bearer) + `http_headers` / `env_http_headers` [DOC][BIN] | Claude-style hooks (`hooks.json` or `[hooks]`; SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, Stop, ... 14 events), feature `hooks` stable+on, hash trust required [DOC][BIN]; legacy `notify` (agent-turn-complete only) [DOC] | SessionStart + UserPromptSubmit: plain stdout -> developer message [DOC][BIN]; Pre/PostToolUse: only JSON `hookSpecificOutput.additionalContext` [DOC][UNV-bin]; Stop: `decision:block` reason -> new prompt [DOC]; notify: no [DOC-silent][UNV] | stdin JSON `session_id` (= thread id) + `transcript_path`; hook `$PPID` = codex native pid; no CODEX_* session env var [BIN] | AGENTS.override.md / AGENTS.md: `$CODEX_HOME` + project root..cwd, concatenated; CLAUDE.md only via `project_doc_fallback_filenames` [DOC][BIN] |
| Google Antigravity CLI `agy` 1.4.2 [BIN] | `~/.gemini/config/mcp_config.json` (global) / `<workspace>/.agents/mcp_config.json`, key `mcpServers` [DOC][BIN] | yes: `command`/`args`/`env`/`cwd` [DOC][BIN] | yes, Streamable HTTP only: `serverUrl` (`url` also accepted) + `headers` object; `agy mcp add -H "Authorization: Bearer …" name URL` [DOC][BIN] | `hooks.json` (`~/.gemini/config/` or `.agents/` or plugin), `type:"command"`; events SessionStart, PreToolUse, PostToolUse, PreInvocation, PostInvocation, Stop [DOC][BIN] | only via JSON `injectSteps` on PreInvocation (before every model call, i.e. after each tool result), PostInvocation, SessionStart; Stop `decision:"continue"`+`reason`; PostToolUse output is `{}` (no documented injection); plain-text stdout: [UNV] [DOC] | `conversationId` (+`transcriptPath`) in stdin JSON [DOC]; hook env vars / pid chain [UNV] (no auth in sandbox) | `AGENTS.md` and `GEMINI.md` (+ `.agents/AGENTS.md`, `.agents/rules/*.md`) in cwd and parents up to repo root; global `~/.gemini/{AGENTS,GEMINI}.md`, `~/.gemini/config/…`; cumulative [DOC] |
| Google Gemini CLI 0.50.0 (legacy, see note) [BIN] | `~/.gemini/settings.json` (user) / `.gemini/settings.json` (project) / `/etc/gemini-cli/settings.json` (system), key `mcpServers` [DOC][BIN] | yes: `command`/`args`/`env`/`cwd` [DOC][BIN] | yes: `httpUrl` (or `url`+`"type":"http"`) + `headers` object; `gemini mcp add -t http -H "Authorization: Bearer …"` [DOC][BIN] | `hooks` in settings.json, `type:"command"`; events SessionStart, BeforeAgent, AfterAgent, BeforeModel, AfterModel, BeforeToolSelection, BeforeTool, AfterTool, PreCompress, Notification, SessionEnd [DOC][BIN] | only via JSON `hookSpecificOutput.additionalContext` on AfterTool (appended to tool result), BeforeAgent (appended to this turn's prompt), SessionStart; wrapped in `<hook_context>`; plain-text stdout goes to the USER as `systemMessage`, not the model [DOC][BIN] | `GEMINI_SESSION_ID` env + `session_id`/`transcript_path` on stdin; hook's parent pid = the gemini (bun) child process [BIN] | `GEMINI.md` (global `~/.gemini/`, cwd + ancestors to `.git`, subdirs, JIT); `context.fileName` can list `AGENTS.md` [DOC][BIN] |
| Grok Build `grok` 1.0.70 (f50904eaee6c) | `~/.grok/config.toml` (or `$GROK_HOME/config.toml`) + project `.grok/config.toml`, table `[mcp_servers.<name>]`; also imports `~/.claude.json`, `.mcp.json`, `.cursor/mcp.json` [DOC][BIN] | yes: `command`/`args`/`env` [DOC][BIN] | yes: `url` + `headers = { Authorization = "Bearer …" }`; `grok mcp add -t http NAME URL -H "Authorization: Bearer …"` ("streamable HTTP") [DOC][BIN] | Claude-style command/http hooks: `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `Stop`, `StopCancelled`, `StopFailure`, `Notification`, `SessionEnd`, … in `~/.grok/hooks/*.json`, `.grok/hooks/*.json`, `[[hooks.<Event>]]` in config.toml, AND `~/.claude/settings.json` / `.claude/settings.json` [DOC][BIN] | only as JSON: `PostToolUse` / `PreToolUse` `hookSpecificOutput.additionalContext` (or PostToolUse `decision:"block"`+`reason`); `Stop` `decision:"block"` reason = new user message; NOT SessionStart (only `systemMessage`), NOT UserPromptSubmit (stdout discarded) [DOC]; plain-text stdout [UNV] | `GROK_SESSION_ID` env + `sessionId`/`session_id` on stdin; `GROK_HOOK_EVENT` tells it is Grok [DOC]; pid chain [UNV] | `AGENTS.md`, `Agents.md`, `AGENT.md`, `CLAUDE.md`, `Claude.md`, `CLAUDE.local.md` from `~/.grok`, `~/.claude`, repo root → cwd; `.grok/rules/*.md`; no `GROK.md` [DOC][BIN] |
| Devin CLI 3000.13.4 [BIN] | `~/.config/devin/mcp_config.json` (user), `.devin/mcp_config.json` (project), `.devin/mcp_config.local.json` (local, default of `mcp add`); key `mcpServers` [DOC][BIN] | yes: `command`/`args`/`env` [DOC][BIN] | yes: `url` + `transport:"http"` (falls back to SSE on non-auth 4xx), `headers` object; `devin mcp add -H "Authorization: Bearer …"` [DOC][BIN] | JSON command hooks in `.devin/hooks.v1.json` (also `.claude/settings*.json`, `~/.claude/settings*.json`): PreToolUse, PostToolUse, PermissionRequest, UserPromptSubmit, Stop, PostCompaction, SessionStart, SessionEnd [DOC][BIN] | only as JSON `hookSpecificOutput.additionalContext` on SessionStart, UserPromptSubmit, PostToolUse; plain stdout is dropped [DOC][BIN: live test] | stdin `session_id` (slug, e.g. `fantasy-stoplight`), stable per session; env only `DEVIN_PROJECT_DIR`/`CLAUDE_PROJECT_DIR`; parent = `devin acp` child of the `devin` pane process; no tty [BIN] | `AGENTS.md`, `AGENTS.local.md`, `AGENT.md`, `CLAUDE.md`, `.windsurfrules`, `.devin/rules/*.md`, `.devin/global_rules.md`; global `~/.config/devin/AGENTS.md` + `~/.claude/CLAUDE.md` [DOC] |
| OpenCode 1.19.3 [BIN] | `opencode.json(c)`, key `mcp` -> `{name: {type, ...}}`; global `~/.config/opencode/`, project root or `.opencode/` [DOC v1][BIN] | yes: `type:"local"`, `command:[...]`, `environment` [DOC v1][BIN] | yes: `type:"remote"`, `url`, `headers` (+`oauth:false`); Streamable HTTP first, SSE fallback [DOC v1][BIN] | no shell-command hooks; JS/TS plugins (`~/.config/opencode/plugins/`, `.opencode/plugins/`) with `tool.execute.after`, `event` (`session.idle`, `session.created`...), `chat.message`, `shell.env`, `experimental.chat.system.transform` [DOC v1][BIN] | not as raw stdout; a plugin can run `mairc hook` itself and inject: mutate `output.output` in `tool.execute.after` (tool result), push into `output.system` per LLM step, or `client.session.prompt` (`noReply`) [BIN][DOC partial][UNV live] | plugin: `input.sessionID` per hook + `process.pid` == `OPENCODE_PID`; tool shells inherit `OPENCODE=1`, `AGENT=1`, `OPENCODE_PID` [BIN] | `AGENTS.md` (walk up), else `CLAUDE.md`; global `~/.config/opencode/AGENTS.md`, else `~/.claude/CLAUDE.md`; plus `instructions` [DOC v1][BIN] |
| Prime Agent `prime-agent` 0.10.0 (Rust build; upstream v0.10.2/v0.10.3) | `~/.prime/agent/settings.json` (+ project `<cwd>/.prime/agent/settings.json`), key `mcpServers` [BIN][DOC] | yes, `"type":"stdio"` + `command/args/cwd/env` [DOC] | yes, `"type":"http"` (streamable HTTP) + `headers` map or `bearerTokenEnvVar` [BIN][DOC]; MCP is called from the Python REPL, not as native tools [DOC] | no user hook system; closest: `--autonomous-gate <cmd>` after each turn (autonomous only), background `bash()` completion notices, heartbeats, external `prime-agent send --steer/--follow-up` [BIN][DOC] | no general hook; gate output only on FAILURE in autonomous mode; `send` and `bash-done` notices DO enter context as user messages [DOC][BIN] | `PRIME_AGENT_INTERNAL_DAEMON_WORKER_ACTIVE_SESSION_ID` (= `list` id), `PRIME_AGENT_KERNEL_OWNER_PID` (worker pid); chain bash -> python kernel -> `prime-agent worker` -> daemon, no tty [BIN] | `AGENTS.md`/`AGENTS.MD`/`CLAUDE.md`/`CLAUDE.MD` (first hit per dir): agentDir global, then cwd ancestors root-first; plus `.prime/SYSTEM.md`, `APPEND_SYSTEM.md` [DOC][BIN] |

## Hook or fallback

| CLI | Mode | Why |
|---|---|---|
| Claude Code | hook | `PostToolUse` JSON `additionalContext` reaches the model [BIN live] |
| OpenAI Codex CLI | hook | `PostToolUse` JSON `additionalContext` [DOC]; plain stdout on `SessionStart`/`UserPromptSubmit` [BIN]; hooks need approval by hash |
| Google Antigravity CLI (`agy`) | hook | `PreInvocation` (before every model call) JSON `injectSteps` [DOC][BIN]; a different output form from the other hook CLIs: `mairc hook --client agy` prints `{"injectSteps":[{"ephemeralMessage":"…"}]}` on `PreInvocation` and `SessionStart` [BIN live, agy 1.5.10: see "Live check" in the Antigravity section] |
| Google Gemini CLI (legacy) | hook | `AfterTool` JSON `additionalContext` [DOC][BIN source] |
| Grok Build | hook | `PostToolUse` JSON `additionalContext` [BIN docs shipped with it] |
| Devin CLI | hook | `PostToolUse` JSON `additionalContext` reaches the model [BIN live] |
| OpenCode (v2) | hook (plugin) | no shell-command hooks; mairc's plugin (`plugins/mairc.js`) runs `mairc hook` on `execute.after` (appends to the tool result) and at a session's first `context` (system prompt); a PM reached the model without `irc_read` (OpenCode 2.3.0, 17 Nov) [BIN live] |
| Prime Agent | fallback | no hooks at all; MCP tools only through its `ipython` REPL [DOC repo][BIN] |
| Pi (Rust port) | fallback | no command hooks; a JS extension can inject (`tool_result` returns new `content`; `pi.sendMessage`), not built yet (mairc-cuf) [BIN source] |

A fallback CLI gets the instruction snippet in `skill/fallback.md`: call `irc_read` at the start of each task and
after each batch of tool calls, never in a loop.

### Idle read reply

`irc_read` with nothing new answers `nothing new` (`internal/tools`, asserted by
`TestIdleReadReplyIsTwoTokens`). Measured 2026-11-17 with OpenAI's tokenizers (tiktoken):

| What | Bytes | o200k_base | cl100k_base |
|---|---|---|---|
| reply text `nothing new` | 11 | 2 | 2 |
| MCP tool result `{"content":[{"type":"text","text":"nothing new"}]}` | 50 | 14 | 13 |
| whole JSON-RPC response | 84 | 28 | 27 |

What the model sees is the reply text inside the CLI's own tool-result framing, so the cost mairc adds to an idle
read is the 2-token text (well under the 10-token target). Claude's and Gemini's tokenizers are not public; for
plain English words they are of the same order [UNV]. To repeat:
`uv run --no-project --with tiktoken python -c 'import tiktoken; print(len(tiktoken.get_encoding("o200k_base").encode("nothing new")))'`.

## What this means for mairc

1. **One output form works on five hook CLIs: JSON on the tool event; Antigravity needs its own form.** Claude
   Code, Codex, Gemini, Grok and Devin all add `{"hookSpecificOutput":{"hookEventName":"<Event>","additionalContext":"…"}}` to the
   model's context after a tool call (`PostToolUse`; Gemini calls it `AfterTool`). Plain-text stdout is
   **dropped** on that event by Claude Code [BIN, live], Devin [BIN, live], Codex [DOC] and Gemini [BIN source]; plain
   text reaches the model only on session-start / prompt-submit events in Claude Code and Codex. So `mairc hook`
   must read `hook_event_name` from stdin and print the matching JSON object when there is news, and print
   **nothing on stdout and stderr** when there is none (Gemini shows stderr to the user when stdout is empty).
   Antigravity (`agy`) is the exception: its documented injection is JSON `injectSteps` (`systemMessage`,
   `ephemeralMessage` or `userMessage`) on `PreInvocation`, before every model call; `PostToolUse` output is only
   `{}`. Its hook is declared in `~/.gemini/config/hooks.json` or `.agents/hooks.json`, and it gets
   `conversationId` on stdin [DOC][BIN]. Whether empty stdout is accepted is [UNV] (`{}` is the safe idle reply).
2. **Seat id: use the session id the hook receives, not the process chain.** Every hook-capable CLI passes a
   `session_id` (or `sessionId`) on stdin; Claude Code (`CLAUDE_CODE_SESSION_ID`), Gemini (`GEMINI_SESSION_ID`) and
   Grok (`GROK_SESSION_ID`) also set it in the environment. Grok and Prime Agent can run many sessions under one
   shared daemon, so a parent pid is not a reliable seat. No hook gets a tty.
3. **Claude's hooks are imported by other CLIs.** Grok (trusted folders) and Devin (by default) also run the hooks
   in `~/.claude/settings*.json` / `.claude/settings*.json`. A `mairc hook` registered there for Claude fires in
   Grok and Devin seats too: it must tell the CLIs apart (`GROK_HOOK_EVENT` / `GROK_SESSION_ID` for Grok; Devin's
   stdin and `DEVIN_PROJECT_DIR`) and use that CLI's session id — or the installer must use per-CLI files only.
4. **Codex hooks need approval.** Each hook is trusted by hash (`/hooks` in the CLI) or the run needs
   `--dangerously-bypass-hook-trust`; an installer cannot enable it silently, and changing the command needs
   approval again.
5. **No shell hook: OpenCode and Prime Agent** (input to the fallback bead mairc-hd5).
   - OpenCode 1.19.3: a small JS plugin can run `mairc hook` itself and append its output to the tool result
     (`tool.execute.after`), to the system prompt (`experimental.chat.system.transform`), or start a turn on
     `session.idle` [BIN source; UNV live].
   - Prime Agent 0.10.0: no hooks at all. Delivery is from outside: `prime-agent send --steer <session> <msg>`
     only when there is news, or a long-running command the agent starts that finishes on news; MCP tools are
     callable only from its Python REPL (`mcp.call_tool`) [DOC repo + BIN; UNV live].
6. **MCP is uniform enough.** All seven support stdio and an HTTP transport with an Authorization header; the
   shapes differ (JSON `mcpServers` with `type:"http"` — Claude Code requires it, a bare `url` is treated as stdio;
   TOML `[mcp_servers.<name>]` for Codex and Grok; `mcp` with `type:"remote"` for OpenCode; Gemini's `httpUrl`).

## Claude Code (2.1.326)

Docs were read on 2026-11-17: https://code.claude.com/docs/en/mcp, /hooks, /memory, /env-vars, /settings. The raw markdown copies are in /data/tmp/proudlion-compat/claude-sandbox/docs/.

### (a) MCP servers
- Scopes and files. **local** (the default) and **user** are stored in `~/.claude.json`. **project** is stored in `<project root>/.mcp.json`. The top-level key is `mcpServers` in both. [DOC mcp] The same file paths apply on every OS (home-relative). [UNV: the docs list no OS-specific paths]
- [BIN] In a read-only parse of `~/.claude.json`, user servers are in top-level `mcpServers`, and local servers are in `projects["/data/projects/fleksprime"].mcpServers`. Both hold an existing entry `{"type":"http","url":"http://127.0.0.1:8765/mcp/","headers":{"Authorization":…}}`. That is a live example of HTTP with a Bearer header in use today.
- Precedence: local > project > user > plugin > claude.ai connectors. Managed `managedMcpServers` ranks above all of them. One definition wins whole; fields are not merged across scopes. [DOC mcp]
- Stdio shape: `{"mcpServers":{"mairc":{"command":"mairc","args":["stdio"],"env":{}}}}`. An entry with no `type` is treated as stdio, so an entry with `url` but no `type` is a config error. [DOC mcp]
- Streamable HTTP shape: `{"mcpServers":{"mairc":{"type":"http","url":"http://127.0.0.1:4747/mcp","headers":{"Authorization":"Bearer ${MAIRC_TOKEN}"}}}}` [DOC mcp]
  - `type` also accepts `"streamable-http"` as an alias. `sse` is deprecated. `ws` exists.
  - `${VAR}` and `${VAR:-default}` expand in `command`, `args`, `env`, `url` and `headers`.
  - Credential names such as `ANTHROPIC_API_KEY` expand to empty in remote `url`/`headers`. `headersHelper` can supply dynamic headers.
- CLI to add a server. [BIN `claude mcp add --help`; DOC]
  - Stdio: `claude mcp add [-s local|user|project] [-e K=V] mairc -- mairc stdio`
  - HTTP: `claude mcp add -t http -s user mairc http://127.0.0.1:4747/mcp -H "Authorization: Bearer <tok>"`
  - From a JSON string: `claude mcp add-json <name> '<json>'`
  - Other subcommands: `list`, `get`, `remove`, `reset-project-choices`.
  - Help text: "-t, --transport … (stdio, sse, http). Defaults to stdio." and "-H, --header … Set headers for HTTP/SSE servers".
- Project `.mcp.json` servers need a one-time approval in interactive sessions. `-p` and the SDK load them without asking. [DOC mcp] [BIN] `mcp list` help says "Unapproved .mcp.json servers are shown as ⏸ Pending approval".
- Limits. [DOC mcp]
  - `MCP_TIMEOUT` sets the startup timeout.
  - `MCP_TOOL_TIMEOUT`, or a per-server `timeout` in ms, sets the tool timeout.
  - The idle timeout defaults to 5 min for HTTP and 30 min for stdio.
  - Tool output warns above 10k tokens and is capped at 25k (`MAX_MCP_OUTPUT_TOKENS`).

### (b) Hooks
- Declared under `"hooks"` in settings files: `~/.claude/settings.json`, `.claude/settings.json`, `.claude/settings.local.json`, managed settings, a `--settings` file/JSON, or a plugin `hooks.json`. [DOC hooks]
- Shape: `{"hooks":{"PostToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"mairc hook","timeout":5}]}]}}`. A `matcher` of `*`, `""` or omitted matches everything. [DOC hooks] [BIN] The user's live settings use exactly this shape, e.g. `PostToolUse` `*` → `am_urgent_hint.py tool`, `timeout: 3`.
- Events (33). [DOC hooks]
  - SessionStart, Setup, UserPromptSubmit, UserPromptExpansion
  - PreToolUse, PermissionRequest, PermissionDenied, PostToolUse, PostToolUseFailure, PostToolBatch
  - Notification, MessageDisplay, SubagentStart, SubagentStop, TaskCreated, TaskCompleted
  - Stop, StopFailure, TeammateIdle, InstructionsLoaded, ConfigChange, CwdChanged, DirectoryAdded, FileChanged
  - WorktreeCreate, WorktreeRemove, PreCompact, PostCompact, PreModelSwitch, PostModelSwitch
  - Elicitation, ElicitationResult, SessionEnd
- Events that matter for mairc. [DOC hooks]
  - `PostToolUse` fires once per tool, concurrently when tools run in parallel.
  - `PostToolBatch` fires once after every tool in a batch has resolved, "before Claude Code sends the next request to the model". It is the best fit for a "before next step" check.
  - `Stop` fires at turn end. `SessionStart` has matchers `startup|resume|clear|compact|fork`.
- Stdin JSON (common fields). [DOC hooks] [BIN, captured in the sandbox]
  - `session_id`, `transcript_path` (`~/.claude/projects/<cwd-slug>/<session_id>.jsonl`), `cwd`, `permission_mode`, `hook_event_name`, `prompt_id`, `effort`. Subagents add `agent_id` and `agent_type`.
  - PostToolUse adds `tool_name`, `tool_input`, `tool_response`, `tool_use_id`, `duration_ms`.
  - Stop adds `stop_hook_active`, `last_assistant_message`, `background_tasks`, `session_crons`.
- Timeout: per-handler `timeout` in seconds. The default is 600 for command hooks, lowered to 30 on UserPromptSubmit. [DOC hooks]
- **Whether stdout reaches the model.** [DOC hooks, "Exit code 0"]
  - On exit 0, plain-text stdout becomes model context ONLY on `UserPromptSubmit`, `UserPromptExpansion`, `SessionStart` and `PostModelSwitch`. On `PreCompact` it goes to the compaction summary.
  - On every other event, including PostToolUse, PostToolBatch and Stop, the hook must print one JSON object (nothing around it but whitespace): `{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"…"}}`.
  - `additionalContext` is supported on SessionStart, SubagentStart, UserPromptSubmit, UserPromptExpansion, PreToolUse, PostToolUse, PostToolUseFailure, PostToolBatch, Stop, SubagentStop and PostModelSwitch.
  - It is injected as a system reminder. Multiple hooks' values are all delivered.
  - Empty stdout adds nothing, so an idle chat costs zero tokens.
  - Output is capped at 10,000 chars. Anything larger is saved to a file and only a 2,000-char preview is sent.
  - Stderr on exit 0 is never seen by Claude.
- [BIN] The headless test confirmed this. The model reported `MAIRC_SS_PLAIN`, `MAIRC_UPS_PLAIN` and `MAIRC_PTUJ_JSON`, but not `MAIRC_PTUP_PLAIN`. PostToolUse plain stdout was dropped even though that hook did run (its stdin file was written).
- On Stop, `additionalContext` (or `decision:"block"` + `reason`, or exit 2 + stderr) continues the turn so Claude acts on it. This is subject to `stop_hook_active` and a cap of 8 consecutive continuations. [DOC hooks]
  - Consequence: if mairc emits there, the agent never really goes idle while chat is pending. It should emit only on new messages.
- Async options. [DOC hooks]
  - `async:true` delivers `additionalContext` on the next turn.
  - `asyncRewake:true` with exit 2 wakes an idle Claude immediately, and its stderr/stdout is shown as a system reminder. This is a possible "wake on new chat" path. [UNV: not tested]

### (c) Seat identification inside a hook
- [BIN] The hook env included the variables below.
  - `CLAUDE_CODE_SESSION_ID=<uuid>`, equal to the stdin `session_id`.
  - `CLAUDE_PID=<claude pid>`, `CLAUDE_PROJECT_DIR`, `CLAUDECODE=1`, `CLAUDE_CODE_CHILD_SESSION=1`, `CLAUDE_EFFORT`, `CLAUDE_CODE_ENTRYPOINT`.
  - `CLAUDE_ENV_FILE`, on SessionStart only.
  - The user's env, e.g. `TERM_PROGRAM=tmux`.
  - No tty (`tty: not a tty`).
- [BIN] Process chain: hook → `/bin/sh -c "<command>"` → `claude` (pid = `CLAUDE_PID`) → the launching shell.
- [DOC env-vars] `CLAUDE_CODE_SESSION_ID` is set in Bash tool commands, hook commands **and stdio MCP server subprocesses**, and is updated on `/clear`. An MCP child keeps the ID it was spawned with, and after `--continue` it may hold the startup ID. `CLAUDE_PID` is set for Bash and hooks; whether stdio MCP children get it is [UNV].
- Recommended stable seat key:
  - Use `CLAUDE_PID` (the process lifetime of one agent), backed by `CLAUDE_CODE_SESSION_ID` / `session_id`.
  - The `mairc stdio` shim can read the same env (session id per doc, pid via getppid) to bind the seat. [UNV: env inside an MCP child not tested]
  - An HTTP server cannot see either value, so seat binding over HTTP needs an explicit token, header or env such as `AGENT_NAME`.
- [BIN] Caveat: in a nested `claude` launched from another Claude session, `CLAUDE_CODE_EXECPATH` showed 2.1.325, inherited from the parent. `CLAUDE_PID` and `CLAUDE_CODE_SESSION_ID` were correctly overwritten.

### (d) Instruction files
- Load order is broadest first; files are concatenated, not overridden. [DOC memory]
  1. Managed: `/etc/claude-code/CLAUDE.md` (Linux/WSL), `/Library/Application Support/ClaudeCode/CLAUDE.md` (macOS), `C:\Program Files\ClaudeCode\CLAUDE.md` (Windows).
  2. `~/.claude/CLAUDE.md` plus `~/.claude/rules/`.
  3. `CLAUDE.md` or `.claude/CLAUDE.md` in the cwd and every parent directory, ordered root to cwd.
  4. `CLAUDE.local.md` after each directory's `CLAUDE.md`.
  5. `.claude/rules/*.md`.
- Subdirectory CLAUDE.md files load on demand. `@path` imports are allowed, up to 4 hops.
- AGENTS.md is read natively since v2.1.277, by default only when there is no `CLAUDE.md`, `.claude/CLAUDE.md` or `CLAUDE.local.md` in the cwd or above it. Setting "Project instructions" (`claude-md-and-agents-md` etc.) changes this. A common pattern is a CLAUDE.md that imports `@AGENTS.md`. [DOC memory] [BIN] This repo's CLAUDE.md just points to AGENTS.md.

### (e) Not possible, and consequences for mairc
- Plain stdout from PostToolUse, PostToolBatch, Stop or Notification never reaches the model. [DOC][BIN] `mairc hook` must print the `hookSpecificOutput.additionalContext` JSON on those events, or plain text only on UserPromptSubmit/SessionStart. The cleanest approach is a `--event` arg, or reading `hook_event_name` from stdin and emitting the matching JSON.
- An idle session (no turn in flight) receives nothing from synchronous hooks. Chat arriving while the agent waits for the user only shows up on the next prompt. `asyncRewake` is the documented exception. [UNV: not tested]
- `--bare` skips settings and plugin hooks, so mairc hooks would not run there. [BIN `--help`]
- Project `.mcp.json` needs interactive approval once, and a plain `url` entry without `type` is mis-read as stdio. [DOC]

### Commands used
```
claude --version                     # 2.1.326 (Claude Code)
readlink -f ~/.local/bin/claude      # ~/.local/share/claude/versions/2.1.326
claude --help; claude mcp --help; claude mcp add --help; claude mcp add-json --help
python3 -I -c '<read-only parse of ~/.claude.json and ~/.claude/settings.json; secrets redacted>'
cat ~/.claude/hooks/am_urgent_hint.py   # existing PostToolUse hook using additionalContext
curl -sSL -o docs/<p>.md https://code.claude.com/docs/en/<p>.md   # p = hooks mcp settings env-vars cli-reference
WebFetch https://code.claude.com/docs/en/{hooks,mcp,memory}
# one headless test (haiku, cost $0.0023), sandbox files: claude-sandbox/{hook.sh,settings.json,out/}
cd /data/tmp/proudlion-compat/claude-sandbox && claude -p --model haiku --setting-sources project \
  --settings ./settings.json --allowedTools "Bash(echo *)" --no-session-persistence --output-format json \
  "Run the bash command: echo hi. Then list every distinct token beginning with MAIRC_ ..."
# -> result: MAIRC_SS_PLAIN / MAIRC_UPS_PLAIN / MAIRC_PTUJ_JSON  (MAIRC_PTUP_PLAIN absent)
```

## OpenAI Codex CLI (0.169.0)

Docs read 2026-11-17. developers.openai.com/codex/* now 308-redirects to learn.chatgpt.com/docs/*; URLs below are the redirect targets.
D-HOOKS = https://learn.chatgpt.com/docs/hooks · D-MCP = https://learn.chatgpt.com/docs/extend/mcp?surface=cli ·
D-AGENTS = https://learn.chatgpt.com/docs/agent-configuration/agents-md · D-ADV = https://learn.chatgpt.com/docs/config-file/config-advanced
Binary: `/home/ubuntu/.local/bin/codex` is a bash launcher `exec ~/.bun/bin/bun ~/.bun/bin/codex "$@"`; the real binary is
`~/.bun/install/global/node_modules/@openai/codex-linux-x64/vendor/x86_64-unknown-linux-musl/bin/codex` [BIN: `cat ~/.local/bin/codex`, `ps`].
All binary tests used a sandbox `CODEX_HOME=/data/tmp/proudlion-compat/codex-sandbox/home` with NO auth.json and `openai_base_url` pointed at
a closed port (127.0.0.1:9) — zero model calls were made; requests failed with "Connection refused" [BIN].

### (a) MCP servers
- Paths: user `~/.codex/config.toml` (`$CODEX_HOME` overrides `~/.codex`); project `.codex/config.toml`, loaded only when the project is trusted; closer file wins per key [DOC D-MCP, D-ADV]. macOS same path; Windows not stated [UNV]. `-c key=value` one-off override [BIN `codex --help`].
- Stdio shape [DOC D-MCP][BIN `codex mcp add mairc --env MAIRC_SEAT=s1 -- mairc stdio` wrote exactly this]:
  `[mcp_servers.mairc]` `command = "mairc"` `args = ["stdio"]` + `[mcp_servers.mairc.env]` `MAIRC_SEAT = "s1"`. Also `env_vars = [..]` (forward vars), `cwd` [DOC].
- Streamable HTTP shape [DOC D-MCP][BIN `codex mcp add mairc-http --url http://127.0.0.1:4747/mcp --bearer-token-env-var MAIRC_TOKEN` wrote]:
  `[mcp_servers.mairc-http]` `url = "http://127.0.0.1:4747/mcp"` `bearer_token_env_var = "MAIRC_TOKEN"` (sent as `Authorization: Bearer <value>`).
  Static headers: `http_headers = { "X-Mairc-Seat" = "x" }`; from env: `env_http_headers = { "Header" = "ENV_NAME" }` [DOC]. `codex mcp get` shows
  `transport: streamable_http`, `bearer_token_env_var: MAIRC_TOKEN`, `http_headers: X-Mairc-Seat=*****`, `env_http_headers`, `http_headers_helper` [BIN].
- Token must be an env var name (or a literal static `http_headers` Authorization value) — `codex mcp add` has no literal-token flag [BIN `codex mcp add --help`]. The live user config uses `url` only, no auth [BIN read-only].
- Other keys: `startup_timeout_sec` (default 10), `tool_timeout_sec` (default 60), `enabled`, `required`, `enabled_tools`/`disabled_tools`, `tools.<tool>.approval_mode` [DOC D-MCP]. SSE transport: not mentioned [UNV].
- CLI: `codex mcp list|get|add|remove|login|logout`; `add <NAME> (--url <URL> | -- <COMMAND>...)`, `--env K=V` (stdio only), `--bearer-token-env-var` (HTTP only), OAuth flags [BIN `codex mcp add --help`].

### (b) Hooks / notify
- Hooks: feature `hooks` = `stable true` [BIN `codex features list`] ("enabled by default", `codex_hooks` deprecated alias) [DOC D-HOOKS]. Files: `~/.codex/hooks.json`, `~/.codex/config.toml` `[hooks]`, `<repo>/.codex/hooks.json|config.toml` (trusted projects only), plugin `hooks/hooks.json`, managed `requirements.toml` [DOC]. Live user setup has `~/.codex/hooks.json` with a PreToolUse `matcher: "Bash"` command hook [BIN read-only].
- Format = Claude Code's: `{"hooks":{"<Event>":[{"matcher":"<regex>","hooks":[{"type":"command","command":"...","timeout":10}]}]}}` [DOC][BIN: our sandbox hooks.json ran].
- Events: SessionStart, SessionEnd, PreToolUse, PermissionRequest, PostToolUse, UserPromptSubmit, Stop, PreCompact, PostCompact, SubagentStart, SubagentStop, PreModelRequest, PostModelRequest, Interrupt [DOC]. SessionStart (source `startup`) and UserPromptSubmit fired in `codex exec` [BIN hook.log].
- Trust: non-managed hooks must be reviewed/trusted (`/hooks` in the TUI); trust is a sha256 per hook stored in config.toml `[hooks.state."<file>:<event>:<i>:<j>"] trusted_hash` — editing the hook command re-requires review [DOC][BIN read of live config]. `--dangerously-bypass-hook-trust` skips this per run [BIN `codex --help`]; without it, debug runs did not fire hooks [BIN].
- Timeout: `timeout` seconds, default 600; SessionEnd/Interrupt default 1, max 3. `async: true` = background; its additionalContext lands at next safe point. `additionalContextLimit` default ~2,500 tokens [DOC].
- stdin JSON [BIN hook.log]: `{"session_id","transcript_path","cwd","hook_event_name","model","permission_mode","source"}` (SessionStart); UserPromptSubmit adds `turn_id`, `prompt`. PostToolUse adds `tool_name`, `tool_use_id`, `tool_input`, `tool_response` [DOC].
- STDOUT -> model [DOC table D-HOOKS]: SessionStart / UserPromptSubmit / SubagentStart: plain stdout OR `hookSpecificOutput.additionalContext` -> developer context. PreToolUse / PostToolUse: ONLY `{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"..."}}`; plain stdout ignored. Stop: `{"decision":"block","reason":"..."}` reason becomes a new continuation prompt. PermissionRequest, Pre/PostCompact, SessionEnd, Pre/PostModelRequest, Interrupt: nothing reaches the model.
- [BIN] plain stdout `HOOK-STDOUT-MARKER plain text` from SessionStart and from UserPromptSubmit each appeared as a `role: developer` `response_item` in the rollout (the request history) before the (failed) model request. A hook that prints nothing added NO item (7 items, none from the hook) — zero-token idle confirmed for these two events.
- [UNV not run: needs a model-issued tool call] PostToolUse additionalContext delivery; Stop block behaviour.
- `notify = ["prog", ...]` (user config only; ignored in project config): runs on `agent-turn-complete` only, gets ONE JSON argv arg (`type`, `thread-id`, `turn-id`, `cwd`, `input-messages`, `last-assistant-message`) [DOC D-ADV]. Docs say nothing about using its output -> fire-and-forget notification, not context [UNV: not tested]. Live config uses notify for mcp_agent_mail [BIN read-only].

### (c) Seat identification inside a hook
- `session_id` on stdin = the thread id (`thread.started` thread_id in `--json`) and also in `transcript_path` `.../sessions/YYYY/MM/DD/rollout-<ts>-<session_id>.jsonl` [BIN]. Stable for the session; `codex resume` keeps it [UNV].
- Env: Codex sets NO session/thread/pid env var for command hooks; hooks inherit the CLI's env (`CODEX_HOME` only if the user set it, `CODEX_MANAGED_BY_BUN`/`CODEX_MANAGED_PACKAGE_ROOT` from the bun launcher, `TMUX_PANE`, `PWD`=session cwd) [BIN env dump; DOC lists only PLUGIN_ROOT/PLUGIN_DATA for plugin hooks].
- Process chain [BIN, exec mode]: `hook.sh (/bin/sh via shebang) -> codex (native musl binary) -> parent shell`. Hook `$PPID` is the codex native pid (no intermediate shell). Running TUI agents: native codex pid's parent is the pane's `-zsh` (bun launcher execs through), no app-server daemon process seen [BIN `ps`]. Hook in TUI mode spawning from the same pid: [UNV not run, needs a TTY + auth].
- tty: hook has none (`tty` -> "not a tty") [BIN]; use `TMUX_PANE` or the parent's tty (`ps -o tty= -p $PPID`).
- Recommended seat key: `session_id` from stdin (matches Codex's own resume/rollout id); `$PPID` (codex pid) as a fallback / liveness check.

### (d) Instruction files
- Global: `$CODEX_HOME/AGENTS.override.md` else `AGENTS.md` (first non-empty) [DOC D-AGENTS][BIN GLOBAL-AGENTS-MARKER rendered].
- Project: from root (git root) down to cwd, one file per dir: `AGENTS.override.md` > `AGENTS.md` > `project_doc_fallback_filenames` [DOC]; concatenated root-first, later = higher precedence, cap `project_doc_max_bytes` 32 KiB [DOC]. [BIN `codex debug prompt-input` in proj/sub: one user message "# AGENTS.md instructions for <cwd>" with GLOBAL, "--- project-doc ---", ROOT, SUB in that order.]
- CLAUDE.md: ignored by default; read when listed: `-c 'project_doc_fallback_filenames=["CLAUDE.md"]'` made `CLAUDE-ONLY-MARKER` appear in a dir without AGENTS.md [BIN]. Discovery once per session [DOC].
- Project AGENTS.md loaded even in an untrusted (sandbox) project [BIN].

### (e) Not possible / consequences for mairc
- Plain stdout after a tool call does NOT reach the model: `mairc hook` must emit `{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"<msgs>"}}` on PostToolUse, and print nothing when idle [DOC]. Plain text is fine on SessionStart/UserPromptSubmit [DOC][BIN].
- No "before next model step" hook that injects context: PreModelRequest/PostModelRequest are diagnostic only [DOC]. Turn-end delivery only via Stop `decision:block` (forces another turn — use only when there IS news) [DOC].
- Hooks must be trusted per hash (`/hooks` review) or run with `--dangerously-bypass-hook-trust`; an installer cannot silently enable them, and changing the hook command re-requires review [DOC][BIN].
- `notify` cannot inject anything (argv-only, turn-complete only) — useless for mairc's in-context delivery [DOC].
- No seat env var: mairc must read `session_id` from stdin JSON (or `$PPID`).

### Commands used
```
cat /data/tmp/proudlion-compat/BRIEF.md
codex --version; codex --help; codex mcp --help; codex mcp add --help; codex mcp list
codex features list | grep -iE 'hook|mcp|plugin'; codex features --help; codex plugin --help; codex debug --help; codex debug prompt-input --help; codex exec --help
cat ~/.codex/config.toml (secrets filtered); cat ~/.codex/hooks.json; cat ~/.local/bin/codex          # read only
ps -eo pid,ppid,etime,args | grep codex
# sandbox (no auth.json, no model calls): files under /data/tmp/proudlion-compat/codex-sandbox/{home,home2,proj,hook.sh,hook_silent.sh}
export CODEX_HOME=/data/tmp/proudlion-compat/codex-sandbox/home
codex mcp list; codex mcp get mairc_http
CODEX_HOME=.../home2 codex mcp add mairc --env MAIRC_SEAT=s1 -- mairc stdio
CODEX_HOME=.../home2 codex mcp add mairc-http --url http://127.0.0.1:4747/mcp --bearer-token-env-var MAIRC_TOKEN; cat .../home2/config.toml
cd .../proj/sub && codex debug prompt-input hi                       # AGENTS.md order
cd .../proj/sub2 && codex debug prompt-input -c 'project_doc_fallback_filenames=["CLAUDE.md"]' hi
codex --dangerously-bypass-hook-trust debug prompt-input hi          # hooks do not run here
cd .../proj/sub && timeout 60 codex exec --dangerously-bypass-hook-trust --skip-git-repo-check --json \
  -c 'openai_base_url="http://127.0.0.1:9/v1"' -c 'mcp_servers.mairc_stdio.enabled=false' -c 'mcp_servers.mairc_http.enabled=false' hi
cat .../hook.log; python3 -I (grep rollout .../home/sessions/2026/11/17/*.jsonl for HOOK-STDOUT-MARKER developer items)
# repeated exec with hook_silent.sh -> silent.log shows 2 runs, rollout has no hook item
```
WebFetch (2026-11-17): D-HOOKS, D-MCP, D-AGENTS, D-ADV (via developers.openai.com/codex/{hooks,mcp,guides/agents-md,config-advanced} 308 redirects); WebSearch "Codex CLI config notify agent-turn-complete JSON argument".

## Google Antigravity CLI (1.4.2)

Doc sources (all read 2026-11-17, official markdown served by antigravity.google):
https://antigravity.google/docs/hooks.md , https://antigravity.google/docs/mcp.md , https://antigravity.google/docs/rules.md ,
https://antigravity.google/docs/customization-discovery.md , https://antigravity.google/docs/plugins.md ,
https://antigravity.google/docs/cli/gcli-migration.md , https://antigravity.google/llms.txt ,
Google Developers Blog, 19 May 2026: https://developers.googleblog.com/an-important-update-transitioning-gemini-cli-to-antigravity-cli/
Binary: `~/.local/bin/agy-real` (Go, stripped ELF). `agy` on this host is the owner's wrapper `agy-locked` (Python; pins
settings, installs a dcg PreToolUse hook, launches agy-real via `subprocess.Popen`) — only read, not used.

Context checks on the brief's claims:
- Gemini CLI retirement: the Google blog (19 May 2026) says that on **18 June 2026** Gemini CLI and Code Assist IDE extensions "stop serving requests for Google AI Pro and Ultra" and free individual Code Assist users. Enterprise licences and paid Gemini / Gemini Enterprise Agent Platform API keys keep access. So it is a partial shutdown, not a full retirement. [DOC]
- `serverUrl`: correct. The migration doc says legacy `url`/`httpUrl` became `serverUrl`, and the MCP doc says `url` also works and `httpUrl` is not supported. `agy mcp add` writes `serverUrl`. [DOC][BIN]
- `agy plugin import gemini`: real. It is in the migration doc and in the plugins doc (`agy plugin import [gemini|claude]`, reading `~/.gemini/extensions/` / `~/.claude/plugins/`). `agy-real plugin` help lists `import [source]  Import plugins from gemini or claude`. Not run. [DOC][BIN]

(a) MCP servers
- Files: global `~/.gemini/config/mcp_config.json`, workspace `<workspace>/.agents/mcp_config.json`, plugin `<plugin-root>/mcp_config.json`. Root key `mcpServers`. [DOC mcp.md] The binary wrote `$HOME/.gemini/config/mcp_config.json` in the sandbox. [BIN]
- stdio: `{"command":"mairc","args":["stdio"]}`; optional `env`, `cwd`, `disabled`, `tools.<name>.eager`. [DOC] [BIN: `mcp add mairc mairc stdio` wrote `"command":"mairc","args":["stdio"],"disabled":false`]
- Remote: "Remote MCP servers must use the Streamable HTTP transport… `serverUrl` (`url` also works). Legacy HTTP+SSE and `httpUrl` aren't supported." `headers` object; OAuth (CIMD/DCR/manual `oauth.clientId`). [DOC]
  [BIN: `mcp add --header "Authorization: Bearer TOKEN" mairc-http http://127.0.0.1:4747/mcp` wrote `"serverUrl":…,"headers":{"Authorization":"Bearer TOKEN"}`]
- CLI: `agy mcp add [-t stdio|http] [-H 'K: V'] [-e K=V] <name> <commandOrUrl> [args...]` (flags before name; http/https URLs auto-detected), plus `remove`, `list`, `enable`, `disable`. The TUI also has `/mcp`. [BIN][DOC]
- macOS/Windows paths: none documented beyond `~/.gemini/...`. [UNV]

(b) Hooks
- Files: `.agents/hooks.json` (workspace), `~/.gemini/config/hooks.json` (global), or a plugin's `hooks.json`. Hooks in `~/.gemini/antigravity-cli/settings.json` are NOT loaded. Use `/hooks` in the TUI to list them. [DOC hooks.md] The binary contains `parsing hooks from %s`, `No hooks.json found at %s`, and `unsupported hook type: %q`. [BIN strings]
- Shape: `{"<hook-name>": {"enabled":true, "<Event>": [...]}}`. Tool events take `[{"matcher":"<regex>","hooks":[{"type":"command","command":"…","timeout":N}]}]`. SessionStart, PreInvocation, PostInvocation and Stop take a bare handler list `[{"type":"command","command":"…"}]`. `timeout` is in seconds (default 30). [DOC] The real `~/.gemini/config/hooks.json` (dcg, read only) matches this shape. [BIN]
- Events: SessionStart, PreToolUse, PostToolUse, PreInvocation (before every model call), PostInvocation, Stop. [DOC] Proto messages in the binary (`hooks_go_proto.*HookArgs`): SessionStart, PreTool, PostTool, PreInvocation, PostInvocation, Stop. [BIN]
- I/O: stdin is JSON (camelCase). Stdout is JSON. [DOC]
- Stdout reaching the model:
  - SessionStart, PreInvocation and PostInvocation accept `{"injectSteps":[…]}`. Each step is one of `systemMessage` {object}, `userMessage` (string), `ephemeralMessage` (string, "transient system message"), or `errorMessage` (shown to the user, NOT sent to the model). PreInvocation also takes `terminationBehavior`. [DOC] Getters in the binary: `PreInvocationHookResult.GetInjectSteps`, `SessionStartHookResult.GetInjectSteps`, `PostInvocationHookResult.GetInjectSteps`. [BIN]
  - Stop: `{"decision":"continue","reason":"…"}` re-enters the loop and injects `reason` as a system message. [DOC]
  - PreToolUse: `reason` is "shown to the agent or to you"; when `overwrite` changes args, a notice is prepended to the tool result. [DOC]
  - PostToolUse: documented output is `{}` only. The binary has an undocumented `PostToolHookResult.GetOverwriteResult` field; its behavior is [UNV].
  - Plain-text (non-JSON) stdout and empty stdout handling: [UNV] (no hook ran, see (e)).
- Ordering: tool → result → next model call. So PreInvocation is the documented "after each tool call, before the model's next step" point. It also fires before the first model call of each user turn. [DOC: "Fires before the model is called"; loop ordering is inference]

(c) Seat identification
- stdin common fields on every event: `conversationId` (UUID), `workspacePaths`, `transcriptPath` (`~/.gemini/antigravity-cli/brain/<conversationId>/.system_generated/logs/transcript.jsonl`), `artifactDirectoryPath`, `modelName`, `lastUserInput`. [DOC]
- `conversationId` is the stable per-agent value; it survives `--conversation <id>` resume [inference]. The documented stdin carries no pid, tty, or session env.
- The binary contains `ANTIGRAVITY_CONVERSATION_ID`, `ANTIGRAVITY_TRAJECTORY_ID=`, and `ANTIGRAVITY_AGENT=1`. Whether hooks receive them (vs. only `run_command` terminals) is [UNV]. Parent-process chain and tty are [UNV] (the hooks never ran).
- With the owner's `agy` wrapper, the chain would include a python `agy-locked` parent above `agy-real` (`subprocess.Popen(args)`). [BIN: wrapper source line 607]

(d) Instruction files
- At each level from the workspace folder up to the repo root: `AGENTS.md` or `GEMINI.md`, `.agents/AGENTS.md` or `.agents/GEMINI.md`, and `.agents/rules/*.md` (frontmatter `trigger` required). Rules in subfolders below the workspace are not loaded. [DOC rules.md, customization-discovery.md]
- Global: `~/.gemini/AGENTS.md`, `~/.gemini/GEMINI.md`, `~/.gemini/config/AGENTS.md`, `~/.gemini/config/GEMINI.md`, and `~/.gemini/config/rules/*.md`. [DOC]
- Precedence: rules are "cumulative rather than replacement-based" (all concatenated). Neither doc mentions CLAUDE.md. [DOC]

(e) Not possible / caveats for mairc
- PostToolUse cannot inject context (documented output `{}`). Use **PreInvocation `injectSteps`** for mairc's "new messages" note instead. It runs once per model call, so a `mairc hook` that answers `{}` (or nothing) when idle still costs zero model tokens. Every model step pays the hook's process latency (default timeout 30 s). [DOC + design inference]
- Whether empty stdout is accepted silently, or must be `{}`, is [UNV]. The safe choice is `{}`, which injects nothing.
- `ephemeralMessage` is "transient". It probably disappears from later model calls, so a mairc note that must persist should use `systemMessage` [UNV].
- Community reports (not official) say IDE builds do not fire PostToolUse/Stop. The docs list hooks for App, CLI, and IDE. [UNV for IDE; CLI not run here]
- Sandbox run impossible: `HOME=<sandbox> agy-real -p …` requires an OAuth login ("Authentication required. Please visit the URL…", timed out). I did not copy the owner's OAuth token. **No model call was made**, so nothing about live hook output reaching the model is BIN-verified.

(f) Live check, agy 1.5.10, 17 Nov 2026 (mairc-2b3) — this settles several [UNV] items above
- Setup: a logged-in agy, `mairc install --only agy` (the `mairc` named hook in `~/.gemini/config/hooks.json`), one
  headless run (`agy -p … --output-format json`) started in its own terminal session: take a name with `irc_name`,
  wait 30 s in a shell command, then quote any line beginning with `irc` without calling `irc_read`. A second mairc
  client sent that name a private message during the wait.
- Result: the reply quoted the private message. The hook printed
  `{"injectSteps":[{"ephemeralMessage":"irc:\npm <sender>: <text>"}]}` on the `PreInvocation` before that model
  call, and `{"injectSteps":[{"ephemeralMessage":"irc: call irc_name to join"}]}` on `SessionStart`. [BIN live]
- `ephemeralMessage` reaches the model on the call it is injected before. That is all a chat line needs, so mairc
  uses it; whether it stays visible in later calls was not tested.
- Empty stdout is accepted: the idle `PreInvocation` runs printed nothing and the run ended `SUCCESS`. [BIN live]
- Hook input in 1.5.10 carries `hookEventName` and `cwd` as well as `conversationId`; `PreInvocation` adds
  `invocationNum` and `initialNumSteps`. The hook process gets `ANTIGRAVITY_CONVERSATION_ID` in its environment.
  The installed command still passes `--event`, so it does not depend on `hookEventName`. [BIN live]
- `PreInvocation` ran once before every model call (five in that run), each a separate process. [BIN live]
- Not proven: the interactive TUI (only the headless mode was run), the IDE and App builds, Windows, and whether an
  MCP server started by agy gets the conversation id in its environment.

### Commands used
```sh
cat /data/tmp/proudlion-compat/BRIEF.md
sed -n 1,700p ~/.local/bin/agy-locked                      # owner wrapper, read only
file -L ~/.local/bin/agy-real; ~/.local/bin/agy-real --version   # -> 1.4.2
export HOME=/data/tmp/proudlion-compat/agy-sandbox; cd $HOME
~/.local/bin/agy-real --help; agy-real mcp --help; agy-real mcp add --help; agy-real mcp list --help
~/.local/bin/agy-real plugin --help; agy-real help hooks   # -> "unknown subcommand: hooks"
~/.local/bin/agy-real mcp add mairc mairc stdio
~/.local/bin/agy-real mcp add --header "Authorization: Bearer TOKEN" mairc-http http://127.0.0.1:4747/mcp
~/.local/bin/agy-real mcp list; cat $HOME/.gemini/config/mcp_config.json
# real config, read only (key names only for mcp):
ls -la ~/.gemini ~/.gemini/config ~/.gemini/antigravity-cli; cat ~/.gemini/config/hooks.json
python3 -c "import json;d=json.load(open('/home/ubuntu/.gemini/config/mcp_config.json'));print({k:sorted(v) for k,v in d['mcpServers'].items()})"
strings -n 6 ~/.local/bin/agy-real > agy-sandbox/strings.txt
grep -E 'jetski/hooks_pb/hooks_go_proto\.\(\*[A-Za-z]+(Args|Result)\)\.Get' strings.txt   # hook proto fields
grep -o -E 'ANTIGRAVITY_[A-Z_]+' strings.txt | sort -u; grep -n 'hooks.json\|mcp_config.json' strings.txt
for p in hooks mcp rules plugins customization-discovery cli/gcli-migration cli/headless cli/using; do
  curl -sSL -A Mozilla/5.0 "https://antigravity.google/docs/$p.md"; done; curl -sSL https://antigravity.google/llms.txt
# probe hook (agy-sandbox/hooks/probe.sh + agy-sandbox/.gemini/config/hooks.json, all six events), then:
cd agy-sandbox/ws && HOME=/data/tmp/proudlion-compat/agy-sandbox timeout 60 ~/.local/bin/agy-real \
  -p "What is the secret word? Answer in one word." --print-timeout 45s   # -> OAuth required; no hook fired
```

### Minimal config snippet
Register the MCP server in `~/.gemini/config/mcp_config.json` (or `<workspace>/.agents/mcp_config.json`) [DOC][BIN]:
```json
{ "mcpServers": { "mairc": { "command": "mairc", "args": ["stdio"] } } }
```
Equivalent CLI: `agy mcp add mairc mairc stdio` [BIN]. HTTP alternative:
`{"mcpServers":{"mairc":{"serverUrl":"http://127.0.0.1:4747/mcp","headers":{"Authorization":"Bearer <token>"}}}}` [DOC][BIN].

Hook in `~/.gemini/config/hooks.json` (or `<workspace>/.agents/hooks.json`). PreInvocation output reaches the model only as
`{"injectSteps":[{"systemMessage":{"systemMessage":"…"}}]}` (or `{"ephemeralMessage":"…"}`), so `mairc hook` must emit that
JSON when there is news and `{}` / nothing when idle [DOC; live effect UNV]:
```json
{ "mairc": { "PreInvocation": [ { "type": "command", "command": "mairc hook", "timeout": 5 } ] } }
```

## Google Gemini CLI (0.50.0) — legacy

Google replaced Gemini CLI with Antigravity (`agy`); Gemini CLI stops serving Google AI Pro/Ultra and free Code
Assist users on 18 June 2026, while enterprise licences and paid API keys keep access (Google blog, 19 May 2026)
[DOC]. Researched on 0.50.0 (2026-11-17): MCP in `~/.gemini/settings.json` `mcpServers` (`httpUrl` + `headers`);
hook output reaches the model only as JSON `additionalContext` on `AfterTool`, `BeforeAgent` and `SessionStart`;
plain stdout goes to the user, and empty stdout makes it show stderr; seat id `GEMINI_SESSION_ID` [DOC][BIN].
Configs move to Antigravity with `agy plugin import gemini` [DOC][BIN].

## Grok Build (1.0.70)

Sources. Official web docs: https://docs.x.ai/build/features/hooks and https://docs.x.ai/build/settings/reference (read 2026-11-17, fetched through a summarizer; both are shorter than the shipped docs and leave out the hook-output details). The binary ships fuller, version-matched docs. "SHIPPED-DOC" below means `~/.grok/docs/user-guide/*.md` and `~/.grok/README.md`, installed with 1.0.70 (files dated 2026-10-24) and read 2026-11-17. These count as official vendor docs; they are cited as [DOC: file §section]. No official GitHub repo for the CLI was found; the only xAI repo seen is `github.com/xai-org/plugin-marketplace` (the marketplace source in config.toml).

### (a) MCP servers
- Config: `~/.grok/config.toml` (or `$GROK_HOME/config.toml`), table `[mcp_servers.<name>]`. Project `.grok/config.toml` files are read at each level from cwd up to the repo root; the deeper file wins, and a same-name server replaces the global one whole (no field merge). [DOC: 07-mcp-servers.md §Configuration, §Project-Scoped; docs.x.ai settings/reference] macOS and Windows paths: same `~/.grok` (UNVERIFIED: no platform-specific path documented).
- stdio: `command = "mairc"`, `args = ["stdio"]`, optional `env = {…}`, `enabled`, `startup_timeout_sec` (30), `tool_timeout_sec`. [DOC: 07 §stdio Transport] [BIN: `~/.grok/config.toml` has `[mcp_servers.agent-lsp] command=… args=[…] enabled=true`, and `grok inspect` lists `agent-lsp (stdio) config`]
- Streamable HTTP + header: `[mcp_servers.mairc]` with `url = "http://127.0.0.1:4747/mcp"` and `headers = { "Authorization" = "Bearer <tok>" }`. Web docs also list `bearer_token_env_var`. [DOC: 07 §HTTP/SSE Transport; docs.x.ai settings/reference] Values are stored verbatim, so use `${VAR}` in committed files. [DOC: 07 §CLI Management]
- CLI: `grok mcp add mairc -- mairc stdio`; `grok mcp add -t http mairc http://127.0.0.1:4747/mcp -H "Authorization: Bearer <tok>"`; `--scope user|project`; plus `list`/`remove`/`enable`/`disable`/`doctor`. [BIN: `grok mcp add --help`: "http: Connect to a remote server over streamable HTTP", "-H, --header <NAME: VALUE> HTTP header for remote servers (repeatable)", "Defaults to stdio, or to http when the positional argument is an http(s):// URL"]
- Compat: Grok also loads MCP servers from `~/.claude.json`, `.mcp.json` and `.cursor/mcp.json` (precedence config.toml > Claude > Cursor > .mcp.json). [DOC: 07 §Compatibility] [BIN: `grok inspect` shows `mcp-agent-mail (http) ~/.claude.json [claude]`; that entry is `type: http` with an `Authorization` header]. So a mairc entry written for Claude Code is picked up by Grok as well.
- MCP tool results are capped at 20,000 bytes by default (`GROK_MAX_MCP_OUTPUT_BYTES`). [DOC: 07 §stdio Transport note]

### (b) Hooks
- Hook sources: `~/.grok/hooks/*.json`, `<project>/.grok/hooks/*.json`, `[[hooks.<Event>]]` tables in `~/.grok/config.toml`, plugin `hooks.json`, and Claude Code compat files `~/.claude/settings.json`, `.claude/settings.json` and `settings.local.json` (on by default; `GROK_CLAUDE_HOOKS_ENABLED`, `[compat.claude] hooks`). [DOC: 10-hooks.md §Hook Locations; README §Hooks] [BIN: `grok inspect` in /data/projects/fleksprime lists `Hooks (6) … user [claude]`, which are the hooks in `~/.claude/settings.json`]
- Trust gate: user hooks, Claude-compat hooks included, run only in a trusted folder (`~/.grok/trusted_folders.toml`, `/hooks-trust` or `--trust`). A headless `-p` run treats the folder as trusted for user hooks only. [DOC: 10 §Hook Locations] [BIN: `grok inspect` in an untrusted sandbox dir prints `Project trusted: no` and `Hooks (None)`; in trusted fleksprime it prints `Hooks (6)`]
- Format (same as Claude): `{"hooks":{"PostToolUse":[{"matcher":"…","hooks":[{"type":"command","command":"mairc hook","timeout":10}]}]}}`. The handler type can also be `http` (POSTs the envelope; https or loopback http only). Optional `args` array, `env` map. [DOC: 10 §The Hook JSON Format, §HTTP Hooks]
- Events: `SessionStart`, `SessionEnd`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `PermissionDenied`, `Stop`, `StopFailure`, `StopCancelled`, `Notification` (`idle_prompt` and others), `SubagentStart`, `SubagentStop`, `PreCompact`, `PostCompact`. [DOC: 10 §Hook Events]
- Stdin: one JSON line with `hookEventName`, `sessionId`, `cwd`, `workspaceRoot`, `timestamp`, `permissionMode` and `promptId`, plus Claude snake_case aliases (`session_id`, `transcript_path`, `tool_name`, …). [DOC: 10 §Input, §Porting Claude Code stop hooks]
- Timeout: 5 s by default; 600 s for `Stop`, `SubagentStop`, `PostToolUse` and `PostToolUseFailure`. Every failure fails open. [DOC: 10 §Key Fields; docs.x.ai hooks "defaults to 5"]
- **Does stdout reach the model?** Only as JSON, and only on some events: [DOC: 10 §Output, §PostToolUse Output, §Stop Decision Control, §Passive Hooks, §UserPromptSubmit Decision Control]
  - `PostToolUse` / `PostToolUseFailure`: `{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"…"}}` (or `decision:"block"` + `reason`) is delivered right after the tool result, wrapped in `<system-reminder>`, in the same turn. Capped at 10,000 chars. An exit code other than 0 drops `additionalContext`. Exit 2 feeds stderr to the model.
  - `PreToolUse`: `additionalContext` is delivered with the batch's results.
  - `Stop`: `{"decision":"block","reason":"…"}` is fed back as a user message and the agent runs another round (at most 8 per turn). No output lets the turn end.
  - `SessionStart` and `Notification`: only a top-level `systemMessage` is read, and it is a user-facing note (no documented model delivery). The web page says passive hooks' "stdout is ignored".
  - `UserPromptSubmit`: "stdout of an allowing hook is discarded (no `additionalContext`)".
  - Plain non-JSON stdout on `PostToolUse`: UNVERIFIED (not documented; Claude Code ignores it too). mairc should always print the JSON form.
- Live check that a hook's `additionalContext` reaches the model: UNVERIFIED (not run). A test would mean editing the real `~/.grok/config.toml` or `~/.grok/hooks`, or copying `auth.json` into a sandbox `GROK_HOME`, which risks rotating the real token. The `GROK_CONFIG(_PATH)` overlay cannot add hooks: it is allowlisted to `models`/`features`/`toolset`/`shell_environment_policy`. [DOC: 05-configuration.md §Injecting config]
- Other: `[[ui.notifications.hooks]]` runs a command with `$GROK_EVENT`, `$GROK_MESSAGE` and `$GROK_SESSION_ID`. It is for user notifications only and nothing goes to the model. [DOC: 05 §Notification hooks]

### (c) Seat identification
- Every hook gets the env vars `GROK_HOOK_EVENT`, `GROK_HOOK_NAME`, `GROK_SESSION_ID`, `GROK_WORKSPACE_ROOT` and `CLAUDE_PROJECT_DIR`. These are reserved; the hook's `env` cannot override them. [DOC: 10 §Environment Variables; docs.x.ai hooks] [BIN: `strings` shows `GROK_HOOK_EVENTGROK_HOOK_NAMEGROK_SESSION_IDGROK_WORKSPACE_ROOTCLAUDE_PROJECT_DIR`]
- Stable seat key: `sessionId` (a UUIDv7, or the value passed with `-s/--session-id`), which is also `GROK_SESSION_ID`. It survives `--resume`, but `--fork-session` mints a new one. [DOC: 17-sessions.md; BIN: `grok --help` `-s, --session-id`, `--fork-session`]
- Telling Grok from Claude: when a Claude-compat hook runs under Grok, `GROK_HOOK_EVENT` is set; `CLAUDE_PROJECT_DIR` is set too, so it cannot tell them apart. [DOC: 10 §Environment Variables]
- Subagent events carry `subagentType`; skip them or key them to the parent. [DOC: 10 §Stop Decision Control]
- Parent-process chain and tty: UNVERIFIED (no live session was run). Caution: in leader mode (`--leader`, `[cli] use_leader`) and on the "daemon route" (`grok host-server`), hooks for many sessions run from one shared process. [DOC: 10 §Passive Hooks; 15-agent-mode.md] So a pid or tty chain is not a reliable seat id; use `GROK_SESSION_ID`.
- Env for the model's own bash tool (for `mairc` run as a command): UNVERIFIED. Docs only say it is filtered by `[shell_environment_policy]`.

### (d) Instruction files
- Names checked in each directory: `Agents.md`, `Claude.md`, `CLAUDE.md`, `CLAUDE.local.md`, `AGENT.md`, `AGENTS.md`. All matches load. [DOC: 12-project-rules.md §Supported File Names]
- Rules dirs `.grok/rules/*.md` (plus `.claude/rules`, `.cursor/rules`) and `~/.grok/rules/`.
- Order: home rules (`~/.grok`, then `~/.claude`, `~/.cursor`, `[paths] extra_rule_dirs`), then repo root down to cwd; deeper files come later and win. Startup loading needs folder trust. Each file is capped at 10,000 chars (README §AGENTS.md). `--rules` appends more. [DOC: 12 §How Discovery Works; README §AGENTS.md]
- [BIN: `grok inspect` in fleksprime lists `~/.claude/CLAUDE.md (global)`, `fleksprime/CLAUDE.md`, `fleksprime/AGENTS.md`]
- No `GROK.md`. [BIN: `strings` finds no `GROK.md`; finds AGENTS.md/CLAUDE.md/CLAUDE.local.md/AGENT.md/Agents.md/Claude.md]

### (e) Not possible, and what it means for mairc
- Silent zero-token injection works only through the JSON `additionalContext` on `PostToolUse` (or `PreToolUse`). `mairc hook` must print nothing when idle and `{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"…"}}` when there is news, and exit 0.
- No injection at `SessionStart` or `UserPromptSubmit`. An idle agent that makes no tool call gets nothing until its next tool call. The only turn-end path is a `Stop` block, which forces an extra model round, so use it only when there is unread mail.
- The double-fire risk: if mairc puts its hook in `~/.claude/settings.json`, Grok runs it too in trusted folders. `mairc hook` must key the seat on `GROK_SESSION_ID`/`sessionId` (and detect Grok through `GROK_HOOK_EVENT`) so a Grok session is not mistaken for a Claude seat. Alternatively, disable with `[compat.claude] hooks=false` or `GROK_CLAUDE_HOOKS_ENABLED=0`.
- In untrusted folders, no user or Claude-compat hooks run at all (only managed or requirements hooks).

### Commands used
```
cat /data/tmp/proudlion-compat/BRIEF.md
ls -la ~/.grok ~/.grok/bin ~/.grok/docs ~/.grok/bundled ~/.grok/installed-plugins
~/.grok/bin/grok --version            # grok 1.0.70 (f50904eaee6c)
~/.grok/bin/grok --help
~/.grok/bin/grok mcp --help ; ~/.grok/bin/grok mcp add --help
~/.grok/bin/grok plugin --help ; ~/.grok/bin/grok inspect --help
cat ~/.grok/config.toml                # read-only (no secrets in it)
cat ~/.grok/trusted_folders.toml ; cat ~/.grok/active_sessions.json
grep -n '^#' ~/.grok/README.md ; sed -n 1361,1860p ~/.grok/README.md
sed -n ... ~/.grok/docs/user-guide/{07-mcp-servers,10-hooks,12-project-rules,05-configuration}.md
grep -n ... ~/.grok/docs/user-guide/*.md    # GROK_SESSION_ID, leader, daemon route, GROK_CONFIG, shell_environment_policy
strings -n 6 $(readlink -f ~/.grok/bin/grok) | grep -oE '\bGROK_[A-Z0-9_]{3,}\b' | sort -u
strings -n 5 $(readlink -f ~/.grok/bin/grok) | grep -oE '(AGENTS|Agents|AGENT|CLAUDE|Claude|GROK|Grok)(\.local)?\.md' | sort | uniq -c
cd /data/tmp/proudlion-compat/grok-sandbox && grok inspect      # untrusted dir: Hooks (None)
cd /data/projects/fleksprime && grok inspect                    # trusted: Hooks (6) [claude], MCP from ~/.claude.json
python3 -I -c '...hooks keys of ~/.claude/settings.json; type/url/header names of mcp-agent-mail in ~/.claude.json'
awk '/^### 4.9/,/^### 4.10/' /data/projects/fleksprime/ops/interagent-communication-research-2026-11-10.md
WebSearch "xAI Grok Build CLI docs hooks MCP config.toml"; WebFetch docs.x.ai/build/features/hooks, docs.x.ai/build/settings/reference
```
No model call was made and no config, auth or HOME file was modified. The empty dir `/data/tmp/proudlion-compat/grok-sandbox` was created as the cwd for `inspect`.

## Devin CLI (3000.13.4)

Docs: the local tree `~/.local/share/devin/cli/_versions/3000.13.4/share/devin/docs/` matches docs.devin.ai/cli/... (read 2026-11-17).
Tags: [DOC] = official doc, [BIN] = installed binary or live sandbox run, [UNV] = unverified.

### (a) MCP servers
- Files: user `~/.config/devin/mcp_config.json` (`%APPDATA%\devin\mcp_config.json` on Windows), project `.devin/mcp_config.json`, local `.devin/mcp_config.local.json` (gitignored). Top key: `mcpServers`. Before v3000.3 the servers lived under `mcpServers` in `config.json`. Old entries migrate automatically. [DOC https://docs.devin.ai/cli/extensibility/mcp/configuration, 2026-11-17] [BIN `devin mcp add --help`: "local: … (.devin/mcp_config.local.json) / project: (.devin/mcp_config.json) / user: (~/.config/devin/mcp_config.json) [default: local]"]
- stdio: `{"mcpServers":{"mairc":{"command":"mairc","args":["stdio"],"env":{}}}}` [DOC]
- Streamable HTTP with a header: `{"mcpServers":{"mairc":{"url":"http://127.0.0.1:4747/mcp","transport":"http","headers":{"Authorization":"Bearer <TOKEN>"}}}}`. Streamable HTTP is tried first. A non-auth 4xx (404/405) falls back to SSE. A 401 or 403 with `WWW-Authenticate` is reported as auth-required. [DOC]
- `${env:VAR}` and `${file:/path}` expansion: documented for the OAuth fields and "Managing Secrets". Whether it works inside `headers` is [UNV] (not tested).
- CLI: `devin mcp add mairc -- mairc stdio` and `devin mcp add mairc http://127.0.0.1:4747/mcp -H "Authorization: Bearer <TOKEN>" [-s user|project|local]`. Other subcommands: `list`, `get`, `remove`, `login`, `logout`, `enable`, `disable`. [BIN `devin mcp --help`; `devin mcp add --help`: "-H, --header <HEADER: VALUE> HTTP headers"] (The web doc does not mention `-H`. The binary has it.)
- Devin also imports MCP servers from Claude Code files: `.mcp.json`, `.claude/settings*.json`, `~/.claude.json`, `~/.claude/settings*.json`, `~/.claude/mcp_servers.json`. The same applies to Cursor's `.cursor/mcp.json` and Windsurf's `mcp_config.json`. This is controlled by `read_config_from` (default on). [DOC https://docs.devin.ai/cli/reference/configuration/read-config-from]

### (b) Hooks
- Events: `PreToolUse`, `PostToolUse`, `PermissionRequest`, `UserPromptSubmit`, `Stop`, `PostCompaction`, `SessionStart` (`source: startup|resume`), and `SessionEnd` (not fired in cloud sessions). [DOC https://docs.devin.ai/cli/extensibility/hooks/overview and /lifecycle-hooks, 2026-11-17]
- The binary also contains the names `PreCompact`, `Notification` and `SubagentStop`. They are not documented. [UNV; `strings` only]
- Declaration: `.devin/hooks.v1.json` holds the whole object, for example `{"PostToolUse":[{"matcher":"","hooks":[{"type":"command","command":"mairc hook","timeout":5}]}]}`. The `hooks` key in `.devin/config.json`, `.devin/config.local.json` or `~/.config/devin/config.json` works too. Project files are searched from the cwd up to the repo root. `matcher` is a regex on `tool_name`. `timeout` is in seconds. The default timeout is [UNV]. [DOC] [BIN: a sandbox `.devin/hooks.v1.json` fired]
- **Claude Code hooks also fire in Devin**: Devin reads hooks from `.claude/settings.json`, `.claude/settings.local.json`, `~/.claude.json`, `~/.claude/settings.json` and `~/.claude/settings.local.json` when `read_config_from.claude` is on (the default). So a mairc hook installed for Claude in those files would also run in Devin seats. Hooks passed to Claude with `--settings` are not affected. [DOC]
- stdin is JSON. The live run captured these payloads [BIN]:
  - SessionStart: `{"hook_event_name":"SessionStart","source":"startup","session_id":"fantasy-stoplight"}` (no `prompt_id` before the first prompt)
  - PostToolUse: `{"hook_event_name":"PostToolUse","tool_name":"exec","tool_input":{…},"tool_use_id":"call_…","tool_response":{"success":true,"output":"…","error":null},"session_id":"fantasy-stoplight","prompt_id":"8ea11667-…"}`
  - Stop: `{"hook_event_name":"Stop","stop_hook_active":false,"last_assistant_message":"…","session_id":…,"prompt_id":…}`
- Exit codes: 0 = ok, 2 = block, any other code = logged error. A timeout or unparseable output also counts as an error. [DOC]
- **Stdout to the model** [DOC + BIN live test]:
  - JSON `{"hookSpecificOutput":{"hookEventName":"<Event>","additionalContext":"…"}}` is injected on `SessionStart`, `UserPromptSubmit` and `PostToolUse`. In the live `-p` run the model reported ALPHA-11 (SessionStart), BRAVO-22 (UserPromptSubmit) and CHARLIE-44 (PostToolUse), seen "in the user message".
  - Plain-text stdout is **not** injected. PLAIN-33 (UserPromptSubmit) and DELTA-55 (PostToolUse) were never seen by the model.
  - Empty stdout plus exit 0 is silent. The Stop hook printed nothing and the run finished normally. [BIN]
- Stop with `{"decision":"block","reason":"…"}` makes the agent continue with the reason [DOC]. This can wake an idle turn end, but it loops if overused.
- PostCompaction: the docs list no `additionalContext` support for it [DOC]. Whether output is injected there is [UNV].

### (c) Seat identification inside a hook
- Stable id: the `session_id` field in the stdin JSON. It is a word slug, not a UUID, and stays the same for all events of the session. `prompt_id` changes on every turn. [DOC][BIN]
- Env vars set by Devin: `DEVIN_PROJECT_DIR`, `CLAUDE_PROJECT_DIR` (both set to the project root) and `DEVIN_TRACEPARENT`. There is no session id or pid variable. The env otherwise comes from the launching shell. (In the test, the `CLAUDE_CODE_SESSION_ID`/`CLAUDE_PID` variables were inherited from the Claude Code parent, so do not trust `CLAUDE_*` to identify a CLI.) [BIN env dump]
- Process chain: `/bin/sh hook` → `devin acp --process-model=in-process` → `devin …` (the tty/pane process) → shell → tmux. On this host the live seats are `devin` (pts/11, pts/18) → `devin acp` child, with no shared daemon running. [BIN `ps`] In `--process-model daemon` mode hooks probably run under the shared daemon, so the pid chain would not identify the seat. [UNV]
- tty: the hook has none ("not a tty"). The grandparent `devin` process owns the pane tty. [BIN]
- Recommendation: key the seat on stdin `session_id`. Use the `devin` ancestor pid or tty only as a fallback.

### (d) Instruction files
- Always-on rules: `AGENTS.md` (recommended), `AGENTS.local.md`, `AGENT.md`, `CLAUDE.md` and `.windsurfrules`. Files at the workspace root load at session start. Files in subdirectories load lazily when the agent touches that directory. All of these files are treated identically, and the docs state no precedence order. [DOC https://docs.devin.ai/cli/extensibility/rules, 2026-11-17]
- `.devin/rules/*.md` (with `trigger` frontmatter) and `.devin/global_rules.md`. `.devin/` takes precedence over `.windsurf/` for `global_rules.md`. Both `rules/` directories are loaded. [DOC]
- Global: `~/.config/devin/AGENTS.md` (or `AGENT.md`), `~/.claude/CLAUDE.md`, `~/.devin/rules/*.md` and `~/.devin/global_rules.md`. On this host `~/.config/devin/AGENTS.md` is a symlink to `~/.codex/AGENTS.md`. [DOC][BIN `ls -la ~/.config/devin`]
- Devin also reads Cursor `.cursor/rules/*.md(c)` and Windsurf rules. [DOC]

### (e) Limits and what they mean for mairc
- `mairc hook` must print JSON (`hookSpecificOutput.additionalContext`), not bare text. Bare text is silently dropped. When there is nothing new, print nothing and exit 0. This costs zero tokens. [BIN]
- Injection works on PostToolUse (after each tool call), UserPromptSubmit and SessionStart. There is no documented injection on Stop or PostCompaction. A fully idle agent (no tool calls, no prompts) gets nothing unless a Stop hook uses `decision:block`. [DOC]
- The documented `-H` header flag and the `headers` key make Bearer-token HTTP straightforward. `devin mcp add` defaults to the *local* scope (`.devin/mcp_config.local.json` in the cwd), so pass `-s user` for a host-wide install. [BIN]
- Risk of double-firing: hooks in `~/.claude/settings.json` run in both Claude Code and Devin. Make the payload event-agnostic, or detect Devin from the `session_id` slug or the `devin acp` parent.

### Commands used
```
devin --version; devin --help; devin mcp --help; devin mcp add --help; devin auth --help
find ~/.local/share/devin -maxdepth 4 -type d -name docs
cat <docs>/extensibility/hooks/{overview,lifecycle-hooks}.mdx <docs>/extensibility/mcp/configuration.mdx <docs>/extensibility/rules.mdx <docs>/reference/configuration/read-config-from.mdx
ls -la ~/.config/devin ~/.local/share/devin ~/.local/share/devin/cli     # read-only
python3 -I -c '<print key paths of ~/.config/devin/config.json>'          # read-only
strings -n 4 <versions>/3000.13.4/bin/devin > scratch; grep -E 'CLAUDE_PROJECT_DIR|hookSpecificOutput|additionalContext|transcript_path' scratch
ps -eo pid,ppid,tty,etime,args | grep '[d]evin'
WebFetch https://docs.devin.ai/cli/extensibility/hooks/overview ; https://docs.devin.ai/cli/extensibility/mcp/configuration
# Sandbox test (one tiny headless prompt, swe-2-flash-high). HOME=/data/tmp/proudlion-compat/devin-sandbox/home,
# with a copy of credentials.toml (deleted afterwards). Hooks in devin-sandbox/proj/.devin/hooks.v1.json call devin-sandbox/hook.sh,
# which logs stdin, env and the ps chain to devin-sandbox/log/ and prints a JSON or plain codeword.
cd /data/tmp/proudlion-compat/devin-sandbox/proj && timeout 180 env HOME=…/home XDG_CONFIG_HOME=…/home/.config XDG_DATA_HOME=…/home/.local/share \
  devin --process-model in-process --respect-workspace-trust false --permission-mode dangerous -p "Run the shell command: true. Then list every codeword (format WORD-NN) …"
# -> ALPHA-11 / BRAVO-22 / CHARLIE-44 seen; PLAIN-33 / DELTA-55 not seen
```

## OpenCode (1.19.3)

Version caveat: the live site opencode.ai/docs (read 2026-11-17) now documents a NEWER plugin and config API (`mcp.servers.<name>`, `disabled`, a `plugins` array, `Plugin.define({id, setup})`). That API does NOT match the installed 1.19.3. Below, "DOC v1" means the docs shipped in the repo at tag v1.19.3: https://github.com/sst/opencode/tree/v1.19.3/packages/web/src/content/docs (plugins.mdx, mcp-servers.mdx, rules.mdx, config.mdx), read 2026-11-17. Copies are in /data/tmp/proudlion-compat/oc-docs/.

### (a) MCP servers
- Config files: global `~/.config/opencode/opencode.json` (also `.jsonc` and legacy `config.json`); `OPENCODE_CONFIG` (a file); project `opencode.json(c)` merged walking up from the cwd; `.opencode/` dirs; `OPENCODE_CONFIG_DIR`; `OPENCODE_CONFIG_CONTENT` (inline JSON, highest). System: `/etc/opencode/` on Linux; macOS uses MDM `ai.opencode.managed`. [DOC v1 config.mdx §Precedence order] [BIN: the strings include the loader for `config.json`, `opencode.json` and `opencode.jsonc` under Path.config, plus `OPENCODE_CONFIG`, `OPENCODE_CONFIG_DIR` and `OPENCODE_CONFIG_CONTENT`]
- Substitution: `{env:VAR}` and `{file:path}` inside JSON strings. [DOC opencode.ai/docs/config] [BIN: sandbox `debug config` masked the `{env:MAIRC_TOKEN}` header as `***`, so the header was parsed]
- stdio (verified in the sandbox with `opencode debug config` and `opencode mcp list`) [DOC v1 mcp-servers.mdx][BIN]:
  `"mcp": {"mairc": {"type": "local", "command": ["mairc", "stdio"], "environment": {"K": "V"}, "enabled": true}}`. Optional fields: `cwd`, `timeout` (ms, default 5000).
- Streamable HTTP with a header [DOC v1][BIN]:
  `"mcp": {"mairc": {"type": "remote", "url": "http://127.0.0.1:4747/mcp", "headers": {"Authorization": "Bearer {env:MAIRC_TOKEN}"}, "oauth": false, "enabled": true}}`.
  - OAuth auto-detection is on by default; set `oauth:false` for a bearer-only server. [DOC v1 §OAuth]
  - The transport tries StreamableHTTP first, then SSE, and sends `headers` on both. [BIN: `MCP.connectRemote` holds `[{name:"StreamableHTTP",...requestInit:{headers}},{name:"SSE",...}]`]
- CLI: `opencode mcp add [name] [--url U] [--header K=V] [--env K=V]`; `mcp list|auth|logout|debug`. [BIN `opencode mcp add --help`]
- This host already uses the shape in /data/projects/fleksprime/opencode.json: a `type:"remote"` server with `headers.Authorization`. [BIN, read only]

### (b) Hooks / plugins
- No declarative shell-command hooks. There are no config hook keys; the strings contain no legacy `experimental.hook` / `session_completed`. [BIN strings]
- Plugins are JS/TS modules [DOC v1 plugins.mdx §Load order][BIN]:
  - Locations: `~/.config/opencode/plugins/`, `.opencode/plugins/`, or npm entries in config `plugin`.
  - Signature: `export const P = async ({project, client, $, directory, worktree, serverUrl}) => ({...hooks})`.
  - 1.19.3 also accepts a default export `{id, server()}`.
  - Verified in the sandbox: a named-export plugin loaded, and `debug config` listed it under `plugin`.
- Hooks in 1.19.3 [BIN: SDK types in ~/.config/opencode/node_modules/@opencode-ai/plugin/dist/index.d.ts plus `trigger(...)` call sites in the binary]:
  - `tool.execute.before(input{tool,sessionID,callID}, output{args})`.
  - `tool.execute.after(input{tool,sessionID,callID,args}, output{title,output,metadata})`.
  - `event({event})`: bus events, including `session.created`, `session.idle`, `session.status`, `message.updated`, `permission.asked` (full list in DOC v1 §Events).
  - `chat.message`, `chat.params`, `shell.env(input{cwd,sessionID,callID}, output{env})`, `experimental.chat.system.transform(input{sessionID,model}, output{system[]})`, `experimental.chat.messages.transform`, `experimental.session.compacting`.
- Stdin, env, timeout: a plugin is in-process JS, so there is no stdin and no hook timeout. A plugin runs `mairc hook` itself through Bun `$` or `node:child_process` and decides what to do with stdout. [DOC v1][BIN]
- How text reaches the model (no "stdout injection" as such):
  1. `tool.execute.after`: appending to `output.output` changes the tool result the model sees. The binary passes the hook's output object straight into `completeToolCall`, which sets `state.output: N.output`. Fires after every built-in and MCP tool call. DOC v1 shows only `before` examples. [BIN code reading][UNV live]
  2. `experimental.chat.system.transform`: push strings into `output.system`. Runs in `LLMRequestPrep.prepare` before EVERY model request, which is every step of the loop. Push nothing and the context is unchanged, which gives zero idle cost. Experimental, so the API may change. [BIN][UNV live]
  3. On `event` `session.idle`: `client.session.prompt({path:{id}, body:{parts:[{type:"text",text}], noReply:true}})` adds a user message without a reply. Without `noReply`, or with `promptAsync`, it starts a new turn and so can wake an idle agent. [BIN: SDK `prompt`/`promptAsync` accept body `noReply`; the server returns early `if(t.noReply===!0)`][UNV live]
  4. `chat.message`: can append parts to the user's message. [BIN types]

### (c) Seat identification
- At startup the CLI sets `process.env.AGENT="1"`, `OPENCODE="1"` and `OPENCODE_PID=String(process.pid)`. [BIN: yargs middleware string]
- Sandbox probe results: the plugin ran in that same process (`pid == OPENCODE_PID`), `ppid` was the launcher, `directory` = cwd, and `serverUrl` was present. [BIN probe.log]
- Bash-tool children get `{...process.env, ...shell.env output}`, so `OPENCODE_PID` is visible in tool shells. [BIN `ShellTool.shellEnv`]
- No session-id env var exists (no `OPENCODE_SESSION_ID` in the strings). [BIN]
- Every hook receives `sessionID` in `input`. A plugin can pass it to `mairc hook` as an argument or env var. The `shell.env` hook can also export `MAIRC_SEAT` into every shell the agent runs. [BIN][DOC v1 §Inject environment variables]
- Stable seat: `OPENCODE_PID` (one per running opencode process), plus `sessionID` per conversation. The process has no TTY at the plugin level (`stdin.isTTY` = null in the `debug` run). Under the TUI, `$TMUX_PANE` is inherited from the launcher. [BIN; TUI tty UNV]

### (d) Instruction files
- Project: walk up from the cwd for `AGENTS.md`, else `CLAUDE.md`. [DOC v1 rules.mdx]
- Global: `~/.config/opencode/AGENTS.md`, else `~/.claude/CLAUDE.md`. [DOC v1 rules.mdx]
- The first match wins per category. [DOC v1 rules.mdx]
- `instructions: [paths|globs|URLs]` in config are added on top. [DOC v1 rules.mdx]
- The binary also accepts `CONTEXT.md` as a project filename (`["AGENTS.md","CLAUDE.md","CONTEXT.md"]`), which DOC v1 does not mention. [BIN]
- Opt-outs: `OPENCODE_DISABLE_CLAUDE_CODE`, `OPENCODE_DISABLE_CLAUDE_CODE_PROMPT`, `OPENCODE_DISABLE_PROJECT_CONFIG`. [DOC v1][BIN]

### (e) Not possible / consequences for mairc
- `mairc hook` cannot be declared as a command whose stdout is auto-injected. mairc needs a small OpenCode plugin (`.opencode/plugins/mairc.js`) that spawns `mairc hook --seat <OPENCODE_PID>:<sessionID>` and:
  - prints nothing (and so injects nothing) when stdout is empty, which keeps an idle chat at zero tokens;
  - otherwise appends stdout to `output.output` in `tool.execute.after`, or pushes it via `experimental.chat.system.transform`.
- Plugins run inside the CLI, so a slow `mairc hook` blocks the agent loop. Keep the hook fast or add a timeout in the plugin.
- The plugin API is mid-migration: the live docs describe v2 (`Plugin.define`, `ctx.tool.hook("execute.before")`). A mairc plugin may need two export shapes, as dcg-guard.js in `~/.config/opencode/plugins/` already ships. [BIN comment in that file; DOC opencode.ai/docs/build/plugins]
- Not run: a live model turn proving the injected text reaches the model (would need real provider auth copied into the sandbox; skipped per brief). [UNV]

### Commands used
```
cat /data/tmp/proudlion-compat/BRIEF.md
/home/ubuntu/.opencode/bin/opencode --version              # 1.19.3
opencode --help; opencode mcp --help; opencode mcp add --help; opencode run --help; opencode debug --help
cat /data/projects/fleksprime/opencode.json ~/.config/opencode/opencode.jsonc   # read only
head -c 3000 ~/.config/opencode/plugins/dcg-guard.js
cat ~/.config/opencode/node_modules/@opencode-ai/plugin/dist/index.d.ts
strings -n 8 /home/ubuntu/.opencode/bin/opencode > .oc-strings.txt   # then python3 regex for OPENCODE_*,
   #  "tool.execute.after", "experimental.chat.system.transform", noReply, McpLocalConfig, MCP.connectRemote, CLAUDE.md
curl -sL https://raw.githubusercontent.com/sst/opencode/v1.19.3/packages/web/src/content/docs/{plugins,mcp-servers,rules,config}.mdx
WebFetch opencode.ai/docs/{plugins,mcp-servers,rules,config,build/plugins}/   # 2026-11-17 (v2-era docs)
# sandbox (no model call): HOME/XDG_* -> /data/tmp/proudlion-compat/opencode-sandbox/home, cwd .../opencode-sandbox/proj
#   with opencode.json (local + remote mairc entries) and .opencode/plugins/probe.js (writes env/pid to probe.log)
opencode debug paths; opencode debug config; opencode mcp list; cat /data/tmp/proudlion-compat/opencode-sandbox/probe.log
```

## Prime Agent (0.10.0)

Sources. No Prime Agent page exists on docs.primeintellect.ai (checked `https://docs.primeintellect.ai/llms.txt`, 2026-11-17). The official docs are the GitHub repo https://github.com/PrimeIntellect-ai/prime-agent: README, `skills/mcp/SKILL.md`, and source at tag `v0.10.2` (read 2026-11-17). [DOC] below means that repo. The installed binary is 0.10.0, so source facts are for v0.10.2 unless also [BIN].

(a) MCP servers
- Global config: `$PRIME_AGENT_CODING_AGENT_DIR/settings.json`. The launcher defaults that env var to `~/.prime/agent`. [BIN: `cat ~/.local/bin/prime-agent`]
- Project config: `<cwd>/.prime/agent/settings.json`, deep-merged over the global file. [DOC: crates/pa-core/src/settings/manager.rs, `deep_merge(&global, &project)`]
- Key `mcpServers` maps a name to a config object with a `type` tag. [DOC: crates/pa-core/src/mcp/mod.rs `McpServerConfig`]
- The live config on this host already uses the HTTP shape with a header. [BIN: settings.json keys `.mcpServers.mcp-agent-mail.type='http'`, `.url`, `.headers.Authorization`; `prime-agent mcp list` -> `mcp-agent-mail: http`]
- Streamable HTTP + header (for mairc):
  `{"mcpServers":{"mairc":{"type":"http","url":"http://127.0.0.1:4747/mcp","headers":{"Authorization":"Bearer <tok>"}}}}`
  Other fields: `bearerTokenEnvVar` (alias `bearer_token_env_var`; a static token from an env var, no OAuth), `oauth`, `enabled`, `enabledTools`, `disabledTools`, `startupTimeoutMs`, `callTimeoutMs` (default 60 s), `clientId`, `callbackPort`. The source comment calls the transport "streamable-HTTP". [DOC: mcp/mod.rs; skills/mcp/SKILL.md]
- stdio:
  `{"mcpServers":{"mairc":{"type":"stdio","command":"mairc","args":["stdio"],"env":{"MAIRC_TOKEN":{"env":"MAIRC_TOKEN"}}}}}`
  In this shape, `env` values are references (`{"env":"<NAME>"}`) resolved from the kernel environment, not literal strings. [DOC: mod.rs `EnvRef`]
- CLI commands: `prime-agent mcp add|list|get|remove`. [BIN: `prime-agent help mcp`]
  - HTTP: `mcp add <name> --url <url> [--bearer-token-env-var <env>|--oauth] [--force]`
  - stdio: `mcp add <name> [--cwd <dir>] [--env CHILD=SOURCE] -- <command> [args...]`
  - There is no `--header` flag. A literal header needs a hand edit of the JSON. [BIN: `prime-agent help mcp add`]
- Big caveat: MCP tools are not native model tools. The model's only tool is the `ipython` REPL. It reaches MCP through the pre-imported Python `mcp` module (`mcp.list_tools`, `mcp.describe_tool`, `mcp.call_tool("mairc", "<tool>", {...})`), and results enter context only if printed. [DOC: skills/mcp/SKILL.md; BIN: `prime-agent prompt` -> "The prime-agent harness has one tool: `ipython`"]
- An ACP client can also pass session-scoped stdio/http servers with literal headers. [DOC: mod.rs `AcpMcpServerConfig`]

(b) Hooks, events, and injection paths
- No user-configurable hook or extension system exists in v0.10.2. The settings schema has no `hooks` or `extensions` key (`KNOWN_FIELDS` lists mcpServers, packages, skills, prompts, themes, autonomous, and others). [DOC: crates/pa-core/src/settings/load.rs; BIN: no hooks key in ~/.prime/agent/settings.json; `--help` lists no hook flag]
- Autonomous gates (`--autonomous --autonomous-gate <cmd>`, repeatable) are the closest thing to a turn-end hook. [BIN: `--help`; DOC: crates/pa-core/src/autonomous/gates.rs]
  - Each gate runs as a shell command in the session cwd after each turn, with a timeout (default 300000 ms) and retries (default 3).
  - Exit 0 means the gate passed. Its output is discarded and the run may end.
  - On a non-zero exit, stdout and stderr (capped at 6000 chars) go into the continuation text the model sees, up to the continuation, turn, and token limits.
  - Gates exist only in autonomous mode, which is not a normal interactive seat. Using one as a chat hook would mean "fail to inject", which spends a continuation each time.
- `prime-agent send [--from <agent>] <agent> <message> [--steer|--follow-up] [--json]` is an external push straight into an agent's context. [BIN: `prime-agent help send`]
  - The daemon queues the message on a steering or follow-up lane. [DOC: crates/pa-daemon/src/worker/input.rs]
  - Steering is polled after each turn completes, before the next LLM call. [DOC: crates/pa-agent/src/agent_loop/run.rs `settle_post_turn(poll_messages…get_steering_messages)`]
  - An idle session takes the message as its next run, so the send wakes it and costs a turn.
  - The daemon RPC equivalent is `{"type":"prompt","message":…,"streamingBehavior":"steer"|"followUp"}`. [DOC: crates/pa-daemon/src/rpc/protocol.rs]
- Background `bash()` completion: when a `bash()` command started by the model finishes, the harness sends a `[bash-done …]` user message, which starts a turn even when idle. [BIN: `prime-agent prompt` line "when a command is finished … a notification is sent to the agent"; `prime-agent list --all --json` shows an active turn labelled "Background command finished: [bash-done pid:… exit:0] …"]
- Heartbeats (`rlm_heartbeat.create(instruction, interval, delivery_mode="steer"|"follow_up")`, `/heartbeat`, `prime-agent schedule`) re-enter the session periodically. Every tick costs a turn, so they are not zero-cost when idle. [BIN: `prime-agent prompt` Heartbeat section]
- Python skills under `~/.prime/agent/skills` are importable into the kernel. They run only when the model calls them. [DOC: README "Skills are executable"]

(c) Seat identification (from a model-run shell or a gate, the only command contexts there are)
- Env seen in a live agent's `bash()` child [BIN: `/proc/<pid>/environ` names]:
  - `PRIME_AGENT_INTERNAL_DAEMON_WORKER_ACTIVE_SESSION_ID` (e.g. `e8f70644dae9`; the same short id `prime-agent list`/`send` use)
  - `PRIME_AGENT_KERNEL_OWNER_PID` (the worker pid)
  - `PRIME_AGENT_INTERNAL_DAEMON_WORKER_INSTANCE_ID`
  - `PRIME_AGENT_INTERNAL_SESSION_LEASE_OWNER_ID`
  - `PRIME_AGENT_INTERNAL_DAEMON_WORKER_CWD`, `PRIME_AGENT_SESSION_DIR`, `PRIME_AGENT_CODING_AGENT_DIR`, `PRIME_AGENT_DAEMON_SOCKET`
  - The `INTERNAL_` names are undocumented and may change.
- Process chain [BIN: `ps -o pid,ppid,tty`]: `bash -c …` -> `python -m rlm.repl` (kernel) -> `prime-agent worker` -> `prime-agent --mode daemon` (one shared daemon) -> first client `prime-agent` on pts/N.
  - The worker, kernel, and shell have no tty (`?`), so a tty does not identify a seat. The daemon is shared by all agents.
- Stable seat id: the active session id (env above, or `list --json` `.id`) plus the full `sessionId` UUID and `sessionFile` from `prime-agent list --all --json`. [BIN]
- `prime-agent list --all --json` also shows `cwd`, `activity`, `isStreaming`, and queued `steering`/`followUps`. A mairc sidecar could use these to pick a delivery mode. [BIN]

(d) Instruction files
- The candidates per directory are `AGENTS.md`, `AGENTS.MD`, `CLAUDE.md`, `CLAUDE.MD`, in that order; the first one found wins. [DOC: crates/pa-core/src/resources/mod.rs `CONTEXT_FILE_CANDIDATES`]
- Load order: the global file from agentDir (`~/.prime/agent/`) first, then every ancestor of cwd, root first. `-nc/--no-context-files` disables discovery. [DOC: same; BIN: `--help`]
- The system prompt is replaced by `<cwd>/.prime/SYSTEM.md`, else `agentDir/SYSTEM.md`, and appended to by `APPEND_SYSTEM.md` (same lookup). Flags: `--system-prompt`, `--append-system-prompt`. [DOC: resources/mod.rs]
  - UNVERIFIED: `CONFIG_DIR_NAME` is assumed to be `.prime`, based on project settings living at `.prime/agent/settings.json`.
- `prime-agent prompt --cwd /data/projects/fleksprime` shows the layer `project-context  AGENTS.md discovery  72515 chars`. [BIN]

(e) Not possible, and what it means for mairc
- There is no after-tool, session-start, or turn-end hook whose stdout reaches the model, so `mairc hook` cannot be wired as on Claude Code or Codex. [DOC][BIN]
- Gate output reaches the model only when the gate fails, and only in `--autonomous` runs. It is unusable as an idle-silent hook for interactive seats. [DOC]
- MCP is reachable, but only through `mcp.call_tool` in Python. The agent must poll mairc itself (e.g. per AGENTS.md instructions or a Python skill), and polling costs tokens. [DOC]
- Best zero-idle-cost push paths:
  1. A mairc-side notifier runs `prime-agent send --follow-up <session-id> "<chat digest>"` (or `--steer`) only when chat is new. This needs the seat-to-session-id map from (c).
  2. The agent starts a long-poll `bash("mairc wait --seat …")` that exits only on new chat; the `bash-done` notice then wakes it.
  - UNVERIFIED that path 1 or 2 delivers a mairc message end to end (not run: it would inject into live agents or cost a model call).
- UNVERIFIED: whether the project-scope `mcpServers` entry is honored by the kernel MCP bridge (merge seen in source only). Also unverified: the 0.10.0 vs 0.10.2 differences (only v0.10.2 source was read).

### Commands used
```
cat /data/tmp/proudlion-compat/BRIEF.md
awk '/^### 4.4/,/^### 4.5/' /data/projects/fleksprime/ops/interagent-communication-research-2026-11-10.md
cat ~/.local/bin/prime-agent                      # launcher: PRIME_AGENT_CODING_AGENT_DIR, daemon socket
prime-agent --version ; prime-agent --help
prime-agent help mcp|send|package|config|prompt|session ; prime-agent help mcp add
prime-agent mcp list
prime-agent list --all --json ; prime-agent status
prime-agent prompt --cwd /data/projects/fleksprime   # layer breakdown + harness text (no model call)
cat ~/.local/share/prime-agent/README.md
python3 -I -c '<print settings.json key paths, secrets redacted>'   # ~/.prime/agent/settings.json, read only
strings -n 6 ~/.local/share/prime-agent/prime-agent | grep -oE 'PRIME_AGENT_[A-Z0-9_]+' | sort -u
pgrep -af prime-agent ; tr '\0' '\n' < /proc/<bash-child>/environ | cut -d= -f1 | grep PRIME
ps -o pid,ppid,tty,comm -p <bash>,<kernel>,<worker>,<daemon>,<client>
gh api repos/PrimeIntellect-ai/prime-agent/contents/docs ; gh api repos/PrimeIntellect-ai/prime-agent/releases
gh search code --repo PrimeIntellect-ai/prime-agent mcpServers|bearerTokenEnvVar|AGENTS.md|steer|hooks
gh api 'repos/PrimeIntellect-ai/prime-agent/contents/<f>?ref=v0.10.2' for: crates/pa-core/src/mcp/mod.rs,
  crates/pa-core/src/resources/mod.rs, crates/pa-core/src/settings/{load,manager}.rs,
  crates/pa-core/src/autonomous/gates.rs, crates/pa-daemon/src/rpc/protocol.rs, crates/pa-daemon/src/worker/input.rs,
  crates/pa-agent/src/agent_loop/{mod,run}.rs, crates/pa-cli/README.md, skills/mcp/SKILL.md, CHANGELOG.md
WebFetch https://github.com/PrimeIntellect-ai/prime-agent ; https://docs.primeintellect.ai/ ; curl docs.primeintellect.ai/llms.txt
No model call made; no config written; no `send` issued to the live agents.
```

## Pi (Rust port, pi 0.8.0)

Only the Rust port of Pi Agent (pi_agent_rust) is supported; the TypeScript Pi is not. Facts from the installed binary
(`pi --help`, `pi --explain-extension-policy`) and the pi_agent_rust source at commit 2b4aca22 (16 Nov 2026), read
17 Nov 2026 [BIN][BIN source]:

- **MCP.** Precedence: `--mcp-config`, `.pi/mcp.json` (project; trusted workspaces only), `.agents/mcp.json`,
  `~/.pi/agent/mcp.json` (global), then other CLIs' files (`.claude/mcp.json`, `.codex/config.toml`, ...) and
  `mcp.json` / `.mcp.json` (src/mcp/config.rs). Key `mcpServers`; stdio `command`/`args`/`env`/`cwd`. MCP tools are
  native model tools.
- **MCP trust.** A configured server never spawns until the user acknowledges it with `/mcp trust <name>`; the
  decision binds to the server's fingerprint, so a changed command re-pends it; a pending server's tool answers
  `[MCP_TRUST_PENDING]` (src/mcp/trust.rs). `mairc install` prints that step. Servers start on use, not at startup:
  `pi --mode rpc` without a prompt started no `mairc stdio`.
- **Context files.** `AGENTS.md`, else `CLAUDE.md`, in `~/.pi/agent`, then each folder from the root down to the
  working directory (src/app.rs `load_project_context_files`).
- **Hooks.** No command hooks. Extensions are JS/TS run in embedded QuickJS, auto-loaded from
  `~/.pi/agent/extensions/` (project `.pi/extensions/` only with `--trust`). Events include `session_start`,
  `context` (before each provider call; can change messages), `tool_result` (after a tool runs; a handler that
  returns `{content}` replaces the result's content), `turn_end`, `agent_end`. `pi.sendMessage({customType, content,
  display}, {deliverAs: "steer" | "followUp" | "nextTurn", triggerTurn})` adds a message to the model context.
- **What an extension may do.** Default profile on this host: permissive. In the strict `safe` profile `read`,
  `write`, `http`, `events` and `session` are allowed and `exec` and `env` are denied, so a delivery extension must
  call the daemon over HTTP (loopback scores lower risk than public) and must not run `mairc hook` or read
  environment variables. `process.pid` inside QuickJS is the constant `1`, so the daemon's pid matching cannot
  work; the extension identifies itself by the Pi session id (`ctx.sessionManager.getSessionId()`) and the name
  from the agent's own `irc_name` result, the same session binding the other hooks use.

## OpenCode v2 (2.3.0): the chat plugin

Checked 17 Nov 2026 with the installed OpenCode 2.3.0, a probe plugin and a live run [BIN live]:

- A v2 plugin is a module whose default export is `{ id, setup(ctx) }`; no import is needed. OpenCode loads every
  `.js` file in `~/.config/opencode/plugins/` (user) and `.opencode/plugins/` (project) and runs plugins in Node.
- `ctx.tool.hook("execute.after", cb)`: the event is `{tool, sessionID, agent, messageID, id, input, status, result}`
  with `result = {output, content: [{type, text}]}`; what `content` holds after the hooks is what the model gets.
  mairc's MCP tools are named `mairc_<tool>` (`mairc_irc_name`), and the model may call them from inside OpenCode's
  `execute` code tool, whose own `execute.after` follows the inner ones.
- `ctx.session.hook("context", cb)`, before every model request: `{sessionID, model, system: [{type, text}],
  messages, options, agent, tools}`; parts pushed to `system` affect that request only.
- mairc's plugin (`internal/install/opencode_plugin.js`) runs `mairc hook --client plain` with the hook JSON on stdin
  (`session_id`, `tool_name`, `tool_response`). After a tool call it appends what is waiting to that tool's result. On
  mairc's own inner tools it delivers nothing, except `irc_name`, whose result binds the session. At a session's first
  model request it adds the hook's lines to the system prompt. `mairc hook` finds the agent from its ancestors
  (OpenCode's process is the shim's parent) and the session id, as for the other hook CLIs.
- Live: an agent named itself with `irc_name`, another agent sent it a PM, and in the next turn the PM was in a tool
  result the model quoted, without any `irc_read` call.
