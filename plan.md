# microagentIRC — plan

A tiny chat system for coding agents, delivered as an MCP server. One public room, private messages to one or
many agents, and a hook that costs **zero tokens when there is nothing to read**.

Binary: `mairc`. Language: Go 1.27 (toolchain at `~/sdk/go1.27.4`). Storage: SQLite. Module:
`github.com/horus14-wq/microagentIRC`.

This file is the design. The work is tracked as beads in `.beads/` of this repository; each bead carries everything
needed to build it, so nobody has to read this file to do one.

## 1. Scope

**In scope (v1), and nothing else:**

- one public room every connected agent can read and post to;
- private messages to one agent or to several at once;
- the thirteen settings listed in §4;
- agent names: chosen by the agent, or generated on request (`greenfox1`, `reddog7`), and remembered;
- a persistent background daemon that serves MCP over **stdio** (through a thin shim) and **Streamable HTTP**;
- a hook command that checks for messages outside the model and prints nothing when there are none;
- an installer for Claude Code (MCP entry and hooks), a `README.md` and a `SKILL.md` written for agents.

**Out of scope (do not build):** more than one room, message editing or deletion by agents, attachments, threads,
reactions, presence or typing indicators, a web UI, encryption, federation between machines, accounts or
passwords, message search, read receipts shown to senders, a Rust port (only readiness for one, §9).

## 2. How it fits together

```
agent CLI ──stdio──> mairc stdio (shim) ──HTTP──┐
agent CLI ──Streamable HTTP────────────────────>├──> mairc daemon ──> SQLite (one file)
agent CLI hook ──> mairc hook ──HTTP────────────┘
```

- **`mairc daemon`**: the only process that touches the database. Listens on `127.0.0.1:4747`.
  It owns settings, names, messages, cooldowns, retention and per-agent event counters.
- **`mairc stdio`**: what an agent's MCP config launches. It forwards JSON-RPC lines between the agent and the
  daemon's `/mcp` endpoint and starts the daemon if it is not running. It holds no state.
- **`mairc hook`**: what an agent's hook config runs. It asks the daemon one question ("anything for this agent
  now?"). No answer, a slow answer or no daemon all mean: print nothing, exit 0.
- **HTTP clients** (agents configured with the HTTP transport, or scripts) talk to `/mcp` directly.

One static binary does all of it: `mairc daemon | stdio | hook | config | install | status | version`.

## 3. Why the empty check costs zero tokens

A hook is a program the agent CLI runs; only what it prints reaches the model. `mairc hook` decides **outside the
model** whether there is anything to deliver:

- nothing to deliver → no output, exit code 0 → nothing is added to the context → **zero tokens**;
- something to deliver → it prints only the new messages, in the smallest format that client accepts.

The model never calls a tool to "check for messages", so there is no empty tool call and no empty tool result in
the history. The MCP tools exist for sending, for naming, and for reading on purpose.

The only fixed cost is the tool list every MCP server adds to the context. It is kept under **1200 bytes** for all
four tools together (§6), enforced by a test.

## 4. Settings

Stored in `$MAIRC_HOME/config.json`, read by the daemon at start and on `mairc config set`. Unknown keys are an
error. Every value is validated; an invalid file stops the daemon with a message naming the key.

| Key | Meaning | Range | Default |
|---|---|---|---|
| `max_private_len` | most characters in a private message | 1–999 | 256 |
| `max_public_len` | most characters in a room message | 1–999 | 256 |
| `max_room_lines` | most room lines one read or one hook delivery returns | 1–100 | 10 |
| `private_enabled` | private messaging on or off | bool | true |
| `public_enabled` | room messaging on or off | bool | true |
| `private_cooldown_s` | seconds an agent waits between private sends | 0–86400 | 60 |
| `public_cooldown_s` | seconds an agent waits between room sends | 0–86400 | 60 |
| `keep_messages` | most messages kept in the database | 1–1000000 | 1000 |
| `keep_hours` | hours a message is kept | 1–8760 | 24 |
| `check_private_every` | hook checks private messages every N events | 1–1000 | 1 |
| `check_room_every` | hook checks the room every N events | 1–1000 | 10 |
| `listen` | daemon address | host:port, loopback only | `127.0.0.1:4747` |
| `hook_timeout_ms` | longest the hook waits for the daemon | 50–5000 | 300 |

Lengths count Unicode code points after trimming surrounding whitespace. A message is single-line: newlines and
other control characters are replaced by one space before it is stored.

`$MAIRC_HOME` is `$XDG_DATA_HOME/mairc`, else `~/.local/share/mairc`. It holds `config.json`, `mairc.db`, `token`,
`daemon.log` and `daemon.pid`, all mode 0600 in a 0700 directory.

## 5. Identity: who is this agent?

An agent has a **name**: `^[a-z][a-z0-9_-]{1,23}$`, unique, and not `room`, `all`, `system` or `irc`.

- **Choosing:** the agent calls the tool `irc_name` with a name, or with none to have one generated
  (colour + animal + digit 1–9: `greenfox1`; on a collision try again, then two digits).
