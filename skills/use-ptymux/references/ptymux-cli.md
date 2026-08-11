# ptymux CLI Reference

## Purpose

`ptymux` keeps long-lived shell processes behind named targets so separate CLI
invocations can share current directory, environment variables, and interactive
shell state.

## Executable

Prefer the skill-local `assets/ptymux` wrapper after resolving the skill
directory:

```sh
/path/to/use-ptymux/assets/ptymux work "pwd"
```

The wrapper selects the matching Linux or macOS Local binary. Use a system
`ptymux` only when the wrapper is unavailable. Remote workflows require
`ptymux-client` or `ptymux-server` on `PATH`; the wrapper does not provide them.

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

Each component must be valid UTF-8, is limited to 64 bytes, and cannot contain
`/`, NUL, or control characters. Command/input text is limited to 128 KiB.

## Executables

- `ptymux ...`: Local Unix-socket client and automatically started daemon.
- `ptymux local ...`: equivalent explicit Local syntax.
- `ptymux-server ...`: foreground HTTP/WS service on port 8443.
- `ptymux-client ...`: registered Remote client.

Examples of equivalent local commands:

```sh
ptymux work "pwd"
ptymux local work "pwd"
ptymux send work "ls"
ptymux local send work "ls"
```

The optional `local` prefix is retained for explicit Local syntax; Remote
operations use the separate executables.

## Local Syntax

```text
ptymux [--socket PATH] <target> <command>
ptymux local [--socket PATH] <target> <command>
ptymux [local] idle [-t DURATION] <target> <input>
ptymux [local] send [-f | -t DURATION] <target> <input>
ptymux [local] text <target> <text>
ptymux [local] command [-f | -t DURATION] <target> <keys>
ptymux [local] keys [-f | -t DURATION] <target> <keys>
ptymux [local] ctrl-c <target>
ptymux [local] read [-n N] <target>
ptymux [local] follow <target>
ptymux [local] list [target]
ptymux [local] kill [target]
ptymux [local] stop
ptymux -h | --help | help
```

### Default Run

```sh
ptymux work "pwd"
ptymux work "cd /tmp"
ptymux work "pwd"
```

Run mode waits for command completion and returns the shell exit code without a
fixed execution timeout. `Ctrl+C` interrupts the current local command and keeps
the target usable. Captured output is limited to 8 MiB; ptymux drains the command
to completion and warns when the displayed output is truncated.

### Idle And Quiet Wait

```sh
ptymux idle work "ssh admin@localhost -p 2222"
ptymux send -t 500ms work "pwd"
```

`idle` defaults to a 500ms quiet period. Durations without a unit are
milliseconds. Quiet wait is heuristic; delayed output can arrive later. The
quiet period is an inactivity threshold rather than a total timeout, and each
output chunk restarts it. Captured quiet-wait output is limited to 8 MiB while
ptymux continues waiting for the quiet boundary.

### Send, Text, Command, And Keys

```sh
ptymux send work "ls"
ptymux send -f work "npm run dev"
ptymux send -t 1s work "ls"
ptymux text work "hello"
ptymux keys work "up enter"
ptymux keys -t 500ms work "ctrl-c"
ptymux command work "ctrl-o d"
```

- `send` writes input and presses Enter.
- `send -f` follows output until the client disconnects.
- `send -t` returns when output has been quiet for the duration.
- `text` writes literal text without Enter.
- `keys` sends key sequences without implicit Enter.
- `command` sends key sequences and appends Enter.

Supported named keys include `enter`, `esc`, `escape`, `tab`, `backspace`,
`space`, `up`, `down`, `left`, `right`, `home`, `end`, `delete`, `pageup`, and
`pagedown`. Spaces separate sequential keys; hyphens combine modifiers.

The compatibility command `ptymux ctrl-c work` sends ETX (`0x03`) and follows
output.

### Read And Follow

```sh
ptymux read work
ptymux read -n 3 work
ptymux follow work
```

`read` returns the current virtual terminal screen with ANSI styling.
`read -n N` returns the most recent `N` ANSI terminal history lines, ordered
oldest to newest. `N` must be between 0 and 4096; zero selects the current
screen. It is bounded line history, not command history or full scrollback. While an
alternate screen is active, `read -n N` returns its last `N` visible rows.
`follow` streams only future raw PTY output, preserving ANSI and other terminal
controls; it does not replay the current screen or history. Ctrl+C ends
observation without closing the target. With a current daemon, local streaming
errors are reported on stderr with a non-zero status. New clients can use older
daemons, but legacy streaming may mix errors into stdout and return zero. Run
`ptymux stop` to restart the daemon before relying on error separation.

### List, Kill, And Stop

```sh
ptymux list
ptymux list work
ptymux list work/main
ptymux kill work
ptymux stop
```

`kill <target>` closes one local target. `kill` without a target closes all
managed targets as a compatibility behavior. `stop` closes all targets and the
local daemon.

### Local Socket

The default socket is:

```text
~/.ptymux/sockets/ptymux-default.sock
```

Use a custom socket for isolation. The path must be absent or a stale Unix
socket owned by the current user; ptymux does not replace regular files,
directories, or symlinks. Place `--socket` before the operation name:

```sh
ptymux --socket /tmp/project-a.sock work "pwd"
ptymux --socket /tmp/project-a.sock read -n 3 work
ptymux local --socket /tmp/project-a.sock follow work
ptymux --socket /tmp/project-a.sock stop
```

## Remote Server Syntax

