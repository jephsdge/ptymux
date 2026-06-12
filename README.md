# ptymux

`ptymux` is a small command-line PTY multiplexer. It keeps long-lived shell
processes behind named targets, so repeated commands can share shell state such
as the current directory and exported environment variables.

Each target is addressed as:

```text
session/pane/tab
```

Shorter forms are allowed:

```text
work             -> work/default/default
work/main        -> work/main/default
work/main/build  -> work/main/build
```

## Install

Build a static binary:

```sh
CGO_ENABLED=0 go build -o ptymux ./cmd/ptymux
```

Optionally move it somewhere on your `PATH`:

```sh
install -m 0755 ptymux ~/.local/bin/ptymux
```

## Usage

Run commands in a persistent target:

```sh
ptymux work "pwd"
ptymux work "cd /tmp"
ptymux work "pwd"
```

The last command prints:

```text
/tmp
```

Use a full target path when you want separate shells:

```sh
ptymux work/main/build "go test ./..."
ptymux work/main/shell "pwd"
```

Targets are created lazily. The first command for a target creates its session,
pane, tab, shell, and PTY automatically.

## Listing Targets

List all sessions, panes, and tabs:

```sh
ptymux list
```

List panes in a session:

```sh
ptymux list work
```

List tabs in a pane:

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

- Each tab is a long-lived `/bin/sh` process attached to a PTY.
- Output from stdout and stderr is combined, like a normal terminal.
- Interactive full-screen programs such as `vim`, `top`, and `ssh` are not a
  goal for the synchronous command mode yet.

## License

MIT
