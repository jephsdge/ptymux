package server

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/hinshun/vt10x"
)

type PTYRunner struct {
	commandMu sync.Mutex
	stateMu   sync.Mutex
	file      *os.File
	fd        int
	command   *exec.Cmd
	seq       uint64
	term      vt10x.Terminal
	history   []string

	subscribers map[uint64]subscriber
	nextSubID   uint64
	closed      bool
	readErr     error
	readerDone  chan struct{}
}

type subscription struct {
	id uint64
	ch <-chan string
}

type subscriber struct {
	ch       chan string
	done     chan struct{}
	reliable bool
}

func NewPTYRunner(shell string) (*PTYRunner, error) {
	if shell == "" {
		shell = "/bin/sh"
	}

	cmd := exec.Command(shell)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	file, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}
	fd := int(file.Fd())
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = file.Close()
		return nil, err
	}

	r := &PTYRunner{
		file:        file,
		fd:          fd,
		command:     cmd,
		term:        vt10x.New(vt10x.WithSize(120, 40)),
		subscribers: make(map[uint64]subscriber),
		readerDone:  make(chan struct{}),
	}
	go r.readLoop()
	r.waitForInitialOutput(100 * time.Millisecond)
	return r, nil
}

func (r *PTYRunner) Run(command string) (RunResult, error) {
	r.commandMu.Lock()
	defer r.commandMu.Unlock()

	prefix := r.currentLine()
	token := fmt.Sprintf("__PTYMUX_DONE_%d_%d__", os.Getpid(), atomic.AddUint64(&r.seq, 1))
	wrapped := wrapCommand(command, token)
	sub := r.subscribeReliable()
	defer r.unsubscribe(sub.id)
	if _, err := io.WriteString(r.file, wrapped); err != nil {
		return RunResult{}, err
	}

	var buf bytes.Buffer
	marker := []byte(token + ":")
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case chunk, ok := <-sub.ch:
			if !ok {
				return RunResult{}, r.subscriptionErr()
			}
			buf.WriteString(chunk)
			if idx := bytes.Index(buf.Bytes(), marker); idx >= 0 {
				_ = r.collectQuiet(sub.ch, &buf, 50*time.Millisecond)
				result := parseMarkedOutput(buf.Bytes(), idx, len(marker), command, prefix)
				r.record(result.Output)
				return result, nil
			}
		case <-deadline.C:
			return RunResult{}, os.ErrDeadlineExceeded
		}
	}
}

func (r *PTYRunner) RunIdle(command string) (RunResult, error) {
	return r.runIdle(command, 500*time.Millisecond, 30*time.Second)
}

func (r *PTYRunner) Send(input string) error {
	r.commandMu.Lock()
	defer r.commandMu.Unlock()

	_, err := io.WriteString(r.file, input+"\n")
	return err
}

func (r *PTYRunner) SendWait(input string, quietFor time.Duration) (RunResult, error) {
	return r.sendWait(input, quietFor, 30*time.Second, true)
}

func (r *PTYRunner) SendFollow(input string, output io.Writer, done <-chan struct{}) error {
	return r.sendFollow(input, output, 0, done)
}

func (r *PTYRunner) Command(keys string) error {
	seq, err := parseKeySequence(keys)
	if err != nil {
		return err
	}

	r.commandMu.Lock()
	defer r.commandMu.Unlock()

	_, err = r.file.Write(seq)
	return err
}

func (r *PTYRunner) CommandWait(keys string, quietFor time.Duration) (RunResult, error) {
	return r.commandWait(keys, quietFor, 30*time.Second)
}

func (r *PTYRunner) CommandFollow(keys string, output io.Writer, done <-chan struct{}) error {
	return r.commandFollow(keys, output, 0, done)
}

func (r *PTYRunner) Follow(output io.Writer, done <-chan struct{}) error {
	sub := r.subscribeBestEffort()
	defer r.unsubscribe(sub.id)
	return r.writeSubscription(output, sub.ch, 0, done)
}

func (r *PTYRunner) CtrlCFollow(output io.Writer, done <-chan struct{}) error {
	return r.ctrlCFollow(output, 0, done)
}

