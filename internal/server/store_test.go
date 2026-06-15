package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreCreatesNestedTargets(t *testing.T) {
	store := NewStore()

	tab := store.GetOrCreate("session1", "pane1", "tab1", func() Runner {
		return &fakeRunner{}
	})

	if tab == nil {
		t.Fatal("GetOrCreate returned nil")
	}

	got := store.Snapshot()
	if len(got.Sessions) != 1 {
		t.Fatalf("len(Sessions) = %d, want 1", len(got.Sessions))
	}
	if got.Sessions[0].Name != "session1" {
		t.Fatalf("session name = %q, want session1", got.Sessions[0].Name)
	}
	if got.Sessions[0].Panes[0].Name != "pane1" {
		t.Fatalf("pane name = %q, want pane1", got.Sessions[0].Panes[0].Name)
	}
	if got.Sessions[0].Panes[0].Tabs[0].Name != "tab1" {
		t.Fatalf("tab name = %q, want tab1", got.Sessions[0].Panes[0].Tabs[0].Name)
	}
}

func TestDaemonStopClosesTabs(t *testing.T) {
	daemon := NewDaemon("")
	runner := &fakeRunner{}
	daemon.store.GetOrCreate("session1", "pane1", "tab1", func() Runner {
		return runner
	})

	resp := daemon.Handle(Request{Action: "stop"})

	if resp.Error != "" {
		t.Fatalf("stop returned error: %s", resp.Error)
	}
	if !runner.closed {
		t.Fatal("runner was not closed")
	}
	if !waitStopped(daemon, 200*time.Millisecond) {
		t.Fatal("daemon was not marked stopped")
	}
}

func TestPrepareSocketPathCreatesSocketDirectory(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), ".ptymux", "sockets", "ptymux-default.sock")

	if err := prepareSocketPath(socketPath); err != nil {
		t.Fatalf("prepareSocketPath returned error: %v", err)
	}

	info, err := os.Stat(filepath.Dir(socketPath))
	if err != nil {
		t.Fatalf("Stat returned error: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", filepath.Dir(socketPath))
	}
}

func TestStoreCloseAllClearsTargets(t *testing.T) {
	store := NewStore()
	runner := &fakeRunner{}
	store.GetOrCreate("session1", "pane1", "tab1", func() Runner {
		return runner
	})

	if err := store.CloseAll(); err != nil {
		t.Fatalf("CloseAll returned error: %v", err)
	}

	if !runner.closed {
		t.Fatal("runner was not closed")
	}
	if got := store.Snapshot(); len(got.Sessions) != 0 {
		t.Fatalf("snapshot = %+v, want no targets", got)
	}
}

func TestStoreCloseTargetClosesAndRemovesExactTarget(t *testing.T) {
	store := NewStore()
	targetRunner := &fakeRunner{}
	otherRunner := &fakeRunner{}
	store.GetOrCreate("work", "main", "build", func() Runner {
		return targetRunner
	})
	store.GetOrCreate("work", "main", "other", func() Runner {
		return otherRunner
	})

	if err := store.CloseTarget("work", "main", "build"); err != nil {
		t.Fatalf("CloseTarget returned error: %v", err)
	}

	if !targetRunner.closed {
		t.Fatal("target runner was not closed")
	}
	if otherRunner.closed {
		t.Fatal("other runner was closed")
	}
	snapshot := store.SnapshotTarget("work", "main", "")
	if len(snapshot.Sessions) != 1 || len(snapshot.Sessions[0].Panes) != 1 {
		t.Fatalf("snapshot = %+v, want work/main to remain", snapshot)
	}
	tabs := snapshot.Sessions[0].Panes[0].Tabs
	if len(tabs) != 1 || tabs[0].Name != "other" {
		t.Fatalf("tabs = %+v, want only other", tabs)
	}
}

func TestDaemonKillTargetClosesOnlyTarget(t *testing.T) {
	daemon := NewDaemon("")
	targetRunner := &fakeRunner{}
	otherRunner := &fakeRunner{}
	daemon.store.GetOrCreate("work", "default", "default", func() Runner {
		return targetRunner
	})
	daemon.store.GetOrCreate("other", "default", "default", func() Runner {
		return otherRunner
	})

	resp := daemon.Handle(Request{Action: "kill", Session: "work", Pane: "default", Tab: "default"})

	if resp.Error != "" {
		t.Fatalf("kill returned error: %s", resp.Error)
	}
	if !targetRunner.closed {
		t.Fatal("target runner was not closed")
	}
	if otherRunner.closed {
		t.Fatal("other runner was closed")
	}
	if got := daemon.store.SnapshotTarget("work", "", ""); len(got.Sessions) != 0 {
		t.Fatalf("work snapshot = %+v, want removed target", got)
	}
}

