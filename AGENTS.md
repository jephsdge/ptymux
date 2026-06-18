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
  `text`, `command`, `keys`, `ctrl-c`, `read`, `follow`, `list`, `stop`,
  `kill`, `daemon`, and `help`.

- `internal/app/client.go`
  Client-side daemon communication. Starts the daemon automatically when needed.
  Streaming commands use a Unix socket stream instead of JSON response decoding.
  The default socket is `~/.ptymux/sockets/ptymux-default.sock`.

- `internal/app/config.go`
  Loads optional user configuration from `~/.ptymux/config.json`. Defaults use
  `/bin/sh` as the target shell and enable automatic release with an 8h target
  idle timeout and a 30m empty daemon idle timeout.

- `internal/server/daemon.go`
  Unix socket server. Decodes requests, locates/creates target runners, and
  dispatches actions. Owns automatic release scheduling for idle targets and
  empty daemons.

- `internal/server/store.go`
  In-memory session/pane/tab target store. Targets are created lazily and track
  last-used time plus active use counts for automatic release.

- `internal/server/tab.go`
  Core PTY runner. Each runner owns one shell process, one PTY, one background
  reader goroutine, a virtual terminal, and live output subscribers.

- `internal/server/cleaner.go`
  Terminal output cleaner. It removes terminal control sequences and applies
  basic line semantics so command and stream output is stable clean text for
  agents.

- `internal/server/keys.go`
  Parser for terminal key sequences used by `command` and `keys`.

- `internal/server/protocol.go`
  JSON request/response types shared by app and server.

- `skills/use-ptymux/assets/ptymux`
  Skill-local wrapper committed with the usage skill. It selects the matching
  generated Linux or macOS platform binary at runtime; agents should invoke this
  wrapper rather than a platform-specific binary.

## PTY Output Model

Each `PTYRunner` has exactly one background reader goroutine. It is the only
code path that reads from the PTY fd. The reader:

- feeds bytes into `vt10x.Terminal`;
- broadcasts output chunks to live subscribers;
- keeps terminal screen state current.

Command methods must not read the PTY directly.

PTY bytes should stay raw until they reach the command result or stream writer.
Use `CleanTerminalString` for complete command/read output and
`TerminalCleaner` for streaming output so split OSC/CSI sequences do not leak.

Each target shell is started through `creack/pty`, which creates a new session
and process group for the shell. Target shutdown must signal the shell process
group, not only the shell PID, so foreground/background jobs such as local SSH
clients are cleaned up with the target.

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

- Text:
  `ptymux text work "hello"`
  Types literal text without automatically pressing Enter.

- Keys:
  `ptymux keys work "up enter"`
  Sends key sequences without an implicit Enter. `keys -t` waits for quiet
  output; `keys -f` streams until the client disconnects.

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

- Kill:
  `ptymux kill work`
  Closes one target, removes it from the store, and leaves the daemon running.
  `ptymux kill` without a target remains a compatibility path for closing all
  targets.

- Auto release:
  `~/.ptymux/config.json`
  Defaults to enabled. `target_idle_timeout` defaults to `8h` and releases idle
  targets. `daemon_idle_timeout` defaults to `30m` and stops an empty idle
  daemon, which removes its socket. A timeout of `0` disables that specific
  release behavior.

- Shell configuration:
  `~/.ptymux/config.json`
  `shell` defaults to `/bin/sh`. Set it to `/bin/bash` when users need bash
  prompt behavior or aliases. Configuration is read when the daemon starts;
  existing daemons and targets do not hot-reload shell changes.

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

Build the skill wrapper platform binaries with:

```sh
TARGET=skill-all ./scripts/build.sh
```

This generates Linux/macOS amd64/arm64 binaries in
`skills/use-ptymux/assets/`. The generated `ptymux-*` files are ignored and
must not be committed.

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

When CLI behavior, flags, command semantics, examples, or user-facing workflows
change, also update the ptymux usage skill:

- `skills/use-ptymux/SKILL.md`
- `skills/use-ptymux/references/ptymux-cli.md`

The skill is for accurate and efficient binary usage only. Do not include
implementation details such as daemon internals, PTY reader/subscriber design,
marker tokens, process groups, Go package layout, build/release steps, or test
strategy in the skill.

After updating the skill as part of a feature or usage change, use a subagent to
validate it before finishing the feature. The subagent review should check that
the skill matches the current CLI, covers the changed usage, avoids internal
implementation details, avoids non-usage build/release content, and gives clear
examples for the relevant commands and flags.

Do not commit `docs/superpowers/`; it is local planning material and is ignored
by `.gitignore`.

## Git And Generated Files

Ignored build outputs include:

- `/ptymux`
- `/bin/`
- `/dist/`
- `skills/use-ptymux/assets/ptymux-*`
- Go test binaries and coverage files

Do not commit generated binaries.

## Development Constraints

- Preserve existing public CLI behavior unless the user explicitly changes it.
- Prefer tests before behavior changes.
- For PTY/concurrency work, verify with both normal tests and race tests.
- For process shutdown work, preserve process-group cleanup semantics.
- Avoid broad refactors that are not required for the requested behavior.
- Use `rg` for searching.
- Keep code and documentation ASCII unless there is a clear reason otherwise.
