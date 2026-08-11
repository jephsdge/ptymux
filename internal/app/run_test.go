package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ptymux/internal/remote"
)

func TestRunRejectsUnknownMode(t *testing.T) {
	if _, err := Run(Config{Mode: Mode("unknown")}); err == nil {
		t.Fatal("Run returned nil error for unknown mode")
	}
}

func TestRunRemoteClientExplicitConfigIgnoresAliasFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".ptymux", "client", "config")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("invalid JSON"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := runRemoteClient(Config{
		Mode:       ModeClient,
		Action:     ActionRead,
		URL:        "https://relay.example",
		Token:      "token",
		ClientName: "alice",
		Password:   "password",
	})
	if err == nil || !strings.Contains(err.Error(), "base URL must be an http://host URL") {
		t.Fatalf("runRemoteClient error = %v, want explicit URL validation error", err)
	}
}

func TestRunClientContextCancelsManagementRequest(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(started)
		<-release
	}))
	defer server.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := RunClientContext(ctx, Config{
			Mode:       ModeClient,
			Action:     ActionRegister,
			URL:        server.URL,
			Token:      "token",
			ClientName: "alice",
		})
		result <- err
	}()

	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("RunClientContext error = %v, want context canceled", err)
	}
}

func TestRunRemoteServerValidatesRequiredPathsBeforeListening(t *testing.T) {
	if _, err := RunServer(Config{Mode: ModeServer, Listen: "127.0.0.1:0"}); err == nil {
		t.Fatal("RunServer returned nil error for incomplete server configuration")
	}
}

func TestRemoteOperationMapsSupportedActions(t *testing.T) {
	tests := map[Action]remote.Operation{
		ActionCreate: remote.OperationCreate,
		ActionList:   remote.OperationList,
		ActionSend:   remote.OperationSend,
		ActionText:   remote.OperationText,
		ActionKeys:   remote.OperationKeys,
		ActionRead:   remote.OperationRead,
		ActionClose:  remote.OperationClose,
	}
	for action, want := range tests {
		got, ok := remoteOperation(action)
		if !ok || got != want {
			t.Fatalf("remoteOperation(%q) = %q, %v; want %q, true", action, got, ok, want)
		}
	}
	if _, ok := remoteOperation(ActionRun); ok {
		t.Fatal("remoteOperation(ActionRun) unexpectedly succeeded")
	}
}

func TestRemoteRequestCopiesTargetInputAndReadCount(t *testing.T) {
	request := remoteRequest(Config{
		Action:    ActionRead,
		Session:   "work",
		Pane:      "main",
		Tab:       "build",
		Command:   "input",
		ReadCount: 3,
	}, remote.OperationRead)
	if request.Operation != remote.OperationRead {
		t.Fatalf("Operation = %q, want %q", request.Operation, remote.OperationRead)
	}
	if request.Target != (remote.Target{Session: "work", Pane: "main", Tab: "build"}) {
		t.Fatalf("Target = %+v", request.Target)
	}
	if request.Input != "input" || request.ReadCount != 3 {
		t.Fatalf("request = %+v", request)
	}
}
