package server

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/creack/pty"
	"github.com/hinshun/vt10x"
)

type PTYRunner struct {
	commandMu       sync.Mutex
	stateMu         sync.Mutex
	file            *os.File
	fd              int
	command         *exec.Cmd
	term            vt10x.Terminal
	terminalPending []byte
	transcript      ansiTranscript

	subscribers   map[uint64]*subscriber
	nextSubID     uint64
	closed        bool
	readErr       error
	readerDone    chan struct{}
	processDone   chan struct{}
	lifecycleDone chan struct{}
	closeOnce     sync.Once
	closeDone     chan struct{}
	closeErr      error
}

var (
	ErrOperationCanceled = errors.New("operation canceled")
	errSubscriberTooSlow = errors.New("subscriber too slow")
)

const maxRunStatusBytes = 32

type runCollector struct {
	marker    []byte
	pending   []byte
	status    []byte
	output    []byte
	limit     int
	found     bool
	complete  bool
	truncated bool
	exitCode  int
}

func newRunCollector(marker []byte, limit int) *runCollector {
	return &runCollector{marker: marker, limit: limit}
}

func (c *runCollector) consume(data []byte) error {
	if c.complete {
		c.appendOutput(data)
		return nil
	}
	if c.found {
		return c.consumeStatus(data)
	}

	c.pending = append(c.pending, data...)
	if markerStart := bytes.Index(c.pending, c.marker); markerStart >= 0 {
		c.appendOutput(c.pending[:markerStart])
		rest := append([]byte(nil), c.pending[markerStart+len(c.marker):]...)
		c.pending = nil
		c.found = true
		return c.consumeStatus(rest)
	}

	keep := len(c.marker) - 1
	if len(c.pending) > keep {
		flush := len(c.pending) - keep
		c.appendOutput(c.pending[:flush])
		c.pending = append(c.pending[:0], c.pending[flush:]...)
	}
	return nil
}

func (c *runCollector) consumeStatus(data []byte) error {
	c.status = append(c.status, data...)
	lineEnd := bytes.IndexAny(c.status, "\r\n")
	if lineEnd < 0 {
		if len(c.status) > maxRunStatusBytes {
			return errors.New("invalid command completion status")
		}
		return nil
	}

	exitCode, err := strconv.Atoi(strings.TrimSpace(string(c.status[:lineEnd])))
	if err != nil {
		return errors.New("invalid command completion status")
	}
	restStart := lineEnd + 1
	if c.status[lineEnd] == '\r' && restStart < len(c.status) && c.status[restStart] == '\n' {
		restStart++
	}
	c.exitCode = exitCode
	c.complete = true
	c.appendOutput(c.status[restStart:])
	c.status = nil
	return nil
}

func (c *runCollector) appendOutput(data []byte) {
	if appendBounded(&c.output, data, c.limit) {
		c.truncated = true
	}
}

func appendBounded(output *[]byte, data []byte, limit int) bool {
	remaining := limit - len(*output)
	if remaining <= 0 {
		return len(data) > 0
	}
	if len(data) > remaining {
		*output = append(*output, data[:remaining]...)
		return true
	}
	*output = append(*output, data...)
	return false
}

func (c *runCollector) result(command, prefix string) RunResult {
	complete := completeUTF8Prefix(c.output)
	if complete < len(c.output) {
		c.truncated = true
	}
	return RunResult{
		Output:    formatRunOutput(c.output[:complete], command, prefix),
		ExitCode:  c.exitCode,
		Truncated: c.truncated,
	}
}

type subscription struct {
	id   uint64
	ch   <-chan string
	done <-chan struct{}
	err  <-chan error
}

type subscriber struct {
	id       uint64
	ch       chan string
	done     chan struct{}
	err      chan error
	reliable bool
	stopOnce sync.Once
}

func (s *subscriber) stop(err error) {
	s.stopOnce.Do(func() {
		if err != nil {
			s.err <- err
		}
		close(s.done)
	})
}

