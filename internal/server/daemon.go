package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Daemon struct {
	store        *Store
	shell        string
	listener     net.Listener
	stopOnce     sync.Once
	stopped      chan struct{}
	options      DaemonOptions
	activityMu   sync.Mutex
	lastActivity time.Time
}

type DaemonOptions struct {
	AutoRelease AutoReleaseOptions
}

type AutoReleaseOptions struct {
	Enabled           bool
	TargetIdleTimeout time.Duration
	DaemonIdleTimeout time.Duration
	SweepInterval     time.Duration
}

func NewDaemon(shell string) *Daemon {
	return NewDaemonWithOptions(shell, DaemonOptions{})
}

func NewDaemonWithOptions(shell string, options DaemonOptions) *Daemon {
	return &Daemon{
		store:        NewStore(),
		shell:        shell,
		stopped:      make(chan struct{}),
		options:      options,
		lastActivity: time.Now(),
	}
}

func (d *Daemon) Serve(socketPath string) error {
	if socketPath == "" {
		return errors.New("missing socket path")
	}
	if err := prepareSocketPath(socketPath); err != nil {
		return err
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	d.listener = listener
	defer listener.Close()
	defer os.Remove(socketPath)
	defer d.store.CloseAll()
	d.startCleanupLoop()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-d.stopped:
				return nil
			default:
			}
			return err
		}
		go d.handle(conn)
	}
}

func prepareSocketPath(socketPath string) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return err
	}
	_ = os.Remove(socketPath)
	return nil
}

func (d *Daemon) handle(conn net.Conn) {
	defer conn.Close()

	var req Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(Response{Error: err.Error()})
		return
	}
	d.markActivity()
	if req.Action == "follow" || req.Action == "ctrl-c" || (req.Action == "send" && req.Follow) || (req.Action == "command" && req.Follow) || (req.Action == "keys" && req.Follow) {
		if req.Action == "command" {
			if _, err := parseKeySequence(req.Command); err != nil {
				_ = json.NewEncoder(conn).Encode(Response{Error: err.Error()})
				return
			}
		}
		if req.Action == "keys" {
			if _, err := parseKeySequenceNoEnter(req.Command); err != nil {
				_ = json.NewEncoder(conn).Encode(Response{Error: err.Error()})
				return
			}
		}
		d.handleStream(conn, req)
		return
	}

	resp := d.Handle(req)
	_ = json.NewEncoder(conn).Encode(resp)
}

func (d *Daemon) handleStream(conn net.Conn, req Request) {
	clientDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, conn)
		close(clientDone)
	}()

	tab, finish := d.beginTargetUse(req)
	defer finish()
	var err error
	switch req.Action {
	case "ctrl-c":
		err = tab.Runner.CtrlCFollow(conn, clientDone)
	case "follow":
		err = tab.Runner.Follow(conn, clientDone)
	case "command":
		err = tab.Runner.CommandFollow(req.Command, conn, clientDone)
	case "keys":
		err = tab.Runner.KeysFollow(req.Command, conn, clientDone)
	default:
		err = tab.Runner.SendFollow(req.Command, conn, clientDone)
	}
	if err != nil {
		_, _ = io.WriteString(conn, err.Error()+"\n")
	}
}

func (d *Daemon) beginTargetUse(req Request) (*Tab, func()) {
	return d.store.BeginUse(req.Session, req.Pane, req.Tab, func() Runner {
		runner, err := NewPTYRunner(d.shell)
		if err != nil {
			return &errorRunner{err: err}
		}
		return runner
	})
}

