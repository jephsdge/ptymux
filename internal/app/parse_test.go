package app

import "testing"

func TestParseRunCommand(t *testing.T) {
	cfg, err := Parse([]string{"-s", "session1", "-p", "pane1", "-t", "tab1", "pwd"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionRun {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionRun)
	}
	if cfg.Session != "session1" {
		t.Fatalf("Session = %q, want session1", cfg.Session)
	}
	if cfg.Pane != "pane1" {
		t.Fatalf("Pane = %q, want pane1", cfg.Pane)
	}
	if cfg.Tab != "tab1" {
		t.Fatalf("Tab = %q, want tab1", cfg.Tab)
	}
	if cfg.Command != "pwd" {
		t.Fatalf("Command = %q, want pwd", cfg.Command)
	}
}

func TestParseDefaults(t *testing.T) {
	cfg, err := Parse([]string{"pwd"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Session != "default" || cfg.Pane != "default" || cfg.Tab != "default" {
		t.Fatalf("defaults = %q/%q/%q, want default/default/default", cfg.Session, cfg.Pane, cfg.Tab)
	}
}

func TestParseTargetPathSession(t *testing.T) {
	cfg, err := Parse([]string{"work", "pwd"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Session != "work" || cfg.Pane != "default" || cfg.Tab != "default" {
		t.Fatalf("target = %q/%q/%q, want work/default/default", cfg.Session, cfg.Pane, cfg.Tab)
	}
	if cfg.Command != "pwd" {
		t.Fatalf("Command = %q, want pwd", cfg.Command)
	}
}

func TestParseTargetPathFull(t *testing.T) {
	cfg, err := Parse([]string{"work/main/build", "go test ./..."})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Session != "work" || cfg.Pane != "main" || cfg.Tab != "build" {
		t.Fatalf("target = %q/%q/%q, want work/main/build", cfg.Session, cfg.Pane, cfg.Tab)
	}
	if cfg.Command != "go test ./..." {
		t.Fatalf("Command = %q, want go test ./...", cfg.Command)
	}
}

func TestParseDaemonAction(t *testing.T) {
	cfg, err := Parse([]string{"daemon"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionDaemon {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionDaemon)
	}
}

func TestParseStopAction(t *testing.T) {
	cfg, err := Parse([]string{"stop"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionStop {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionStop)
	}
}

func TestParseIdleTargetPath(t *testing.T) {
	cfg, err := Parse([]string{"idle", "work/main", "printf hi"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionIdle {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionIdle)
	}
	if cfg.Session != "work" || cfg.Pane != "main" || cfg.Tab != "default" {
		t.Fatalf("target = %q/%q/%q, want work/main/default", cfg.Session, cfg.Pane, cfg.Tab)
	}
	if cfg.Command != "printf hi" {
		t.Fatalf("Command = %q, want printf hi", cfg.Command)
	}
}

func TestParseSendTargetPath(t *testing.T) {
	cfg, err := Parse([]string{"send", "work", "exit"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionSend {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionSend)
	}
	if cfg.Session != "work" || cfg.Pane != "default" || cfg.Tab != "default" {
		t.Fatalf("target = %q/%q/%q, want work/default/default", cfg.Session, cfg.Pane, cfg.Tab)
	}
	if cfg.Command != "exit" {
		t.Fatalf("Command = %q, want exit", cfg.Command)
	}
}

func TestParseSendRequiresTargetAndInput(t *testing.T) {
	if _, err := Parse([]string{"send", "work"}); err == nil {
		t.Fatal("Parse returned nil error, want error")
	}
}

func TestParseCtrlCTargetPath(t *testing.T) {
	cfg, err := Parse([]string{"ctrl-c", "work/main"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionCtrlC {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionCtrlC)
	}
	if cfg.Session != "work" || cfg.Pane != "main" || cfg.Tab != "default" {
		t.Fatalf("target = %q/%q/%q, want work/main/default", cfg.Session, cfg.Pane, cfg.Tab)
	}
}

func TestParseListActionAfterFlags(t *testing.T) {
	cfg, err := Parse([]string{"--socket", "/tmp/ptymux.sock", "list"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionList {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionList)
	}
	if cfg.Socket != "/tmp/ptymux.sock" {
		t.Fatalf("Socket = %q, want /tmp/ptymux.sock", cfg.Socket)
	}
}

func TestParseListTargetPath(t *testing.T) {
	cfg, err := Parse([]string{"list", "work/main"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionList {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionList)
	}
	if cfg.Session != "work" || cfg.Pane != "main" || cfg.Tab != "" {
		t.Fatalf("target = %q/%q/%q, want work/main/empty", cfg.Session, cfg.Pane, cfg.Tab)
	}
}

func TestParseListTargetPathAfterFlags(t *testing.T) {
	cfg, err := Parse([]string{"--socket", "/tmp/ptymux.sock", "list", "work/main"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionList {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionList)
	}
	if cfg.Session != "work" || cfg.Pane != "main" || cfg.Tab != "" {
		t.Fatalf("target = %q/%q/%q, want work/main/empty", cfg.Session, cfg.Pane, cfg.Tab)
	}
}
