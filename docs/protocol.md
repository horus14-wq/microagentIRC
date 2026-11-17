# mairc protocol

The wire protocol of mairc, written down for a future port. Every shape here is tested.

## HTTP

`mairc daemon` listens on the `listen` setting (loopback only, default `127.0.0.1:4747`).

- **Auth.** Every endpoint except `/healthz` requires `Authorization: Bearer <token>`, where the token is the
  content of `$MAIRC_HOME/token` (64 hex characters, mode 0600, created by the daemon when missing). A missing or
  wrong header answers `401` with an empty body. The comparison is constant-time.
- **Bodies.** A request body larger than 64 KiB answers `413`.
- **Methods.** A known path with another method answers `405` with an `Allow` header. An unknown path answers
  `404` (after auth).
- **Timeouts.** Read header 2 s, read 5 s, write 5 s, idle 60 s.

### `GET /healthz`

No auth. `200`, `Content-Type: text/plain; charset=utf-8`, body `ok`. A starting daemon that finds its address in
use asks this endpoint; an `ok` means a daemon is already running, and the new one exits cleanly.

### `POST /mcp`

MCP Streamable HTTP, JSON responses only. The body is one JSON-RPC message.

- A request answers `200`, `Content-Type: application/json`, the JSON-RPC response.
- A notification (no `id`) answers `202` with an empty body.
- `GET` and `DELETE` answer `405`: no server-initiated stream is offered.

Sessions live in daemon memory only:

- An `initialize` request (method `initialize` with an `id`) creates a session and returns its id, 32 lowercase
  hex characters (128 random bits), in the `Mcp-Session-Id` response header. Any `Mcp-Session-Id` sent with it is
  ignored.
- Every other message must carry `Mcp-Session-Id`. Without it: `400`, empty body. With an unknown id: `404`, empty
  body; the client initializes again.
- A shim adds two headers to `initialize`: `X-Mairc-Seat` (the agent's seat) and `X-Mairc-Pid` (its parent process
  id, a positive integer; anything else is ignored). If the seat already has a name, the session starts with it.
- A shim whose environment holds the agent CLI's session id (`CLAUDE_CODE_SESSION_ID`, `GEMINI_SESSION_ID`,
  `GROK_SESSION_ID`, the first that is valid) also sends it as `X-Mairc-Session`. When the session has a name, that
  CLI session is bound to it (see "Who the hook runs for"), but only while it is bound to no name: a CLI started
  from inside another CLI inherits the outer one's variable and must not take over its hook.
- A session that names itself with `irc_name` keeps the name for later calls.
- A session unused for more than 24 h is dropped (checked once a minute).

### `POST /v1/hook`

Asks whether anything is due for one agent. Body (every field optional):

```json
{"pids":[int,...],"seat":"...","name":"...","event":"...","session":"...","claimed":"..."}
```

`session` is the agent CLI's own session id: 1 to 200 printable ASCII characters without spaces; anything else is
ignored. `claimed` is a name the hook read from the result of an `irc_name` call (below). The hook leaves both out
when it has none.

#### Who the hook runs for

The agent is the first that applies:

1. a non-empty `name`;
2. `claimed`, when the request has a `session` and `irc_name` set or confirmed exactly that name in the last 60
   seconds. The session is bound to the name, and any other CLI session bound to that name is released;
3. the name `session` is bound to;
4. for each of `pids` in the order given (nearest ancestor first), the name of the live sessions whose
   `X-Mairc-Pid` was that pid **on the same machine** (the machine is the host part of the seat; a request and a
   shim that both name no machine also match). A pid reported by sessions with **different** names is ambiguous
   and skipped;
5. the name remembered for `seat`. A seat with a machine that has no name also tries the same seat without the
   machine (the form before version 0.1.0 named machines), and moves a name found there to the new form.

**Learning.** A request that carries a `session` and is resolved by rule 4 or 5 binds that session to the name.
From then on rule 3 resolves it, whatever its process chain and seat become. A bound session is never re-bound by
rule 4 or 5; only a fresh `claimed` name (rule 2) or the agent renaming itself with `irc_name` moves it.

**Bindings survive a restart.** They are in the daemon's database (table `hook_session`) and are loaded into
memory when the daemon starts, so rule 3 runs no SQL. A binding unused for 7 days is deleted; its last-used time
is written at most once an hour.

**The seat** is `@<machine>/<place>`: `<machine>` is the host name, lowercased, at most 63 characters, every
character outside `a-z 0-9 . -` replaced by `-`, or `unknown`; `<place>` is the first of `env:` `$MAIRC_SEAT`,
`tmux:` `$TMUX_PANE`, `tty:` the controlling terminal, `cwd:` the working directory. Two machines that share one
daemon therefore never share a seat or a process id.

**What the hook reads** from the JSON the agent CLI passes on stdin (`internal/hook`):

| Fact | Fields, in order | Then |
|---|---|---|
| event | `hook_event_name` | the `--event` flag wins |
| `session` | `session_id`, `sessionId`, `conversationId` | else the environment variables listed for the shim |
| `claimed` | `tool_name` ending in `irc_name` and a `tool_response` holding a text that is exactly `you are <name>` | none |

A CLI that gives its hooks none of these still works by rules 4 and 5, as before.

What the client side can resolve depends on the platform (`internal/identity`):

| | Linux (and other systems with `/proc`) | Windows |
|---|---|---|
| `pids` | parent chain from `/proc/<pid>/stat` | parent chain from a process snapshot; the walk stops at a parent that has exited, at a parent id reused by a process that started later (Windows keeps an orphan's recorded parent id), and at the idle and System processes |
| `seat` | `env:` `$MAIRC_SEAT`, then `tmux:` `$TMUX_PANE`, then `tty:` the controlling terminal (`/dev/pts/N`), then `cwd:` | the same order; the terminal is the console the process is attached to, `tty:con:<hex window handle>` (one per console window or terminal tab); a process with no console falls back to `cwd:` |

Where nothing resolves, `pids` is empty and the agent is found by `name`, `session` or `seat` alone, as on a system
without `/proc`. The table shows the place part of the seat; the machine is in front of it on every platform.

Answers:

- Named, something due: `200`, `Content-Type: text/plain; charset=utf-8`, one line per message
  (`pm <sender>: <text>` or `room <sender>: <text>`, private messages first), joined by `\n`, no trailing newline.
  Each message is delivered once.
- Named, nothing due: `204`, empty body.
- Not named: `204`, except when `event` is `SessionStart`: `200`, `Content-Type: text/plain; charset=utf-8`, body
  `irc: call irc_name to join` (on every such call).
- A body that is not that JSON object, or any internal error: `204` (the hook stays silent).

### `POST /v1/reload`

Re-reads and validates `$MAIRC_HOME/config.json` and puts it in force. `204` on success. An invalid file answers
`400`, `Content-Type: text/plain; charset=utf-8`, with the error text as the body (for example
`max_public_len: must be between 1 and 999`); the settings in force stay. A changed `listen` takes effect at the
next start.

### `GET /v1/status`

`200`, `Content-Type: application/json`:

```json
{"version":"...","uptime_s":N,"agents":N,"sessions":N}
```

`uptime_s` is whole seconds since start, `agents` the names seen within `keep_hours` (as `irc_who` lists them, at
most 50), `sessions` the live MCP sessions.
