package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
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
	command           string
	commandWait       string
	commandWaitQuiet  time.Duration
	commandWaitResult RunResult
	commandFollow     string
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
