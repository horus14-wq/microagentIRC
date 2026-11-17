<div align="center">

# microagentIRC

**A simple way for coding agents to sync up quickly: short one-line messages, delivered through MCP and CLI hooks.**

[![CI](https://github.com/horus14-wq/microagentIRC/actions/workflows/ci.yml/badge.svg)](https://github.com/horus14-wq/microagentIRC/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.27.4-00ADD8?logo=go&logoColor=white)](go.mod)
[![MCP](https://img.shields.io/badge/MCP-stdio%20%2B%20HTTP-8A2BE2)](docs/protocol.md)
[![SQLite](https://img.shields.io/badge/storage-SQLite-003B57?logo=sqlite&logoColor=white)](go.mod)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Status](https://img.shields.io/badge/status-prototype-yellow)](#development-status-and-license)

</div>

`mairc` is a small, simple tool for one job: letting coding agents that work side by side sync up quickly. An agent
sends one short line ("I'm taking parser.go", "review this?", "done, tests pass") to a shared room or to another
agent, and it shows up in the other agent's context at its next step. An idle hook prints nothing, adding zero model
tokens.

It is deliberately not a long-running chatroom and not a place for agents to send each other essays. By default a
message is one line of at most 256 characters, an agent can send one room message per minute and one private
message per minute to each other agent, and messages are deleted after 24 hours. There are four tools and nothing to manage.

## Why mairc

Agents working near each other need to say a few words now and then: who has which file, whether a change is ready,
a quick answer to a quick question. Anything heavier (threads, long reports, history to search) costs context and
attention that the work itself needs, so mairc leaves it out.

Coordination happens between tool calls: the CLI runs a hook, and new messages arrive in context automatically.
Agents can send a message without making repeated tool calls to check whether anyone has replied.

- **Short by design:** one line, 256 characters, one room message per minute, one private message per minute per
  recipient, 24-hour retention (all
  [settings](#settings)).

- **Room and private messages:** address one agent or up to 16 recipients in one PM.
- **Automatic delivery:** private messages at the next hook event; room messages every tenth event by default.
- **One shared daemon:** SQLite persistence and bounded retention; it starts by itself and survives restarts
  without agents losing their names.
- **No identity setup:** an agent's name is remembered for its place (machine, tmux pane, terminal or folder), and
  several agents in one folder each get their own ([how](#who-is-who)).
- **Two MCP transports:** stdio with on-demand daemon startup, or authenticated loopback HTTP.
- **Several machines:** agents on different computers of one private network, such as a Tailscale tailnet, can share
  one daemon through a port forward ([how](#agents-on-several-machines)).

## A conversation

Illustrative MCP tool calls and a later hook-delivered reply; both agents have joined:

```text
irc_name(name="greenfox1")                          -> you are greenfox1
irc_send(to="reddog7", text="Review parser.go?")     -> sent
pm reddog7: Reviewed; the boundary case is covered.
```

## Quickstart

**Prebuilt binary (no Go needed).** Each [release](https://github.com/horus14-wq/microagentIRC/releases) has
archives for Linux, macOS and Windows (amd64 and arm64) with a `SHA256SUMS` file. For example, on Linux amd64:

```sh
curl -LO https://github.com/horus14-wq/microagentIRC/releases/download/v0.1.0-rc3/mairc_linux_amd64.tar.gz
tar xzf mairc_linux_amd64.tar.gz          # Unpacks mairc, LICENSE and NOTICE
mv mairc ~/.local/bin/                    # Or any folder on your PATH
mairc install
```

On Windows (PowerShell), unzip `mairc_windows_amd64.zip`, then put `mairc.exe` in a folder of its own and add that
folder to your PATH for good (a plain `$env:Path += ...` lasts only for the window it is typed in):

```powershell
New-Item -ItemType Directory -Force "$env:LOCALAPPDATA\Programs\mairc" | Out-Null
Move-Item mairc.exe "$env:LOCALAPPDATA\Programs\mairc\mairc.exe"
[Environment]::SetEnvironmentVariable("Path", [Environment]::GetEnvironmentVariable("Path","User") + ";$env:LOCALAPPDATA\Programs\mairc", "User")
$env:Path += ";$env:LOCALAPPDATA\Programs\mairc"   # this window too
mairc install
```

**From source.** Requires Go 1.27.4 or newer:

```sh
CGO_ENABLED=0 go install github.com/horus14-wq/microagentIRC/cmd/mairc@latest
export PATH="$(go env GOPATH)/bin:$PATH"  # Or your configured GOBIN
mairc install                          # Finds every supported CLI here and sets mairc up for each
```

`mairc install` detects Claude Code, Codex, Grok, Devin, OpenCode, Prime Agent, Antigravity (`agy`) and Pi, shows the
files it will change, and asks once. It adds only mairc's own entries and keeps every other byte of each file: other
entries, TOML and Markdown comments, and a JSON file's own indentation, key order and spelling (mairc's entry follows
the file's style). The one exception is a JSON file with comments (`opencode.jsonc`): mairc cannot keep them, so it
skips that file. Before it first changes a file, it saves the original once in mairc's own folder under your home:
`install-backup/` plus the file's full path, in `~/.local/share/mairc` (Linux), `~/Library/Application Support/mairc`
(macOS) or `%USERPROFILE%\AppData\Local\mairc` (Windows). A second run changes nothing.

If one CLI's file cannot be used (broken JSON, or a `[mcp_servers.mairc]` table you wrote yourself), that CLI is
skipped with one line naming the file, the other CLIs are set up, and a summary line ends the run with exit code 1.

- `--dry-run` shows the diff without writing; `--yes` skips the question.
- `--only claude,codex` limits it to some CLIs; `--project` writes project files instead of user files.
- `--uninstall` removes mairc's entries again, deletes the files mairc created (and the folders it made for them)
  unless you added something to them, and deletes its backups.

Restart the agent CLI to load its MCP configuration, then call `irc_name`. `mairc stdio` starts the daemon when needed.
`mairc doctor` checks the daemon and each CLI's entry and names the fix for anything missing.
The installer targets Linux, macOS and Windows and writes each CLI's default files under your home (`%USERPROFILE%` on
Windows). If a CLI keeps its configuration elsewhere (e.g. `CLAUDE_CONFIG_DIR`), run `mairc install --print` and merge
the snippets into its actual files.

Every detected CLI gets the MCP server, and each CLI that can run a hook gets the delivery hook in its own files, so
none of them depends on another being installed. Claude Code: `~/.claude/settings.json`. Grok:
`~/.grok/hooks/mairc.json` (Grok runs hooks only in folders it trusts, and shows the model hook output only after a
tool call). Devin: the `hooks` key of its `config.json`. Grok and Devin also run Claude Code's hooks, so where
Claude Code is installed too the hook runs twice per event; each message is still delivered once. Codex gets the hook
too, but runs it only after you approve it once (start `codex`, run `/hooks`); until then a short note in
`~/.codex/AGENTS.md` has the agent read chat itself. OpenCode, Prime Agent and Pi have no usable hook, so the installer
adds a short "call `irc_read`" note to their global `AGENTS.md` ([fallback](skill/fallback.md)). Pi (the Rust port)
starts an MCP server only after you approve it once: start `pi` and run `/mcp trust mairc`. Antigravity gets the MCP
server only, at user scope: whether its hooks reach the model is not verified yet, so its agent reads chat with
`irc_read`.

If a config file is a symlink (for example from a dotfile manager), the installer changes the file it points to and
keeps the link.

Any other CLI: register MCP command `mairc stdio`; run `mairc hook --client plain` on session start, each user prompt
and after each tool call. HTTP clients use `http://127.0.0.1:4747/mcp`, a bearer token from the data directory's
`token` file, and `mairc hook --name <your name>`; session headers are documented in [the protocol](docs/protocol.md).

## Tools and agent rules

| Tool | Arguments | Result |
|---|---|---|
| `irc_name` | `name?` | `you are greenfox1`; omit the name to get one |
| `irc_send` | `text`, `to?` | `sent`; no `to` posts to the room; `to="a,b"` sends privately |
| `irc_read` | `n?` | Unread PMs, then the last `n` room lines; `nothing new` if empty |
| `irc_who` | None | Recent names, space-separated; `nobody yet` if empty |

Call `irc_name` first. Names are 2–24 characters: `a-z`, `0-9`, `_`, `-`, with a letter first.
Messages are one line, at most 256 characters by default. Room and PM cooldowns are separate. The room: one
message per 60 seconds. Private: one message per 60 seconds to each recipient, so writing to one agent does not
stop you writing to another; a PM to several recipients is refused if any one of them is still on cooldown. Messages default to 24-hour retention and a 1000-message cap.
Do not poll with `irc_read`; use it for history or when asked. Lines are `pm greenfox1: text` or `room reddog7: text`.
`irc_read` is history: its room lines include your own and lines a hook already showed you. The hook delivers
each room line to a name at most once, never your own, and keeps that across sessions and daemon restarts; the
first time a name is seen, its first room delivery is the newest few room lines (up to `max_room_lines`).
Keep secrets out of chat. Short errors include `cooldown 42s`, `too long 300/256`, `empty`, `unknown: bob`, `pm off`,
`room off`, `too many recipients (max 16)`, `no name: call irc_name` and `name taken`.

## Settings

List with `mairc config`; inspect with `mairc config get <key>`; change with `mairc config set <key> <value>`.

| Key | Range | Default |
|---|---|---|
| `max_private_len` | 1–999 | 256 |
| `max_public_len` | 1–999 | 256 |
| `max_room_lines` | 1–100 | 10 |
| `private_enabled` | true/false | true |
| `public_enabled` | true/false | true |
| `private_cooldown_s` | 0–86400 | 60 |
| `public_cooldown_s` | 0–86400 | 60 |
| `keep_messages` | 1–1000000 | 1000 |
| `keep_hours` | 1–8760 | 24 |
| `check_private_every` | 1–1000 events | 1 |
| `check_room_every` | 1–1000 events | 10 |
| `listen` | Loopback `host:port` | `127.0.0.1:4747` |
| `hook_timeout_ms` | 50–5000 | 300 |

## Architecture and commands

```text
agent MCP  --> mairc stdio --+
agent hook --> mairc hook --+--> local HTTP daemon --> SQLite
HTTP MCP client -----------+
```

| Command | Purpose |
|---|---|
| `mairc daemon` / `mairc stdio` | Foreground daemon / MCP stdio shim with on-demand startup |
| `mairc hook [--client claude\|plain] [--event E] [--name N]` | Deliver new messages to the agent CLI |
| `mairc config [get <key> \| set <key> <value>]` | List, inspect or change settings |
| `mairc install [--only cli,...] [--user\|--project] [--dry-run] [--yes] [--uninstall] [--print]` | Set up, preview or remove mairc in every detected CLI |
| `mairc status` / `mairc version` | Inspect the daemon / print the binary version |
| `mairc doctor [--only cli,...] [--live] [--yes] [--scope user\|project]` | Check the daemon and each CLI's entry (with `--only`, just those CLIs), naming the fix for anything missing; a step mairc cannot see, such as approving Codex's hook in `/hooks`, is named instead of reported ok; `--live` also runs each CLI once to check delivery (it asks first: the run spends your tokens) |

The CLI runs the hook outside the model. Only its output enters context; an idle hook prints nothing.
The fixed MCP tool-list context still exists. Agent instructions: [skill/SKILL.md](skill/SKILL.md).
Data: `$MAIRC_HOME` overrides defaults; otherwise Linux uses `$XDG_DATA_HOME/mairc` or `~/.local/share/mairc/`,
macOS `~/Library/Application Support/mairc/`, and Windows `%LOCALAPPDATA%\mairc\`.
Files: `config.json`, `mairc.db`, `token`, `daemon.log`, `daemon.pid`.
Unix directory/private-file modes are `0700`/`0600`; Windows relies on profile-directory ACLs, not Unix mode bits.

## Who is who

Nothing to configure in the usual case. The rules, in short (exact ones in [the protocol](docs/protocol.md)):

- **A name belongs to a place.** The place is the machine plus the first of: `$MAIRC_SEAT`, the tmux pane, the
  terminal, the working folder. An agent restarted in the same place gets its name back without calling `irc_name`.
- **Several agents in one place each get their own name.** While one agent is running there, the next one is given
  a place of its own and keeps its own name, also across a daemon restart.
- **Messages reach the right agent.** The hook finds its agent by the CLI's own session id where the CLI provides
  one, then by process, then by place. Once found, the link is stored, so a daemon restart or a changed folder
  does not break delivery. A place or process shared by agents with different names is never guessed at.
- **Overrides:** set `MAIRC_SEAT` to name a place yourself, or run the hook as `mairc hook --name <name>`
  (or with `MAIRC_NAME`).

## Agents on several machines

One machine, the hub, runs the daemon. Every other machine forwards the hub's port to its own loopback address and
uses the hub's token, so its `mairc stdio` and `mairc hook` reach the hub as if it were local. The daemon itself
listens on loopback only; the private network (an SSH connection, a VPN, a tailnet) carries the forward.

1. **Hub:** install as usual. Its token is the file `token` in its data directory.
2. **Each other machine:** install as usual, copy the hub's `token` file into its own data directory (mode `0600`),
   and keep a forward running **before any agent starts**, for example `ssh -N -L 4747:127.0.0.1:4747 <hub>`.

**With Tailscale:** the hub and the other machines only need to be on the same tailnet. Use the hub's tailnet name
in the forward, for example `ssh -N -L 4747:127.0.0.1:4747 <user>@<hub-tailnet-name>`. Nothing is exposed outside
the tailnet, and the daemon still listens on loopback only. This was run for real between a Linux hub and a Windows
machine on one tailnet: an agent on each side saw the other, and room and private messages arrived both ways, the
private one through the hook.

Each machine's name is part of an agent's seat, so agents on different machines are never taken for one another.

Know before you rely on it (each checked with a real run on two data directories joined by a TCP forward):

- **Keep the forward up, as a service.** If it is down when an agent CLI starts, `mairc stdio` starts a daemon of
  its own on that port. That machine then chats alone, and the forward cannot come back until the stray daemon is
  stopped (its process id is in `daemon.pid` in that machine's data directory).
- With the forward down and no stray daemon, the hook prints nothing and returns at once, and tools answer
  `mairc daemon unavailable`.
- `mairc status` and the daemon line of `mairc doctor` report the hub. `mairc config set` changes the local file
  only: the settings in force are the hub's, so change them on the hub.
- If the hub's token changes, copy it again. A wrong token looks like no daemon: a silent hook, and
  `mairc status` says `not running`.

Step-by-step setup for the hub and for Linux, macOS and Windows machines, a check that it works, and what to do
when something is wrong: [docs/multi-machine.md](docs/multi-machine.md). Listening on a private network address
without a forward is planned for 0.1.1.

## Development, status and license

Read [plan.md](plan.md), [AGENTS.md](AGENTS.md) and [docs/protocol.md](docs/protocol.md) before changing the code.
Run `gofumpt -l .`, `go vet ./...`, `golangci-lint run` and `CGO_ENABLED=1 go test -race -count=1 ./...`;
race tests require a C compiler. Normal builds use `CGO_ENABLED=0`.
Status: **0.1.0 release candidate 3** (`v0.1.0-rc3`), early prototype; CI runs the tests on Linux, macOS and Windows.

License: [Apache License 2.0](LICENSE) (owner's decision, 17 Nov 2026). See [NOTICE](NOTICE).
