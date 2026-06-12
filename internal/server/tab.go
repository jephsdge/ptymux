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
)

type PTYRunner struct {
	mu      sync.Mutex
	file    *os.File
	fd      int
	command *exec.Cmd
	seq     uint64
}

func NewPTYRunner(shell string) (*PTYRunner, error) {
	if shell == "" {
		shell = "/bin/sh"
	}

	cmd := exec.Command(shell)
	cmd.Env = append(os.Environ(), "PS1=", "TERM=xterm-256color")

	file, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}
	fd := int(file.Fd())
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = file.Close()
		return nil, err
	}

	r := &PTYRunner{file: file, fd: fd, command: cmd}
	_, _ = io.WriteString(r.file, "stty -echo\n")
	_ = r.drain(100 * time.Millisecond)
	return r, nil
}

func (r *PTYRunner) Run(command string) (RunResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	token := fmt.Sprintf("__PTYMUX_DONE_%d_%d__", os.Getpid(), atomic.AddUint64(&r.seq, 1))
	tokenA := token[:len(token)/2]
	tokenB := token[len(token)/2:]
	wrapped := fmt.Sprintf("%s\n__ptymux_status=$?\n__ptymux_token_a=%q\n__ptymux_token_b=%q\nprintf '\\n%%s%%s:%%s\\n' \"$__ptymux_token_a\" \"$__ptymux_token_b\" \"$__ptymux_status\"\n", command, tokenA, tokenB)
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
			buf.Write(tmp[:n])
			if idx := bytes.Index(buf.Bytes(), marker); idx >= 0 {
				result := parseMarkedOutput(buf.Bytes(), idx, len(marker))
				result.Output = stripCommandEcho(result.Output, command)
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
		if _, err := syscall.Read(r.fd, buf); err != nil {
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

func parseMarkedOutput(raw []byte, markerStart, markerLen int) RunResult {
	output := string(raw[:markerStart])
	rest := string(raw[markerStart+markerLen:])
	line := rest
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	exitCode, _ := strconv.Atoi(strings.TrimSpace(line))

	output = strings.ReplaceAll(output, "\r\n", "\n")
	output = cleanTerminalNoise(output)
	lines := strings.Split(output, "\n")
	lines = dropEchoLines(lines)
	output = strings.TrimRight(strings.Join(lines, "\n"), "\n")

	return RunResult{Output: output, ExitCode: exitCode}
}

func dropEchoLines(lines []string) []string {
	out := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(line, "__ptymux_status=$?") {
			continue
		}
		if strings.HasPrefix(line, "__ptymux_token_a=") {
			continue
		}
		if strings.HasPrefix(line, "__ptymux_token_b=") {
			continue
		}
		if strings.HasPrefix(line, "printf '\\n%s%s:%s\\n'") {
			continue
		}
		out = append(out, line)
	}
	for len(out) > 0 && strings.TrimSpace(out[0]) == "" {
		out = out[1:]
	}
	return out
}

func stripCommandEcho(output, command string) string {
	output = cleanTerminalNoise(output)
	outputLines := strings.Split(output, "\n")
	commandLines := strings.Split(strings.ReplaceAll(command, "\r\n", "\n"), "\n")
	for len(outputLines) >= len(commandLines) {
		matched := true
		for i := range commandLines {
			if outputLines[i] != commandLines[i] {
				matched = false
				break
			}
		}
		if !matched {
			break
		}
		outputLines = outputLines[len(commandLines):]
	}
	return strings.TrimRight(strings.Join(outputLines, "\n"), "\n")
}

func cleanTerminalNoise(output string) string {
	output = strings.ReplaceAll(output, "\x1b[?2004h", "")
	output = strings.ReplaceAll(output, "\x1b[?2004l", "")
	output = strings.ReplaceAll(output, "\r", "")
	return output
}
