package server

import (
	"io"
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

type fakeRunner struct {
	closed bool
}

func (f *fakeRunner) Run(string) (RunResult, error) { return RunResult{}, nil }
func (f *fakeRunner) RunIdle(string) (RunResult, error) {
	return RunResult{}, nil
}
func (f *fakeRunner) Send(string) error { return nil }
func (f *fakeRunner) SendFollow(string, io.Writer, <-chan struct{}) error {
	return nil
}
func (f *fakeRunner) CtrlCFollow(io.Writer, <-chan struct{}) error {
	return nil
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
