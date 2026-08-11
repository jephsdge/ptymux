# ptymux

[中文文档](README.zh-CN.md)

`ptymux` is a small command-line PTY multiplexer. It keeps long-lived shell
processes behind named targets, so repeated commands can share shell state such
as the current directory, environment variables, and an active SSH session.

It provides three executables:

- `ptymux`: the local Unix-socket client and automatically started daemon. The
  optional `local` prefix remains available.
- `ptymux-server`: a foreground HTTP/WS service that owns remote targets.
- `ptymux-client`: a registered, authenticated client for remote targets.

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
`target` as the public concept so day-to-day commands stay simple. Each component
must be valid UTF-8, is limited to 64 bytes, and cannot contain `/`, NUL, or
control characters. Command/input text is limited to 128 KiB.

Local targets are created lazily. The first local command for a target creates
its backing shell process and PTY automatically. Remote targets are created
explicitly with `ptymux-client ... create <target>`.

## Install

Build the three static executables:

```sh
./scripts/build.sh
```

The default Linux amd64 outputs use `CGO_ENABLED=0`:

```text
dist/ptymux
dist/ptymux-client
dist/ptymux-server
```

You can override the target platform or output directory:

```sh
GOOS=linux GOARCH=arm64 ./scripts/build.sh
GOOS=darwin GOARCH=arm64 ./scripts/build.sh
OUT_DIR=. CGO_ENABLED=0 ./scripts/build.sh
```

To build the platform binaries used by the bundled skill wrapper:

```sh
TARGET=skill-all ./scripts/build.sh
```

That command writes ignored platform binaries to `skills/use-ptymux/assets/`.
The committed `skills/use-ptymux/assets/ptymux` wrapper selects the matching
Linux or macOS binary at runtime.

Manual equivalent:

```sh
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/ptymux ./cmd/ptymux
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/ptymux-client ./cmd/ptymux-client
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/ptymux-server ./cmd/ptymux-server
```

Optionally install the executables on your `PATH`:

```sh
install -m 0755 dist/ptymux dist/ptymux-client dist/ptymux-server ~/.local/bin/
```

## Local, Server, And Client Executables

`ptymux` is local-only. Adding the optional `local` prefix is equivalent:

```sh
ptymux work "pwd"
ptymux local work "pwd"
ptymux send work "ls"
ptymux local send work "ls"
```

Local commands use the automatically started Unix-socket daemon described below.
Remote operations use the separate `ptymux-client` and `ptymux-server`
executables.

Run a remote HTTP/WS server on port 8443 in the foreground after provisioning
its global token:

```sh
ptymux-server
```

The default server paths are:

```text
~/.ptymux/server/token
~/.ptymux/server/clients.json
```

The non-empty global token must already exist; ptymux does not generate it. The
registry and its secure parent directory are created automatically. Explicit
`--token-file` and `--client-registry` flags override these defaults. The server
stays in the foreground and keeps targets alive until they exit, are closed, or
the server stops. Targets do not survive a server or container restart.

Token/password authentication is access control only. HTTP/WS does not encrypt
credentials, commands, or output. Expose ptymux only on a trusted internal
network, and never expose it directly to public or otherwise untrusted networks.

Server protection defaults are 256 accepted TCP connections that have not yet
successfully authenticated, 256 pending plus active authenticated WebSockets,
16 authenticated WebSockets per client, and 64 targets per client. Registration
attempts and failed authentication attempts use separate per-source token
buckets, each refilling at 5 tokens per second with a burst of 20; successful
authentication does not consume tokens. Override these defaults with
`--max-pre-auth-connections`, `--max-connections`,
`--max-connections-per-client`, `--max-targets-per-client`, `--auth-rate`, and
`--auth-burst`.

Install the shared server token at the client default path, then register a
client name. The server prints a generated password exactly once:

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

The default token and name-derived password paths are used automatically:

```sh
ptymux-client --url http://host:8443 --name tianyijie create work
ptymux-client --url http://host:8443 --name tianyijie \
  send work "relay-cli login -u tianyijie"
```

Explicit `--token`, `--token-file`, `--password`, and `--password-file` options
remain available and override the default files.

Remote operations are `create`, `list`, `send`, `text`, `keys`, `read`,
`follow`, and `close`. `send` presses Enter, `text` does not, and `keys` sends
named key sequences. `Ctrl+C` ends a remote `follow` without closing the target.
Remote operations other than `create` and `list` require an existing target.

