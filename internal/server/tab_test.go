package server

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hinshun/vt10x"
)

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

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

func TestParseMarkedOutputCleansTerminalControls(t *testing.T) {
	raw := []byte("pwd\r\n\x1b]0;user@host:/path\x07\x1b[01;32mhost app $\x1b[00m\x1b[Kpwd\r\n/home/work\r\n__PTYMUX_DONE_TEST__:0\r\n\x1b[01;32mhost app $\x1b[00m\x1b[K")
	marker := []byte("__PTYMUX_DONE_TEST__:")
	idx := bytes.Index(raw, marker)
	if idx < 0 {
		t.Fatal("marker not found in test input")
	}

	result := parseMarkedOutput(raw, idx, len(marker), "pwd", "host app $")

	if strings.ContainsAny(result.Output, "\x1b\a") ||
		strings.Contains(result.Output, "]0;") ||
		strings.Contains(result.Output, "[01;32m") ||
		strings.Contains(result.Output, "[K") {
		t.Fatalf("Output leaked terminal controls: %q", result.Output)
	}
	if !strings.Contains(result.Output, "host app $pwd") ||
		!strings.Contains(result.Output, "/home/work") {
		t.Fatalf("Output = %q, want clean prompt and command output", result.Output)
	}
}

func TestRunCollectorDetectsSplitMarkerAfterOutputLimit(t *testing.T) {
	marker := []byte("__PTYMUX_DONE_RANDOM__:")
	collector := newRunCollector(marker, 4)
	for _, chunk := range [][]byte{
		[]byte("abcdef__PTYMUX_DONE_"),
		[]byte("RANDOM__:7\r"),
		[]byte("\nprompt$ "),
	} {
		if err := collector.consume(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if !collector.complete {
		t.Fatal("collector did not find completion marker")
	}
	result := collector.result("command", "")
	if result.ExitCode != 7 {
		t.Fatalf("ExitCode = %d, want 7", result.ExitCode)
	}
	if !result.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	if len(result.Output) > 4 {
		t.Fatalf("output length = %d, want at most 4", len(result.Output))
	}
}

func TestNewCommandTokenIsRandom(t *testing.T) {
	first, err := newCommandToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newCommandToken()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("generated duplicate command token %q", first)
	}
	if !strings.HasPrefix(first, "__PTYMUX_DONE_") || !strings.HasSuffix(first, "__") {
		t.Fatalf("token = %q, want ptymux marker form", first)
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

func TestPTYRunnerStartsShellInOwnProcessGroup(t *testing.T) {
	runner, err := NewPTYRunner("/bin/sh")
	if err != nil {
		t.Fatalf("NewPTYRunner returned error: %v", err)
	}
	defer runner.Close()

	pid := runner.command.Process.Pid
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatalf("Getpgid returned error: %v", err)
	}
	if pgid != pid {
		t.Fatalf("pgid = %d, want shell pid %d", pgid, pid)
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

func TestPTYRunnerRunCancellationInterruptsCommandAndPreservesShell(t *testing.T) {
	runner, err := NewPTYRunner("/bin/sh")
	if err != nil {
		t.Fatalf("NewPTYRunner returned error: %v", err)
	}
	defer runner.Close()

	clientDone := make(chan struct{})
	runDone := make(chan error, 1)
	go func() {
		_, err := runner.RunWithDone("sleep 10", clientDone)
		runDone <- err
	}()
	waitForSubscriberCount(t, runner, 1)
	time.Sleep(100 * time.Millisecond)
	close(clientDone)

	select {
	case err := <-runDone:
		if !errors.Is(err, ErrOperationCanceled) {
			t.Fatalf("RunWithDone error = %v, want %v", err, ErrOperationCanceled)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunWithDone did not return after cancellation")
	}

	result, err := runner.Run("printf recovered")
	if err != nil {
		t.Fatalf("Run after cancellation returned error: %v", err)
	}
	if !strings.Contains(result.Output, "recovered") {
		t.Fatalf("Output = %q, want recovered shell output", result.Output)
	}
}

func TestPTYRunnerRunOutputLimitPreservesExitStatusAndNextCommand(t *testing.T) {
	runner, err := NewPTYRunner("/bin/sh")
	if err != nil {
		t.Fatalf("NewPTYRunner returned error: %v", err)
	}
	defer runner.Close()

	result, err := runner.runWithLimit("sh -c 'printf abcdefghij; exit 7'", nil, 4)
	if err != nil {
		t.Fatalf("runWithLimit returned error: %v", err)
	}
	if result.ExitCode != 7 {
		t.Fatalf("ExitCode = %d, want 7", result.ExitCode)
	}
	if !result.Truncated {
		t.Fatal("Truncated = false, want true")
	}

	next, err := runner.Run("printf synchronized")
	if err != nil {
		t.Fatalf("Run after truncated command returned error: %v", err)
	}
	if !strings.Contains(next.Output, "synchronized") {
		t.Fatalf("Output = %q, want synchronized", next.Output)
	}
}

func TestCollectUntilQuietStartsTimerBeforeOutput(t *testing.T) {
	runner := &PTYRunner{}
	sub := subscription{
		ch:   make(chan string),
		done: make(chan struct{}),
		err:  make(chan error, 1),
	}
	started := time.Now()
	output, truncated, err := runner.collectUntilQuiet(sub, 40*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if output != "" || truncated {
		t.Fatalf("output = %q, truncated = %v; want empty, false", output, truncated)
	}
	if elapsed := time.Since(started); elapsed < 30*time.Millisecond || elapsed > time.Second {
		t.Fatalf("elapsed = %v, want quiet timer duration", elapsed)
	}
}

func TestCollectUntilQuietResetsTimerForEachChunk(t *testing.T) {
	runner := &PTYRunner{}
	chunks := make(chan string, 3)
	sub := subscription{
		ch:   chunks,
		done: make(chan struct{}),
		err:  make(chan error, 1),
	}
	type result struct {
		output    string
		truncated bool
		err       error
	}
	resultCh := make(chan result, 1)
	started := time.Now()
	go func() {
		output, truncated, err := runner.collectUntilQuiet(sub, 60*time.Millisecond)
		resultCh <- result{output: output, truncated: truncated, err: err}
	}()
	chunks <- "one"
	time.Sleep(40 * time.Millisecond)
	chunks <- "two"
	time.Sleep(40 * time.Millisecond)
	chunks <- "three"

	got := <-resultCh
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.output != "onetwothree" || got.truncated {
		t.Fatalf("output = %q, truncated = %v; want onetwothree, false", got.output, got.truncated)
	}
	if elapsed := time.Since(started); elapsed < 120*time.Millisecond {
		t.Fatalf("elapsed = %v, timer was not reset by output", elapsed)
	}
}

func TestCollectUntilQuietBoundsCapturedOutput(t *testing.T) {
	runner := &PTYRunner{}
	chunks := make(chan string, 1)
	chunks <- string(bytes.Repeat([]byte("x"), MaxRunOutputBytes+1))
	sub := subscription{
		ch:   chunks,
		done: make(chan struct{}),
		err:  make(chan error, 1),
	}

	output, truncated, err := runner.collectUntilQuiet(sub, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Fatal("truncated = false, want true")
	}
	if len(output) != MaxRunOutputBytes {
		t.Fatalf("output length = %d, want %d", len(output), MaxRunOutputBytes)
	}
}

func TestPTYRunnerIdleReturnsAfterOutputQuiets(t *testing.T) {
	runner, err := NewPTYRunner("/bin/sh")
	if err != nil {
		t.Fatalf("NewPTYRunner returned error: %v", err)
	}
	defer runner.Close()

	result, err := runner.runIdle("printf idle-output", 50*time.Millisecond)
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

func TestPTYRunnerReadRecentTerminalLines(t *testing.T) {
	runner := &PTYRunner{term: vt10x.New(vt10x.WithSize(80, 10))}
	runner.observe([]byte("one\r\narrow -> text\r\nthree"))

	result, err := runner.Read(2)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}

	want := "arrow -> text\nthree"
	if result.Output != want {
		t.Fatalf("Output = %q, want %q", result.Output, want)
	}
}

func TestPTYRunnerReadRecentLinesPreservesANSI(t *testing.T) {
	runner := &PTYRunner{term: vt10x.New(vt10x.WithSize(80, 10))}
	runner.observe([]byte("\x1b[31mred\x1b[0m\r\nplain"))

	result, err := runner.Read(2)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if !strings.Contains(result.Output, "\x1b[31mred\x1b[0m") || !strings.HasSuffix(result.Output, "plain") {
		t.Fatalf("Output did not preserve ANSI history: %q", result.Output)
	}
}

func TestPTYRunnerReadCurrentScreenPreservesStyledSpaces(t *testing.T) {
	runner := &PTYRunner{term: vt10x.New(vt10x.WithSize(20, 4))}
	runner.observe([]byte("\x1b[41m  \x1b[0mQR"))

	result, err := runner.Read(0)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if !strings.Contains(result.Output, "\x1b[48;5;1m  \x1b[0mQR") {
		t.Fatalf("Output did not preserve background-colored spaces: %q", result.Output)
	}
}

func TestPTYRunnerReadHistorySurvivesScreenClear(t *testing.T) {
	runner := &PTYRunner{term: vt10x.New(vt10x.WithSize(40, 5))}
	runner.observe([]byte("old-one\r\nold-two\r\n\x1b[2J\x1b[Hcurrent"))

	current, err := runner.Read(0)
	if err != nil {
		t.Fatalf("Read current returned error: %v", err)
	}
	if strings.Contains(current.Output, "old-one") || current.Output != "current" {
		t.Fatalf("Current screen = %q, want current only", current.Output)
	}

	history, err := runner.Read(3)
	if err != nil {
		t.Fatalf("Read history returned error: %v", err)
	}
	if history.Output != "old-one\nold-two\ncurrent" {
		t.Fatalf("History = %q", history.Output)
	}
}

func TestPTYRunnerReadHidesInternalMarkerLines(t *testing.T) {
	runner := &PTYRunner{term: vt10x.New(vt10x.WithSize(120, 10))}
	runner.observe([]byte("visible\r\n\x1b[31m__PTYMUX_DONE_TEST__:0\x1b[0m\r\nsh-5.3$ "))

	for _, count := range []int{0, 5} {
		result, err := runner.Read(count)
		if err != nil {
			t.Fatalf("Read(%d) returned error: %v", count, err)
		}
		if strings.Contains(result.Output, "__PTYMUX_DONE_") {
			t.Fatalf("Read(%d) leaked marker internals: %q", count, result.Output)
		}
		if !strings.Contains(result.Output, "visible") || !strings.Contains(result.Output, "sh-5.3$") {
			t.Fatalf("Read(%d) = %q, want visible output", count, result.Output)
		}
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

func TestPTYRunnerSendFollowPropagatesWriterError(t *testing.T) {
	runner, err := NewPTYRunner("/bin/sh")
	if err != nil {
		t.Fatalf("NewPTYRunner returned error: %v", err)
	}
	defer runner.Close()

	writeErr := errors.New("output failed")
	if err := runner.sendFollow("printf follow-output", failingWriter{err: writeErr}, 100*time.Millisecond, nil); !errors.Is(err, writeErr) {
		t.Fatalf("sendFollow error = %v, want %v", err, writeErr)
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

func TestPTYRunnerDisconnectsSlowObserverWithoutBlockingBroadcast(t *testing.T) {
	runner := &PTYRunner{subscribers: make(map[uint64]*subscriber)}
	sub := runner.subscribeBestEffort()

	for i := 0; i < 129; i++ {
		runner.broadcast("output")
	}
	if got := runner.subscriberCount(); got != 0 {
		t.Fatalf("subscriber count = %d, want slow observer removed", got)
	}
	<-sub.done
	if err := runner.subscriptionResultErr(sub); !errors.Is(err, errSubscriberTooSlow) {
		t.Fatalf("subscription error = %v, want %v", err, errSubscriberTooSlow)
	}
}

func TestPTYRunnerBackpressuresReliableSubscriberWithoutDroppingOutput(t *testing.T) {
	runner := &PTYRunner{subscribers: make(map[uint64]*subscriber)}
	sub := runner.subscribeReliable()
	defer runner.unsubscribe(sub.id)

	for i := 0; i < 128; i++ {
		runner.broadcast("output")
	}
	broadcastDone := make(chan struct{})
	go func() {
		runner.broadcast("last")
		close(broadcastDone)
	}()
	select {
	case <-broadcastDone:
		t.Fatal("reliable broadcast completed before queue space was available")
	case <-time.After(20 * time.Millisecond):
	}
	if got := <-sub.ch; got != "output" {
		t.Fatalf("first chunk = %q, want output", got)
	}
	select {
	case <-broadcastDone:
	case <-time.After(time.Second):
		t.Fatal("reliable broadcast did not resume after queue space was available")
	}
	if got := runner.subscriberCount(); got != 1 {
		t.Fatalf("subscriber count = %d, want reliable subscriber retained", got)
	}
}

func TestWriteSubscriptionCleansSplitTerminalControls(t *testing.T) {
	runner := &PTYRunner{}
	ch := make(chan string, 3)
	ch <- "\x1b]0;user@"
	ch <- "host:/path\x07\x1b[01;32mhost $"
	ch <- "\x1b[00m\x1b[K\n"
	close(ch)

	var out bytes.Buffer
	if err := runner.writeSubscription(&out, testSubscription(ch), 0, nil); err != nil {
		t.Fatalf("writeSubscription returned error: %v", err)
	}

	got := out.String()
	want := "host $\n"
	if got != want {
		t.Fatalf("streamed output = %q, want %q", got, want)
	}
}

func TestWriteSubscriptionAppliesCarriageReturnAcrossChunks(t *testing.T) {
	runner := &PTYRunner{}
	ch := make(chan string, 2)
	ch <- "abc\r"
	ch <- "xyz"
	close(ch)

	var out bytes.Buffer
	if err := runner.writeSubscription(&out, testSubscription(ch), 0, nil); err != nil {
		t.Fatalf("writeSubscription returned error: %v", err)
	}

	got := out.String()
	want := "xyz"
	if got != want {
		t.Fatalf("streamed output = %q, want %q", got, want)
	}
}

func TestWriteSubscriptionAppliesBackspaceAcrossChunks(t *testing.T) {
	runner := &PTYRunner{}
	ch := make(chan string, 2)
	ch <- "abc\b"
	ch <- " \b"
	close(ch)

	var out bytes.Buffer
	if err := runner.writeSubscription(&out, testSubscription(ch), 0, nil); err != nil {
		t.Fatalf("writeSubscription returned error: %v", err)
	}

	got := out.String()
	want := "ab"
	if got != want {
		t.Fatalf("streamed output = %q, want %q", got, want)
	}
}

func TestWriteSubscriptionFlushesPromptWithoutNewline(t *testing.T) {
	runner := &PTYRunner{}
	ch := make(chan string, 1)
	done := make(chan struct{})

	var out safeBuffer
	errCh := make(chan error, 1)
	go func() {
		errCh <- runner.writeSubscription(&out, testSubscription(ch), 0, done)
	}()
	ch <- "Password: "
	waitForOutput(t, &out, "Password: ")
	close(done)

	if err := <-errCh; err != nil {
		t.Fatalf("writeSubscription returned error: %v", err)
	}
}

func TestWriteRawSubscriptionPreservesTerminalBytes(t *testing.T) {
	runner := &PTYRunner{}
	ch := make(chan string, 3)
	chunks := []string{"\x1b]0;user@", "host:/path\x07\x1b[41m  ", "\x1b[0mabc\rxy\b\n"}
	for _, chunk := range chunks {
		ch <- chunk
	}
	close(ch)

	var out bytes.Buffer
	if err := runner.writeRawSubscription(&out, testSubscription(ch), nil); err != nil {
		t.Fatalf("writeRawSubscription returned error: %v", err)
	}
	if got, want := out.String(), strings.Join(chunks, ""); got != want {
		t.Fatalf("streamed output = %q, want %q", got, want)
	}
}

func TestWriteRawSubscriptionReturnsWriterError(t *testing.T) {
	runner := &PTYRunner{}
	ch := make(chan string, 1)
	ch <- "output"
	close(ch)
	writeErr := errors.New("write failed")

	if err := runner.writeRawSubscription(failingWriter{err: writeErr}, testSubscription(ch), nil); !errors.Is(err, writeErr) {
		t.Fatalf("writeRawSubscription error = %v, want %v", err, writeErr)
	}
}

func TestWriteRawSubscriptionWritesPromptImmediately(t *testing.T) {
	runner := &PTYRunner{}
	ch := make(chan string, 1)
	done := make(chan struct{})

	var out safeBuffer
	errCh := make(chan error, 1)
	go func() {
		errCh <- runner.writeRawSubscription(&out, testSubscription(ch), done)
	}()
	ch <- "Password: "
	waitForOutput(t, &out, "Password: ")
	close(done)
	if err := <-errCh; err != nil {
		t.Fatalf("writeRawSubscription returned error: %v", err)
	}
}

func testSubscription(ch <-chan string) subscription {
	errCh := make(chan error)
	close(errCh)
	return subscription{ch: ch, err: errCh}
}

func TestSkipPrefixWriterSkipsSplitDuplicatePrefix(t *testing.T) {
	var out bytes.Buffer
	w := &skipPrefixWriter{w: &out, prefix: "^C"}

	if _, err := w.Write([]byte("^")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if _, err := w.Write([]byte("C\nsh$ ")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	got := out.String()
	want := "\nsh$ "
	if got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}