```text
ptymux-server \
  [--listen ADDRESS] \
  [--token-file PATH] \
  [--client-registry PATH] \
  [--shell PATH] \
  [--shutdown-timeout DURATION] \
  [--max-pre-auth-connections N] \
  [--max-connections N] \
  [--max-connections-per-client N] \
  [--max-targets-per-client N] \
  [--auth-rate N] \
  [--auth-burst N]
```

Example:

```sh
ptymux-server \
  --listen 0.0.0.0:8443 \
  --token-file /run/secrets/ptymux.token \
  --client-registry /var/lib/ptymux/clients.json \
  --shell /bin/bash
```

The server stays in the foreground and defaults to HTTP on port 8443. Default
files are `~/.ptymux/server/token` and `~/.ptymux/server/clients.json`. The token
must be provisioned; the registry is created automatically. Explicit file flags
override these paths. The registry persists client registrations; running
targets exist only for the server process lifetime. Resource defaults are 256
accepted TCP connections that have not authenticated, 256 pending plus active
authenticated WebSockets, 16 authenticated WebSockets per client, and 64 targets
per client. `--max-pre-auth-connections` controls the first limit;
`--max-connections` controls the second. Registration attempts and failed
authentication attempts use separate per-source token buckets, each refilling at
5 tokens per second with a burst of 20. Successful authentication does not
consume tokens.

A server token file
may be a read-only Docker secret such as mode `0444`, but it must be a regular
non-symlink file and must not be writable by group or other users. Client-side
token/password files use the stricter private-file rules described below.

## Remote Registration

Register a unique lowercase client name with the global server token:

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

The server generates the password and returns it once. Names use 1-64 lowercase
ASCII characters from `a-z`, `0-9`, `.`, `_`, and `-`.

## Remote Client Syntax

Explicit connection form:

```text
ptymux-client \
  --url http://HOST:8443 \
  [--token VALUE | --token-file PATH] \
  --name NAME \
  [--password VALUE | --password-file PATH] \
  <operation> ...
```

Alias form:

```text
ptymux-client ALIAS <operation> ...
```

Operations:

```text
create <target>
list [target]
send <target> <input>
text <target> <text>
keys <target> <keys>
read [-n N] <target>
follow <target>
close <target>
rotate
revoke
```

Remote targets must be created explicitly. Target operations such as `send`,
`text`, `keys`, `read`, `follow`, and `close` require an existing target.

```sh
ptymux-client relay create work
ptymux-client relay list
ptymux-client relay send work "relay-cli login -u tianyijie"
ptymux-client relay text work "literal text"
ptymux-client relay keys work "enter"
ptymux-client relay read -n 20 work
ptymux-client relay follow work
ptymux-client relay close work
```

For operations available in both modes, remote input/output semantics match the
local operation; supported flags still follow the remote syntax above. Remote
`read` preserves ANSI styling, and remote `follow` forwards future raw PTY bytes.
Ctrl+C or network disconnect ends a remote follower but leaves the target alive.

Every registered owner identity has a private target namespace. Different owner
identities can use the same target path without sharing or exposing state;
multiple aliases containing the same credentials still access the same owner.

## Remote Aliases And Configuration

Remote aliases are read only from `~/.ptymux/client/config`. There is no
fallback to `~/.ptymux/config` or `~/.ptymux/config.json` for aliases.

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

The default token is `~/.ptymux/client/server.token`; the default password is
`~/.ptymux/client/<client-name>.password`. Alias fields override default secret
paths, and explicit flags override alias fields. Local shell and auto-release
settings remain in `~/.ptymux/config`, with `config.json` as their legacy
fallback.

Explicit flags override alias values. Unknown aliases and conflicting inline/file
secret options fail before networking. Client config and token/password files
must be regular, non-symlinked, and inaccessible to group/other users. Mode
`0600` is a common choice, while `0400` is also accepted.

Operation names (`register`, `rotate`, `revoke`, `create`, `list`, `send`,
`text`, `keys`, `read`, `follow`, and `close`) are reserved and cannot be used as
selectable aliases.

Prefer token/password files over inline values so secrets do not appear in shell
history or process listings. Token/password authentication is access control
only: HTTP/WS leaves all credentials, commands, and output unencrypted. Expose
the server only on a trusted internal network and never directly to public or
otherwise untrusted networks.

## Rotate And Revoke

Capture a rotated password in a private temporary file and replace the alias's
actual password file only after rotation succeeds. Use the name-derived default
only when the alias has no `password_file` override. If the alias contains an
inline password, migrate that value to a private password file and update the
alias before rotating. Do not redirect over the current password file because
the shell would truncate it before ptymux authenticates.

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

`rotate` preserves the owner identity and targets, invalidates the old password,
and closes old-generation connections. As a separate destructive action:

```sh
ptymux-client relay revoke
```

`revoke` closes that owner's connections and targets. A later registration of
the same name receives a new owner identity.

## Local Configuration

The top-level `shell` and `auto_release` settings apply to local mode. Defaults:

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

Restart the local daemon with `ptymux stop` after changing local settings. Set a
timeout to `"0"` to disable that release behavior, or set `enabled` to `false`
to disable local auto release.

## Output And Attach Scope

PTY output combines stdout and stderr. Completed command and quiet-wait results,
plus input-follow streams, are cleaned of terminal controls while preserving
prompt text. `read` emits ANSI-styled screen/history output, and standalone
`follow` forwards future raw PTY output. Remote mode has no full interactive
attach; use `send`, `text`, `keys`, `read`, and `follow`.
