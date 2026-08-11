package main

import (
	"fmt"
	"os"

	"ptymux/internal/app"
)

func main() {
	cfg, err := app.ParseServer(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if cfg.Action == app.ActionHelp {
		fmt.Print(app.ServerHelpText())
		return
	}
	if _, err := app.RunServer(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