func (r *PTYRunner) Read(count int) (RunResult, error) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()

	if r.term == nil {
		return RunResult{}, nil
	}
	screen := strings.TrimRight(r.term.String(), "\n")
	if count > 0 {
		return RunResult{Output: recentTerminalEntries(screen, count), ExitCode: 0}, nil
	}
	return RunResult{Output: readableTerminalScreen(screen), ExitCode: 0}, nil
}

func (r *PTYRunner) runIdle(command string, quietFor, maxWait time.Duration) (RunResult, error) {
	r.commandMu.Lock()
	defer r.commandMu.Unlock()

	prefix := r.currentLine()
	sub := r.subscribeReliable()
	defer r.unsubscribe(sub.id)
	if _, err := io.WriteString(r.file, command+"\n"); err != nil {
		return RunResult{}, err
	}

	output, timedOut, err := r.collectUntilQuiet(sub.ch, quietFor, maxWait)
	if err != nil {
		return RunResult{}, err
	}
	output = cleanTerminalNoise(output)
	output = formatCommandTranscript(trimOutputBoundary(output), command, prefix)
	result := RunResult{Output: output, ExitCode: 0}
	if timedOut {
		result.ExitCode = 124
	}
	r.record(result.Output)
	return result, nil
}

func (r *PTYRunner) sendWait(input string, quietFor, maxWait time.Duration, returnOutput bool) (RunResult, error) {
	r.commandMu.Lock()
	defer r.commandMu.Unlock()

	prefix := r.currentLine()
	sub := r.subscribeReliable()
	defer r.unsubscribe(sub.id)
	if _, err := io.WriteString(r.file, input+"\n"); err != nil {
		return RunResult{}, err
	}

	output, timedOut, err := r.collectUntilQuiet(sub.ch, quietFor, maxWait)
	if err != nil {
		return RunResult{}, err
	}
	output = formatCommandTranscript(trimOutputBoundary(output), input, prefix)
	result := RunResult{Output: output, ExitCode: 0}
	if timedOut {
		result.ExitCode = 124
	}
	r.record(result.Output)
	if !returnOutput {
		result.Output = ""
	}
	return result, nil
}

func (r *PTYRunner) commandWait(keys string, quietFor, maxWait time.Duration) (RunResult, error) {
	seq, err := parseKeySequence(keys)
	if err != nil {
		return RunResult{}, err
	}

	r.commandMu.Lock()
	defer r.commandMu.Unlock()

	prefix := r.currentLine()
	sub := r.subscribeReliable()
	defer r.unsubscribe(sub.id)
	if _, err := r.file.Write(seq); err != nil {
		return RunResult{}, err
	}

	output, timedOut, err := r.collectUntilQuiet(sub.ch, quietFor, maxWait)
	if err != nil {
		return RunResult{}, err
	}
	output = formatCommandTranscript(trimOutputBoundary(output), keys, prefix)
	result := RunResult{Output: output, ExitCode: 0}
	if timedOut {
		result.ExitCode = 124
	}
	r.record(result.Output)
	return result, nil
}

func (r *PTYRunner) sendFollow(input string, output io.Writer, quietFor time.Duration, done <-chan struct{}) error {
	r.commandMu.Lock()

	sub := r.subscribeBestEffort()
	prefix := r.currentLine()
	if _, err := io.WriteString(r.file, input+"\n"); err != nil {
		r.unsubscribe(sub.id)
		r.commandMu.Unlock()
		return err
	}
	r.commandMu.Unlock()
	defer r.unsubscribe(sub.id)

	if prefix != "" {
		if _, err := io.WriteString(output, prefix); err != nil {
			return nil
		}
	}

	var observed bytes.Buffer
	err := r.writeSubscription(io.MultiWriter(output, &observed), sub.ch, quietFor, done)
	if observed.Len() > 0 {
		r.record(prefix + strings.TrimRight(observed.String(), "\n"))
	}
	return err
}