type skipPrefixWriter struct {
	w       io.Writer
	prefix  string
	pending string
	done    bool
}

func (w *skipPrefixWriter) Write(p []byte) (int, error) {
	if w.done || w.prefix == "" {
		_, err := w.w.Write(p)
		return len(p), err
	}

	w.pending += string(p)
	if strings.HasPrefix(w.prefix, w.pending) {
		if w.pending == w.prefix {
			w.done = true
			w.pending = ""
		}
		return len(p), nil
	}
	w.done = true
	if strings.HasPrefix(w.pending, w.prefix) {
		w.pending = strings.TrimPrefix(w.pending, w.prefix)
	}
	_, err := io.WriteString(w.w, w.pending)
	w.pending = ""
	return len(p), err
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
		file:          file,
		fd:            fd,
		command:       cmd,
		term:          vt10x.New(vt10x.WithSize(120, 40)),
		subscribers:   make(map[uint64]*subscriber),
		readerDone:    make(chan struct{}),
		processDone:   make(chan struct{}),
		lifecycleDone: make(chan struct{}),
		closeDone:     make(chan struct{}),
	}
	go r.waitProcessLoop()
	go r.readLoop()
	go r.waitLifecycleLoop()
	r.waitForInitialOutput(100 * time.Millisecond)
	return r, nil
}

func (r *PTYRunner) Run(command string) (RunResult, error) {
	return r.RunWithDone(command, nil)
}

func (r *PTYRunner) RunWithDone(command string, done <-chan struct{}) (RunResult, error) {
	return r.runWithLimit(command, done, MaxRunOutputBytes)
}

func (r *PTYRunner) runWithLimit(command string, done <-chan struct{}, outputLimit int) (RunResult, error) {
	r.commandMu.Lock()
	defer r.commandMu.Unlock()

	if done != nil {
		select {
		case <-done:
			return RunResult{}, ErrOperationCanceled
		default:
		}
	}

	token, err := newCommandToken()
	if err != nil {
		return RunResult{}, err
	}
	prefix := r.currentLine()
	wrapped := wrapCommand(command, token)
	sub := r.subscribeReliable()
	defer r.unsubscribe(sub.id)
	if _, err := io.WriteString(r.file, wrapped); err != nil {
		return RunResult{}, err
	}

	collector := newRunCollector([]byte(token+":"), outputLimit)
	canceled := false
	doneCh := done
	for {
		subscriberDone := sub.done
		if len(sub.ch) > 0 {
			subscriberDone = nil
		}
		select {
		case <-doneCh:
			interrupt := append([]byte{3}, []byte(wrapCompletion(token))...)
			if _, err := r.file.Write(interrupt); err != nil {
				return RunResult{}, err
			}
			canceled = true
			doneCh = nil
		case <-subscriberDone:
			return RunResult{}, r.subscriptionResultErr(sub)
		case chunk, ok := <-sub.ch:
			if !ok {
				return RunResult{}, r.subscriptionResultErr(sub)
			}
			if err := collector.consume([]byte(chunk)); err != nil {
				return RunResult{}, err
			}
			if collector.complete {
				_ = r.collectQuietOutput(sub, collector, 50*time.Millisecond)
				result := collector.result(command, prefix)
				if canceled {
					return result, ErrOperationCanceled
				}
				return result, nil
			}
		}
	}
}

func (r *PTYRunner) RunIdle(command string) (RunResult, error) {
	return r.runIdle(command, 500*time.Millisecond)
}

func (r *PTYRunner) Send(input string) error {
	r.commandMu.Lock()
	defer r.commandMu.Unlock()

	_, err := io.WriteString(r.file, input+"\n")
	return err
}

func (r *PTYRunner) SendWait(input string, quietFor time.Duration) (RunResult, error) {
	return r.sendWait(input, quietFor, true)
}

func (r *PTYRunner) SendFollow(input string, output io.Writer, done <-chan struct{}) error {
	return r.sendFollow(input, output, 0, done)
}