Each registered client receives an internal immutable owner identity. Its target
namespace is private: another client cannot list, read, write, follow, or close
its targets, even when both clients use the same target name.

## Basic Usage

The commands in this section use implicit local mode.

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
output, and returns the command exit code. Run mode has no fixed execution
timeout. Pressing `Ctrl+C` interrupts the current local command, waits for the
shell to resynchronize, and keeps the target available for later commands.
Captured command output is bounded at 8 MiB; if that limit is reached, ptymux
continues draining the command to completion and prints a truncation warning.
Use this mode for normal shell commands.

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
`sleep 2 && echo done`, can return before all output arrives. The quiet duration
is not a total timeout: every output chunk restarts it, so continuously producing
commands can keep the request open indefinitely. Captured quiet-wait output is
also bounded at 8 MiB while ptymux continues waiting for the quiet boundary.

### Send Mode

Use `send` when you want to write input to the target without a completion
marker:

```sh
ptymux send work "ls"
```

By default, `send` writes input and returns without printing output. The
background reader keeps the current screen and bounded ANSI terminal-line
history up to date.

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

Use `command` to send terminal key sequences and automatically press Enter at
the end:

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

### Text And Keys

Use `text` to type literal text without pressing Enter:

```sh
ptymux text work "hello"
ptymux keys work "enter"
```

Use `keys` to send key sequences without an implicit Enter:

```sh
ptymux keys work "ctrl-c"
ptymux keys work "up enter"
ptymux keys -t 500ms work "ctrl-c"
ptymux keys -f work "pageup"
```

Supported named keys include `enter`, `esc`, `escape`, `tab`, `backspace`,
`space`, `up`, `down`, `left`, `right`, `home`, `end`, `delete`, `pageup`, and
`pagedown`.

Prefer `text` and `keys` for programmable interaction because they do exactly
what their names say. `command` remains available when the desired behavior is
"send these keys, then press Enter". The legacy `ctrl-c` command remains as a
compatibility alias.

### Ctrl+C

Send Ctrl+C to a target:

```sh
ptymux ctrl-c work
```

This writes the ETX byte (`0x03`) to the target PTY and follows output, just like
`send`. Stop observing with `Ctrl+C`; the target remains alive.

### Read Mode

Read the current terminal screen with ANSI styling:

```sh
ptymux read work
```

Read the most recent ANSI terminal history lines:

```sh
ptymux read -n 3 work
```

Lines are returned from oldest to newest within the selected window. This is
bounded, line-oriented terminal history, not command history or full scrollback.
`N` must be between 0 and 4096; zero selects the current screen like plain
`read`. While an alternate screen is active, `read -n N` returns its last `N` visible
rows. `read` is read-only and does not block commands running in other clients.

### Follow Mode

Stream future raw PTY output without sending input:

```sh
ptymux follow work
```

Stop observing with `Ctrl+C`; the target remains alive. `follow` preserves ANSI
and other terminal control sequences, does not replay the current screen or
history, and does not lock the target. With a current daemon, local streaming
failures are kept separate from terminal bytes: they are printed on stderr and
return a non-zero status. New clients can still use older daemons, but legacy
streaming may mix daemon errors into stdout and may return a zero status. Run
`ptymux stop` to restart the daemon and use the current behavior.

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

Once shutdown starts, the daemon stops admitting new operations before it
closes targets and waits for accepted requests to finish.

Close one target without stopping the daemon:

```sh
ptymux kill work
```

The default socket path is:

```text
~/.ptymux/sockets/ptymux-default.sock
```

`ptymux` creates the `~/.ptymux/sockets` directory automatically when the daemon
starts. A custom socket path never replaces a regular file, directory, or
symlink. An existing Unix socket is removed only when it belongs to the current
user and is confirmed stale; shutdown removes only the socket created by that
daemon.

Use a custom socket when you want a separate daemon:

```sh
ptymux --socket /tmp/project-a.sock work "pwd"
ptymux --socket /tmp/project-a.sock stop
```

## Configuration And Remote Aliases

Local daemon settings remain in `~/.ptymux/config`, with
`~/.ptymux/config.json` as a legacy fallback:

```json
{
  "shell": "/bin/bash",
  "auto_release": {
    "enabled": true,
    "target_idle_timeout": "8h",
    "daemon_idle_timeout": "30m"
  }
}
```

Remote aliases are loaded only from the private file
`~/.ptymux/client/config`:

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

This is an incompatible alias-path migration: remote `clients` entries in
`~/.ptymux/config` and `~/.ptymux/config.json` are ignored. The new client
config and all client secret files must be private regular non-symlink files;
use mode `0600` and keep `~/.ptymux/client` at mode `0700`. An alias may still
set `token_file` and `password_file` to override the default secret paths.

The alias makes remote commands shorter:

```sh
ptymux-client relay create work
ptymux-client relay send work "relay-cli login -u tianyijie"
ptymux-client relay read -n 20 work
ptymux-client relay follow work
ptymux-client relay keys work ctrl-c
ptymux-client relay close work
```

Explicit connection flags override alias fields. Prefer `token_file` and
`password_file` over inline `token` and `password` fields or CLI values so
secrets do not appear in shell history or process listings. Secret files must be
private regular files, must not be symlinks, and should use mode `0600`.

The top-level `shell` and `auto_release` settings apply to the local daemon.
`shell` defaults to `/bin/sh`. `target_idle_timeout` defaults to `8h`, and
`daemon_idle_timeout` defaults to `30m`. Set a timeout to `"0"` to disable that
specific release behavior, or set `enabled` to `false` to disable local
automatic release entirely. Restart the local daemon with `ptymux stop` after
changing these settings.

## Rotate Or Revoke A Remote Client

Rotate a client password while preserving its owner identity and targets. Set
`password_file` below to the alias's actual `password_file` path, or use the
name-derived default when the alias has no override. Migrate inline alias
passwords to a private password file before rotating.

```sh
umask 077
password_file="$HOME/.ptymux/client/tianyijie.password"
password_tmp="$(mktemp "${password_file}.XXXXXX")"
if ptymux-client relay rotate > "$password_tmp"; then
  mv -- "$password_tmp" "$password_file"
else
  rm -f "$password_tmp"
  false
fi
```

Do not redirect rotation directly over the current password file: the shell
would truncate it before ptymux could authenticate with the old password.

Rotation invalidates the old password and connections authenticated with the old
credential generation. Revoke a client and close only that owner's connections
and targets:

```sh
ptymux-client relay revoke
```

A revoked name can be registered again, but it receives a new owner identity and
does not regain access to old targets.

## Relay Docker Image

The deployment Dockerfile lives in the separate `xiaogang_pty` checkout and
builds ptymux through named BuildKit contexts. Set both checkout paths explicitly:

```sh
ptymux_src=/path/to/ptymux
xiaogang_pty=/path/to/xiaogang_pty
gomod_cache="$(go env GOMODCACHE)"
docker build --network host \
  --build-context ptymux-src="$ptymux_src" \
  --build-context gomod-cache="$gomod_cache" \
  -t ptymux-relay:dev \
  -f "$xiaogang_pty/Dockerfile" "$xiaogang_pty"
```

Run it with only the token file mounted read-only and a persistent registry
volume:

```sh
docker run --rm -p 8443:8443 \
  -v "$PWD/secrets/ptymux.token:/run/secrets/ptymux.token:ro" \
  -v ptymux-data:/var/lib/ptymux \
  --name relay-dev \
  ptymux-relay:dev
```

The image runs `ptymux-server` as the non-root `work` user, installs
`relay-cli`, and stores the client registry in `/var/lib/ptymux`. The registry
survives container replacement when the volume is retained; running shells do
not.

## Notes

- Each full target path resolves to a long-lived shell process attached to a PTY.
- PTY output is combined stdout/stderr, like a normal terminal.
- Completed command and quiet-wait output is cleaned of terminal controls while
  preserving prompt text. `read` returns ANSI-styled screen or history output.
- `send -f`, key-follow modes, and `ctrl-c` stream cleaned output. Local and
  remote `follow` stream only future raw PTY output; disconnecting the follower
  does not stop the target.
- Disconnecting a remote client does not close its targets. Use remote `close`,
  client `revoke`, or server shutdown when the target should stop.
- There is no full interactive attach mode in the first remote version; input is
  sent with `send`, `text`, or `keys`.

## License

MIT
