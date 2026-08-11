package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"ptymux/internal/app"
	"ptymux/internal/server"
)

func TestWriteActionOutputPreservesReadBytes(t *testing.T) {
	var output bytes.Buffer
	writeActionOutput(&output, app.ActionRead, "\x1b[41m  \x1b[0m")
	if got, want := output.String(), "\x1b[41m  \x1b[0m"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestWriteActionOutputAddsCommandNewline(t *testing.T) {
	var output bytes.Buffer
	writeActionOutput(&output, app.ActionRun, "result")
	if got, want := output.String(), "result\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestWriteTruncationWarning(t *testing.T) {
	var output bytes.Buffer
	writeTruncationWarning(&output, server.Response{})
	if output.Len() != 0 {
		t.Fatalf("unexpected warning = %q", output.String())
	}
	writeTruncationWarning(&output, server.Response{Truncated: true})
	if got := output.String(); got != "ptymux: output truncated at 8388608 bytes\n" {
		t.Fatalf("warning = %q", got)
	}
}

func TestRunLocalWithSignalsCancelsRequest(t *testing.T) {
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			socketPath := filepath.Join(t.TempDir(), "ptymux.sock")
			listener, err := net.Listen("unix", socketPath)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()

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
				_, err = io.Copy(io.Discard, conn)
				serveErr <- err
			}()

			signals := make(chan os.Signal, 1)
			type runResult struct {
				signal os.Signal
				err    error
			}
			result := make(chan runResult, 1)
			go func() {
				_, received, err := runLocalWithSignals(app.Config{
					Mode:   app.ModeLocal,
					Action: app.ActionList,
					Socket: socketPath,
				}, signals)
				result <- runResult{signal: received, err: err}
			}()
			<-requestRead
			signals <- sig

			select {
			case got := <-result:
				if got.signal != sig {
					t.Fatalf("signal = %v, want %v", got.signal, sig)
				}
				if !errors.Is(got.err, context.Canceled) {
					t.Fatalf("error = %v, want context canceled", got.err)
				}
			case <-time.After(time.Second):
				t.Fatal("runLocalWithSignals did not return after signal")
			}
			if err := <-serveErr; err != nil {
				t.Fatal(err)
			}
		})
	}
}