func (r *PTYRunner) Text(input string) error {
	r.commandMu.Lock()
	defer r.commandMu.Unlock()

	_, err := io.WriteString(r.file, input)
	return err
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
	return r.commandWait(keys, quietFor)
}

func (r *PTYRunner) CommandFollow(keys string, output io.Writer, done <-chan struct{}) error {
	return r.commandFollow(keys, output, 0, done)
}

func (r *PTYRunner) Keys(keys string) error {
	seq, err := parseKeySequenceNoEnter(keys)
	if err != nil {
		return err
	}

	r.commandMu.Lock()
	defer r.commandMu.Unlock()

	_, err = r.file.Write(seq)
	return err
}

func (r *PTYRunner) KeysWait(keys string, quietFor time.Duration) (RunResult, error) {
	return r.keysWait(keys, quietFor)
}

func (r *PTYRunner) KeysFollow(keys string, output io.Writer, done <-chan struct{}) error {
	return r.keysFollow(keys, output, 0, done)
}

func (r *PTYRunner) Follow(output io.Writer, done <-chan struct{}) error {
	sub := r.subscribeBestEffort()
	defer r.unsubscribe(sub.id)
	return r.writeRawSubscription(output, sub, done)
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
	if count > 0 {
		if r.term.Mode()&vt10x.ModeAltScreen != 0 {
			return RunResult{Output: renderTerminalScreen(r.term, count), ExitCode: 0}, nil
		}
		return RunResult{Output: r.transcript.RecentLines(count), ExitCode: 0}, nil
	}
	return RunResult{Output: renderTerminalScreen(r.term, 0), ExitCode: 0}, nil
}

func (r *PTYRunner) runIdle(command string, quietFor time.Duration) (RunResult, error) {
	r.commandMu.Lock()
	defer r.commandMu.Unlock()

	prefix := r.currentLine()
	sub := r.subscribeReliable()
	defer r.unsubscribe(sub.id)
	if _, err := io.WriteString(r.file, command+"\n"); err != nil {
		return RunResult{}, err
	}

	output, truncated, err := r.collectUntilQuiet(sub, quietFor)
	if err != nil {
		return RunResult{}, err
	}
	output = cleanTerminalNoise(output)
	output = formatCommandTranscript(trimOutputBoundary(output), command, prefix)
	return RunResult{Output: output, ExitCode: 0, Truncated: truncated}, nil
}

func (r *PTYRunner) sendWait(input string, quietFor time.Duration, returnOutput bool) (RunResult, error) {
	r.commandMu.Lock()
	defer r.commandMu.Unlock()

	prefix := r.currentLine()
	sub := r.subscribeReliable()
	defer r.unsubscribe(sub.id)
	if _, err := io.WriteString(r.file, input+"\n"); err != nil {
		return RunResult{}, err
	}

	output, truncated, err := r.collectUntilQuiet(sub, quietFor)
	if err != nil {
		return RunResult{}, err
	}
	output = cleanTerminalNoise(output)
	output = formatCommandTranscript(trimOutputBoundary(output), input, prefix)
	result := RunResult{Output: output, ExitCode: 0, Truncated: truncated}
	if !returnOutput {
		result.Output = ""
	}
	return result, nil
}

func (r *PTYRunner) commandWait(keys string, quietFor time.Duration) (RunResult, error) {
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

	output, truncated, err := r.collectUntilQuiet(sub, quietFor)
	if err != nil {
		return RunResult{}, err
	}
	output = cleanTerminalNoise(output)
	output = formatCommandTranscript(trimOutputBoundary(output), keys, prefix)
	return RunResult{Output: output, ExitCode: 0, Truncated: truncated}, nil
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
			return err
		}
	}

	return r.writeSubscription(output, sub, quietFor, done)
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

	return r.writeSubscription(output, sub, quietFor, done)
}

