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
	mu      sync.Mutex
	file    *os.File
	fd      int
	command *exec.Cmd
	seq     uint64
	term    vt10x.Terminal
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

	r := &PTYRunner{file: file, fd: fd, command: cmd, term: vt10x.New(vt10x.WithSize(120, 40))}
	_ = r.drain(100 * time.Millisecond)
	return r, nil
}

func (r *PTYRunner) Run(command string) (RunResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	prefix := r.currentLine()
	token := fmt.Sprintf("__PTYMUX_DONE_%d_%d__", os.Getpid(), atomic.AddUint64(&r.seq, 1))
	wrapped := wrapCommand(command, token)
	if _, err := io.WriteString(r.file, wrapped); err != nil {
		return RunResult{}, err
	}

	var buf bytes.Buffer
	marker := []byte(token + ":")
	tmp := make([]byte, 4096)
	deadline := time.Now().Add(30 * time.Second)
	for {
		n, err := syscall.Read(r.fd, tmp)
		if n > 0 {
			chunk := r.observe(tmp[:n])
			buf.WriteString(chunk)
			if idx := bytes.Index(buf.Bytes(), marker); idx >= 0 {
				_ = r.readQuiet(&buf, 50*time.Millisecond)
				result := parseMarkedOutput(buf.Bytes(), idx, len(marker), command, prefix)
				return result, nil
			}
		}
		if err != nil {
			if isRetryableRead(err) && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			return RunResult{}, err
		}
	}
}

func (r *PTYRunner) readQuiet(buf *bytes.Buffer, quietFor time.Duration) error {
	tmp := make([]byte, 4096)
	quietDeadline := time.Now().Add(quietFor)
	for {
		n, err := syscall.Read(r.fd, tmp)
		if n > 0 {
			buf.WriteString(r.observe(tmp[:n]))
			quietDeadline = time.Now().Add(quietFor)
		}
		if time.Now().After(quietDeadline) {
			return nil
		}
		if err != nil && !isRetryableRead(err) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (r *PTYRunner) RunIdle(command string) (RunResult, error) {
	return r.runIdle(command, 500*time.Millisecond, 30*time.Second)
}

func (r *PTYRunner) Send(input string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, err := io.WriteString(r.file, input+"\n")
	return err
}

func (r *PTYRunner) SendFollow(input string, output io.Writer, done <-chan struct{}) error {
	return r.sendFollow(input, output, 0, done)
}

func (r *PTYRunner) CtrlCFollow(output io.Writer, done <-chan struct{}) error {
	return r.ctrlCFollow(output, 0, done)
}

func (r *PTYRunner) runIdle(command string, quietFor, maxWait time.Duration) (RunResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	prefix := r.currentLine()
	if _, err := io.WriteString(r.file, command+"\n"); err != nil {
		return RunResult{}, err
	}

	var buf bytes.Buffer
	tmp := make([]byte, 4096)
	deadline := time.Now().Add(maxWait)
	quietDeadline := time.Now().Add(quietFor)
	for {
		n, err := syscall.Read(r.fd, tmp)
		if n > 0 {
			buf.WriteString(r.observe(tmp[:n]))
			quietDeadline = time.Now().Add(quietFor)
		}
		now := time.Now()
		if now.After(quietDeadline) && buf.Len() > 0 {
			output := cleanTerminalNoise(buf.String())
			output = formatCommandTranscript(trimOutputBoundary(output), command, prefix)
			return RunResult{Output: output, ExitCode: 0}, nil
		}
		if now.After(deadline) {
			output := cleanTerminalNoise(buf.String())
			output = formatCommandTranscript(trimOutputBoundary(output), command, prefix)
			return RunResult{Output: output, ExitCode: 124}, nil
		}
		if err != nil && !isRetryableRead(err) {
			return RunResult{}, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (r *PTYRunner) sendFollow(input string, output io.Writer, quietFor time.Duration, done <-chan struct{}) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	prefix := r.currentLine()
	if _, err := io.WriteString(r.file, input+"\n"); err != nil {
		return err
	}
	if prefix != "" {
		if _, err := io.WriteString(output, prefix); err != nil {
			return nil
		}
	}

	return r.follow(output, quietFor, done)
}

func (r *PTYRunner) follow(output io.Writer, quietFor time.Duration, done <-chan struct{}) error {
	tmp := make([]byte, 4096)
	var quietDeadline time.Time
	if quietFor > 0 {
		quietDeadline = time.Now().Add(quietFor)
	}
	for {
		if done != nil {
			select {
			case <-done:
				return nil
			default:
			}
		}

		n, err := syscall.Read(r.fd, tmp)
		if n > 0 {
			chunk := r.observe(tmp[:n])
			if _, writeErr := io.WriteString(output, chunk); writeErr != nil {
				return nil
			}
			if quietFor > 0 {
				quietDeadline = time.Now().Add(quietFor)
			}
		}
		if quietFor > 0 && !quietDeadline.IsZero() && time.Now().After(quietDeadline) {
			return nil
		}
		if err != nil && !isRetryableRead(err) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (r *PTYRunner) ctrlCFollow(output io.Writer, quietFor time.Duration, done <-chan struct{}) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, err := r.file.Write([]byte{3}); err != nil {
		return err
	}

	return r.follow(output, quietFor, done)
}

func trimOutputBoundary(output string) string {
	output = strings.TrimRight(output, "\n")
	for strings.HasPrefix(output, "\n") {
		output = strings.TrimPrefix(output, "\n")
	}
	return output
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
	if r.command != nil && r.command.Process != nil {
		_ = r.command.Process.Kill()
		_, _ = r.command.Process.Wait()
	}
	return nil
}

func (r *PTYRunner) drain(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	buf := make([]byte, 1024)
	for {
		n, err := syscall.Read(r.fd, buf)
		if n > 0 {
			_ = r.observe(buf[:n])
		}
		if err != nil {
			if isRetryableRead(err) && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			return err
		}
	}
}

func isRetryableRead(err error) bool {
	return errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, os.ErrDeadlineExceeded)
}

func (r *PTYRunner) observe(data []byte) string {
	if r.term != nil {
		_, _ = r.term.Write(data)
	}
	return cleanTerminalNoise(string(data))
}

func (r *PTYRunner) currentLine() string {
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
		if strings.Contains(line, "__ptymux_status=$?") ||
			strings.Contains(line, "__ptymux_token_a=") ||
			strings.Contains(line, "__ptymux_token_b=") ||
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
