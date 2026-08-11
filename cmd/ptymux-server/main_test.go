package main

import (
	"strings"
	"testing"

	"ptymux/internal/app"
)

func TestServerHelpUsesDirectExecutable(t *testing.T) {
	help := app.ServerHelpText()
	if !strings.Contains(help, "ptymux-server [--listen ADDRESS]") || strings.Contains(help, "ptymux server") {
		t.Fatalf("unexpected server help:\n%s", help)
	}
}
