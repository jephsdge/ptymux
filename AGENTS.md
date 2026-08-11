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
prefer the word `target`. Target components are valid UTF-8, at most 64 bytes,
and exclude `/`, NUL, and control characters. Input is capped at 128 KiB;
`read -n` accepts 0 through 4096.

## Current Architecture

- `cmd/ptymux/main.go`, `cmd/ptymux-client/main.go`, and
  `cmd/ptymux-server/main.go`
  Separate Local, Remote client, and Remote server entrypoints. `ptymux` retains
  the internal Local daemon action used by automatic daemon startup.

- `internal/app/parse.go`
  Shared CLI argument parsing with role-specific `ParseLocal`, `ParseClient`, and
  `ParseServer` entrypoints. Existing local commands remain compatible; remote
  operations are `register`, `rotate`, `revoke`, `create`, `list`, `send`,
  `text`, `keys`, `read`, `follow`, and `close`.

- `internal/app/client.go`
  Client-side daemon communication. Starts the daemon automatically when needed.
  Streaming commands use a Unix socket stream instead of JSON response decoding.
  The default socket is `~/.ptymux/sockets/ptymux-default.sock`.

- `internal/app/config.go` and `internal/app/remote_config.go`
  Load local settings from `~/.ptymux/config`, with `~/.ptymux/config.json` as a
  legacy fallback. Remote aliases are loaded only from
  `~/.ptymux/client/config`; old local config files are not a remote alias
  fallback. Remote profiles and secret files are validated before networking.
  Local defaults use `/bin/sh` with an 8h target idle timeout and a 30m empty
  daemon idle timeout.

- `internal/server/service.go` and `internal/server/daemon.go`
  `Service` provides transport-independent target operations with a shutdown
  admission barrier. The daemon owns accepted-handler shutdown, identity-safe
  Unix socket cleanup, local automatic release scheduling, a 128-connection
  default limit, 5s initial-request deadlines, and 5s per-write deadlines.

- `internal/server/store.go`
  In-memory session/pane/tab target store. Concurrent creation uses startup
  placeholders so shell startup and shutdown happen outside the store lock.
  Targets track last-used time plus active use counts for automatic release.

- `internal/server/tab.go`
  Core PTY runner. Each runner owns one shell process, one PTY, one background
  reader goroutine, a virtual terminal, and live output subscribers.

- `internal/server/cleaner.go`
  Terminal output cleaner for completed command/quiet-wait results and input-
  follow streams such as `send -f`, key-follow modes, and `ctrl-c`. Standalone
  `follow` bypasses it and forwards raw PTY bytes.

- `internal/server/transcript.go`
  Maintains bounded ANSI terminal-line history and renders ANSI-styled virtual
  terminal screens for `read`. Alternate-screen output is excluded from normal
  history; `CSI 3J` clears saved history.

- `internal/server/keys.go`
  Parser for terminal key sequences used by `command` and `keys`.

- `internal/server/protocol.go` and `internal/server/local_stream.go`
  Bounded local request validation plus the versioned local stream protocol.
  Streaming uses explicit started/data/error/end frames and falls back to the
  legacy raw stream when a new client talks to an older daemon.

- `internal/remote/`
  Versioned HTTP/WS transport, persistent client registry, global-token and
  client-password authentication, keyed token-bucket abuse limits, separate
  pre-auth TCP and authenticated WebSocket limits, bounded targets/follow
  delivery, credential rotation/revocation, and per-owner service isolation. Authentication is access control only: all
  credentials, commands, and output are unencrypted, so expose the server only
  on a trusted internal network and never directly to public or untrusted
  networks.

- `skills/use-ptymux/assets/ptymux`
  Skill-local wrapper committed with the usage skill. It selects the matching
  generated Linux or macOS platform binary at runtime; agents should invoke this
  wrapper rather than a platform-specific binary.

## PTY Output Model

Each `PTYRunner` has exactly one background reader goroutine. It is the only
code path that reads from the PTY fd. The reader:

- feeds bytes into `vt10x.Terminal`;
- records bounded ANSI terminal-line history;
- broadcasts raw output chunks to live subscribers;
- keeps terminal screen state current.

Command methods must not read the PTY directly.

Keep PTY chunks raw in the reader and subscriber layer. Use
`CleanTerminalString` for completed command and quiet-wait results, and
`TerminalCleaner` for cleaned input-follow streams. `read` renders ANSI-styled
screen/history output, while standalone `follow` writes subscribed PTY chunks
unchanged.

Each target shell is started through `creack/pty`, which creates a new session
and process group for the shell. Target shutdown must signal the shell process
group, not only the shell PID, so foreground/background jobs such as local SSH
clients are cleaned up with the target.

Subscription types:

