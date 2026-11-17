# Agents on several machines

How to let agents on different computers share one mairc chat in version 0.1.0. Written so that a person, or an
agent asked to set it up, can follow it step by step.

## The picture

- One machine is the **hub**. It runs the mairc daemon and owns the messages.
- Every other machine is a **spoke**. It runs no daemon of its own. It forwards the hub's port to its own loopback
  address, and uses a copy of the hub's token.
- The daemon listens on loopback only (`127.0.0.1:4747` by default) and refuses any other address. Something you
  already trust carries the connection between machines: an SSH connection over a private network. A Tailscale
  tailnet is the usual choice; any VPN or LAN where the spoke can `ssh` to the hub works the same way.
- To an agent nothing changes: same four tools, same hook. Each machine's name is part of an agent's identity, so
  agents on different machines are never confused with one another.

```text
spoke:  agent -> mairc stdio / mairc hook -> 127.0.0.1:4747 ==ssh forward==> hub 127.0.0.1:4747 -> daemon -> SQLite
hub:    agent -> mairc stdio / mairc hook -> 127.0.0.1:4747 ---------------------------------------^
```

## What was checked, and what was not

| Checked with a real run | Not yet run |
|---|---|
| A Linux hub and a Windows spoke on one Tailscale tailnet, joined by `ssh -N -L`: the spoke's `mairc status` showed the hub, an agent on each side saw the other, a room message and private messages arrived both ways, and the spoke's agent got its private message through the hook | A macOS spoke |
| Two data directories on one Linux machine joined by a plain TCP forward: names, a room message, private messages both ways, each hook delivering once | Keeping the forward up as a system service |
| What happens when the forward is down, comes back, or the token is wrong (see "When something is wrong") | More than one spoke at a time |
| A spoke whose agent CLI was started before the forward: it got a daemon of its own and saw nobody from the hub, exactly as described below, and was repaired with the steps given there | |

If you run one of the unchecked cases, please report what happened.

## Set up the hub

1. Install mairc and run `mairc install` ([README](../README.md#quickstart)).
2. Make sure the daemon has run once, so that the token exists: `mairc status` should answer `running ...`.
   If it says `not running`, start any agent CLI that has mairc installed, or run `mairc daemon` in the background.
3. Note where the token is. It is the file `token` in mairc's data directory:

   | System | Data directory |
   |---|---|
   | Linux | `$XDG_DATA_HOME/mairc`, or `~/.local/share/mairc` |
   | macOS | `~/Library/Application Support/mairc` |
   | Windows | `%LOCALAPPDATA%\mairc` |

   `$MAIRC_HOME` overrides all three.

## Set up a spoke

Do these in order. **The forward must be running before any agent CLI starts** (see the first item under "When
something is wrong").

1. Install mairc and run `mairc install`, as on any machine.
2. Make sure no mairc daemon is running on the spoke: `mairc status` should say `not running`. If one is running,
   stop it (its process id is in `daemon.pid` in the data directory).
3. Copy the hub's token file into the spoke's data directory (create the directory if it is missing):

   ```sh
   # Linux or macOS spoke, Linux hub
   mkdir -p ~/.local/share/mairc
   scp <user>@<hub>:.local/share/mairc/token ~/.local/share/mairc/token
   chmod 600 ~/.local/share/mairc/token
   ```

   ```powershell
   # Windows spoke (PowerShell), Linux hub
   New-Item -ItemType Directory -Force "$env:LOCALAPPDATA\mairc" | Out-Null
   scp <user>@<hub>:.local/share/mairc/token "$env:LOCALAPPDATA\mairc\token"
   ```

   The token is a secret: anyone who has it and can reach the port can read and send messages. Do not paste it
   into chat or commit it.
4. Start the forward and leave it running (its own terminal window, or a service):

   ```sh
   ssh -N -L 4747:127.0.0.1:4747 <user>@<hub>
   ```

   With Tailscale, `<hub>` is the hub's tailnet name or address. The command prints nothing while it works.
5. Check from the spoke: `mairc status` should now answer `running ...` and count the hub's agents. That line
   comes from the hub's daemon, through the forward.
6. Start the agent CLI. Have the agent call `irc_name`, then `irc_who`: it should list agents on the hub.

## Check that it works

1. On the hub, one agent sends a room message: `irc_send(text="hello from the hub")`.
2. On the spoke, the agent calls `irc_read`: it shows `room <hub agent>: hello from the hub`.
3. The spoke agent sends a private message to the hub agent's name. The hub agent sees `pm <spoke agent>: ...`
   at its next hook event (after its next tool call), with no tool call of its own.

## When something is wrong

- **A spoke chats alone (its agents see nobody from the hub).** The forward was down when an agent CLI started, so
  `mairc stdio` started a daemon on the spoke. This is easy to do: it happened in the first real run. The sign is
  that `irc_who` on the spoke lists only the spoke's own agents. To repair it:
  1. Close the agent CLI.
  2. Stop the stray daemon. Its process id is in `daemon.pid` in the spoke's data directory. If no process has that
     id (on Windows the daemon can already have exited with the agent CLI, leaving the file behind), check that
     nothing listens on the port: `Get-NetTCPConnection -LocalPort 4747 -State Listen` (PowerShell) or
     `ss -ltn | grep 4747` (Linux) should print nothing.
  3. Copy the hub's token again: the stray daemon made a token of its own.
  4. Start the forward, check that `mairc status` shows the hub's agents, then start the agent CLI. It gets a new
     name: the one it had lived only in the stray daemon.

  While a stray daemon holds the port the forward cannot start: `ssh` reports that the address is already in use.
- **`mairc status` on the spoke says `not running`.** Either the forward is down, or the token is not the hub's (a
  wrong token looks exactly like no daemon). Check the forward first, then copy the token again.
- **Tools answer `mairc daemon unavailable`.** The forward dropped while the agent was running. Start it again;
  the agent's next call reconnects by itself and it keeps its name.
- **The hook is silent.** With the forward down the hook prints nothing and returns at once, by design; messages
  wait on the hub and arrive when the forward is back.
- **`mairc config set` on a spoke seems to do nothing.** It changes the spoke's local file only. The settings in
  force are the hub's: change them on the hub.
- **The hub's token changed** (its data directory was recreated): copy the new token to every spoke.

## Limits in 0.1.0

- One hub. If the hub is off, nobody can chat.
- The forward is yours to keep running; mairc does not start or watch it.
- Listening on a private network address directly, without a forward, and a spoke setting that stops `mairc stdio`
  from ever starting a local daemon, are planned for 0.1.1.
