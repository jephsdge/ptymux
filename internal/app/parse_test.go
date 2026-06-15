package app

import (
	"strings"
	"testing"
	"time"
)

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

func TestParseHelpFlags(t *testing.T) {
	tests := [][]string{
		{"-h"},
		{"--help"},
		{"help"},
		{"send", "-h"},
		{"read", "--help"},
	}

	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cfg, err := Parse(args)
			if err != nil {
				t.Fatalf("Parse returned error: %v", err)
			}
			if cfg.Action != ActionHelp {
				t.Fatalf("Action = %q, want %q", cfg.Action, ActionHelp)
			}
		})
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

func TestParseKillAll(t *testing.T) {
	cfg, err := Parse([]string{"kill"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionKill {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionKill)
	}
	if cfg.Session != "" || cfg.Pane != "" || cfg.Tab != "" {
		t.Fatalf("target = %q/%q/%q, want empty target for kill all", cfg.Session, cfg.Pane, cfg.Tab)
	}
}

func TestParseKillTargetPath(t *testing.T) {
	cfg, err := Parse([]string{"kill", "work/main/build"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionKill {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionKill)
	}
	if cfg.Session != "work" || cfg.Pane != "main" || cfg.Tab != "build" {
		t.Fatalf("target = %q/%q/%q, want work/main/build", cfg.Session, cfg.Pane, cfg.Tab)
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

func TestParseSendDirectSocketFlag(t *testing.T) {
	cfg, err := Parse([]string{"send", "--socket", "/tmp/ptymux.sock", "work", "ls"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionSend {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionSend)
	}
	if cfg.Socket != "/tmp/ptymux.sock" {
		t.Fatalf("Socket = %q, want /tmp/ptymux.sock", cfg.Socket)
	}
	if cfg.Session != "work" || cfg.Command != "ls" {
		t.Fatalf("target/command = %q/%q, want work/ls", cfg.Session, cfg.Command)
	}
}

func TestParseSendGlobalSocketFlag(t *testing.T) {
	cfg, err := Parse([]string{"--socket", "/tmp/ptymux.sock", "send", "work", "ls"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionSend {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionSend)
	}
	if cfg.Socket != "/tmp/ptymux.sock" {
		t.Fatalf("Socket = %q, want /tmp/ptymux.sock", cfg.Socket)
	}
	if cfg.Session != "work" || cfg.Command != "ls" {
		t.Fatalf("target/command = %q/%q, want work/ls", cfg.Session, cfg.Command)
	}
}

func TestParseSendFollowFlag(t *testing.T) {
	cfg, err := Parse([]string{"send", "-f", "work", "ls"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionSend || !cfg.Follow {
		t.Fatalf("action/follow = %q/%v, want send/true", cfg.Action, cfg.Follow)
	}
	if cfg.Wait != 0 {
		t.Fatalf("Wait = %s, want 0", cfg.Wait)
	}
}

func TestParseSendTimeoutFlagDefaultsToMilliseconds(t *testing.T) {
	cfg, err := Parse([]string{"send", "-t", "100", "work", "ls"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionSend {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionSend)
	}
	if cfg.Follow {
		t.Fatal("Follow = true, want false")
	}
	if cfg.Wait != 100*time.Millisecond {
		t.Fatalf("Wait = %s, want 100ms", cfg.Wait)
	}
}

func TestParseSendTimeoutFlagSupportsUnits(t *testing.T) {
	cfg, err := Parse([]string{"send", "-t", "1s", "work", "ls"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Wait != time.Second {
		t.Fatalf("Wait = %s, want 1s", cfg.Wait)
	}
}

func TestParseSendFollowAndTimeoutConflict(t *testing.T) {
	if _, err := Parse([]string{"send", "-f", "-t", "1s", "work", "ls"}); err == nil {
		t.Fatal("Parse returned nil error, want conflict error")
	}
}

func TestParseSendRequiresTargetAndInput(t *testing.T) {
	if _, err := Parse([]string{"send", "work"}); err == nil {
		t.Fatal("Parse returned nil error, want error")
	}
}

func TestParseTextTargetPath(t *testing.T) {
	cfg, err := Parse([]string{"text", "work/main", "hello world"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionText {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionText)
	}
	if cfg.Session != "work" || cfg.Pane != "main" || cfg.Tab != "default" {
		t.Fatalf("target = %q/%q/%q, want work/main/default", cfg.Session, cfg.Pane, cfg.Tab)
	}
	if cfg.Command != "hello world" {
		t.Fatalf("Command = %q, want hello world", cfg.Command)
	}
}

func TestParseTextDirectSocketFlag(t *testing.T) {
	cfg, err := Parse([]string{"text", "--socket", "/tmp/ptymux.sock", "work", "hello"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionText {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionText)
	}
	if cfg.Socket != "/tmp/ptymux.sock" {
		t.Fatalf("Socket = %q, want /tmp/ptymux.sock", cfg.Socket)
	}
	if cfg.Session != "work" || cfg.Command != "hello" {
		t.Fatalf("target/command = %q/%q, want work/hello", cfg.Session, cfg.Command)
	}
}

func TestParseTextGlobalSocketFlag(t *testing.T) {
	cfg, err := Parse([]string{"--socket", "/tmp/ptymux.sock", "text", "work", "hello"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionText {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionText)
	}
	if cfg.Socket != "/tmp/ptymux.sock" {
		t.Fatalf("Socket = %q, want /tmp/ptymux.sock", cfg.Socket)
	}
	if cfg.Session != "work" || cfg.Command != "hello" {
		t.Fatalf("target/command = %q/%q, want work/hello", cfg.Session, cfg.Command)
	}
}

func TestParseTextRequiresTargetAndText(t *testing.T) {
	if _, err := Parse([]string{"text", "work"}); err == nil {
		t.Fatal("Parse returned nil error, want error")
	}
}

func TestParseKeysTargetPath(t *testing.T) {
	cfg, err := Parse([]string{"keys", "work/main", "up enter"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionKeys {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionKeys)
	}
	if cfg.Session != "work" || cfg.Pane != "main" || cfg.Tab != "default" {
		t.Fatalf("target = %q/%q/%q, want work/main/default", cfg.Session, cfg.Pane, cfg.Tab)
	}
	if cfg.Command != "up enter" {
		t.Fatalf("Command = %q, want up enter", cfg.Command)
	}
}

func TestParseKeysDirectSocketFlag(t *testing.T) {
	cfg, err := Parse([]string{"keys", "--socket", "/tmp/ptymux.sock", "work", "ctrl-c"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionKeys {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionKeys)
	}
	if cfg.Socket != "/tmp/ptymux.sock" {
		t.Fatalf("Socket = %q, want /tmp/ptymux.sock", cfg.Socket)
	}
	if cfg.Session != "work" || cfg.Command != "ctrl-c" {
		t.Fatalf("target/command = %q/%q, want work/ctrl-c", cfg.Session, cfg.Command)
	}
}

func TestParseKeysGlobalSocketFlag(t *testing.T) {
	cfg, err := Parse([]string{"--socket", "/tmp/ptymux.sock", "keys", "work", "ctrl-c"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionKeys {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionKeys)
	}
	if cfg.Socket != "/tmp/ptymux.sock" {
		t.Fatalf("Socket = %q, want /tmp/ptymux.sock", cfg.Socket)
	}
	if cfg.Session != "work" || cfg.Command != "ctrl-c" {
		t.Fatalf("target/command = %q/%q, want work/ctrl-c", cfg.Session, cfg.Command)
	}
}

func TestParseKeysWaitAndFollow(t *testing.T) {
	waitCfg, err := Parse([]string{"keys", "-t", "500ms", "work", "ctrl-d"})
	if err != nil {
		t.Fatalf("Parse wait returned error: %v", err)
	}
	if waitCfg.Action != ActionKeys {
		t.Fatalf("wait Action = %q, want %q", waitCfg.Action, ActionKeys)
	}
	if waitCfg.Follow {
		t.Fatal("wait Follow = true, want false")
	}
	if waitCfg.Wait != 500*time.Millisecond {
		t.Fatalf("wait Wait = %s, want 500ms", waitCfg.Wait)
	}
	if waitCfg.Session != "work" || waitCfg.Command != "ctrl-d" {
		t.Fatalf("wait target/command = %q/%q, want work/ctrl-d", waitCfg.Session, waitCfg.Command)
	}

	followCfg, err := Parse([]string{"keys", "-f", "work", "ctrl-c"})
	if err != nil {
		t.Fatalf("Parse follow returned error: %v", err)
	}
	if followCfg.Action != ActionKeys {
		t.Fatalf("follow Action = %q, want %q", followCfg.Action, ActionKeys)
	}
	if !followCfg.Follow {
		t.Fatal("follow Follow = false, want true")
	}
	if followCfg.Wait != 0 {
		t.Fatalf("follow Wait = %s, want 0", followCfg.Wait)
	}
	if followCfg.Session != "work" || followCfg.Command != "ctrl-c" {
		t.Fatalf("follow target/command = %q/%q, want work/ctrl-c", followCfg.Session, followCfg.Command)
	}
}

func TestParseKeysRejectsFollowAndWait(t *testing.T) {
	if _, err := Parse([]string{"keys", "-f", "-t", "1s", "work", "ctrl-c"}); err == nil {
		t.Fatal("Parse returned nil error, want conflict error")
	}
}

func TestParseCommand(t *testing.T) {
	cfg, err := Parse([]string{"command", "work/main", "ctrl-o d"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionCommand {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionCommand)
	}
	if cfg.Session != "work" || cfg.Pane != "main" || cfg.Tab != "default" {
		t.Fatalf("target = %q/%q/%q, want work/main/default", cfg.Session, cfg.Pane, cfg.Tab)
	}
	if cfg.Command != "ctrl-o d" {
		t.Fatalf("Command = %q, want ctrl-o d", cfg.Command)
	}
}

func TestParseCommandDirectSocketFlag(t *testing.T) {
	cfg, err := Parse([]string{"command", "--socket", "/tmp/ptymux.sock", "work", "ctrl-c"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionCommand {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionCommand)
	}
	if cfg.Socket != "/tmp/ptymux.sock" {
		t.Fatalf("Socket = %q, want /tmp/ptymux.sock", cfg.Socket)
	}
	if cfg.Session != "work" || cfg.Command != "ctrl-c" {
		t.Fatalf("target/command = %q/%q, want work/ctrl-c", cfg.Session, cfg.Command)
	}
}

func TestParseCommandGlobalSocketFlag(t *testing.T) {
	cfg, err := Parse([]string{"--socket", "/tmp/ptymux.sock", "command", "work", "ctrl-c"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionCommand {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionCommand)
	}
	if cfg.Socket != "/tmp/ptymux.sock" {
		t.Fatalf("Socket = %q, want /tmp/ptymux.sock", cfg.Socket)
	}
	if cfg.Session != "work" || cfg.Command != "ctrl-c" {
		t.Fatalf("target/command = %q/%q, want work/ctrl-c", cfg.Session, cfg.Command)
	}
}

func TestParseCommandFollow(t *testing.T) {
	cfg, err := Parse([]string{"command", "-f", "work", "ctrl-c"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionCommand {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionCommand)
	}
	if !cfg.Follow {
		t.Fatal("Follow = false, want true")
	}
}

func TestParseCommandWait(t *testing.T) {
	cfg, err := Parse([]string{"command", "-t", "1s", "work", "ctrl-o d"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionCommand {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionCommand)
	}
	if cfg.Wait != time.Second {
		t.Fatalf("Wait = %s, want 1s", cfg.Wait)
	}
}

func TestParseCommandRejectsFollowAndWait(t *testing.T) {
	if _, err := Parse([]string{"command", "-f", "-t", "1s", "work", "ctrl-c"}); err == nil {
		t.Fatal("Parse returned nil error, want conflict error")
	}
}

func TestParseIdleDefaultsToSendTimeout500ms(t *testing.T) {
	cfg, err := Parse([]string{"idle", "work", "ssh host"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionIdle {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionIdle)
	}
	if cfg.Wait != 500*time.Millisecond {
		t.Fatalf("Wait = %s, want 500ms", cfg.Wait)
	}
}

func TestParseIdleDirectSocketFlag(t *testing.T) {
	cfg, err := Parse([]string{"idle", "--socket", "/tmp/ptymux.sock", "work", "cmd"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionIdle {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionIdle)
	}
	if cfg.Socket != "/tmp/ptymux.sock" {
		t.Fatalf("Socket = %q, want /tmp/ptymux.sock", cfg.Socket)
	}
	if cfg.Session != "work" || cfg.Command != "cmd" {
		t.Fatalf("target/command = %q/%q, want work/cmd", cfg.Session, cfg.Command)
	}
}

func TestParseIdleGlobalSocketFlag(t *testing.T) {
	cfg, err := Parse([]string{"--socket", "/tmp/ptymux.sock", "idle", "work", "cmd"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionIdle {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionIdle)
	}
	if cfg.Socket != "/tmp/ptymux.sock" {
		t.Fatalf("Socket = %q, want /tmp/ptymux.sock", cfg.Socket)
	}
	if cfg.Session != "work" || cfg.Command != "cmd" {
		t.Fatalf("target/command = %q/%q, want work/cmd", cfg.Session, cfg.Command)
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

func TestParseCtrlCDirectSocketFlag(t *testing.T) {
	cfg, err := Parse([]string{"ctrl-c", "--socket", "/tmp/ptymux.sock", "work"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionCtrlC {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionCtrlC)
	}
	if cfg.Socket != "/tmp/ptymux.sock" {
		t.Fatalf("Socket = %q, want /tmp/ptymux.sock", cfg.Socket)
	}
	if cfg.Session != "work" {
		t.Fatalf("Session = %q, want work", cfg.Session)
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

func TestParseReadTargetPath(t *testing.T) {
	cfg, err := Parse([]string{"read", "-n", "2", "work/main"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionRead {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionRead)
	}
	if cfg.ReadCount != 2 {
		t.Fatalf("ReadCount = %d, want 2", cfg.ReadCount)
	}
	if cfg.Session != "work" || cfg.Pane != "main" || cfg.Tab != "default" {
		t.Fatalf("target = %q/%q/%q, want work/main/default", cfg.Session, cfg.Pane, cfg.Tab)
	}
}

func TestParseFollowTargetPath(t *testing.T) {
	cfg, err := Parse([]string{"follow", "work/main"})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if cfg.Action != ActionFollow {
		t.Fatalf("Action = %q, want %q", cfg.Action, ActionFollow)
	}
	if !cfg.Follow {
		t.Fatal("Follow = false, want true")
	}
	if cfg.Session != "work" || cfg.Pane != "main" || cfg.Tab != "default" {
		t.Fatalf("target = %q/%q/%q, want work/main/default", cfg.Session, cfg.Pane, cfg.Tab)
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
