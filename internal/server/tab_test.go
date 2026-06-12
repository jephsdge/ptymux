package server

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hinshun/vt10x"
)

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func waitForOutput(t *testing.T, out *safeBuffer, want string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("output = %q, want it to contain %q", out.String(), want)
}

func waitForSubscriberCount(t *testing.T, runner *PTYRunner, want int) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runner.subscriberCount() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("subscriber count = %d, want %d", runner.subscriberCount(), want)
}

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

func TestPTYRunnerRunDoesNotDropMarkerAfterHighOutput(t *testing.T) {
	runner, err := NewPTYRunner("/bin/sh")
	if err != nil {
		t.Fatalf("NewPTYRunner returned error: %v", err)
	}
	defer runner.Close()

	result, err := runner.Run("i=0; while [ $i -lt 300 ]; do printf 'line-%03d\\n' $i; i=$((i+1)); done")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0; output=%q", result.ExitCode, result.Output)
	}
	if !strings.Contains(result.Output, "line-000") || !strings.Contains(result.Output, "line-299") {
		t.Fatalf("Output = %q, want high output range", result.Output)
	}
	if strings.Contains(result.Output, "__PTYMUX_DONE_") {
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

func TestPTYRunnerSendWaitReturnsAfterQuiet(t *testing.T) {
	runner, err := NewPTYRunner("/bin/sh")
	if err != nil {
		t.Fatalf("NewPTYRunner returned error: %v", err)
	}
	defer runner.Close()

	result, err := runner.SendWait("printf wait-output", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("SendWait returned error: %v", err)
	}

	if !strings.Contains(result.Output, "wait-output") {
		t.Fatalf("Output = %q, want wait-output", result.Output)
	}
}

func TestPTYRunnerReadRecentEntriesFromTerminalScreenWindow(t *testing.T) {
	runner := &PTYRunner{term: vt10x.New(vt10x.WithSize(80, 10))}
	runner.observe([]byte("sh-5.3$ one\r\n1\r\nsh-5.3$ two\r\n2\r\nsh-5.3$ three\r\n3\r\nsh-5.3$ "))

	result, err := runner.Read(2)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}

	want := "sh-5.3$ two\n2\nsh-5.3$ three\n3\nsh-5.3$"
	if result.Output != want {
		t.Fatalf("Output = %q, want %q", result.Output, want)
	}
}

func TestPTYRunnerReadRecentEntriesFromTerminalScreen(t *testing.T) {
	runner := &PTYRunner{
		term:    vt10x.New(vt10x.WithSize(80, 10)),
		history: []string{"history-only"},
	}
	runner.observe([]byte("sh-5.3$ echo old\r\nold\r\nsh-5.3$ pwd\r\n/home/work\r\nsh-5.3$ "))

	result, err := runner.Read(1)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}

	want := "sh-5.3$ pwd\n/home/work\nsh-5.3$"
	if result.Output != want {
		t.Fatalf("Output = %q, want %q", result.Output, want)
	}
}

func TestPTYRunnerReadRecentEntriesHidesInternalMarkerLines(t *testing.T) {
	runner := &PTYRunner{term: vt10x.New(vt10x.WithSize(120, 10))}
	runner.observe([]byte("sh-5.3$ pwd\r\n/home/work\r\nsh-5.3$ __ptymux_status=$?; __ptymux_token_a=\"__PTYMUX_DON\"; __ptymux_token_b=\"E_TEST__\"; printf '\\n%s%s:%s\\n' \"$__ptymux_token_a\" \"$__ptymux_token_b\" \"$__ptymux_status\"\r\n\r\n__PTYMUX_DONE_TEST__:0\r\nsh-5.3$ "))

	result, err := runner.Read(5)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}

	want := "sh-5.3$ pwd\n/home/work\nsh-5.3$"
	if result.Output != want {
		t.Fatalf("Output = %q, want %q", result.Output, want)
	}
}

func TestPTYRunnerReadScreenHidesInternalMarkerLines(t *testing.T) {
	runner := &PTYRunner{term: vt10x.New(vt10x.WithSize(120, 10))}
	runner.observe([]byte("sh-5.3$ pwd\r\n/home/work\r\nsh-5.3$ __ptymux_status=$?; __ptymux_token_a=\"__PTYMUX_DON\"; __ptymux_token_b=\"E_TEST__\"; printf '\\n%s%s:%s\\n' \"$__ptymux_token_a\" \"$__ptymux_token_b\" \"$__ptymux_status\"\r\n\r\n__PTYMUX_DONE_TEST__:0\r\nsh-5.3$ "))

	result, err := runner.Read(0)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}

	if strings.Contains(result.Output, "__ptymux_") || strings.Contains(result.Output, "__PTYMUX_DONE_") {
		t.Fatalf("Output leaked marker internals: %q", result.Output)
	}
	if !strings.Contains(result.Output, "sh-5.3$ pwd") || !strings.Contains(result.Output, "/home/work") {
		t.Fatalf("Output = %q, want visible command output", result.Output)
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

func TestPTYRunnerFollowDoesNotBlockRun(t *testing.T) {
	runner, err := NewPTYRunner("/bin/sh")
	if err != nil {
		t.Fatalf("NewPTYRunner returned error: %v", err)
	}
	defer runner.Close()

	done := make(chan struct{})

	var followed safeBuffer
	followErr := make(chan error, 1)
	go func() {
		followErr <- runner.Follow(&followed, done)
	}()
	waitForSubscriberCount(t, runner, 1)

	if _, err := runner.file.Write([]byte("printf follow-ready\n")); err != nil {
		t.Fatalf("write follow-ready returned error: %v", err)
	}
	waitForOutput(t, &followed, "follow-ready")

	resultCh := make(chan RunResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := runner.Run("printf run-output")
		resultCh <- result
		errCh <- err
	}()

	select {
	case result := <-resultCh:
		if err := <-errCh; err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
		if !strings.Contains(result.Output, "run-output") {
			t.Fatalf("Output = %q, want it to contain run-output", result.Output)
		}
	case <-time.After(2 * time.Second):
		close(done)
		t.Fatal("Run blocked while Follow was active")
	}

	close(done)
	if err := <-followErr; err != nil {
		t.Fatalf("Follow returned error: %v", err)
	}
}

func TestPTYRunnerMultipleFollowersReceiveOutput(t *testing.T) {
	runner, err := NewPTYRunner("/bin/sh")
	if err != nil {
		t.Fatalf("NewPTYRunner returned error: %v", err)
	}
	defer runner.Close()

	done := make(chan struct{})

	var first safeBuffer
	var second safeBuffer
	firstErr := make(chan error, 1)
	secondErr := make(chan error, 1)
	go func() {
		firstErr <- runner.Follow(&first, done)
	}()
	go func() {
		secondErr <- runner.Follow(&second, done)
	}()
	waitForSubscriberCount(t, runner, 2)

	if _, err := runner.file.Write([]byte("printf followers-ready\n")); err != nil {
		t.Fatalf("write followers-ready returned error: %v", err)
	}
	waitForOutput(t, &first, "followers-ready")
	waitForOutput(t, &second, "followers-ready")

	if _, err := runner.Run("printf shared-output"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	waitForOutput(t, &first, "shared-output")
	waitForOutput(t, &second, "shared-output")

	close(done)
	if err := <-firstErr; err != nil {
		t.Fatalf("first Follow returned error: %v", err)
	}
	if err := <-secondErr; err != nil {
		t.Fatalf("second Follow returned error: %v", err)
	}
}
