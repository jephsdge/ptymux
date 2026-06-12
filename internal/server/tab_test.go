package server

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/hinshun/vt10x"
)

func TestWrapCommandUsesSingleMarkerLine(t *testing.T) {
	wrapped := wrapCommand("pwd", "__PTYMUX_DONE_TEST__")
	lines := strings.Split(strings.TrimRight(wrapped, "\n"), "\n")

	if len(lines) != 2 {
		t.Fatalf("wrapped command has %d lines, want 2:\n%s", len(lines), wrapped)
	}
	if lines[0] != "pwd" {
		t.Fatalf("first line = %q, want pwd", lines[0])
	}
	if !strings.Contains(lines[1], "__ptymux_status=$?;") {
		t.Fatalf("marker line does not capture status in one line: %q", lines[1])
	}
	if !strings.Contains(lines[1], "printf") {
		t.Fatalf("marker line does not print marker: %q", lines[1])
	}
}

func TestFormatCommandTranscriptPrefixesEchoWithTrailingPrompt(t *testing.T) {
	output := "pwd\n/home/work\nwork@DESKTOP-4IX8CCY:~$"

	got := formatCommandTranscript(output, "pwd", "")
	want := "work@DESKTOP-4IX8CCY:~$pwd\n/home/work\nwork@DESKTOP-4IX8CCY:~$"
	if got != want {
		t.Fatalf("formatted output = %q, want %q", got, want)
	}
}

func TestFormatCommandTranscriptUsesSnapshotPrompt(t *testing.T) {
	output := "pwd\n/home/work\nsh-5.3$"

	got := formatCommandTranscript(output, "pwd", "snapshot$ ")
	want := "snapshot$ pwd\n/home/work\nsh-5.3$"
	if got != want {
		t.Fatalf("formatted output = %q, want %q", got, want)
	}
}

func TestFormatCommandTranscriptLeavesNonPromptOutputAlone(t *testing.T) {
	output := "printf idle-ok\nidle-ok"

	got := formatCommandTranscript(output, "printf idle-ok", "")
	if got != output {
		t.Fatalf("formatted output = %q, want %q", got, output)
	}
}

func TestParseMarkedOutputKeepsPromptAfterMarker(t *testing.T) {
	raw := []byte("pwd\n/home/work\n__PTYMUX_DONE_TEST__:0\nsh-5.3$ ")
	marker := []byte("__PTYMUX_DONE_TEST__:")
	idx := bytes.Index(raw, marker)
	if idx < 0 {
		t.Fatal("marker not found in test input")
	}

	result := parseMarkedOutput(raw, idx, len(marker), "pwd", "sh-5.3$ ")

	want := "sh-5.3$ pwd\n/home/work\nsh-5.3$ "
	if result.Output != want {
		t.Fatalf("Output = %q, want %q", result.Output, want)
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", result.ExitCode)
	}
}

func TestCurrentLineReadsTerminalScreenState(t *testing.T) {
	runner := &PTYRunner{term: vt10x.New(vt10x.WithSize(40, 10))}
	runner.observe([]byte("sh-5.3$ "))

	if got := runner.currentLine(); got != "sh-5.3$ " {
		t.Fatalf("currentLine = %q, want sh-5.3$ ", got)
	}
}

func TestPTYRunnerPreservesShellState(t *testing.T) {
	runner, err := NewPTYRunner("/bin/sh")
	if err != nil {
		t.Fatalf("NewPTYRunner returned error: %v", err)
	}
	defer runner.Close()

	if _, err := runner.Run("cd /tmp"); err != nil {
		t.Fatalf("cd command returned error: %v", err)
	}

	result, err := runner.Run("pwd")
	if err != nil {
		t.Fatalf("pwd command returned error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0; output=%q", result.ExitCode, result.Output)
	}
	if !strings.Contains(result.Output, "/tmp") {
		t.Fatalf("Output = %q, want it to contain /tmp", result.Output)
	}
	if !strings.Contains(result.Output, "pwd") {
		t.Fatalf("Output = %q, want it to contain command echo", result.Output)
	}
	if strings.Contains(result.Output, "__ptymux_") || strings.Contains(result.Output, "__PTYMUX_DONE_") {
		t.Fatalf("Output leaked marker internals: %q", result.Output)
	}
}

func TestPTYRunnerIdleReturnsAfterOutputQuiets(t *testing.T) {
	runner, err := NewPTYRunner("/bin/sh")
	if err != nil {
		t.Fatalf("NewPTYRunner returned error: %v", err)
	}
	defer runner.Close()

	result, err := runner.runIdle("printf idle-output", 50*time.Millisecond, time.Second)
	if err != nil {
		t.Fatalf("runIdle returned error: %v", err)
	}
	if !strings.Contains(result.Output, "idle-output") {
		t.Fatalf("Output = %q, want it to contain idle-output", result.Output)
	}
	if !strings.Contains(result.Output, "printf idle-output") {
		t.Fatalf("Output = %q, want it to contain command echo", result.Output)
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", result.ExitCode)
	}
}

func TestPTYRunnerSendDoesNotWaitForMarker(t *testing.T) {
	runner, err := NewPTYRunner("/bin/sh")
	if err != nil {
		t.Fatalf("NewPTYRunner returned error: %v", err)
	}
	defer runner.Close()

	if err := runner.Send("cd /tmp"); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	result, err := runner.Run("pwd")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.Contains(result.Output, "/tmp") {
		t.Fatalf("Output = %q, want it to contain /tmp", result.Output)
	}
}

func TestPTYRunnerSendFollowStreamsOutputUntilQuietForTest(t *testing.T) {
	runner, err := NewPTYRunner("/bin/sh")
	if err != nil {
		t.Fatalf("NewPTYRunner returned error: %v", err)
	}
	defer runner.Close()

	var out bytes.Buffer
	if err := runner.sendFollow("printf follow-output", &out, 50*time.Millisecond, nil); err != nil {
		t.Fatalf("sendFollow returned error: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "follow-output") {
		t.Fatalf("streamed output = %q, want it to contain follow-output", got)
	}
}

func TestPTYRunnerCtrlCFollowsOutput(t *testing.T) {
	runner, err := NewPTYRunner("/bin/sh")
	if err != nil {
		t.Fatalf("NewPTYRunner returned error: %v", err)
	}
	defer runner.Close()

	var out bytes.Buffer
	if err := runner.ctrlCFollow(&out, 50*time.Millisecond, nil); err != nil {
		t.Fatalf("ctrlCFollow returned error: %v", err)
	}

	if !strings.Contains(out.String(), "^C") {
		t.Fatalf("streamed output = %q, want Ctrl+C echo", out.String())
	}
}