func (r *PTYRunner) commandFollow(keys string, output io.Writer, quietFor time.Duration, done <-chan struct{}) error {
	seq, err := parseKeySequence(keys)
	if err != nil {
		return err
	}

	r.commandMu.Lock()

	sub := r.subscribeBestEffort()
	if _, err := r.file.Write(seq); err != nil {
		r.unsubscribe(sub.id)
		r.commandMu.Unlock()
		return err
	}
	r.commandMu.Unlock()
	defer r.unsubscribe(sub.id)

	var observed bytes.Buffer
	err = r.writeSubscription(io.MultiWriter(output, &observed), sub.ch, quietFor, done)
	if observed.Len() > 0 {
		r.record(strings.TrimRight(observed.String(), "\n"))
	}
	return err
}

func (r *PTYRunner) ctrlCFollow(output io.Writer, quietFor time.Duration, done <-chan struct{}) error {
	r.commandMu.Lock()

	sub := r.subscribeBestEffort()
	if _, err := r.file.Write([]byte{3}); err != nil {
		r.unsubscribe(sub.id)
		r.commandMu.Unlock()
		return err
	}
	r.commandMu.Unlock()
	defer r.unsubscribe(sub.id)

	var observed bytes.Buffer
	err := r.writeSubscription(io.MultiWriter(output, &observed), sub.ch, quietFor, done)
	if observed.Len() > 0 {
		r.record(strings.TrimRight(observed.String(), "\n"))
	}
	return err
}

func (r *PTYRunner) record(output string) {
	output = strings.Trim(output, "\n")
	if output == "" {
		return
	}
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	r.history = append(r.history, output)
	const maxHistory = 200
	if len(r.history) > maxHistory {
		r.history = r.history[len(r.history)-maxHistory:]
	}
}

func (r *PTYRunner) readLoop() {
	defer close(r.readerDone)

	tmp := make([]byte, 4096)
	for {
		n, err := syscall.Read(r.fd, tmp)
		if n > 0 {
			chunk := r.observe(tmp[:n])
			if chunk != "" {
				r.broadcast(chunk)
			}
		}
		if err != nil {
			if isRetryableRead(err) {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			r.closeSubscribers(err)
			return
		}
	}
}

func (r *PTYRunner) waitForInitialOutput(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if r.currentLine() != "" {
			return
		}
		if r.subscriptionErr() != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (r *PTYRunner) subscribeBestEffort() subscription {
	return r.subscribe(false)
}

func (r *PTYRunner) subscribeReliable() subscription {
	return r.subscribe(true)
}

func (r *PTYRunner) subscribe(reliable bool) subscription {
	ch := make(chan string, 128)
	done := make(chan struct{})

	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if r.closed {
		close(ch)
		return subscription{ch: ch}
	}
	id := r.nextSubID
	r.nextSubID++
	r.subscribers[id] = subscriber{ch: ch, done: done, reliable: reliable}
	return subscription{id: id, ch: ch}
}

func (r *PTYRunner) unsubscribe(id uint64) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if sub, ok := r.subscribers[id]; ok {
		delete(r.subscribers, id)
		close(sub.done)
	}
}

func (r *PTYRunner) broadcast(chunk string) {
	r.stateMu.Lock()
	subs := make([]subscriber, 0, len(r.subscribers))
	for _, sub := range r.subscribers {
		subs = append(subs, sub)
	}
	r.stateMu.Unlock()

	for _, sub := range subs {
		if sub.reliable {
			select {
			case sub.ch <- chunk:
			case <-sub.done:
			}
			continue
		}
		select {
		case sub.ch <- chunk:
		case <-sub.done:
		default:
		}
	}
}

func (r *PTYRunner) closeSubscribers(err error) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	r.readErr = err
	for id, sub := range r.subscribers {
		close(sub.ch)
		close(sub.done)
		delete(r.subscribers, id)
	}
}

func (r *PTYRunner) subscriberCount() int {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	return len(r.subscribers)
}

func (r *PTYRunner) subscriptionErr() error {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if r.readErr != nil {
		return r.readErr
	}
	if r.closed {
		return io.EOF
	}
	return nil
}