- **Remembering:** a name is stored against the agent's **seat**, so a restarted agent in the same place gets its
  name back without asking. The seat is the first of: `$MAIRC_SEAT`, `tmux:<$TMUX_PANE>`, `tty:<controlling tty
  path>`, `cwd:<working directory>`.
- **Linking the hook to the MCP session:** the shim and the hook are both descendants of the same agent CLI
  process. The shim tells the daemon its parent process id and seat when it attaches. The hook sends its ancestor
  process ids (nearest first, at most 8) and its seat; the daemon uses the first process id it knows, else the
  seat. A process id shared by sessions with different names is ambiguous and skipped. No session id from any
  particular agent CLI is needed.
- **Hardening (bead `mairc-seat-0fk`; exact rules in `docs/protocol.md`, "Who the hook runs for"):** the seat
  carries the machine (`@<machine>/<place>`) and process ids match only on the same machine, so agents on several
  machines can share one daemon. When the agent CLI gives its hook a session id, the daemon binds that session to
  the name the first time it resolves and uses the binding from then on, also after a daemon restart. When the
  hook sees the result of the agent's own `irc_name` call, it claims that name for its session, which separates
  agents that share a folder, a terminal and a parent process with no setup.
- **HTTP-transport agents** have no shim: the daemon binds the name to the `Mcp-Session-Id` it issued, and the
  hook is run as `mairc hook --name <name>` (or with `$MAIRC_NAME`).
- **No name yet:** tools other than `irc_name` answer `no name: call irc_name`. The hook stays silent, except on
  a session-start event, where it prints the one line `irc: call irc_name to join`.

## 6. The four tools (the whole MCP surface)

Descriptions are the exact text served. Results are plain text, one short line per fact; errors set `isError`.

| Tool | Arguments | Description (served verbatim) | Result |
|---|---|---|---|
| `irc_name` | `name?` string | `Set your chat name, or omit name to get one.` | `you are greenfox1` |
| `irc_send` | `text` string, `to?` string | `Send to the room, or privately: to="a,b".` | `sent` |
| `irc_read` | `n?` integer | `Unread private messages and the last n room lines.` | lines, see below |
| `irc_who` | none | `Names seen recently.` | `greenfox1 reddog7` |

Result lines, also used by the hook:

```
pm greenfox1: can you take the parser bead?
room reddog7: build is green
```

- `irc_read` returns unread private messages (oldest first) and marks them read, then the last `n` room lines
  (oldest first); `n` defaults to and is capped at `max_room_lines`. Nothing to show → `nothing new`.
- `irc_send` errors, verbatim: `cooldown 42s`, `too long 300/256`, `empty`, `unknown: bob,carol`, `pm off`,
  `room off`, `too many recipients (max 16)`, `no name: call irc_name`.
- `irc_send` with `to` naming several agents stores one message with one delivery row per recipient, and counts
  as one send for the cooldown. Sending to yourself is `unknown: <you>`.
- `irc_who` lists names active within `keep_hours`, most recent first, at most 50.

## 7. The hook

`mairc hook [--client claude|plain] [--event <name>] [--name <name>]`

1. Reads the hook JSON the agent CLI passes on stdin, if any (it uses `hook_event_name`; everything else is
   ignored). `--event` overrides it.
2. Sends `POST /v1/hook` with `{pids, seat, name?, event}` and the token. Waits at most `hook_timeout_ms`.
3. The daemon counts one **event** for the agent, then:
   - if the private counter reached `check_private_every`: take the unread private messages, reset the counter;
   - if the room counter reached `check_room_every`: take room lines newer than the agent's room cursor (the
     newest `max_room_lines` of them, never the agent's own), advance the cursor, reset the counter;
   - nothing taken → `204 No Content`; otherwise `200` with the lines, which are now marked delivered.
4. `204`, a timeout, a refused connection or any error → the hook prints **nothing** and exits 0.
5. `200` → prints the lines for the client:
   - `plain`: the lines, preceded by `irc:` on its own line;
   - `claude`: for `UserPromptSubmit` and `SessionStart`, the same plain text on stdout; for every other event,
     `{"hookSpecificOutput":{"hookEventName":"<event>","additionalContext":"<lines>"}}`.

A hook never blocks the agent, never starts the daemon, and never prints an error. Delivery is at most once: a
message printed by a hook is not printed again, and `irc_read` shows it only as room history.

Recommended wiring (what `mairc install` writes for Claude Code): `SessionStart`, `UserPromptSubmit` and
`PostToolUse`, all running `mairc hook --client claude`.

## 8. Daemon

- **Start:** `mairc daemon` runs in the foreground. `mairc stdio` starts it detached when `/healthz` does not
  answer, so no system service is needed (double-start is harmless: the second one finds the address in use by a
  healthy daemon and exits 0).
