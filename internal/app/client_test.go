package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"ptymux/internal/server"
)

func TestStreamSendReadsFramedDataWithoutInterpretingPayload(t *testing.T) {
	listener, socketPath := listenUnixForTest(t)
	streamData := []byte("\x1b[31mPTMX {\"error\":\"terminal data\"}\x00\xff")
	envelopeCh := make(chan server.Request, 1)
	serveErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serveErr <- err
			return
		}
		defer conn.Close()
		var envelope server.Request
		if err := json.NewDecoder(conn).Decode(&envelope); err != nil {
			serveErr <- err
			return
		}
		envelopeCh <- envelope
		if err := server.WriteLocalStreamFrame(conn, server.LocalStreamFrameStarted, nil); err != nil {
			serveErr <- err
			return
		}
		if err := server.WriteLocalStreamFrame(conn, server.LocalStreamFrameData, streamData); err != nil {
			serveErr <- err
			return
		}
		serveErr <- server.WriteLocalStreamFrame(conn, server.LocalStreamFrameEnd, nil)
	}()

	req := server.Request{Action: "follow", Session: "work", Pane: "main", Tab: "shell"}
	var output bytes.Buffer
	if err := streamSend(socketPath, req, &output); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), streamData) {
		t.Fatalf("output = %q, want %q", output.Bytes(), streamData)
	}
	envelope := <-envelopeCh
	if envelope.Action != server.LocalStreamEnvelopeAction || envelope.StreamVersion != server.LocalStreamVersion || envelope.StreamAction != req.Action {
		t.Fatalf("envelope = %+v", envelope)
	}
	if err := <-serveErr; err != nil {
		t.Fatal(err)
	}
}

func TestStreamSendReturnsFramedDaemonError(t *testing.T) {
	listener, socketPath := listenUnixForTest(t)
	serveErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serveErr <- err
			return
		}
		defer conn.Close()
		var envelope server.Request
		if err := json.NewDecoder(conn).Decode(&envelope); err != nil {
			serveErr <- err
			return
		}
		if err := server.WriteLocalStreamFrame(conn, server.LocalStreamFrameStarted, nil); err != nil {
			serveErr <- err
			return
		}
		serveErr <- server.WriteLocalStreamFrame(conn, server.LocalStreamFrameError, []byte("target not found"))
	}()

	err := streamSend(socketPath, server.Request{Action: "follow", Session: "work", Pane: "main", Tab: "shell"}, io.Discard)
	if err == nil || err.Error() != "target not found" {
		t.Fatalf("streamSend error = %v, want target not found", err)
	}
	var connectErr *daemonConnectError
	if errors.As(err, &connectErr) {
		t.Fatalf("daemon application error was classified as connect error: %v", err)
	}
	if err := <-serveErr; err != nil {
		t.Fatal(err)
	}
}

func TestStreamSendFallsBackToLegacyDaemonOnce(t *testing.T) {
	listener, socketPath := listenUnixForTest(t)
	legacyData := []byte("legacy raw \x1b[32mPTMX\x00\xff")
	requests := make(chan server.Request, 2)
	serveErr := make(chan error, 1)
	go func() {
		first, err := listener.Accept()
		if err != nil {
			serveErr <- err
			return
		}
		var envelope server.Request
		if err := json.NewDecoder(first).Decode(&envelope); err != nil {
			_ = first.Close()
			serveErr <- err
			return
		}
		requests <- envelope
		if err := json.NewEncoder(first).Encode(server.Response{Error: `unknown action "stream"`}); err != nil {
			_ = first.Close()
			serveErr <- err
			return
		}
		_ = first.Close()

		second, err := listener.Accept()
		if err != nil {
			serveErr <- err
			return
		}
		defer second.Close()
		var legacy server.Request
		if err := json.NewDecoder(second).Decode(&legacy); err != nil {
			serveErr <- err
			return
		}
		requests <- legacy
		_, err = second.Write(legacyData)
		serveErr <- err
	}()

	req := server.Request{Action: "follow", Session: "work", Pane: "main", Tab: "shell"}
	var output bytes.Buffer
	if err := streamSend(socketPath, req, &output); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), legacyData) {
		t.Fatalf("output = %q, want %q", output.Bytes(), legacyData)
	}
	first := <-requests
	second := <-requests
	if first.Action != server.LocalStreamEnvelopeAction || first.StreamAction != req.Action {
		t.Fatalf("first request = %+v, want stream envelope", first)
	}
	if second.Action != req.Action || second.StreamAction != "" || second.StreamVersion != 0 {
		t.Fatalf("fallback request = %+v, want original request", second)
	}
	if err := <-serveErr; err != nil {
		t.Fatal(err)
	}
}

