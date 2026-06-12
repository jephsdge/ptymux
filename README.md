# ptymux

`ptymux` is a small command-line PTY multiplexer. It keeps long-lived shell
processes behind named targets, so repeated commands can share shell state such
as the current directory and exported environment variables.

A target is a path with up to three parts:

```text
name
name/group
name/group/shell
```

Shorter forms are allowed:

```text
work             -> work/default/default
work/main        -> work/main/default
work/main/build  -> work/main/build
```

Internally, those three parts map to `session`, `pane`, and `tab`. The CLI uses
`target` as the public concept so day-to-day commands stay simple.

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

The output is terminal-like transcript output. Commands and prompts are visible,
but ptymux internal marker lines are hidden. `run`, `idle`, and `send` use the
target's terminal screen state to render the current prompt line before command
echo. The last command includes:

```text
/tmp
```

Use a full target path when you want separate shells:

```sh
ptymux work/main/build "go test ./..."
ptymux work/main/shell "pwd"
```

Targets are created lazily. The first command for a target creates its backing
shell and PTY automatically.

## Idle Mode

Use `idle` for commands that enter or leave an interactive shell, such as `ssh`.
Idle mode does not append a marker. It returns after PTY output has been quiet
for a short period.

```sh
ptymux idle work "ssh admin@localhost -p 2222"
ptymux work "pwd"
ptymux idle work "exit"
```

Default command mode is still better for normal commands because it has a
reliable completion marker and exit code. The marker is internal and is filtered
from output:

```sh
ptymux work "git status"
```

Idle mode is heuristic. Commands with delayed output, such as
`sleep 2 && echo done`, can return before all output arrives.

## Send Mode

Use `send` when you want to write input to the target and then follow its
output. It does not append a completion marker. It keeps streaming output until
you stop the client with `Ctrl+C`; the target keeps running.

```sh
ptymux send work "exit"
```

`send` uses the target's terminal screen state to print the current prompt line
before streamed output, so command echoes look like a normal terminal:

```text
sh-5.3$ ls
LICENSE  README.md  cmd  go.mod  go.sum  internal  ptymux
sh-5.3$
```

This is useful when the target is inside an interactive program or a remote
shell and a marker would not be reliable. For example, after an SSH password
prompt:

```sh
ptymux send work "your-password"
```

For SSH password prompts, prefer SSH keys or an agent. Avoid putting passwords
directly in command arguments because they can be saved in shell history or
visible in process listings.

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
- Output from stdout and stderr is combined, like a normal terminal.
- Interactive full-screen programs such as `vim`, `top`, and `ssh` are not a
  goal for the synchronous command mode yet.

## License

MIT
