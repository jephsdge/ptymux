# AGENTS.md

This file is the project handoff guide for AI coding agents working on ptymux.
Keep it concise, factual, and updated when behavior or architecture changes.

## Project Summary

`ptymux` is a Go command-line PTY multiplexer. It keeps long-lived shell
processes behind named targets so separate CLI invocations can share shell
state, including current directory, environment variables, and interactive SSH
sessions.

The public target syntax is:

```text
name
name/group
name/group/shell
```

Internally those map to `session`, `pane`, and `tab`. The public docs should
prefer the word `target`.

## Current Architecture

- `cmd/ptymux/main.go`
  CLI entrypoint. Parses config, calls `app.Run`, prints response output or
  target lists.

- `internal/app/parse.go`
  CLI argument parsing. Supports default run mode plus `idle`, `send`,
  `command`, `ctrl-c`, `read`, `follow`, `list`, `stop`, `kill`, `daemon`, and
  `help`.

- `internal/app/client.go`
  Client-side daemon communication. Starts the daemon automatically when needed.
  Streaming commands use a Unix socket stream instead of JSON response decoding.

- `internal/server/daemon.go`
  Unix socket server. Decodes requests, locates/creates target runners, and
  dispatches actions.

- `internal/server/store.go`
  In-memory session/pane/tab target store. Targets are created lazily.

- `internal/server/tab.go`
  Core PTY runner. Each runner owns one shell process, one PTY, one background
  reader goroutine, a virtual terminal, and live output subscribers.

- `internal/server/keys.go`
  Parser for terminal key sequences used by `command`.

- `internal/server/protocol.go`
  JSON request/response types shared by app and server.

## PTY Output Model

Each `PTYRunner` has exactly one background reader goroutine. It is the only
code path that reads from the PTY fd. The reader:

- feeds bytes into `vt10x.Terminal`;
- broadcasts output chunks to live subscribers;
- keeps terminal screen state current.

Command methods must not read the PTY directly.

Subscription types:

- Reliable subscriptions are used by boundary-sensitive operations such as
  `run`, `idle`, and `send -t`; they must not drop marker or quiet-wait output.
- Best-effort subscriptions are used by observers such as `follow`, `send -f`,
  `command -f`, and legacy `ctrl-c`; slow observers must not block the runner.

Locking rules:

- `commandMu` serializes writes that need stable command boundaries.
- `stateMu` protects terminal state, subscribers, history fields, and closed
  state.
- Do not hold `stateMu` while writing to client sockets.
- Do not introduce another direct `syscall.Read` outside `readLoop`.

## Command Modes

- Default run:
  `ptymux work "pwd"`
  Appends an internal marker, waits for it, filters marker internals, returns
  output and exit code.

- Idle:
  `ptymux idle work "ssh host"`
  Equivalent to quiet-wait behavior with a default 500ms timeout.

- Send:
  `ptymux send work "pwd"`
  Writes input and returns immediately. Output still updates the virtual
  terminal through the background reader.

- Send wait:
  `ptymux send -t 500ms work "pwd"`
  Writes input, waits until output is quiet, returns output.

- Send follow:
  `ptymux send -f work "tail -f file"`
  Writes input, then streams future output until the client disconnects.

- Command:
  `ptymux command work "ctrl-o d"`
  Sends key sequences. Spaces mean sequential keys; hyphens combine modifiers.
  The sequence automatically appends Enter.

- Legacy Ctrl+C:
  `ptymux ctrl-c work`
  Compatibility path. It sends only ETX byte `0x03` and must not append Enter.

- Read:
  `ptymux read work`
  Reads the virtual terminal screen and filters ptymux internal marker lines.

- Read recent:
  `ptymux read -n 3 work`
  Also reads from the virtual terminal screen, then extracts the recent command
  regions in chronological order. It must not depend on internal history.

- Follow:
  `ptymux follow work`
  Read-only live subscription. It must not block other commands.

- Help:
  `ptymux -h`, `ptymux --help`, `ptymux help`, and subcommand help flags such
  as `ptymux send -h` print local usage text and must not contact the daemon.

## Internal Marker Rules

Run mode writes internal marker commands into the PTY. User-facing output must
hide these internals:

- `__ptymux_status=$?`
- `__ptymux_token_a=...`
- `__ptymux_token_b=...`
- `$__ptymux_token_a`
- `$__ptymux_token_b`
- `$__ptymux_status`
- `__PTYMUX_DONE_...`

This filtering applies to run output, `read`, and `read -n`.

## Build And Test

Run all tests:

```sh
go test ./... -count=1
```

Run race tests before changes to concurrency, PTY reading, subscriptions, or
daemon streaming:

```sh
go test -race ./... -count=1
```

Build a static Linux amd64 binary:

```sh
./scripts/build.sh
```

The default output is `dist/ptymux`. Verify static output with:

```sh
ldd dist/ptymux
file dist/ptymux
```

Expected `ldd` result:

```text
not a dynamic executable
```

## Manual Smoke Tests

Use a temporary socket so tests do not disturb a user's default daemon:

```sh
tmp_socket="/tmp/ptymux-smoke-$$.sock"
./ptymux --socket "$tmp_socket" stop >/dev/null 2>&1 || true
./ptymux --socket "$tmp_socket" work "pwd"
./ptymux --socket "$tmp_socket" send -t 500ms work "echo send-wait-ok"
./ptymux --socket "$tmp_socket" read work
./ptymux --socket "$tmp_socket" read -n 5 work
./ptymux --socket "$tmp_socket" command -t 500ms work "ctrl-c"
./ptymux --socket "$tmp_socket" stop
```

If behavior changes are not visible during manual testing, stop the daemon and
retry. Existing daemon processes keep running old code until restarted.

## Documentation

Keep both user READMEs in sync:

- `README.md`
- `README.zh-CN.md`

Do not commit `docs/superpowers/`; it is local planning material and is ignored
by `.gitignore`.

## Git And Generated Files

Ignored build outputs include:

- `/ptymux`
- `/bin/`
- `/dist/`
- Go test binaries and coverage files

Do not commit generated binaries.

## Development Constraints

- Preserve existing public CLI behavior unless the user explicitly changes it.
- Prefer tests before behavior changes.
- For PTY/concurrency work, verify with both normal tests and race tests.
- Avoid broad refactors that are not required for the requested behavior.
- Use `rg` for searching.
- Keep code and documentation ASCII unless there is a clear reason otherwise.