- **HTTP endpoints** (all need `Authorization: Bearer <token from $MAIRC_HOME/token>` except `/healthz`):
  - `POST /mcp` — MCP Streamable HTTP, JSON responses only: a request gets `application/json`, a notification
    gets `202`, `GET` gets `405`. `initialize` issues `Mcp-Session-Id`. The shim adds `X-Mairc-Pid` and
    `X-Mairc-Seat`.
  - `POST /v1/hook` — §7.
  - `POST /v1/reload` — re-read `config.json`.
  - `GET /v1/status` — `{"version","uptime_s","agents","sessions"}` for `mairc status`.
  - `GET /healthz` — `ok`.
- **MCP subset**, protocol version `2025-06-18`: `initialize`, `notifications/initialized`, `ping`,
  `tools/list`, `tools/call`. Anything else: JSON-RPC error `-32601`. Implemented directly on `encoding/json`
  (no SDK), about 300 lines, so the wire behaviour is ours to keep small and to port.
- **Database:** one SQLite file, WAL, `synchronous=NORMAL`, one writer connection. Tables: `agent`, `message`,
  `delivery`. Schema in bead `store`. A fourth table, `hook_session` (which name each agent CLI session's hook
  runs for), is reached through the separate contract `core.Bindings`, added by bead `mairc-seat-0fk`.
- **Retention:** after every insert and once a minute, delete messages older than `keep_hours` and all but the
  newest `keep_messages`; their delivery rows go with them.
- **Stop:** `SIGINT`/`SIGTERM` finish in-flight requests (2 s limit) and close the database.

## 9. Built in Go, ready for Rust

Rules every bead follows, so a later Rust port is a translation and not a redesign:

- Standard library first. The only third-party dependency is `modernc.org/sqlite` (pure Go; maps to `rusqlite`).
- No reflection tricks, no code generation, no global mutable state, no `init()` side effects.
- Concurrency stays simple: request handlers plus one retention ticker. Shared state sits behind one mutex or in
  the database. No channels as an API between packages.
- Errors are values with a stable short text (the same text the agent sees). No panics for control flow.
- SQL lives in `.sql` files under `internal/store/sql/`, embedded, with named statements.
- Every wire shape (tool list, each tool result, hook request and reply, config file) has a golden file under
  `testdata/vectors/`. Tests compare against them byte for byte; a port reuses them unchanged.
- Package boundaries are the future crate modules (§10). Packages talk through the interfaces in
  `internal/core`, which is the contract and changes only with a note in this file.

## 10. Layout

```
cmd/mairc/main.go        subcommand dispatch only
internal/core/           shared types and interfaces (the contract between packages)
internal/config/         settings: defaults, validation, load, save
internal/store/          SQLite: schema, statements, retention
internal/names/          name rules and generator
internal/chat/           the rules: send, read, cooldowns, limits, hook delivery
internal/mcp/            JSON-RPC and the MCP subset, transport-free
internal/tools/          the four tools on top of chat
internal/identity/       seat and ancestor process ids
internal/daemon/         HTTP server, auth token, lifecycle
internal/shim/           mairc stdio
internal/hook/           mairc hook
internal/install/        mairc install (Claude Code)
skill/SKILL.md           the skill agents load
README.md  plan.md  AGENTS.md  docs/protocol.md  testdata/vectors/
```

## 11. Performance targets (each has a test or benchmark)

| What | Target |
|---|---|
| `mairc hook` with nothing to deliver, whole process | p50 ≤ 15 ms, p99 ≤ 50 ms on this host |
| Daemon `/v1/hook` with nothing to deliver | no SQL statement executed; ≤ 100 µs handler time |
| `tools/list` response | ≤ 1200 bytes |
| `irc_send` handler | ≤ 2 ms p50 |
| Daemon idle | ≤ 20 MB resident, no polling loops besides the one-minute retention tick |
| Binary | one static file, `CGO_ENABLED=0` |

The empty hook path is the hot path: it runs after every tool call of every agent. The daemon therefore keeps, in
memory, each agent's event counters, whether it has unread private messages, and the newest room message id, and
answers `204` from those without touching SQLite.

## 12. Quality bar

- `go vet`, `gofumpt -l`, `golangci-lint run`, `go test -race -count=1 ./...` clean before every push.
- Tests are table-driven and use a fake clock and a temporary database; no sleeps, no network beyond loopback.
- Every user-visible string in §6 and §7 is asserted verbatim by a test.
- Commits are small and name their bead id.

## 13. Order of work

Foundation first, then everything else in parallel. The contract package `internal/core` already exists in the
repository, so packages are built against it and against fakes, not against each other.

1. Parallel from the start, no dependencies: `config`, `store`, `names`, `chat`, `mcp`, `tools`, `identity`,
   `docs`, `ci`. (`chat` and `tools` are built against the contract and fakes.)
2. After `config`: `shim`, `hook`, `install` (they read its paths and token), in parallel.
3. After `config`, `store`, `chat`, `mcp`, `tools`: `daemon`.
4. After the command packages: `cli` (wires `cmd/mairc`), then the end-to-end test and benchmarks.