func (r *PTYRunner) writeSubscription(output io.Writer, ch <-chan string, quietFor time.Duration, done <-chan struct{}) error {
	var doneCh <-chan struct{}
	if done != nil {
		doneCh = done
	}

	var quiet <-chan time.Time
	var timer *time.Timer
	if quietFor > 0 {
		timer = time.NewTimer(quietFor)
		defer timer.Stop()
		quiet = timer.C
	}

	for {
		select {
		case <-doneCh:
			return nil
		case <-quiet:
			return nil
		case chunk, ok := <-ch:
			if !ok {
				return r.subscriptionErr()
			}
			if _, err := io.WriteString(output, chunk); err != nil {
				return nil
			}
			if timer != nil {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(quietFor)
			}
		}
	}
}

func (r *PTYRunner) collectUntilQuiet(ch <-chan string, quietFor, maxWait time.Duration) (string, bool, error) {
	var buf bytes.Buffer
	deadline := time.NewTimer(maxWait)
	defer deadline.Stop()

	var quiet <-chan time.Time
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		select {
		case chunk, ok := <-ch:
			if !ok {
				return "", false, r.subscriptionErr()
			}
			buf.WriteString(chunk)
			if timer == nil {
				timer = time.NewTimer(quietFor)
				quiet = timer.C
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(quietFor)
			}
		case <-quiet:
			return buf.String(), false, nil
		case <-deadline.C:
			return buf.String(), true, nil
		}
	}
}

func (r *PTYRunner) collectQuiet(ch <-chan string, buf *bytes.Buffer, quietFor time.Duration) error {
	timer := time.NewTimer(quietFor)
	defer timer.Stop()

	for {
		select {
		case chunk, ok := <-ch:
			if !ok {
				return r.subscriptionErr()
			}
			buf.WriteString(chunk)
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(quietFor)
		case <-timer.C:
			return nil
		}
	}
}

func trimOutputBoundary(output string) string {
	output = strings.TrimRight(output, "\n")
	for strings.HasPrefix(output, "\n") {
		output = strings.TrimPrefix(output, "\n")
	}
	return output
}

func recentTerminalEntries(screen string, count int) string {
	if count <= 0 {
		return strings.TrimRight(screen, "\n")
	}
	lines := normalizeScreenLines(screen)
	var entries [][]string
	var current []string
	for _, line := range lines {
		if isPromptCommandLine(line) {
			if len(current) > 0 {
				entries = append(entries, current)
			}
			current = []string{line}
			continue
		}
		if len(current) > 0 {
			current = append(current, line)
		}
	}
	if len(current) > 0 {
		entries = append(entries, current)
	}
	if len(entries) == 0 {
		return ""
	}
	start := len(entries) - count
	if start < 0 {
		start = 0
	}
	out := make([]string, 0, len(entries)-start)
	for _, entry := range entries[start:] {
		out = append(out, strings.Join(compactTerminalEntry(entry), "\n"))
	}
	return strings.Join(out, "\n")
}

func readableTerminalScreen(screen string) string {
	return strings.Join(normalizeScreenLines(screen), "\n")
}

func compactTerminalEntry(lines []string) []string {
	lines = trimTrailingBlankLines(lines)
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		if strings.TrimSpace(line) == "" && i+1 < len(lines) && isPromptLike(lines[i+1]) {
			continue
		}
		out = append(out, line)
	}
	return out
}

func normalizeScreenLines(screen string) []string {
	raw := strings.Split(screen, "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimRight(line, " \t\r")
		if isInternalMarkerLine(line) {
			continue
		}
		lines = append(lines, line)
	}
	return trimTrailingBlankLines(lines)
}

func trimTrailingBlankLines(lines []string) []string {
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return lines[:end]
}

func isPromptCommandLine(line string) bool {
	line = strings.TrimRight(line, " \t")
	for _, marker := range []string{"$", "#", ">", "%"} {
		idx := strings.LastIndex(line, marker)
		if idx < 0 || idx == len(line)-1 {
			continue
		}
		after := line[idx+1:]
		if strings.TrimSpace(after) == "" {
			continue
		}
		if strings.HasPrefix(after, " ") {
			return strings.TrimSpace(after) != ""
		}
		return true
	}
	return false
}