func TestDaemonCleanupClosesIdleTargets(t *testing.T) {
	now := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	daemon := NewDaemonWithOptions("", DaemonOptions{
		AutoRelease: AutoReleaseOptions{
			Enabled:           true,
			TargetIdleTimeout: 8 * time.Hour,
			DaemonIdleTimeout: 30 * time.Minute,
		},
	})
	idleRunner := &fakeRunner{}
	activeRunner := &fakeRunner{}
	idleTab := daemon.store.GetOrCreate("idle", "default", "default", func() Runner {
		return idleRunner
	})
	activeTab := daemon.store.GetOrCreate("active", "default", "default", func() Runner {
		return activeRunner
	})
	idleTab.LastUsedAt = now.Add(-9 * time.Hour)
	activeTab.LastUsedAt = now.Add(-7 * time.Hour)

	daemon.cleanupIdle(now)

	if !idleRunner.closed {
		t.Fatal("idle runner was not closed")
	}
	if activeRunner.closed {
		t.Fatal("active runner was closed")
	}
	if got := daemon.store.SnapshotTarget("idle", "", ""); len(got.Sessions) != 0 {
		t.Fatalf("idle snapshot = %+v, want removed target", got)
	}
	if got := daemon.store.SnapshotTarget("active", "", ""); len(got.Sessions) != 1 {
		t.Fatalf("active snapshot = %+v, want active target to remain", got)
	}
}

func TestDaemonCleanupStopsEmptyIdleDaemon(t *testing.T) {
	now := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	daemon := NewDaemonWithOptions("", DaemonOptions{
		AutoRelease: AutoReleaseOptions{
			Enabled:           true,
			TargetIdleTimeout: 8 * time.Hour,
			DaemonIdleTimeout: 30 * time.Minute,
		},
	})
	daemon.lastActivity = now.Add(-31 * time.Minute)

	daemon.cleanupIdle(now)

	if !daemon.Stopped() {
		t.Fatal("daemon was not marked stopped")
	}
}

func TestDaemonCleanupKeepsNonEmptyDaemonPastDaemonTimeout(t *testing.T) {
	now := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	daemon := NewDaemonWithOptions("", DaemonOptions{
		AutoRelease: AutoReleaseOptions{
			Enabled:           true,
			TargetIdleTimeout: 8 * time.Hour,
			DaemonIdleTimeout: 30 * time.Minute,
		},
	})
	daemon.lastActivity = now.Add(-31 * time.Minute)
	tab := daemon.store.GetOrCreate("work", "default", "default", func() Runner {
		return &fakeRunner{}
	})
	tab.LastUsedAt = now.Add(-7 * time.Hour)

	daemon.cleanupIdle(now)

	if daemon.Stopped() {
		t.Fatal("daemon was stopped while a target remained")
	}
}

func TestDaemonHandleCommandRoutesToRunner(t *testing.T) {
	daemon := NewDaemon("")
	runner := &fakeRunner{}
	daemon.store.GetOrCreate("session1", "pane1", "tab1", func() Runner {
		return runner
	})

	resp := daemon.Handle(Request{Action: "command", Session: "session1", Pane: "pane1", Tab: "tab1", Command: "ctrl-o d"})

	if resp.Error != "" {
		t.Fatalf("command returned error: %s", resp.Error)
	}
	if runner.command != "ctrl-o d" {
		t.Fatalf("runner command = %q, want ctrl-o d", runner.command)
	}
}

func TestDaemonHandleCommandWaitRoutesToRunner(t *testing.T) {
	daemon := NewDaemon("")
	runner := &fakeRunner{commandWaitResult: RunResult{Output: "waited", ExitCode: 0}}
	daemon.store.GetOrCreate("session1", "pane1", "tab1", func() Runner {
		return runner
	})

	resp := daemon.Handle(Request{Action: "command", Session: "session1", Pane: "pane1", Tab: "tab1", Command: "ctrl-c", WaitMillis: 25})

	if resp.Error != "" {
		t.Fatalf("command wait returned error: %s", resp.Error)
	}
	if runner.commandWait != "ctrl-c" {
		t.Fatalf("runner commandWait = %q, want ctrl-c", runner.commandWait)
	}
	if runner.commandWaitQuiet != 25*time.Millisecond {
		t.Fatalf("quiet = %v, want 25ms", runner.commandWaitQuiet)
	}
	if resp.Output != "waited" {
		t.Fatalf("Output = %q, want waited", resp.Output)
	}
}

