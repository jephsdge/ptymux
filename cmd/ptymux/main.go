package main

import (
	"fmt"
	"os"

	"ptymux/internal/app"
	"ptymux/internal/server"
)

func main() {
	cfg, err := app.Parse(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if cfg.Action == app.ActionHelp {
		fmt.Print(app.HelpText())
		return
	}

	resp, err := app.Run(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	switch cfg.Action {
	case app.ActionRun, app.ActionIdle, app.ActionSend, app.ActionText, app.ActionCommand, app.ActionKeys, app.ActionRead:
		fmt.Print(resp.Output)
		if resp.Output != "" && resp.Output[len(resp.Output)-1] != '\n' {
			fmt.Println()
		}
		os.Exit(resp.ExitCode)
	case app.ActionCtrlC, app.ActionFollow:
		os.Exit(resp.ExitCode)
	case app.ActionList:
		printList(resp.Snapshot)
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