func (r *PTYRunner) keysWait(keys string, quietFor time.Duration) (RunResult, error) {
	seq, err := parseKeySequenceNoEnter(keys)
	if err != nil {
		return RunResult{}, err
	}

	r.commandMu.Lock()
	defer r.commandMu.Unlock()

	sub := r.subscribeReliable()
	defer r.unsubscribe(sub.id)
	if _, err := r.file.Write(seq); err != nil {
		return RunResult{}, err
	}

	output, truncated, err := r.collectUntilQuiet(sub, quietFor)
	if err != nil {
		return RunResult{}, err
	}
	output = cleanTerminalNoise(output)
	output = trimOutputBoundary(output)
	return RunResult{Output: output, ExitCode: 0, Truncated: truncated}, nil
}

func (r *PTYRunner) keysFollow(keys string, output io.Writer, quietFor time.Duration, done <-chan struct{}) error {
	seq, err := parseKeySequenceNoEnter(keys)
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

	return r.writeSubscription(output, sub, quietFor, done)
}

func (r *PTYRunner) ctrlCFollow(output io.Writer, quietFor time.Duration, done <-chan struct{}) error {
	r.commandMu.Lock()

	sub := r.subscribeBestEffort()
	if _, err := r.file.Write([]byte{3}); err != nil {
		r.unsubscribe(sub.id)
		r.commandMu.Unlock()
		return err
	}
	if _, err := io.WriteString(output, "^C"); err != nil {
		r.unsubscribe(sub.id)
		r.commandMu.Unlock()
		return err
	}
	r.commandMu.Unlock()
	defer r.unsubscribe(sub.id)

	return r.writeSubscription(&skipPrefixWriter{
		w:      output,
		prefix: "^C",
	}, sub, quietFor, done)
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
	sub := &subscriber{
		ch:       make(chan string, 128),
		done:     make(chan struct{}),
		err:      make(chan error, 1),
		reliable: reliable,
	}

	r.stateMu.Lock()
	if r.closed {
		err := r.readErr
		if err == nil {
			err = io.EOF
		}
		r.stateMu.Unlock()
		sub.stop(err)
		return subscription{ch: sub.ch, done: sub.done, err: sub.err}
	}
	sub.id = r.nextSubID
	r.nextSubID++
	r.subscribers[sub.id] = sub
	r.stateMu.Unlock()
	return subscription{id: sub.id, ch: sub.ch, done: sub.done, err: sub.err}
}

func (r *PTYRunner) unsubscribe(id uint64) {
	r.stateMu.Lock()
	sub := r.subscribers[id]
	delete(r.subscribers, id)
	r.stateMu.Unlock()
	if sub != nil {
		sub.stop(nil)
	}
}

func (r *PTYRunner) broadcast(chunk string) {
	r.stateMu.Lock()
	subscribers := make([]*subscriber, 0, len(r.subscribers))
	for _, sub := range r.subscribers {
		subscribers = append(subscribers, sub)
	}
	r.stateMu.Unlock()

	for _, sub := range subscribers {
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
			sub.stop(errSubscriberTooSlow)
			r.stateMu.Lock()
			if r.subscribers[sub.id] == sub {
				delete(r.subscribers, sub.id)
			}
			r.stateMu.Unlock()
		}
	}
}

