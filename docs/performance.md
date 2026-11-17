# mairc performance

Measured on 17 November 2026 against commit `c2548dc` plus the end-to-end tests, on the development host
(12 cores, Linux), with the static binary (`CGO_ENABLED=0`, `-trimpath -ldflags "-s -w"`).

**The host was busy during every measurement: load average 19 to 22** (other agents' builds). Three targets are
missed below. For scale, the bare process start of the same binary (`mairc version`) took 11.4 ms at the median
and 60.5 ms at the 99th percentile in the same minutes, so most of the hook's time here is the operating system
starting a process on a loaded machine. The misses are reported as misses; bead `mairc-perf-901` asks for a
second run on a quiet host before anything is changed.

| What | Target | Measured | Result |
|---|---|---|---|
| `mairc hook`, nothing to deliver, whole process | p50 ≤ 15 ms, p99 ≤ 50 ms | p50 16.2 ms, p99 109 ms (500 runs, in-test); p50 17.7 ms, p99 89.0 ms (300 runs, separate script) | **miss** (p50 by 1 to 3 ms; p99 by about 2x) |
| daemon `POST /v1/hook`, nothing to deliver | p99 ≤ 2 ms | p50 0.7 ms, p99 9.2 ms, 5,818 requests/s, 20,000 of 20,000 answered `204` (8 concurrent clients) | **miss** at p99; p50 and p90 (2.8 ms) show the handler itself is fast |
| `tools/list` result | ≤ 1200 bytes | 604 bytes | pass |
| `irc_send` to the room, HTTP round trip, cooldown 0 | p50 ≤ 2 ms | p50 1.43 ms, p99 23.8 ms (2000 sends, one session) | pass |
| daemon resident memory after 2000 sends (1000 kept) and 60 s idle | ≤ 20 MB | 19.3 MB (19,272 kB right after the sends, 19,320 kB after the idle minute) | pass, with little room |
| binary | one static file | 11,825,312 bytes, `statically linked` (`file`) | pass |

Package-level numbers reported by the beads that own them, for reference: `chat` `BenchmarkHookEventEmpty`
100.8 ns/op with 0 allocations; `store` `InsertRoom` 312 µs/op (file-backed) and `HasUnreadPM` 14 µs/op.

## What the numbers say

- **The empty hook path does no work that shows.** With nothing due the daemon answers `204` in 0.7 ms at the
  median under eight concurrent clients, without touching SQLite for a hook resolved by name or by process id
  (a hook resolved by seat does one read, by design; see `docs/protocol.md`).
- **The hook's cost is the process, not the check.** `mairc hook` is 5 to 6 ms slower than `mairc version`: it
  reads `config.json` and the token, walks up to eight ancestors in `/proc`, and makes one loopback request.
- **Memory is close to its target.** 19.3 MB of the 20 MB budget is used with 1000 messages kept. Most of it is
  the Go runtime and the pure-Go SQLite engine, not the messages.

## How to repeat

```sh
export PATH=$HOME/sdk/go1.27.4/bin:$HOME/go/bin:$PATH

# Hook process time, tools/list size, irc_send round trip, daemon memory (takes about 80 s: it idles for 60).
MAIRC_PERF=1 go test -count=1 -run TestPerformance -v ./e2e

# The daemon's hook endpoint under load. Start a daemon with its own home and name one agent "perf" first
# (TestPerformance logs a running daemon's address and token file while it runs).
hey -n 20000 -c 8 -m POST -H "Authorization: Bearer $(cat "$MAIRC_HOME/token")" \
  -d '{"name":"perf","event":"PostToolUse"}' http://127.0.0.1:4747/v1/hook

# The binary.
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o mairc ./cmd/mairc && ls -l mairc && file mairc
```

Record the load average (`uptime`) with every run: these numbers move with it.