func TestDaemonHandleTextRoutesToRunner(t *testing.T) {
	daemon := NewDaemon("")
	runner := &fakeRunner{}
	daemon.store.GetOrCreate("session1", "pane1", "tab1", func() Runner {
		return runner
	})

	resp := daemon.Handle(Request{Action: "text", Session: "session1", Pane: "pane1", Tab: "tab1", Command: "hello"})

	if resp.Error != "" {
		t.Fatalf("text returned error: %s", resp.Error)
	}
	if runner.text != "hello" {
		t.Fatalf("runner text = %q, want hello", runner.text)
	}
}

func TestDaemonHandleKeysRoutesToRunner(t *testing.T) {
	daemon := NewDaemon("")
	runner := &fakeRunner{}
	daemon.store.GetOrCreate("session1", "pane1", "tab1", func() Runner {
		return runner
	})

	resp := daemon.Handle(Request{Action: "keys", Session: "session1", Pane: "pane1", Tab: "tab1", Command: "left enter"})

	if resp.Error != "" {
		t.Fatalf("keys returned error: %s", resp.Error)
	}
	if runner.keys != "left enter" {
		t.Fatalf("runner keys = %q, want left enter", runner.keys)
	}
}

func TestDaemonHandleKeysWaitRoutesToRunner(t *testing.T) {
	daemon := NewDaemon("")
	runner := &fakeRunner{keysWaitResult: RunResult{Output: "keys waited", ExitCode: 0}}
	daemon.store.GetOrCreate("session1", "pane1", "tab1", func() Runner {
		return runner
	})

	resp := daemon.Handle(Request{Action: "keys", Session: "session1", Pane: "pane1", Tab: "tab1", Command: "ctrl-c", WaitMillis: 25})

	if resp.Error != "" {
		t.Fatalf("keys wait returned error: %s", resp.Error)
	}
	if runner.keysWait != "ctrl-c" {
		t.Fatalf("runner keysWait = %q, want ctrl-c", runner.keysWait)
	}
	if runner.keysWaitQuiet != 25*time.Millisecond {
		t.Fatalf("quiet = %v, want 25ms", runner.keysWaitQuiet)
	}
	if resp.Output != "keys waited" {
		t.Fatalf("Output = %q, want keys waited", resp.Output)
	}
}

func TestDaemonHandleStreamCommandUsesCommandFollowAndCtrlCUsesCtrlCFollow(t *testing.T) {
	daemon := NewDaemon("")
	runner := &fakeRunner{}
	daemon.store.GetOrCreate("session1", "pane1", "tab1", func() Runner {
		return runner
	})

	commandConn := newStreamTestConn(nil)
	daemon.handleStream(commandConn, Request{Action: "command", Session: "session1", Pane: "pane1", Tab: "tab1", Command: "ctrl-o d", Follow: true})

	if runner.commandFollow != "ctrl-o d" {
		t.Fatalf("runner commandFollow = %q, want ctrl-o d", runner.commandFollow)
	}
	if !strings.Contains(commandConn.String(), "followed ctrl-o d") {
		t.Fatalf("streamed output = %q, want command follow output", commandConn.String())
	}

	ctrlCConn := newStreamTestConn(nil)
	daemon.handleStream(ctrlCConn, Request{Action: "ctrl-c", Session: "session1", Pane: "pane1", Tab: "tab1"})

	if !runner.ctrlCFollow {
		t.Fatal("runner CtrlCFollow was not called")
	}
	if !strings.Contains(ctrlCConn.String(), "ctrl-c followed") {
		t.Fatalf("streamed output = %q, want ctrl-c follow output", ctrlCConn.String())
	}

	keysConn := newStreamTestConn(nil)
	daemon.handleStream(keysConn, Request{Action: "keys", Session: "session1", Pane: "pane1", Tab: "tab1", Command: "left enter", Follow: true})

	if runner.keysFollow != "left enter" {
		t.Fatalf("runner keysFollow = %q, want left enter", runner.keysFollow)
	}
	if !strings.Contains(keysConn.String(), "keys followed left enter") {
		t.Fatalf("streamed output = %q, want keys follow output", keysConn.String())
	}
}

func TestDaemonHandleStreamCommandRejectsInvalidKeysBeforeCreatingTarget(t *testing.T) {
	daemon := NewDaemon("")
	var input bytes.Buffer
	if err := json.NewEncoder(&input).Encode(Request{Action: "command", Session: "session1", Pane: "pane1", Tab: "tab1", Command: "alt-x", Follow: true}); err != nil {
		t.Fatalf("encode request returned error: %v", err)
	}
	conn := newStreamTestConn(input.Bytes())

	daemon.handle(conn)

	var resp Response
	if err := json.NewDecoder(strings.NewReader(conn.String())).Decode(&resp); err != nil {
		t.Fatalf("decode response returned error: %v", err)
	}
	if resp.Error == "" {
		t.Fatal("Error is empty, want invalid key error")
	}
	if got := daemon.store.Snapshot(); len(got.Sessions) != 0 {
		t.Fatalf("snapshot = %+v, want no created targets", got)
	}
}

