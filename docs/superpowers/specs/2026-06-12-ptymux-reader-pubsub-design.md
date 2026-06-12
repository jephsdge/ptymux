# ptymux Reader Pub/Sub Design

Date: 2026-06-12

## Goal

Refactor ptymux so each target has exactly one background reader for its PTY.
All command modes consume output through snapshots or subscriptions instead of
reading the PTY directly.

This fixes the current lock contention where `follow`, `send -f`, or `ctrl-c`
can occupy the PTY read path and block other clients.

## Scope

In scope:

- A single background PTY reader per tab.
- In-memory broadcast subscriptions for live output.
- Read-only `follow` and `read` behavior.
- Subscriber-based `run`, `idle`, `send -t`, `send -f`, and `ctrl-c`.
- Bounded in-memory history for `read -n`.

Out of scope:

- Persistent terminal logs on disk.
- Replay of historical output for `follow`.
- Cross-daemon sharing of target state.
- TUI rendering.

## Architecture

Each tab owns a `PTYRunner`. When the runner is created, it starts one reader
goroutine that continuously reads from the PTY file descriptor. No command mode
reads the PTY directly.

The reader goroutine is responsible for:

- Feeding bytes into the `vt10x.Terminal` virtual screen.
- Appending completed command transcript entries to bounded in-memory history.
- Broadcasting raw output chunks to live subscribers.
- Detecting read errors and closing active subscribers.

The runner exposes command methods that either write to the PTY, read snapshots,
or subscribe to future output.

## State And Locks

The runner maintains two distinct synchronization boundaries:

- `stateMu` protects terminal state, transcript history, and subscriber registry.
- `commandMu` serializes command operations that need a stable output boundary.

`stateMu` must not be held while writing to client sockets. The reader copies
the current subscriber set, releases `stateMu`, then delivers output.

`commandMu` is held by:

- `run`
- `idle`
- `send -t`

`commandMu` is not held by:

- `follow`
- `read`
- the streaming phase of `send -f`
- the streaming phase of `ctrl-c`

`send -f` and `ctrl-c` may briefly take `commandMu` only while writing their
input bytes, then release it before streaming.

## Subscriptions

A subscription receives PTY output chunks from the moment it is registered.
It does not receive historical replay.

Each subscription has:

- A buffered channel for output chunks.
- A cancellation path triggered by client disconnect, command completion, idle
  timeout, PTY close, or socket write error.
- Automatic unregistration when canceled.

Slow subscribers must not block the PTY reader indefinitely. If a subscriber
cannot keep up, the implementation may either drop that subscriber with an error
or use a bounded timeout before unregistering it.

## Mode Semantics

### `ptymux follow <target>`

`follow` is read-only. It subscribes to future PTY output and streams it until
the client exits or the target closes.

It does not acquire `commandMu`, so other clients can continue to run commands
while `follow` is active.

### `ptymux read <target>`

`read` is read-only. It takes a snapshot of the current virtual terminal screen
from `vt10x.Terminal` and returns it.

It does not subscribe and does not acquire `commandMu`.

### `ptymux read -n N <target>`

`read -n` returns the most recent `N` command transcript entries. The selection
is recent, but output order is chronological within that selected window: oldest
selected entry first, newest selected entry last.

### `ptymux send <target> <input>`

Default `send` writes input to the target and returns immediately. The
background reader continues recording screen state and history after the client
returns.

### `ptymux send -t <duration> <target> <input>`

`send -t` writes input, subscribes to output, and returns after the PTY has been
quiet for the configured duration. Numeric durations without a unit are treated
as milliseconds.

It holds `commandMu` until completion so concurrent boundary-sensitive commands
do not mix their output.

### `ptymux idle <target> <input>`

`idle` is equivalent to `send -t 500ms`.

### `ptymux send -f <target> <input>`

`send -f` writes input, then follows future output until the client disconnects.
It releases `commandMu` after the input is written, so other clients may run
commands while this client is observing.

### `ptymux <target> <command>`

Default run mode keeps marker-based command completion. The marker is used only
internally and is hidden from returned output.

Run mode subscribes to output before writing the wrapped command and cancels the
subscription when the marker is observed.

### `ptymux ctrl-c <target>`

`ctrl-c` writes ETX byte `0x03` to the PTY, then streams output like
`send -f`. It releases any write lock before streaming.

## Error Handling

- If PTY startup fails, target creation returns the existing error response.
- If the reader receives EOF or a terminal read error, it closes the runner and
  notifies active subscribers.
- If a client socket write fails, only that subscriber is canceled.
- If a boundary-sensitive command times out, it returns a timeout exit code and
  unregisters its subscription.
- `read` against a closed target returns the last known snapshot if available;
  otherwise it returns a normal target error.

## History

History is in-memory and bounded. It stores command transcript entries, not an
unbounded raw byte log.

The initial bound should be conservative and configurable internally. A default
of 200 entries is enough for current `read -n` behavior without making long-lived
targets grow indefinitely.

## Testing

Add or update tests for:

- `follow` does not block another client from running a command.
- Multiple `follow` subscribers receive the same future output.
- `read` returns the current virtual terminal screen snapshot.
- `read -n` returns the recent window in chronological order.
- `send -t` returns after the configured idle period.
- `idle` uses the same path as `send -t 500ms`.
- `run` unregisters its subscription after marker completion.
- `send -f` unregisters after client cancellation.
- PTY reader shutdown closes active subscribers.

## Compatibility

The CLI surface remains the same as currently planned:

- `ptymux <target> <command>`
- `ptymux idle <target> <input>`
- `ptymux send [-f | -t duration] <target> <input>`
- `ptymux read [-n N] <target>`
- `ptymux follow <target>`
- `ptymux ctrl-c <target>`

`-f` and `-t` remain mutually exclusive for `send`.
