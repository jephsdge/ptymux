package pty_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"ptymux/pty"
)

type chunkWriter struct {
	mu     sync.Mutex
	output string
	ready  chan struct{}
	marker string
	once   sync.Once
}

func (w *chunkWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	w.output += string(data)
	matched := strings.Contains(w.output, w.marker)
	w.mu.Unlock()
	if matched {
		w.once.Do(func() { close(w.ready) })
	}
	return len(data), nil
}

func TestServiceFacadeTargetLifecycle(t *testing.T) {
	service := pty.New("/bin/sh")
	t.Cleanup(func() { _ = service.CloseAll() })
	target := pty.Target{Session: "facade", Pane: "default", Tab: "default"}

	if err := service.Create(target); err != nil {
		t.Fatal(err)
	}
	if !service.Exists(target) {
		t.Fatal("created target does not exist")
	}
	targets, err := service.Targets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0] != target {
		t.Fatalf("Targets = %+v, want %+v", targets, target)
	}
	prefixTargets, err := service.Targets(pty.Target{Session: target.Session})
	if err != nil {
		t.Fatal(err)
	}
	if len(prefixTargets) != 1 || prefixTargets[0] != (pty.Target{Session: target.Session, Pane: target.Pane}) {
		t.Fatalf("prefix Targets = %+v", prefixTargets)
	}

	if err := service.Send(target, "printf 'facade-send\\n'"); err != nil {
		t.Fatal(err)
	}
	waitForRead(t, service, target, "facade-send")
	if err := service.Text(target, "printf 'facade-text\\n'"); err != nil {
		t.Fatal(err)
	}
	if err := service.Keys(target, "enter"); err != nil {
		t.Fatal(err)
	}
	waitForRead(t, service, target, "facade-text")

	ctx, cancel := context.WithCancel(context.Background())
	writer := &chunkWriter{ready: make(chan struct{}), marker: "facade-follow"}
	followDone := make(chan error, 1)
	go func() { followDone <- service.Follow(ctx, target, writer) }()
	time.Sleep(50 * time.Millisecond)
	if err := service.Send(target, "printf 'facade-follow\\n'"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-writer.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("Follow did not receive target output")
	}
	cancel()
	select {
	case err := <-followDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Follow did not stop after context cancellation")
	}

	if err := service.Close(target); err != nil {
		t.Fatal(err)
	}
	if service.Exists(target) {
		t.Fatal("closed target still exists")
	}
	if err := service.Close(target); !errors.Is(err, pty.ErrTargetNotFound) {
		t.Fatalf("second Close error = %v, want ErrTargetNotFound", err)
	}
}

func TestServiceFacadeAdmissionAndValidation(t *testing.T) {
	service := pty.New("/bin/sh")
	if err := service.Create(pty.Target{Session: "bad/name", Pane: "default", Tab: "default"}); !errors.Is(err, pty.ErrInvalidRequest) {
		t.Fatalf("invalid target error = %v, want ErrInvalidRequest", err)
	}
	service.StopAdmission()
	if err := service.Create(pty.Target{Session: "stopped", Pane: "default", Tab: "default"}); !errors.Is(err, pty.ErrServiceStopping) {
		t.Fatalf("stopped Create error = %v, want ErrServiceStopping", err)
	}
	service.Wait()
	if err := service.CloseAll(); err != nil {
		t.Fatal(err)
	}
}

func waitForRead(t *testing.T, service *pty.Service, target pty.Target, marker string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		result, err := service.Read(target, 0)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(result.Output, marker) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Read did not contain %q", marker)
}