func TestDaemonHandleStreamKeysRejectsInvalidKeysBeforeCreatingTarget(t *testing.T) {
	daemon := NewDaemon("")
	var input bytes.Buffer
	if err := json.NewEncoder(&input).Encode(Request{Action: "keys", Session: "session1", Pane: "pane1", Tab: "tab1", Command: "alt-x", Follow: true}); err != nil {
		t.Fatalf("encode request returned error: %v", err)
	}
	conn := newStreamTestConn(input.Bytes())

	daemon.handle(conn)

	var resp Response
	if err := json.NewDecoder(strings.NewReader(conn.String())).Decode(&resp); err != nil {
		t.Fatalf("decode response returned error: %v", err)
	}
	if resp.Error == "" {
		t.Fatal("Error is empty, want invalid key error")
	}
	if got := daemon.store.Snapshot(); len(got.Sessions) != 0 {
		t.Fatalf("snapshot = %+v, want no created targets", got)
	}
}

type streamTestConn struct {
	input *bytes.Reader
	mu    sync.Mutex
	out   bytes.Buffer
}

func newStreamTestConn(input []byte) *streamTestConn {
	return &streamTestConn{input: bytes.NewReader(input)}
}

func (c *streamTestConn) Read(p []byte) (int, error) {
	return c.input.Read(p)
}

func (c *streamTestConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out.Write(p)
}

func (c *streamTestConn) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out.String()
}

func (c *streamTestConn) Close() error                     { return nil }
func (c *streamTestConn) LocalAddr() net.Addr              { return testAddr("local") }
func (c *streamTestConn) RemoteAddr() net.Addr             { return testAddr("remote") }
func (c *streamTestConn) SetDeadline(time.Time) error      { return nil }
func (c *streamTestConn) SetReadDeadline(time.Time) error  { return nil }
func (c *streamTestConn) SetWriteDeadline(time.Time) error { return nil }

type testAddr string

func (a testAddr) Network() string { return "test" }
func (a testAddr) String() string  { return string(a) }

type fakeRunner struct {
	closed            bool
	text              string
	command           string
	commandWait       string
	commandWaitQuiet  time.Duration
	commandWaitResult RunResult
	commandFollow     string
	keys              string
	keysWait          string
	keysWaitQuiet     time.Duration
	keysWaitResult    RunResult
	keysFollow        string
	ctrlCFollow       bool
}

func (f *fakeRunner) Run(string) (RunResult, error) { return RunResult{}, nil }
func (f *fakeRunner) RunIdle(string) (RunResult, error) {
	return RunResult{}, nil
}
func (f *fakeRunner) Send(string) error { return nil }
func (f *fakeRunner) SendWait(string, time.Duration) (RunResult, error) {
	return RunResult{}, nil
}
func (f *fakeRunner) SendFollow(string, io.Writer, <-chan struct{}) error {
	return nil
}
func (f *fakeRunner) Text(input string) error {
	f.text = input
	return nil
}
func (f *fakeRunner) Follow(io.Writer, <-chan struct{}) error {
	return nil
}
func (f *fakeRunner) CtrlCFollow(output io.Writer, done <-chan struct{}) error {
	f.ctrlCFollow = true
	_, _ = io.WriteString(output, "ctrl-c followed")
	return nil
}
func (f *fakeRunner) Command(keys string) error {
	f.command = keys
	return nil
}
func (f *fakeRunner) CommandWait(keys string, quietFor time.Duration) (RunResult, error) {
	f.commandWait = keys
	f.commandWaitQuiet = quietFor
	return f.commandWaitResult, nil
}
func (f *fakeRunner) CommandFollow(keys string, output io.Writer, done <-chan struct{}) error {
	f.commandFollow = keys
	_, _ = io.WriteString(output, "followed "+keys)
	return nil
}
func (f *fakeRunner) Keys(keys string) error {
	f.keys = keys
	return nil
}
func (f *fakeRunner) KeysWait(keys string, quietFor time.Duration) (RunResult, error) {
	f.keysWait = keys
	f.keysWaitQuiet = quietFor
	return f.keysWaitResult, nil
}
func (f *fakeRunner) KeysFollow(keys string, output io.Writer, done <-chan struct{}) error {
	f.keysFollow = keys
	_, _ = io.WriteString(output, "keys followed "+keys)
	return nil
}
func (f *fakeRunner) Read(int) (RunResult, error) {
	return RunResult{}, nil
}
func (f *fakeRunner) Close() error {
	f.closed = true
	return nil
}

func waitStopped(daemon *Daemon, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if daemon.Stopped() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
