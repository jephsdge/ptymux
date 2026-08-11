package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"ptymux/internal/app"
	"ptymux/internal/server"
)

func main() {
	cfg, err := app.ParseLocal(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if cfg.Action == app.ActionHelp {
		fmt.Print(app.LocalHelpText())
		return
	}

	resp, receivedSignal, err := run(cfg)
	if receivedSignal != nil {
		if receivedSignal == syscall.SIGTERM {
			os.Exit(143)
		}
		os.Exit(130)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	writeTruncationWarning(os.Stderr, resp)

	switch cfg.Action {
	case app.ActionRun, app.ActionIdle, app.ActionSend, app.ActionText, app.ActionCommand, app.ActionKeys, app.ActionRead:
		writeActionOutput(os.Stdout, cfg.Action, resp.Output)
		os.Exit(resp.ExitCode)
	case app.ActionCtrlC, app.ActionFollow:
		os.Exit(resp.ExitCode)
	case app.ActionList:
		printList(resp.Snapshot)
	}
}

func run(cfg app.Config) (server.Response, os.Signal, error) {
	if cfg.Action == app.ActionDaemon {
		resp, err := app.RunLocal(cfg)
		return resp, nil, err
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	return runLocalWithSignals(cfg, signals)
}

func runLocalWithSignals(cfg app.Config, signals <-chan os.Signal) (server.Response, os.Signal, error) {
	ctx, cancel := context.WithCancel(context.Background())
	received := make(chan os.Signal, 1)
	go func() {
		select {
		case sig := <-signals:
			received <- sig
			cancel()
		case <-ctx.Done():
		}
	}()

	resp, err := app.RunLocalContext(ctx, cfg)
	cancel()
	select {
	case sig := <-received:
		return server.Response{}, sig, err
	default:
		return resp, nil, err
	}
}

func writeTruncationWarning(output io.Writer, response server.Response) {
	if response.Truncated {
		fmt.Fprintf(output, "ptymux: output truncated at %d bytes\n", server.MaxRunOutputBytes)
	}
}

func writeActionOutput(output io.Writer, action app.Action, value string) {
	_, _ = io.WriteString(output, value)
	if action != app.ActionRead && value != "" && value[len(value)-1] != '\n' {
		_, _ = io.WriteString(output, "\n")
	}
}

func printList(snapshot server.Snapshot) {
	for _, session := range snapshot.Sessions {
		fmt.Println(session.Name)
		for _, pane := range session.Panes {
			fmt.Printf("  %s\n", pane.Name)
			for _, tab := range pane.Tabs {
				fmt.Printf("    %s\n", tab.Name)
			}
		}
	}
}