func (d *Daemon) Handle(req Request) Response {
	d.markActivity()
	switch req.Action {
	case "run":
		tab, done := d.beginTargetUse(req)
		defer done()
		result, err := tab.Runner.Run(req.Command)
		if err != nil {
			return Response{Error: err.Error()}
		}
		return Response{Output: result.Output, ExitCode: result.ExitCode}
	case "idle":
		tab, done := d.beginTargetUse(req)
		defer done()
		wait := time.Duration(req.WaitMillis) * time.Millisecond
		if wait <= 0 {
			wait = 500 * time.Millisecond
		}
		result, err := tab.Runner.SendWait(req.Command, wait)
		if err != nil {
			return Response{Error: err.Error()}
		}
		return Response{Output: result.Output, ExitCode: result.ExitCode}
	case "send":
		tab, done := d.beginTargetUse(req)
		defer done()
		if req.WaitMillis > 0 {
			result, err := tab.Runner.SendWait(req.Command, time.Duration(req.WaitMillis)*time.Millisecond)
			if err != nil {
				return Response{Error: err.Error()}
			}
			return Response{Output: result.Output, ExitCode: result.ExitCode}
		}
		if err := tab.Runner.Send(req.Command); err != nil {
			return Response{Error: err.Error()}
		}
		return Response{}
	case "text":
		tab, done := d.beginTargetUse(req)
		defer done()
		if err := tab.Runner.Text(req.Command); err != nil {
			return Response{Error: err.Error()}
		}
		return Response{}
	case "command":
		tab, done := d.beginTargetUse(req)
		defer done()
		if req.WaitMillis > 0 {
			result, err := tab.Runner.CommandWait(req.Command, time.Duration(req.WaitMillis)*time.Millisecond)
			if err != nil {
				return Response{Error: err.Error()}
			}
			return Response{Output: result.Output, ExitCode: result.ExitCode}
		}
		if err := tab.Runner.Command(req.Command); err != nil {
			return Response{Error: err.Error()}
		}
		return Response{}
	case "keys":
		tab, done := d.beginTargetUse(req)
		defer done()
		if req.WaitMillis > 0 {
			result, err := tab.Runner.KeysWait(req.Command, time.Duration(req.WaitMillis)*time.Millisecond)
			if err != nil {
				return Response{Error: err.Error()}
			}
			return Response{Output: result.Output, ExitCode: result.ExitCode}
		}
		if err := tab.Runner.Keys(req.Command); err != nil {
			return Response{Error: err.Error()}
		}
		return Response{}
	case "read":
		tab, done := d.beginTargetUse(req)
		defer done()
		result, err := tab.Runner.Read(req.ReadCount)
		if err != nil {
			return Response{Error: err.Error()}
		}
		return Response{Output: result.Output, ExitCode: result.ExitCode}
	case "list":
		if req.Session != "" && req.Pane != "" && req.Tab != "" {
			d.store.TouchTarget(req.Session, req.Pane, req.Tab)
		}
		return Response{Snapshot: d.store.SnapshotTarget(req.Session, req.Pane, req.Tab)}
	case "kill":
		var err error
		if req.Session == "" {
			err = d.store.CloseAll()
		} else {
			err = d.store.CloseTarget(req.Session, req.Pane, req.Tab)
		}
		if err != nil {
			return Response{Error: err.Error()}
		}
		return Response{}
	case "stop":
		if err := d.store.CloseAll(); err != nil {
			return Response{Error: err.Error()}
		}
		go func() {
			time.Sleep(50 * time.Millisecond)
			d.requestStop()
		}()
		return Response{}
	default:
		return Response{Error: fmt.Sprintf("unknown action %q", req.Action)}
	}
}

func (d *Daemon) startCleanupLoop() {
	if !d.options.AutoRelease.Enabled {
		return
	}
	interval := d.options.AutoRelease.SweepInterval
	if interval <= 0 {
		interval = time.Minute
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				d.cleanupIdle(time.Now())
			case <-d.stopped:
				return
			}
		}
	}()
}

func (d *Daemon) cleanupIdle(now time.Time) {
	options := d.options.AutoRelease
	if !options.Enabled {
		return
	}
	if err := d.store.CloseIdleTargets(now, options.TargetIdleTimeout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
	}
	if options.DaemonIdleTimeout <= 0 || !d.store.Empty() {
		return
	}

	d.activityMu.Lock()
	idleFor := now.Sub(d.lastActivity)
	d.activityMu.Unlock()
	if idleFor >= options.DaemonIdleTimeout {
		d.requestStop()
	}
}

func (d *Daemon) markActivity() {
	d.activityMu.Lock()
	d.lastActivity = time.Now()
	d.activityMu.Unlock()
}

func (d *Daemon) Stopped() bool {
	select {
	case <-d.stopped:
		return true
	default:
		return false
	}
}

func (d *Daemon) requestStop() {
	d.stopOnce.Do(func() {
		close(d.stopped)
		if d.listener != nil {
			_ = d.listener.Close()
		}
	})
}

type errorRunner struct {
	err error
}

func (r *errorRunner) Run(string) (RunResult, error) { return RunResult{}, r.err }
func (r *errorRunner) RunIdle(string) (RunResult, error) {
	return RunResult{}, r.err
}
func (r *errorRunner) Send(string) error { return r.err }
func (r *errorRunner) SendWait(string, time.Duration) (RunResult, error) {
	return RunResult{}, r.err
}
func (r *errorRunner) SendFollow(string, io.Writer, <-chan struct{}) error {
	return r.err
}
func (r *errorRunner) Text(string) error    { return r.err }
func (r *errorRunner) Command(string) error { return r.err }
func (r *errorRunner) CommandWait(string, time.Duration) (RunResult, error) {
	return RunResult{}, r.err
}
func (r *errorRunner) CommandFollow(string, io.Writer, <-chan struct{}) error {
	return r.err
}
func (r *errorRunner) Keys(string) error { return r.err }
func (r *errorRunner) KeysWait(string, time.Duration) (RunResult, error) {
	return RunResult{}, r.err
}
func (r *errorRunner) KeysFollow(string, io.Writer, <-chan struct{}) error {
	return r.err
}
func (r *errorRunner) Follow(io.Writer, <-chan struct{}) error {
	return r.err
}
func (r *errorRunner) CtrlCFollow(io.Writer, <-chan struct{}) error {
	return r.err
}
func (r *errorRunner) Read(int) (RunResult, error) {
	return RunResult{}, r.err
}
func (r *errorRunner) Close() error { return nil }