func TestStreamSendDoesNotFallbackForDaemonApplicationError(t *testing.T) {
	listener, socketPath := listenUnixForTest(t)
	serveErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serveErr <- err
			return
		}
		defer conn.Close()
		var envelope server.Request
		if err := json.NewDecoder(conn).Decode(&envelope); err != nil {
			serveErr <- err
			return
		}
		serveErr <- json.NewEncoder(conn).Encode(server.Response{Error: "target not found"})
	}()

	err := streamSend(socketPath, server.Request{Action: "follow", Session: "work", Pane: "main", Tab: "shell"}, io.Discard)
	if err == nil || err.Error() != "target not found" {
		t.Fatalf("streamSend error = %v, want target not found", err)
	}
	if err := <-serveErr; err != nil {
		t.Fatal(err)
	}
}

func TestStreamSendPropagatesOutputWriterError(t *testing.T) {
	listener, socketPath := listenUnixForTest(t)
	serveErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serveErr <- err
			return
		}
		defer conn.Close()
		var envelope server.Request
		if err := json.NewDecoder(conn).Decode(&envelope); err != nil {
			serveErr <- err
			return
		}
		if err := server.WriteLocalStreamFrame(conn, server.LocalStreamFrameStarted, nil); err != nil {
			serveErr <- err
			return
		}
		serveErr <- server.WriteLocalStreamFrame(conn, server.LocalStreamFrameData, []byte("output"))
	}()

	writeErr := errors.New("output failed")
	err := streamSend(socketPath, server.Request{Action: "follow", Session: "work", Pane: "main", Tab: "shell"}, errorWriter{err: writeErr})
	if !errors.Is(err, writeErr) {
		t.Fatalf("streamSend error = %v, want %v", err, writeErr)
	}
	if err := <-serveErr; err != nil {
		t.Fatal(err)
	}
}

func TestStreamSendTimesOutDuringNegotiation(t *testing.T) {
	listener, socketPath := listenUnixForTest(t)
	serveErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serveErr <- err
			return
		}
		defer conn.Close()
		var envelope server.Request
		if err := json.NewDecoder(conn).Decode(&envelope); err != nil {
			serveErr <- err
			return
		}
		_, _ = io.Copy(io.Discard, conn)
		serveErr <- nil
	}()

	err := streamSendWithNegotiationTimeout(socketPath, server.Request{Action: "follow"}, io.Discard, 30*time.Millisecond)
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("streamSend error = %v, want negotiation timeout", err)
	}
	if err := <-serveErr; err != nil {
		t.Fatal(err)
	}
}

func TestStreamSendClassifiesInitialDialFailure(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "missing.sock")
	err := streamSend(socketPath, server.Request{Action: "follow"}, io.Discard)
	var connectErr *daemonConnectError
	if !errors.As(err, &connectErr) {
		t.Fatalf("streamSend error = %v, want daemonConnectError", err)
	}
}

func TestSendContextCancellationClosesConnection(t *testing.T) {
	listener, socketPath := listenUnixForTest(t)
	requestRead := make(chan struct{})
	serveErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serveErr <- err
			return
		}
		defer conn.Close()
		var req server.Request
		if err := json.NewDecoder(conn).Decode(&req); err != nil {
			serveErr <- err
			return
		}
		close(requestRead)
		_, _ = io.Copy(io.Discard, conn)
		serveErr <- nil
	}()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := sendContext(ctx, socketPath, server.Request{Action: "list"})
		result <- err
	}()
	<-requestRead
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("sendContext error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("sendContext did not return after cancellation")
	}
	if err := <-serveErr; err != nil {
		t.Fatal(err)
	}
}

func TestStreamSendContextCancellationClosesConnection(t *testing.T) {
	listener, socketPath := listenUnixForTest(t)
	started := make(chan struct{})
	serveErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serveErr <- err
			return
		}
		defer conn.Close()
		var envelope server.Request
		if err := json.NewDecoder(conn).Decode(&envelope); err != nil {
			serveErr <- err
			return
		}
		if err := server.WriteLocalStreamFrame(conn, server.LocalStreamFrameStarted, nil); err != nil {
			serveErr <- err
			return
		}
		close(started)
		_, _ = io.Copy(io.Discard, conn)
		serveErr <- nil
	}()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- streamSendContext(ctx, socketPath, server.Request{Action: "follow"}, io.Discard)
	}()
	<-started
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("streamSendContext error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("streamSendContext did not return after cancellation")
	}
	if err := <-serveErr; err != nil {
		t.Fatal(err)
	}
}

func TestRunLocalContextDoesNotAutoStartAfterCancellation(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "missing.sock")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := runLocalContext(ctx, Config{Action: ActionList, Socket: socketPath})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runLocalContext error = %v, want context canceled", err)
	}
	matches, err := filepath.Glob(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("canceled request created daemon socket %s", socketPath)
	}
}

func listenUnixForTest(t *testing.T) (*net.UnixListener, string) {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "ptymux.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		listener.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener, socketPath
}

type errorWriter struct {
	err error
}

func (w errorWriter) Write([]byte) (int, error) {
	return 0, w.err
}
