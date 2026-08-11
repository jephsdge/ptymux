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
	cfg, err := app.ParseClient(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if cfg.Action == app.ActionHelp {
		fmt.Print(app.ClientHelpText())
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

	switch cfg.Action {
	case app.ActionRegister, app.ActionRotate, app.ActionSend, app.ActionText, app.ActionKeys, app.ActionRead:
		writeActionOutput(os.Stdout, cfg.Action, resp.Output)
		os.Exit(resp.ExitCode)
	case app.ActionFollow:
		os.Exit(resp.ExitCode)
	case app.ActionList:
		printList(os.Stdout, resp.Snapshot)
	}
}

func run(cfg app.Config) (server.Response, os.Signal, error) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	return runClientWithSignals(cfg, signals)
}

func runClientWithSignals(cfg app.Config, signals chan os.Signal) (server.Response, os.Signal, error) {
	ctx, cancel := context.WithCancel(context.Background())
	received := make(chan os.Signal, 1)
	go func() {
		select {
		case sig := <-signals:
			signal.Stop(signals)
			received <- sig
			cancel()
		case <-ctx.Done():
		}
	}()

	resp, err := app.RunClientContext(ctx, cfg)
	cancel()
	select {
	case sig := <-received:
		return server.Response{}, sig, err
	default:
		return resp, nil, err
	}
}

func writeActionOutput(output io.Writer, action app.Action, value string) {
	_, _ = io.WriteString(output, value)
	if action != app.ActionRead && value != "" && value[len(value)-1] != '\n' {
		_, _ = io.WriteString(output, "\n")
	}
}

func printList(output io.Writer, snapshot server.Snapshot) {
	for _, session := range snapshot.Sessions {
		fmt.Fprintln(output, session.Name)
		for _, pane := range session.Panes {
			fmt.Fprintf(output, "  %s\n", pane.Name)
			for _, tab := range pane.Tabs {
				fmt.Fprintf(output, "    %s\n", tab.Name)
			}
		}
	}
}