func (r *PTYRunner) closeSubscribers(err error) {
	r.stateMu.Lock()
	if r.closed {
		r.stateMu.Unlock()
		return
	}
	r.closed = true
	r.readErr = err
	if err == nil {
		err = io.EOF
	}
	subscribers := make([]*subscriber, 0, len(r.subscribers))
	for id, sub := range r.subscribers {
		subscribers = append(subscribers, sub)
		delete(r.subscribers, id)
	}
	r.stateMu.Unlock()
	for _, sub := range subscribers {
		sub.stop(err)
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

func (r *PTYRunner) subscriptionResultErr(sub subscription) error {
	select {
	case err, ok := <-sub.err:
		if ok && err != nil {
			return err
		}
	default:
	}
	return r.subscriptionErr()
}

func (r *PTYRunner) writeRawSubscription(output io.Writer, sub subscription, done <-chan struct{}) error {
	var doneCh <-chan struct{}
	if done != nil {
		doneCh = done
	}
	for {
		subscriberDone := sub.done
		if len(sub.ch) > 0 {
			subscriberDone = nil
		}
		select {
		case <-doneCh:
			return nil
		case <-subscriberDone:
			return r.subscriptionResultErr(sub)
		case chunk, ok := <-sub.ch:
			if !ok {
				return r.subscriptionResultErr(sub)
			}
			if _, err := io.WriteString(output, chunk); err != nil {
				return err
			}
		}
	}
}

func (r *PTYRunner) writeSubscription(output io.Writer, sub subscription, quietFor time.Duration, done <-chan struct{}) error {
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
	cleaner := NewTerminalCleaner()
	flushTimer := time.NewTimer(time.Hour)
	if !flushTimer.Stop() {
		<-flushTimer.C
	}
	defer flushTimer.Stop()
	var flush <-chan time.Time
	const streamFlushInterval = 100 * time.Millisecond
	flushNow := func() error {
		if flushed := cleaner.Flush(); flushed != "" {
			_, err := io.WriteString(output, flushed)
			return err
		}
		return nil
	}
	scheduleFlush := func() {
		if !cleaner.Pending() {
			flush = nil
			if !flushTimer.Stop() {
				select {
				case <-flushTimer.C:
				default:
				}
			}
			return
		}
		if !flushTimer.Stop() {
			select {
			case <-flushTimer.C:
			default:
			}
		}
		flushTimer.Reset(streamFlushInterval)
		flush = flushTimer.C
	}

	for {
		subscriberDone := sub.done
		if len(sub.ch) > 0 {
			subscriberDone = nil
		}
		select {
		case <-doneCh:
			return flushNow()
		case <-quiet:
			return flushNow()
		case <-flush:
			if err := flushNow(); err != nil {
				return err
			}
			flush = nil
		case <-subscriberDone:
			if err := flushNow(); err != nil {
				return err
			}
			return r.subscriptionResultErr(sub)
		case chunk, ok := <-sub.ch:
			if !ok {
				if err := flushNow(); err != nil {
					return err
				}
				return r.subscriptionResultErr(sub)
			}
			cleaned := cleaner.WriteString(chunk)
			if _, err := io.WriteString(output, cleaned); err != nil {
				return err
			}
			scheduleFlush()
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

func (r *PTYRunner) collectUntilQuiet(sub subscription, quietFor time.Duration) (string, bool, error) {
	var output []byte
	truncated := false
	timer := time.NewTimer(quietFor)
	defer timer.Stop()

	for {
		subscriberDone := sub.done
		if len(sub.ch) > 0 {
			subscriberDone = nil
		}
		select {
		case <-subscriberDone:
			return "", false, r.subscriptionResultErr(sub)
		case chunk, ok := <-sub.ch:
			if !ok {
				return "", false, r.subscriptionResultErr(sub)
			}
			if appendBounded(&output, []byte(chunk), MaxRunOutputBytes) {
				truncated = true
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(quietFor)
		case <-timer.C:
			complete := completeUTF8Prefix(output)
			if complete < len(output) {
				truncated = true
			}
			return string(output[:complete]), truncated, nil
		}
	}
}

func (r *PTYRunner) collectQuietOutput(sub subscription, collector *runCollector, quietFor time.Duration) error {
	timer := time.NewTimer(quietFor)
	defer timer.Stop()

	for {
		subscriberDone := sub.done
		if len(sub.ch) > 0 {
			subscriberDone = nil
		}
		select {
		case <-subscriberDone:
			return r.subscriptionResultErr(sub)
		case chunk, ok := <-sub.ch:
			if !ok {
				return r.subscriptionResultErr(sub)
			}
			collector.appendOutput([]byte(chunk))
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

func isInternalMarkerLine(line string) bool {
	return strings.Contains(line, "__ptymux_status=$?") ||
		strings.Contains(line, "__ptymux_token_a=") ||
		strings.Contains(line, "__ptymux_token_b=") ||
		strings.Contains(line, "$__ptymux_token_a") ||
		strings.Contains(line, "$__ptymux_token_b") ||
		strings.Contains(line, "$__ptymux_status") ||
		strings.Contains(line, "__PTYMUX_DONE_")
}

func newCommandToken() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "__PTYMUX_DONE_" + hex.EncodeToString(random[:]) + "__", nil
}

func wrapCommand(command, token string) string {
	return command + "\n" + wrapCompletion(token)
}

func wrapCompletion(token string) string {
	tokenA := token[:len(token)/2]
	tokenB := token[len(token)/2:]
	return fmt.Sprintf("__ptymux_status=$?; __ptymux_token_a=%q; __ptymux_token_b=%q; printf '\\n%%s%%s:%%s\\n' \"$__ptymux_token_a\" \"$__ptymux_token_b\" \"$__ptymux_status\"\n", tokenA, tokenB)
}

func (r *PTYRunner) Close() error {
	r.closeOnce.Do(func() {
		defer close(r.closeDone)

		if r.command != nil && r.command.Process != nil {
			if err := signalProcessGroup(r.command.Process.Pid, syscall.SIGTERM); err != nil && r.closeErr == nil {
				r.closeErr = err
			}
		}
		if !waitWithTimeout(r.processDone, 500*time.Millisecond) {
			if r.command != nil && r.command.Process != nil {
				if err := signalProcessGroup(r.command.Process.Pid, syscall.SIGKILL); err != nil && r.closeErr == nil {
					r.closeErr = err
				}
			}
			waitWithTimeout(r.processDone, time.Second)
		}
		if r.file != nil {
			_ = r.file.Close()
		}
		if r.readerDone != nil {
			<-r.readerDone
		}
	})
	<-r.closeDone
	return r.closeErr
}

func (r *PTYRunner) Done() <-chan struct{} {
	return r.lifecycleDone
}

func (r *PTYRunner) waitProcessLoop() {
	if r.command != nil {
		_ = r.command.Wait()
	}
	close(r.processDone)
}

func (r *PTYRunner) waitLifecycleLoop() {
	<-r.processDone
	<-r.readerDone
	close(r.lifecycleDone)
}

func waitWithTimeout(done <-chan struct{}, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func signalProcessGroup(pid int, signal syscall.Signal) error {
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	if err := syscall.Kill(-pgid, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func isRetryableRead(err error) bool {
	return errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, os.ErrDeadlineExceeded)
}

func completeUTF8Prefix(data []byte) int {
	for offset := 0; offset < len(data); {
		_, size := utf8.DecodeRune(data[offset:])
		if size == 1 && !utf8.FullRune(data[offset:]) {
			return offset
		}
		offset += size
	}
	return len(data)
}

func (r *PTYRunner) observe(data []byte) string {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if r.term != nil {
		terminalData := data
		if len(r.terminalPending) > 0 {
			terminalData = append(append([]byte(nil), r.terminalPending...), data...)
		}
		complete := completeUTF8Prefix(terminalData)
		written, _ := r.term.Write(terminalData[:complete])
		r.terminalPending = append(r.terminalPending[:0], terminalData[written:]...)
		r.transcript.Write(data)
	}
	return string(data)
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
	return RunResult{Output: formatRunOutput([]byte(output), command, prefix), ExitCode: exitCode}
}

func formatRunOutput(raw []byte, command, prefix string) string {
	output := strings.ReplaceAll(string(raw), "\r\n", "\n")
	output = cleanTerminalNoise(output)
	lines := strings.Split(output, "\n")
	lines = dropEchoLines(lines)
	output = strings.Join(lines, "\n")
	output = strings.Trim(output, "\n")
	return formatCommandTranscript(output, command, prefix)
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
	return CleanTerminalString(output)
}
