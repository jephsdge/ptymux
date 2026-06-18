# ptymux CLI Reference

## Purpose

`ptymux` is a command-line PTY multiplexer. It keeps long-lived shell processes behind named targets so separate CLI invocations can share shell state, including current directory, environment variables, and interactive SSH sessions.

## Executable

Prefer invoking the skill-local executable at `assets/ptymux` after resolving the
skill directory:

```sh
/path/to/use-ptymux/assets/ptymux work "pwd"
```

The `assets/ptymux` wrapper automatically selects the matching Linux or macOS
platform binary. Do not choose a platform-specific `ptymux-linux-*` or
`ptymux-darwin-*` file manually.

If the wrapper reports a missing platform binary, run this in the ptymux repo:

```sh
TARGET=skill-all ./scripts/build.sh
```

Use a system `ptymux` only when the skill-local executable is unavailable for
another reason.

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
ptymux [--socket PATH] idle [-t DURATION] <target> <input>
ptymux [--socket PATH] send [-f | -t DURATION] <target> <input>
ptymux [--socket PATH] text <target> <text>
ptymux [--socket PATH] command [-f | -t DURATION] <target> <keys>
ptymux [--socket PATH] keys [-f | -t DURATION] <target> <keys>
ptymux [--socket PATH] read [-n N] <target>
ptymux [--socket PATH] follow <target>
ptymux [--socket PATH] list [target]
ptymux [--socket PATH] kill [target]
ptymux [--socket PATH] stop
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

Send terminal key sequences and automatically press Enter at the end:

```sh
ptymux command work "ctrl-c"
ptymux command work "ctrl-o d"
ptymux command -t 500ms work "ctrl-c"
ptymux command -f work "ctrl-o d"
```

Spaces mean sequential key presses. Hyphens combine modifiers with a key. `ptymux` appends Enter after the sequence. For example, `ctrl-o d` sends Ctrl+O, then `d`, then Enter.

Supported named keys include `enter`, `esc`, `escape`, `tab`, `backspace`, and `space`.

## Text And Keys

Type literal text without pressing Enter:

```sh
ptymux text work "hello"
ptymux keys work "enter"
```

Send exact key sequences without an implicit Enter:

```sh
ptymux keys work "ctrl-c"
ptymux keys work "up enter"
ptymux keys -t 500ms work "ctrl-c"
ptymux keys -f work "pageup"
```

Supported named keys include `enter`, `esc`, `escape`, `tab`, `backspace`,
`space`, `up`, `down`, `left`, `right`, `home`, `end`, `delete`, `pageup`, and
`pagedown`.

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
~/.ptymux/sockets/ptymux-default.sock
```

`ptymux` creates the `~/.ptymux/sockets` directory automatically when the daemon
starts.

Use a custom socket for isolation:

```sh
ptymux --socket /tmp/project-a.sock work "pwd"
ptymux --socket /tmp/project-a.sock stop
```

Stop the default daemon and close all managed shells:

```sh
ptymux stop
```

## Clean Text Output

`ptymux` returns clean text by default. Terminal color, title, cursor, and
line-control sequences are removed from command output and streams. Plain prompt
text remains visible so agents can still infer the current shell context.

## Auto Release

`ptymux` reads optional user configuration from:

```text
~/.ptymux/config.json
```

Default configuration:

```json
{
  "shell": "/bin/sh",
  "auto_release": {
    "enabled": true,
    "target_idle_timeout": "8h",
    "daemon_idle_timeout": "30m"
  }
}
```

`shell` controls the program used for newly created targets. Use `/bin/bash`
when bash prompt behavior or aliases are needed.

Configuration is read when the daemon starts. Restart the daemon with
`ptymux stop` for shell or auto-release changes to affect an already running
daemon.

`target_idle_timeout` releases a target after it has not been used for the
configured duration. `daemon_idle_timeout` stops an empty daemon after it has no
client requests for the configured duration. Set a timeout to `"0"` to disable
that release behavior, or set `enabled` to `false` to disable auto release.
