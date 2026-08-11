package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

func TestWriteActionOutputAddsPasswordNewline(t *testing.T) {
	var output bytes.Buffer
	writeActionOutput(&output, app.ActionRegister, "secret")
	if got, want := output.String(), "secret\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestPrintList(t *testing.T) {
	snapshot := server.Snapshot{Sessions: []server.SessionSnapshot{{
		Name: "work",
		Panes: []server.PaneSnapshot{{
			Name: "main",
			Tabs: []server.TabSnapshot{{Name: "shell"}},
		}},
	}}}
	var output bytes.Buffer
	printList(&output, snapshot)
	if got, want := output.String(), "work\n  main\n    shell\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRunClientWithSignalsCancelsRequest(t *testing.T) {
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				close(started)
				<-release
			}))
			defer server.Close()
			defer close(release)

			signals := make(chan os.Signal, 1)
			type runResult struct {
				signal os.Signal
				err    error
			}
			result := make(chan runResult, 1)
			go func() {
				_, received, err := runClientWithSignals(app.Config{
					Mode:       app.ModeClient,
					Action:     app.ActionRegister,
					URL:        server.URL,
					Token:      "token",
					ClientName: "alice",
				}, signals)
				result <- runResult{signal: received, err: err}
			}()

			<-started
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
				t.Fatal("runClientWithSignals did not return after signal")
			}
		})
	}
}

func TestClientHelpUsesDirectExecutable(t *testing.T) {
	help := app.ClientHelpText()
	if !strings.Contains(help, "ptymux-client ALIAS") || strings.Contains(help, "ptymux client [") {
		t.Fatalf("unexpected client help:\n%s", help)
	}
}