- Reliable subscriptions are used by boundary-sensitive operations such as
  `run`, `idle`, and `send -t`; they must not drop marker or quiet-wait output.
- Observer subscriptions are used by `follow`, `send -f`, `command -f`, and
  legacy `ctrl-c`; a full subscriber queue disconnects only that slow observer
  and must not block the runner or silently drop bytes.

Locking rules:

- `commandMu` serializes writes that need stable command boundaries.
- `stateMu` protects virtual-terminal state, ANSI transcript state, subscribers,
  and closed/read-error state.
- Do not hold `stateMu` while writing to client sockets.
- Do not introduce another direct `syscall.Read` outside `readLoop`.

## Command Modes

- Default run:
  `ptymux work "pwd"`
  Appends a random internal marker, waits without a fixed execution timeout,
  filters marker internals, and returns output plus exit code. Local client
  cancellation writes one ETX followed by a fresh marker command, waits for
  shell resynchronization, and preserves the target. Captured output is bounded
  to 8 MiB while marker detection continues beyond the capture limit.

- Idle:
  `ptymux idle work "ssh host"`
  Equivalent to quiet-wait behavior with a default 500ms timeout.

- Send:
  `ptymux send work "pwd"`
  Writes input and returns immediately. Output still updates the virtual
  terminal through the background reader.

- Send wait:
  `ptymux send -t 500ms work "pwd"`
  Writes input, waits until output is quiet, returns output. The duration is an
  inactivity threshold with no separate total deadline; every chunk resets it.
  Captured quiet-wait output is bounded to 8 MiB while draining continues.

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
  Renders the current virtual terminal screen with ANSI styling and filters
  ptymux internal marker lines.

- Read recent:
  `ptymux read -n 3 work`
  Returns the most recent `N` lines from bounded ANSI terminal history, oldest to
  newest. It is line-oriented, not command history or full scrollback. While the
  alternate screen is active, it returns the last `N` visible screen rows.

- Follow:
  `ptymux follow work`
  Read-only subscription that forwards future raw PTY chunks unchanged. It does
  not replay screen/history and must not block other commands.

- Kill:
  `ptymux kill work`
  Closes one target, removes it from the store, and leaves the daemon running.
  `ptymux kill` without a target remains a compatibility path for closing all
  targets.

- Auto release:
  `~/.ptymux/config` (with `config.json` as a legacy fallback)
  Defaults to enabled. `target_idle_timeout` defaults to `8h` and releases idle
  targets. `daemon_idle_timeout` defaults to `30m` and stops an empty idle
  daemon, which removes its socket. A timeout of `0` disables that specific
  release behavior.

- Shell configuration:
  `~/.ptymux/config` (with `config.json` as a legacy fallback)
  `shell` defaults to `/bin/sh`. Set it to `/bin/bash` when users need bash
  prompt behavior or aliases. Configuration is read when the daemon starts;
  existing daemons and targets do not hot-reload shell changes.

- Remote server:
  `ptymux-server` defaults to `~/.ptymux/server/token` and
  `~/.ptymux/server/clients.json`;
  explicit flags override these paths. It uses HTTP on port 8443 and runs in the
  foreground. A non-empty global token must be provisioned; the locked
  persistent registry is created automatically. Pre-auth accepted TCP
  connections, pending plus active authenticated WebSockets, per-client
  WebSockets/targets, registration, and failed-authentication attempts have
  separate limits configurable through server flags.

- Remote client:
  `ptymux-client register ...` creates a random owner identity and one-time
  password. The default token is `~/.ptymux/client/server.token`, and ordinary
  operations default to `~/.ptymux/client/<name>.password`. Remote aliases are
  read only from `~/.ptymux/client/config`. Explicit values override alias
  fields, which override default secret paths.
  Explicit flags override alias fields.

- Remote targets:
  `create` is explicit. `send`, `text`, `keys`, `read`, `follow`, and `close`
  require an existing target. Each OwnerID has an isolated `Service`, so equal
  target names across clients do not share state. Client/follow disconnect does
  not close the target. The first remote version has no attach mode.

- Remote credentials:
  `rotate` preserves OwnerID and targets while invalidating old-generation
  connections. `revoke` closes only that owner's connections and targets.
  Token/password authentication controls access but does not encrypt the HTTP/WS
  transport; credentials, commands, and output remain plaintext.

- Help:
  `ptymux -h`, `ptymux --help`, `ptymux help`, and subcommand help flags such
  as `ptymux send -h` print usage text and must not contact a daemon or server.

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

Build the static Linux amd64 executables:

```sh
./scripts/build.sh
```

The default outputs are `dist/ptymux`, `dist/ptymux-client`, and
`dist/ptymux-server`. Verify each with `ldd` and `file`, for example:

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
