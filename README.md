# ptymux

[中文文档](README.zh-CN.md)

`ptymux` is a small command-line PTY multiplexer. It keeps long-lived shell
processes behind named targets, so repeated commands can share shell state such
as the current directory, environment variables, and an active SSH session.

## Target Paths

A target is a path with up to three parts:

```text
name
name/group
name/group/shell
```

Shorter forms are expanded with `default`:

```text
work             -> work/default/default
work/main        -> work/main/default
work/main/build  -> work/main/build
```

Internally, those three parts map to `session`, `pane`, and `tab`. The CLI uses
`target` as the public concept so day-to-day commands stay simple.

Targets are created lazily. The first command for a target creates its backing
`/bin/sh` process and PTY automatically.

## Install

Build a static binary:

```sh
./scripts/build.sh
```

The default output is `dist/ptymux` for Linux amd64 with `CGO_ENABLED=0`.

You can override the target:

```sh
GOOS=linux GOARCH=arm64 ./scripts/build.sh
OUT_DIR=. BIN_NAME=ptymux CGO_ENABLED=0 ./scripts/build.sh
```

Manual equivalent:

```sh
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/ptymux ./cmd/ptymux
```

Optionally move it somewhere on your `PATH`:

```sh
install -m 0755 dist/ptymux ~/.local/bin/ptymux
```

## Basic Usage

Show CLI help:

```sh
ptymux -h
```

Run commands in a persistent target:

```sh
ptymux work "pwd"
ptymux work "cd /tmp"
ptymux work "pwd"
```

The final `pwd` runs in the same shell and includes:

```text
/tmp
```

Use a full target path when you want separate shells:

```sh
ptymux work/main/build "go test ./..."
ptymux work/main/shell "pwd"
```

Output is terminal-like transcript output. Prompts and command echoes are
visible, but ptymux internal marker lines are hidden. `run`, `idle`, and `send`
use a VT terminal emulator to render the current prompt line before command
echo, so output looks like a normal terminal:

```text
sh-5.3$ pwd
/home/work/Projects/ptymux
sh-5.3$
```

## Command Modes

### Run Mode

Run mode is the default:

```sh
ptymux work "git status"
```

It appends an internal completion marker, waits for that marker, filters it from
output, and returns the command exit code. Use this for normal shell commands.

### Idle Mode

Use `idle` for commands that enter or leave an interactive shell, such as SSH:

```sh
ptymux idle work "ssh admin@localhost -p 2222"
ptymux work "pwd"
ptymux idle work "exit"
```

Idle mode does not append a marker. It sends the command and returns after PTY
output has been quiet for 500ms. It is equivalent to `send -t 500ms`.

Idle mode is heuristic. Commands with delayed output, such as
`sleep 2 && echo done`, can return before all output arrives.

### Send Mode

Use `send` when you want to write input to the target without a completion
marker:

```sh
ptymux send work "ls"
```

By default, `send` writes input and returns without printing output. The
background reader keeps recording terminal state and command history.

Follow output after sending:

```sh
ptymux send -f work "ls"
```

`send -f` keeps streaming output until you stop the client with `Ctrl+C`; the
target keeps running.

Wait until output is quiet, then return the new output:

```sh
ptymux send -t 100 work "ls"   # 100ms
ptymux send -t 1s work "ls"    # 1 second
ptymux send -t 1m work "ls"    # 1 minute
ptymux send -t 1ms work "ls"   # 1 millisecond
```

Durations without a unit are interpreted as milliseconds. `-f` and `-t` are
mutually exclusive.

`send` is useful when the target is inside an interactive program or remote
shell and a marker would not be reliable. For example, after an SSH password
prompt:

```sh
ptymux send work "your-password"
```

For SSH password prompts, prefer SSH keys or an agent. Avoid putting passwords
directly in command arguments because they can be saved in shell history or
visible in process listings.

### Command Mode

Use `command` to send terminal key sequences:

```sh
ptymux command work "ctrl-c"
ptymux command work "ctrl-o d"
ptymux command -t 500ms work "ctrl-c"
ptymux command -f work "ctrl-o d"
```

Spaces mean sequential key presses. Hyphens combine modifiers with a key.
ptymux appends Enter after the sequence. For example, `ctrl-o d` sends Ctrl+O,
then `d`, then Enter.

Supported named keys include `enter`, `esc`, `escape`, `tab`, `backspace`, and
`space`. `-f` and `-t` behave like `send`: follow until interrupted, or wait
until output has been quiet for the requested duration.

Use `command` for new key-sequence automation. The legacy `ctrl-c` command
remains as a compatibility alias.

### Ctrl+C

Send Ctrl+C to a target:

```sh
ptymux ctrl-c work
```

This writes the ETX byte (`0x03`) to the target PTY and follows output, just like
`send`. Stop observing with `Ctrl+C`; the target remains alive.

### Read Mode

Read the current terminal screen:

```sh
ptymux read work
```

Read recent command transcript entries:

```sh
ptymux read -n 3 work
```

Entries are returned from oldest to newest within the selected recent window.
`read` is read-only and does not block commands running in other clients.

### Follow Mode

Stream future PTY output without sending input:

```sh
ptymux follow work
```

Stop observing with `Ctrl+C`; the target remains alive.
`follow` is read-only and subscribes to future output without locking the
target.

### Kill Mode

Close one target and remove it from the daemon:

```sh
ptymux kill work
ptymux kill work/main/build
```

`kill` sends signals to the target shell's process group, closes the PTY, and
removes the target from the in-memory store. The next command for that target
starts a fresh shell.

For compatibility, `ptymux kill` without a target closes all managed shells.

## Listing Targets

List all targets:

```sh
ptymux list
```

List child groups under a target:

```sh
ptymux list work
```

List shells under a target group:

```sh
ptymux list work/main
```

## Daemon

`ptymux` starts its daemon automatically when needed. You usually do not need to
start it by hand.

Stop the daemon and close all managed shells:

```sh
ptymux stop
```

Close one target without stopping the daemon:

```sh
ptymux kill work
```

The default socket path is:

```text
/tmp/ptymux-<uid>.sock
```

Use a custom socket when you want a separate daemon:

```sh
ptymux --socket /tmp/project-a.sock work "pwd"
ptymux --socket /tmp/project-a.sock stop
```

## Notes

- Each full target path resolves to a long-lived `/bin/sh` process attached to a
  PTY.
- PTY output is combined stdout/stderr, like a normal terminal.
- `send -f`, `follow`, and `ctrl-c` stream output until the client disconnects.
- There is no full interactive attach mode yet; input is still sent one command
  at a time.

## License

MIT
