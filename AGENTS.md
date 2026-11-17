# AGENTS.md — microagentIRC

Rules for agents working in this repository. Short on purpose.

## What this is

`mairc`: a tiny chat system for coding agents, served as an MCP server. One public room, private messages, a
daemon, a stdio shim and a hook that prints nothing when there is nothing to read. Design: `plan.md`.
Work items: beads in `.beads/` (prefix `mairc`). **Each bead is self-contained: you do not need plan.md to do one.**

## Hard rules

1. **No scope creep.** Build what the bead says. `plan.md` §1 lists what is out of scope; do not add it.
2. **`internal/core/core.go` is the contract.** Build against its types and interfaces and against fakes, not
   against another package's concrete types. Change it only if your bead says so, together with `plan.md`, and
   say so in the commit message.
3. **Rust-port readiness** (`plan.md` §9): standard library first; the only third-party dependency is
   `modernc.org/sqlite`; no reflection tricks, no code generation, no global mutable state, no `init()` side
   effects; errors are values with stable short texts.
4. **User-visible strings are exact.** Tool names, descriptions, result lines and error texts are given verbatim
   in the beads. Tests assert them byte for byte.
5. **Stay in your package.** A bead names the files it owns. Touch other packages only where the bead says.
6. Never delete another agent's work, never force-push, never rewrite history. Branch `main` only.

## Toolchain

Go 1.27.4: `export PATH=$HOME/sdk/go1.27.4/bin:$HOME/go/bin:$PATH`. Build with `CGO_ENABLED=0`.

## Before every push

```sh
gofumpt -l . && go vet ./... && golangci-lint run && go test -race -count=1 ./...
```

All four clean. Tests are table-driven, use a fake clock and a temporary database, and never sleep.

## Beads

```sh
br ready                      # what can be started now
br show <id>                  # the full bead
br update <id> --claim --actor "<model> (<your name>)"
br close <id> --reason "<commit> + the test names that prove it"
```

Run `br` from the repository root. Commit `.beads/issues.jsonl` with your work. One bead per commit where you
can; name the bead id in the commit message. Pull with rebase before you push; push when the bead is done.
