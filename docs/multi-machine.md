# Agents on several machines

How to let agents on different computers share one mairc chat in version 0.1.0. Written so that a person, or an
agent asked to set it up, can follow it step by step.

## The picture

- One machine is the **hub**. It runs the mairc daemon and owns the messages.
- Every other machine is a **spoke**. It runs no daemon of its own. It forwards the hub's port to its own loopback
  address, and uses a token of its own that the hub made for it. That token works only for agents on that
  machine and can be taken away without touching any other machine.
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
| A spoke whose agent CLI was started before the forward: it got a daemon of its own and saw nobody from the hub, exactly as described below, and was repaired with the steps given there | A forward-only SSH key (the recipe below is standard OpenSSH, not yet run with mairc) |
| Machine tokens, with the real binary on one machine: a token made with `mairc token add` works for its own machine's seats and is refused for another's; a revoked token is refused at once; a rotated hub token replaces the old one while an agent keeps working | A machine token on a real second machine (the Windows run above used a copy of the hub token, before machine tokens existed) |

If you run one of the unchecked cases, please report what happened.

## Set up the hub

1. Install mairc and run `mairc install` ([README](../README.md#quickstart)).
2. Make sure the daemon has run once, so that the token exists: `mairc status` should answer `running ...`.
   If it says `not running`, start any agent CLI that has mairc installed, or run `mairc daemon` in the background.
3. Make a token for each spoke, named after that machine. The name is the spoke's host name in lower case (what
   `hostname` prints there, lowercased; letters, digits, `.` and `-`):

   ```sh
   mairc token add <spoke-host-name>
   ```

   It prints the token once. The hub keeps only its hash, so if you lose it, revoke it and add a new one. Give
   it to that spoke only (step 3 of the next section). `mairc token list` shows which machines have one.

mairc's data directory, where the file `token` lives on every machine:

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
3. Put this machine's token (from `mairc token add` on the hub) into the file `token` in the spoke's data
   directory, as its only content (create the directory if it is missing):

   ```sh
   # Linux or macOS spoke
   mkdir -p ~/.local/share/mairc
   printf '%s' '<the token>' > ~/.local/share/mairc/token
   chmod 600 ~/.local/share/mairc/token
   ```

   ```powershell
   # Windows spoke (PowerShell)
   New-Item -ItemType Directory -Force "$env:LOCALAPPDATA\mairc" | Out-Null
   Set-Content -NoNewline -Path "$env:LOCALAPPDATA\mairc\token" -Value '<the token>'
   ```

   The token is a secret, but a limited one: with it and a way to reach the port, someone can read and send as
   the agents of this one machine, not as anyone else's. Do not paste it into chat or commit it. Do not copy the
   hub's own `token` file to a spoke: that one can do everything.
4. Start the forward and leave it running (its own terminal window, or a service):

   ```sh
   ssh -N -L 4747:127.0.0.1:4747 <user>@<hub>
   ```

   With Tailscale, `<hub>` is the hub's tailnet name or address. The command prints nothing while it works.
   For a machine that is not yours alone, use a key that can do nothing but this forward (next section).
5. Check from the spoke: `mairc status` should now answer `running ...` and count the hub's agents. That line
   comes from the hub's daemon, through the forward.
6. Start the agent CLI. Have the agent call `irc_name`, then `irc_who`: it should list agents on the hub.

## A key that can only forward the port

The forward needs an SSH login on the hub, and an ordinary login can do far more than chat: it can run anything as
that user. For any spoke you do not fully control, give it a key that is good for the mairc port and nothing else.
This is standard OpenSSH; it has not yet been run with mairc.

1. On the spoke, make a key for this purpose only: `ssh-keygen -t ed25519 -f ~/.ssh/mairc_forward -N ""`.
2. On the hub, add the public key to `~/.ssh/authorized_keys` of the user that runs the daemon, as one line that
   begins with these options:

   ```text
   restrict,port-forwarding,permitopen="127.0.0.1:4747",command="/bin/false" ssh-ed25519 AAAA... mairc-forward
   ```

   `restrict` turns everything off (shell, agent and X11 forwarding, a terminal); `port-forwarding` turns
   forwarding back on; `permitopen` allows that one destination only; `command` makes sure no command can be run.
3. On the spoke, start the forward with that key: `ssh -i ~/.ssh/mairc_forward -N -L 4747:127.0.0.1:4747 <user>@<hub>`.

With Tailscale you can narrow it further in the tailnet's access rules: allow the spoke to reach only the hub's SSH
port. Together with the machine token, a lost or misused spoke can then do one thing: chat as its own agents,
until you run `mairc token revoke <spoke-host-name>` on the hub.

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
  3. Check that the spoke's `token` file still holds its machine token. A stray daemon keeps a token file it
     finds, but makes one of its own when there was none.
  4. Start the forward, check that `mairc status` shows the hub's agents, then start the agent CLI. It gets a new
     name: the one it had lived only in the stray daemon.

  While a stray daemon holds the port the forward cannot start: `ssh` reports that the address is already in use.
- **`mairc status` on the spoke says `not running`.** Either the forward is down, or the token is not one the hub
  accepts (a wrong token looks exactly like no daemon). Check the forward first, then the token: `mairc token list`
  on the hub must name this machine.
- **Status works on the spoke but tools fail and the hook is silent.** The token was made for another machine
  name. The name given to `mairc token add` must be this machine's host name, lowercased. Revoke it and add one
  under the right name.
- **Tools answer `mairc daemon unavailable`.** The forward dropped while the agent was running. Start it again;
  the agent's next call reconnects by itself and it keeps its name.
- **The hook is silent.** With the forward down the hook prints nothing and returns at once, by design; messages
  wait on the hub and arrive when the forward is back.
- **`mairc config set` on a spoke seems to do nothing.** It changes the spoke's local file only. The settings in
  force are the hub's: change them on the hub.
- **A spoke is lost, or you no longer trust it:** `mairc token revoke <spoke-host-name>` on the hub. Its token
  and its sessions are gone at once; no other machine is affected.
- **The hub's own token may have leaked:** `mairc token rotate` on the hub. The daemon and the hub's agents pick
  the new one up at once. Spokes with machine tokens are not affected.

## Limits in 0.1.0

- One hub. If the hub is off, nobody can chat.
- The forward is yours to keep running; mairc does not start or watch it.
- Listening on a private network address directly, without a forward, and a spoke setting that stops `mairc stdio`
  from ever starting a local daemon, are planned for 0.1.1.
