package server

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrepareSocketPathPreservesNonSockets(t *testing.T) {
	tests := []struct {
		name  string
		setup func(string) error
	}{
		{name: "regular file", setup: func(path string) error { return os.WriteFile(path, []byte("keep"), 0o600) }},
		{name: "directory", setup: func(path string) error { return os.Mkdir(path, 0o700) }},
		{name: "symlink", setup: func(path string) error {
			target := path + ".target"
			if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
				return err
			}
			return os.Symlink(target, path)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ptymux.sock")
			if err := test.setup(path); err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := prepareSocketPath(path); err == nil {
				t.Fatal("prepareSocketPath unexpectedly succeeded")
			}
			after, err := os.Lstat(path)
			if err != nil {
				t.Fatalf("path was removed: %v", err)
			}
			if !os.SameFile(before, after) {
				t.Fatal("path was replaced")
			}
		})
	}
}

func TestPrepareSocketPathHandlesLiveAndStaleSockets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ptymux.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := prepareSocketPath(path); err == nil || !strings.Contains(err.Error(), "already listening") {
		t.Fatalf("live socket error = %v", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("live socket was removed: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := prepareSocketPath(path); err != nil {
		t.Fatalf("stale socket was not accepted: %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("stale socket still exists: %v", err)
	}
}

func TestDaemonShutdownPreservesReplacementPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ptymux.sock")
	daemon := NewDaemon("")
	serveDone := make(chan error, 1)
	go func() { serveDone <- daemon.Serve(path) }()
	waitForUnixSocket(t, path)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	daemon.requestStop(false)
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not stop")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("replacement path was removed: %v", err)
	}
	if string(data) != "replacement" {
		t.Fatalf("replacement data = %q", data)
	}
}

func TestDaemonStopRespondsAndClosesIdleConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ptymux.sock")
	daemon := NewDaemon("")
	serveDone := make(chan error, 1)
	go func() { serveDone <- daemon.Serve(path) }()
	waitForUnixSocket(t, path)

	idle, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	stopConn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(stopConn).Encode(Request{Action: "stop"}); err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.NewDecoder(stopConn).Decode(&response); err != nil {
		t.Fatalf("decode stop response: %v", err)
	}
	_ = stopConn.Close()
	if response.Error != "" {
		t.Fatalf("stop response error = %q", response.Error)
	}

	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve was blocked by idle accepted connection")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("daemon socket still exists: %v", err)
	}
}

func TestDaemonClosesConnectionThatDoesNotSendRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ptymux.sock")
	daemon := NewDaemonWithOptions("", DaemonOptions{
		InitialRequestTimeout: 30 * time.Millisecond,
		WriteTimeout:          time.Second,
	})
	serveDone := make(chan error, 1)
	go func() { serveDone <- daemon.Serve(path) }()
	waitForUnixSocket(t, path)

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		t.Fatalf("decode timeout response: %v", err)
	}
	if !strings.Contains(response.Error, "timeout") {
		t.Fatalf("response error = %q, want request timeout", response.Error)
	}

	stopDaemonForTest(t, daemon, serveDone)
}

func TestDaemonLimitsConnectionsAndRestoresCapacity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ptymux.sock")
	daemon := NewDaemonWithOptions("", DaemonOptions{
		MaxConnections:        1,
		InitialRequestTimeout: 5 * time.Second,
	})
	serveDone := make(chan error, 1)
	go func() { serveDone <- daemon.Serve(path) }()
	waitForUnixSocket(t, path)
	waitForConnectionCount(t, daemon, 0)

	first, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	waitForConnectionCount(t, daemon, 1)

	second, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var one [1]byte
	if _, err := second.Read(one[:]); err == nil {
		t.Fatal("connection above limit remained open")
	}
	_ = second.Close()

	_ = first.Close()
	waitForConnectionCount(t, daemon, 0)

	third, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(third).Encode(Request{Action: "list"}); err != nil {
		t.Fatal(err)
	}
	if err := third.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.NewDecoder(third).Decode(&response); err != nil {
		t.Fatalf("decode response after capacity restored: %v", err)
	}
	_ = third.Close()
	if response.Error != "" {
		t.Fatalf("response error = %q", response.Error)
	}

	stopDaemonForTest(t, daemon, serveDone)
}

