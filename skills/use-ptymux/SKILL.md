---
name: use-ptymux
description: Use when Codex needs to run shell commands through ptymux so state persists across invocations, including current directory changes, exported variables, interactive SSH sessions, long-running process output, terminal key sequences, target reads, target following, or target cleanup.
---

# Use ptymux

## Overview

Use `ptymux` to operate persistent named shell targets from Codex. Prefer it when command state matters across turns or invocations, such as `cd`, exported variables, interactive SSH sessions, long-running development servers, or processes that need follow/read/interrupt behavior.

For full CLI syntax and examples, read `references/ptymux-cli.md`.

## Executable

Prefer the skill-local executable at `assets/ptymux` when it exists and can run
on the host. Resolve it relative to the skill directory, then run commands like:

```sh
/path/to/use-ptymux/assets/ptymux work "pwd"
```

If that executable is unavailable or incompatible, use a user-provided `ptymux`
on `PATH`.

## Workflow

1. Choose a stable target name for the task, such as `work`, `repo/build`, or `host/ssh`.
2. Use default run mode for normal shell commands that should complete and return an exit code:

```sh
ptymux work "pwd"
ptymux work "cd /path/to/repo"
ptymux work "go test ./..."
```

3. Use `idle` or `send -t` for interactive shells or commands where normal
completion waiting is not a good fit:

```sh
ptymux idle host "ssh user@example.com"
ptymux send -t 500ms host "pwd"
```

4. Use `send` for fire-and-forget input, `send -f` to stream after sending, and `follow` to observe without sending:

```sh
ptymux send work "tail -f app.log"
ptymux send -f work "npm run dev"
ptymux follow work
```

5. Use `command` for terminal key sequences and `ctrl-c` only for the legacy Ctrl+C compatibility path:

```sh
ptymux command work "ctrl-c"
ptymux command -t 500ms work "ctrl-o d"
ptymux ctrl-c work
```

6. Use `read` when the current terminal screen is enough, and `read -n N` for recent command regions visible in the terminal screen:

```sh
ptymux read work
ptymux read -n 3 work
```

7. Use `kill <target>` to close one target without stopping the daemon. Use
`stop` to close all targets and stop the daemon:

```sh
ptymux kill work
ptymux stop
```

## Operating Rules

- Keep target names stable within a task so shell state is reused intentionally.
- Remember that the default socket is
  `~/.ptymux/sockets/ptymux-default.sock`.
- Use a task-specific socket when isolation matters: `ptymux --socket /tmp/name.sock ...`.
- Stop a temporary daemon when done: `ptymux --socket /tmp/name.sock stop`.
- Use `~/.ptymux/config.json` with `"shell": "/bin/bash"` when bash prompt
  behavior or aliases are needed; restart the daemon with `ptymux stop` after
  changing shell configuration.
- Use `ptymux kill <target>` to release one target while keeping the daemon
  alive.
- Remember that `~/.ptymux/config.json` controls automatic target and daemon
  release; defaults are enabled with `target_idle_timeout` of `8h` and
  `daemon_idle_timeout` of `30m`.
- Remember that PTY output combines stdout and stderr, like a normal terminal.
- Expect clean text output by default: terminal color, title, cursor, and
  line-control sequences are removed, while prompt text remains visible.
- Treat `idle` and `send -t` as quiet-output heuristics; delayed output may arrive after the command returns.
- Do not rely on `read -n` as full scrollback; it reads recent command regions from the virtual terminal screen.
- Avoid putting secrets in command arguments, especially SSH passwords.
