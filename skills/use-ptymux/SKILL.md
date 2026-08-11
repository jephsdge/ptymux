---
name: use-ptymux
description: Use when Codex needs persistent local or remote shell targets, including directory or environment state, interactive SSH sessions, long-running output, terminal key sequences, target reads, target following, or target cleanup.
---

# Use ptymux

## Overview

Use the ptymux executables when shell state must persist across separate
invocations:

- `ptymux` provides Local targets through an automatically started Unix-socket
  daemon;
- `ptymux-server` runs the foreground HTTP/WS service on port 8443;
- `ptymux-client` operates owner-isolated Remote targets.

For full syntax and examples, read `references/ptymux-cli.md`.

## Executable

Prefer the skill-local executable at `assets/ptymux` when it exists and can run
on the host. Resolve it relative to the skill directory:

```sh
/path/to/use-ptymux/assets/ptymux work "pwd"
```

The wrapper selects the matching Linux or macOS Local binary. Do not invoke a
platform-specific asset directly. If the wrapper is unavailable, use a
user-provided `ptymux` on `PATH`. Remote workflows require `ptymux-client` or
`ptymux-server` on `PATH`; the skill wrapper does not provide them.

## Local Workflow

Existing commands are implicit local mode. The `local` prefix is equivalent:

```sh
ptymux work "pwd"
ptymux local work "pwd"
```

1. Choose a stable target such as `work`, `repo/build`, or `host/ssh`. Each
   component is limited to 64 UTF-8 bytes and cannot contain `/` or control
   characters. Command/input text is limited to 128 KiB.
2. Use default run mode for ordinary commands that should complete and return an
   exit code. It has no fixed execution timeout. `Ctrl+C` interrupts the current
   local command while preserving the target; output beyond 8 MiB is truncated
   after the command is drained to completion:

```sh
ptymux work "cd /path/to/repo"
ptymux work "git status"
```

3. Use `idle` or `send -t` for entering or leaving an interactive shell:

```sh
ptymux idle host "ssh user@example.com"
ptymux send -t 500ms host "pwd"
```

4. Use `send` for fire-and-forget input, `send -f` to follow cleaned output
   after sending, and `follow` to observe future raw PTY output without sending:

```sh
ptymux send work "tail -f app.log"
ptymux send -f work "npm run dev"
ptymux follow work
```

5. Use `text` for literal text without Enter, `keys` for exact key sequences
   without implicit Enter, and `command` when Enter should be appended:

```sh
ptymux text work "hello"
ptymux keys work "enter"
ptymux keys -t 500ms work "ctrl-c"
ptymux command work "ctrl-o d"
```

6. Use `read` for the current ANSI-styled virtual screen and `read -n N` for
   the most recent ANSI terminal history lines:

```sh
ptymux read work
ptymux read -n 3 work
```

`N` counts lines, not commands. It must be between 0 and 4096; zero selects the
current screen. The history is bounded and is not full scrollback. While an
alternate screen is active, `read -n N` returns its last `N` visible rows.

7. Close one local target with `kill`; close all targets and stop the daemon with
   `stop`:

```sh
ptymux kill work
ptymux stop
```

## Remote Client Workflow

Prefer a configured alias in `~/.ptymux/client/config`:

```json
{
  "clients": {
    "relay": {
      "url": "http://host:8443",
      "name": "tianyijie"
    }
  }
}
```

The default token is `~/.ptymux/client/server.token`, and the default password
is `~/.ptymux/client/<client-name>.password`. Explicit CLI values override alias
fields, which override these default files.

Remote targets are not created implicitly. Create one before operating on it:

```sh
ptymux-client relay create work
ptymux-client relay list
ptymux-client relay send work "relay-cli login -u tianyijie"
ptymux-client relay read -n 20 work
ptymux-client relay follow work
ptymux-client relay keys work ctrl-c
ptymux-client relay close work
```

Remote operation semantics:

- `send` writes text and presses Enter.
- `text` writes literal text without Enter.
- `keys` sends named terminal keys without implicit Enter.
- `read` returns the current ANSI-styled screen; `read -n N` returns the most
  recent `N` ANSI terminal history lines, oldest to newest. While an alternate
  screen is active, it returns the last `N` visible rows instead.
- `follow` streams future raw PTY output and preserves terminal controls. Ctrl+C
  or disconnect ends only the follower.
- `close` stops one remote target.
- Client disconnect does not stop the remote target.

Every registered owner identity has a private target namespace. Different
aliases can still refer to the same owner when they contain the same credentials.

## Remote Registration And Credentials

When setup is required, register a client name with the global server token. The
server-generated password is printed once:

```sh
install -d -m 0700 ~/.ptymux/client
install -m 0600 /path/to/server-token ~/.ptymux/client/server.token
umask 077
password_tmp="$(mktemp ~/.ptymux/client/tianyijie.password.XXXXXX)"
if ptymux-client register \
  --url http://host:8443 \
  --name tianyijie > "$password_tmp"; then
  mv "$password_tmp" ~/.ptymux/client/tianyijie.password
else
  rm -f "$password_tmp"
  false
fi
```

Use private token/password files instead of inline secrets. Token/password
authentication is access control only: HTTP/WS leaves all credentials, commands,
and output unencrypted. Connect only over a trusted internal network, and never
expose the server directly to public or otherwise untrusted networks.

Rotate or revoke an alias only when explicitly requested. Use the alias's
actual `password_file`, or the name-derived default when the alias has no
override. If the alias stores an inline password, migrate it to a private
password file before rotating. Capture rotation in a private temporary file;
never redirect over the old file before ptymux authenticates:

```sh
umask 077
# Use the alias's password_file path here when one is configured.
password_file="$HOME/.ptymux/client/tianyijie.password"
password_tmp="$(mktemp "${password_file}.XXXXXX")"
if ptymux-client relay rotate > "$password_tmp"; then
  mv -- "$password_tmp" "$password_file"
else
  rm -f "$password_tmp"
  false
fi
```

Rotation preserves targets and closes connections authenticated with the old
credential. As a separate destructive action, revocation closes that owner
identity's connections and targets:

```sh
ptymux-client relay revoke
```

## Operating Rules

- Keep target names stable so state is reused intentionally.
- The optional `ptymux local ...` prefix is equivalent to direct Local syntax.
- Use a task-specific local socket when daemon isolation matters:
  `ptymux --socket /tmp/name.sock ...`. The path must be absent or a stale Unix
  socket owned by the current user; ptymux will not replace files or symlinks.
- Stop temporary local daemons when done:
  `ptymux --socket /tmp/name.sock stop`.
- Local settings use `~/.ptymux/config`, with `config.json` as a legacy
  fallback. Remote aliases are read only from `~/.ptymux/client/config`; old
  local config files are not an alias fallback.
- Client config and token/password files must be regular, non-symlinked, and
  inaccessible to group/other users.
- Do not use operation names (`register`, `rotate`, `revoke`, `create`, `list`,
  `send`, `text`, `keys`, `read`, `follow`, or `close`) as aliases.
- Local `shell` and auto-release changes require `ptymux stop` before they apply
  to a running daemon.
- PTY output combines stdout and stderr. Command results and input-follow streams
  are cleaned; `read` preserves ANSI styling, and standalone `follow` forwards
  raw PTY controls. With a current daemon, local streaming errors use stderr and
  a non-zero status. An older daemon may mix errors into stdout and return zero;
  run `ptymux stop` to restart it before relying on error separation.
- Treat `idle` and `send -t` as quiet-output heuristics; delayed output can arrive
  after the command returns. The duration is an inactivity threshold, not a total
  timeout, and each output chunk restarts it. Captured quiet-wait output is
  bounded at 8 MiB while ptymux continues waiting for quiet.
- Treat `read -n N` as bounded terminal line history, not command history or full
  scrollback.
- Avoid putting secrets in command arguments.
- There is no full interactive attach mode; use `send`, `text`, `keys`, `read`,
  and `follow`.