func TestDaemonClosingAcceptedConnectionsUnblocksStreamWriter(t *testing.T) {
	daemon := NewDaemonWithOptions("", DaemonOptions{WriteTimeout: 5 * time.Second})
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	if !daemon.registerConnection(serverConn) {
		t.Fatal("registerConnection rejected test connection")
	}
	daemon.handlers.Add(1)
	go daemon.handleTracked(serverConn)

	envelope := Request{
		Action:        LocalStreamEnvelopeAction,
		StreamAction:  "follow",
		StreamVersion: LocalStreamVersion,
		Session:       "work",
		Pane:          "default",
		Tab:           "default",
	}
	if err := json.NewEncoder(clientConn).Encode(envelope); err != nil {
		t.Fatal(err)
	}
	waitForConnectionActive(t, daemon)

	handlersDone := make(chan struct{})
	go func() {
		daemon.handlers.Wait()
		close(handlersDone)
	}()
	daemon.closeAcceptedConnections()
	select {
	case <-handlersDone:
	case <-time.After(time.Second):
		t.Fatal("stream handler remained blocked after connection close")
	}
}

func TestDaemonClientDisconnectCancelsRunAndPreservesTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ptymux.sock")
	daemon := NewDaemon("/bin/sh")
	serveDone := make(chan error, 1)
	go func() { serveDone <- daemon.Serve(path) }()
	waitForUnixSocket(t, path)

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	runRequest := Request{
		Action:  "run",
		Session: "work",
		Pane:    "default",
		Tab:     "default",
		Command: "printf started; sleep 10",
	}
	if err := json.NewEncoder(conn).Encode(runRequest); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for daemon.service.TargetCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if daemon.service.TargetCount() == 0 {
		t.Fatal("run request did not create target")
	}
	time.Sleep(100 * time.Millisecond)
	_ = conn.Close()

	recovery, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Close()
	runRequest.Command = "printf recovered"
	if err := json.NewEncoder(recovery).Encode(runRequest); err != nil {
		t.Fatal(err)
	}
	if err := recovery.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.NewDecoder(recovery).Decode(&response); err != nil {
		t.Fatalf("decode recovery response: %v", err)
	}
	if response.Error != "" {
		t.Fatalf("recovery response error = %q", response.Error)
	}
	if !strings.Contains(response.Output, "recovered") {
		t.Fatalf("recovery output = %q, want recovered", response.Output)
	}

	stopDaemonForTest(t, daemon, serveDone)
}

func stopDaemonForTest(t *testing.T, daemon *Daemon, serveDone <-chan error) {
	t.Helper()
	daemon.requestStop(false)
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not stop")
	}
}

func waitForConnectionCount(t *testing.T, daemon *Daemon, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		daemon.lifecycleMu.Lock()
		count := len(daemon.connections)
		daemon.lifecycleMu.Unlock()
		if count == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	daemon.lifecycleMu.Lock()
	count := len(daemon.connections)
	daemon.lifecycleMu.Unlock()
	t.Fatalf("connection count = %d, want %d", count, want)
}

func waitForConnectionActive(t *testing.T, daemon *Daemon) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		daemon.lifecycleMu.Lock()
		active := false
		for _, isActive := range daemon.connections {
			active = active || isActive
		}
		daemon.lifecycleMu.Unlock()
		if active {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("connection did not become active")
}

func waitForUnixSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", path, 20*time.Millisecond)
		if err == nil {
			_ = conn.SetDeadline(time.Now().Add(100 * time.Millisecond))
			encodeErr := json.NewEncoder(conn).Encode(Request{Action: "list"})
			var response Response
			decodeErr := json.NewDecoder(conn).Decode(&response)
			_ = conn.Close()
			if encodeErr == nil && decodeErr == nil && response.Error == "" {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("socket %s did not become ready", path)
}
