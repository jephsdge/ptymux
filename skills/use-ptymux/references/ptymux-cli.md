# ptymux CLI Reference

## Purpose

`ptymux` is a command-line PTY multiplexer. It keeps long-lived shell processes behind named targets so separate CLI invocations can share shell state, including current directory, environment variables, and interactive SSH sessions.

## Executable

Prefer invoking the skill-local executable at `assets/ptymux` after resolving the
skill directory:

```sh
/path/to/use-ptymux/assets/ptymux work "pwd"
```

Use a system `ptymux` only when the skill-local executable is unavailable or
incompatible with the host platform.

## Target Paths

A target path has up to three parts:

```text
name
name/group
name/group/shell
```

Shorter forms expand with `default`:

```text
work             -> work/default/default
work/main        -> work/main/default
work/main/build  -> work/main/build
```

Use target paths consistently so later commands reuse the intended shell state.

## General Usage

```sh
ptymux [--socket PATH] <target> <command>
ptymux idle [-t DURATION] [--socket PATH] <target> <input>
ptymux send [-f | -t DURATION] [--socket PATH] <target> <input>
ptymux command [-f | -t DURATION] [--socket PATH] <target> <keys>
ptymux read [-n N] <target>
ptymux follow <target>
ptymux list [target]
ptymux kill [target]
ptymux stop
ptymux -h | --help | help
```

## Default Run Mode

Use run mode for normal shell commands:

```sh
ptymux work "pwd"
ptymux work "cd /tmp"
ptymux work "pwd"
```

Run mode waits for the command to complete and returns the shell command exit
code.

## Idle Mode

Use `idle` for commands that enter or leave interactive shells, such as SSH:

```sh
ptymux idle work "ssh admin@localhost -p 2222"
ptymux work "pwd"
ptymux idle work "exit"
```

`idle` sends the command and returns after PTY output has been quiet for 500ms
by default. It is equivalent to `send -t 500ms`.

Quiet wait is heuristic. Commands with delayed output, such as `sleep 2 && echo done`, can return before all output arrives.

## Send Mode

Write input to a target and return immediately:

```sh
ptymux send work "ls"
```

Follow output after sending:

```sh
ptymux send -f work "ls"
```

Wait until output is quiet:

```sh
ptymux send -t 100 work "ls"
ptymux send -t 1s work "ls"
ptymux send -t 1m work "ls"
ptymux send -t 1ms work "ls"
```

Durations without a unit are milliseconds. `-f` and `-t` are mutually exclusive.

## Command Mode

Send terminal key sequences:

```sh
ptymux command work "ctrl-c"
ptymux command work "ctrl-o d"
ptymux command -t 500ms work "ctrl-c"
ptymux command -f work "ctrl-o d"
```

Spaces mean sequential key presses. Hyphens combine modifiers with a key. `ptymux` appends Enter after the sequence. For example, `ctrl-o d` sends Ctrl+O, then `d`, then Enter.

Supported named keys include `enter`, `esc`, `escape`, `tab`, `backspace`, and `space`.

## Ctrl+C

Use the compatibility command to send only ETX byte `0x03` without appending Enter:

```sh
ptymux ctrl-c work
```

It follows output until the client disconnects; the target remains alive.

## Read Mode

Read the current virtual terminal screen:

```sh
ptymux read work
```

Read recent visible command regions:

```sh
ptymux read -n 3 work
```

`read -n` reads from the virtual terminal screen and extracts recent command regions in chronological order. It is not full scrollback.

## Follow Mode

Stream future PTY output without sending input:

```sh
ptymux follow work
```

Stop observing with Ctrl+C. The target remains alive.

## Listing Targets

```sh
ptymux list
ptymux list work
ptymux list work/main
```

## Kill Targets

Close one target without stopping the daemon:

```sh
ptymux kill work
ptymux kill work/main/build
```

Short target paths expand the same way as other commands. `ptymux kill work`
closes `work/default/default`. Without a target, `ptymux kill` closes all
managed targets as a compatibility behavior.

## Daemon And Socket

`ptymux` starts its daemon automatically when needed. The default socket path is:

```text
/tmp/ptymux-<uid>.sock
```

Use a custom socket for isolation:

```sh
ptymux --socket /tmp/project-a.sock work "pwd"
ptymux --socket /tmp/project-a.sock stop
```

Stop the default daemon and close all managed shells:

```sh
ptymux stop
```
