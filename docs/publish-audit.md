# Publish audit: making microagentIRC public

Bead `mairc-rl9`, 17 Nov 2026, on `main` at `c2548dc` (31 commits on all refs). The visibility switch is the owner's
action; this report says whether it is safe and what to decide first.

## Verdict

**Not yet, because of three owner decisions; no secret was found.** None of the three is a leak. Once the owner has
made them (below), it is **safe to flip**:

1. **Choose a licence.** There is no `LICENSE` file. A public repository without one is "all rights reserved", so
   others may read it but not use, modify or redistribute it. The recommendation is **MIT** (shortest, matches most
   dependencies) or **Apache-2.0** (adds an explicit patent grant). Add the file with the owner's name and year.
2. **Decide what happens to `.beads/`** (the tracker). See below; the recommendation is to keep it public.
3. **Turn on GitHub's private vulnerability reporting** (repository Settings > Code security), which the new
   `SECURITY.md` points reporters to.

## What was checked, and how

Every blob reachable from any ref (`git rev-list --all --objects`) was scanned, not only the working tree, together
with every commit message and commit identity.

```sh
git rev-list --all | wc -l                                   # 31 commits
git log --all --format="%an <%ae> | %cn <%ce>" | sort | uniq -c
# every blob in every commit, one pattern at a time (a pattern from the list below):
git grep -nIE '<pattern>' $(git rev-list --all) -- . | sort -u
git ls-tree -r --name-only $(git rev-list --all) | sort -u | grep -E '(^|/)(\.env|id_rsa|id_ed25519)|\.(pem|key|p12)$'
git log --all --format="%B" | grep -nE '<pattern>'           # commit messages
```

No secret scanner (gitleaks, trufflehog, detect-secrets) is installed on the build host, so the scan is a
deterministic pattern scan. Patterns: private keys (`BEGIN … PRIVATE KEY`), AWS, GitHub, Slack and `sk-` style keys,
other known token prefixes, `Bearer` literals, `api_key/secret/password/token = "…"` assignments, secret-like file
names (`.env*`, `*.pem`, `*.key`, `id_rsa`), absolute paths of the development host, the private project's names, host
names, IPv4 addresses, email addresses, the development agents' names and their mail system.

## Findings

| Area | Result |
|---|---|
| Keys, tokens, passwords, secret files | **None**, in any commit. Two "assigned secret" matches are test fixtures (`cmd/mairc/main_test.go`, `internal/daemon/handler_test.go`). |
| Private project | **None**: no reference to the private project the developers also work on. |
| IP addresses | Loopback and test addresses only (`127.0.0.1`, `0.0.0.0`, `10.0.0.1` in a config test). |
| Email addresses in files | **None.** |
| Commit identities | The owner's GitHub `noreply` address (29 commits); two commits by `CobaltWren <cobaltwren@local>`, a development agent's pseudonym with a non-routable address. Harmless; rewriting would change every later hash. |
| Commit messages | No paths or private names; two mention a development agent by pseudonym, and two carry a `Co-Authored-By: Devin` trailer. Harmless. |
| Owner handle | `horus14-wq` appears as the module path `github.com/horus14-wq/microagentIRC`, which is public by definition once the repository is. |
| `.beads/issues.jsonl` | **The only file with internal content**, committed in 18 of the 31 commits (128 KB now): a few absolute paths of the development host (two log files, and the project directory in older versions), the development agents' pseudonyms, and their mail system's message ids. Nothing secret. |

## `.beads/`: recommendation

**Keep the tracker public, as it is.** It holds the project's plan, decisions and review history, which is useful to
readers and contributors. The `beads` tracker is designed to live in git, and projects built this way publish it. Its
only internal content is low-sensitivity (a host's directory layout, agent pseudonyms, message ids) and no secret.
Removing it now would also need a history rewrite to mean anything, because all of it is already in 18 commits.

If the owner prefers it private instead, the only effective way is to rewrite history before the switch:

```sh
# owner's decision only; rewrites every commit hash and needs a force-push and fresh clones for everyone
git filter-repo --path .beads/ --invert-paths     # git-filter-repo is not installed on the build host yet
echo ".beads/" >> .gitignore                      # and move the tracker out of the repository
```

Sanitising the file in a new commit would not hide the history and is not recommended.

## Licences of dependencies

`go.mod`: module `github.com/horus14-wq/microagentIRC`, Go 1.27.4, one direct dependency. Read from each module's
`LICENSE` in the module cache:

| Module | Licence |
|---|---|
| modernc.org/sqlite v1.62.0 (direct) | BSD-3-Clause |
| modernc.org/libc v1.80.1, mathutil v1.7.1, memory v1.12.1 | BSD-3-Clause |
| github.com/google/uuid v1.6.0 | BSD-3-Clause |
| github.com/remyoudompheng/bigfft | BSD-3-Clause |
| golang.org/x/sys v0.49.0 | BSD-3-Clause |
| github.com/dustin/go-humanize v1.1.0 | MIT |
| github.com/mattn/go-isatty v0.0.24 | MIT |
| github.com/ncruces/go-strftime v1.1.0 | MIT |

All permissive; MIT or Apache-2.0 for this project are both compatible. A binary distribution must carry their
notices.

## Changes made in this bead

- `SECURITY.md`: how to report a vulnerability privately, the scope, supported versions.
- `README.md`: the status line now says what works (Linux) and what is in progress (macOS, Windows, CI matrix).
- This report.

Not changed, by design: the licence (owner's choice), `.beads/` and history (owner's choice), the repository's
visibility (owner's action).