func isInternalMarkerLine(line string) bool {
	return strings.Contains(line, "__ptymux_status=$?") ||
		strings.Contains(line, "__ptymux_token_a=") ||
		strings.Contains(line, "__ptymux_token_b=") ||
		strings.Contains(line, "$__ptymux_token_a") ||
		strings.Contains(line, "$__ptymux_token_b") ||
		strings.Contains(line, "$__ptymux_status") ||
		strings.Contains(line, "__PTYMUX_DONE_")
}

func wrapCommand(command, token string) string {
	tokenA := token[:len(token)/2]
	tokenB := token[len(token)/2:]
	return fmt.Sprintf("%s\n__ptymux_status=$?; __ptymux_token_a=%q; __ptymux_token_b=%q; printf '\\n%%s%%s:%%s\\n' \"$__ptymux_token_a\" \"$__ptymux_token_b\" \"$__ptymux_status\"\n", command, tokenA, tokenB)
}

func (r *PTYRunner) Close() error {
	if r.file != nil {
		_ = r.file.Close()
	}
	if r.readerDone != nil {
		<-r.readerDone
	}
	if r.command != nil && r.command.Process != nil {
		_ = r.command.Process.Kill()
		_, _ = r.command.Process.Wait()
	}
	return nil
}

func isRetryableRead(err error) bool {
	return errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, os.ErrDeadlineExceeded)
}

func (r *PTYRunner) observe(data []byte) string {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if r.term != nil {
		_, _ = r.term.Write(data)
	}
	return cleanTerminalNoise(string(data))
}

func (r *PTYRunner) currentLine() string {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if r.term == nil {
		return ""
	}
	r.term.Lock()
	defer r.term.Unlock()

	cursor := r.term.Cursor()
	if cursor.X <= 0 {
		return ""
	}
	var b strings.Builder
	for x := 0; x < cursor.X; x++ {
		ch := r.term.Cell(x, cursor.Y).Char
		if ch == 0 {
			ch = ' '
		}
		b.WriteRune(ch)
	}
	return b.String()
}

func parseMarkedOutput(raw []byte, markerStart, markerLen int, command, prefix string) RunResult {
	beforeMarker := strings.TrimRight(string(raw[:markerStart]), "\r\n")
	rest := string(raw[markerStart+markerLen:])
	line := rest
	afterStatus := ""
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
		afterStatus = rest[i+1:]
	}
	exitCode, _ := strconv.Atoi(strings.TrimSpace(line))

	output := beforeMarker
	if strings.Trim(afterStatus, "\r\n") != "" {
		output += "\n" + strings.TrimLeft(afterStatus, "\r\n")
	}
	output = strings.ReplaceAll(output, "\r\n", "\n")
	output = cleanTerminalNoise(output)
	lines := strings.Split(output, "\n")
	lines = dropEchoLines(lines)
	output = strings.Join(lines, "\n")
	output = strings.Trim(output, "\n")
	output = formatCommandTranscript(output, command, prefix)

	return RunResult{Output: output, ExitCode: exitCode}
}

func dropEchoLines(lines []string) []string {
	out := lines[:0]
	for _, line := range lines {
		if isInternalMarkerLine(line) ||
			strings.Contains(line, "printf '\\n%s%s:%s\\n'") {
			continue
		}
		out = append(out, line)
	}
	return out
}

func formatCommandTranscript(output, command, prefix string) string {
	lines := strings.Split(output, "\n")
	if len(lines) < 2 || lines[0] != command {
		return output
	}
	prompt := prefix
	if prompt == "" {
		prompt = lines[len(lines)-1]
	}
	if !isPromptLike(prompt) {
		return output
	}
	lines[0] = prompt + lines[0]
	return strings.Join(lines, "\n")
}

func isPromptLike(line string) bool {
	line = strings.TrimSpace(line)
	return strings.HasSuffix(line, "$") ||
		strings.HasSuffix(line, "#") ||
		strings.HasSuffix(line, ">") ||
		strings.HasSuffix(line, "%")
}

func cleanTerminalNoise(output string) string {
	output = strings.ReplaceAll(output, "\x1b[?2004h", "")
	output = strings.ReplaceAll(output, "\x1b[?2004l", "")
	output = strings.ReplaceAll(output, "\r", "")
	return output
}
