package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

type Daemon struct {
	store    *Store
	shell    string
	listener net.Listener
	stopOnce sync.Once
	stopped  chan struct{}
}

func NewDaemon(shell string) *Daemon {
	return &Daemon{store: NewStore(), shell: shell, stopped: make(chan struct{})}
}

func (d *Daemon) Serve(socketPath string) error {
	if socketPath == "" {
		return errors.New("missing socket path")
	}
	_ = os.Remove(socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	d.listener = listener
	defer listener.Close()
	defer os.Remove(socketPath)
	defer d.store.CloseAll()

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

func (d *Daemon) handle(conn net.Conn) {
	defer conn.Close()

	var req Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(Response{Error: err.Error()})
		return
	}
	if req.Action == "follow" || req.Action == "ctrl-c" || (req.Action == "send" && req.Follow) || (req.Action == "command" && req.Follow) {
		if req.Action == "command" {
			if _, err := parseKeySequence(req.Command); err != nil {
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
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, conn)
		close(done)
	}()

	tab := d.store.GetOrCreate(req.Session, req.Pane, req.Tab, func() Runner {
		runner, err := NewPTYRunner(d.shell)
		if err != nil {
			return &errorRunner{err: err}
		}
		return runner
	})
	var err error
	switch req.Action {
	case "ctrl-c":
		err = tab.Runner.CtrlCFollow(conn, done)
	case "follow":
		err = tab.Runner.Follow(conn, done)
	case "command":
		err = tab.Runner.CommandFollow(req.Command, conn, done)
	default:
		err = tab.Runner.SendFollow(req.Command, conn, done)
	}
	if err != nil {
		_, _ = io.WriteString(conn, err.Error()+"\n")
	}
}

func (d *Daemon) Handle(req Request) Response {
	switch req.Action {
	case "run":
		tab := d.store.GetOrCreate(req.Session, req.Pane, req.Tab, func() Runner {
			runner, err := NewPTYRunner(d.shell)
			if err != nil {
				return &errorRunner{err: err}
			}
			return runner
		})
		result, err := tab.Runner.Run(req.Command)
		if err != nil {
			return Response{Error: err.Error()}
		}
		return Response{Output: result.Output, ExitCode: result.ExitCode}
	case "idle":
		tab := d.store.GetOrCreate(req.Session, req.Pane, req.Tab, func() Runner {
			runner, err := NewPTYRunner(d.shell)
			if err != nil {
				return &errorRunner{err: err}
			}
			return runner
		})
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
		tab := d.store.GetOrCreate(req.Session, req.Pane, req.Tab, func() Runner {
			runner, err := NewPTYRunner(d.shell)
			if err != nil {
				return &errorRunner{err: err}
			}
			return runner
		})
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
	case "command":
		tab := d.store.GetOrCreate(req.Session, req.Pane, req.Tab, func() Runner {
			runner, err := NewPTYRunner(d.shell)
			if err != nil {
				return &errorRunner{err: err}
			}
			return runner
		})
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
	case "read":
		tab := d.store.GetOrCreate(req.Session, req.Pane, req.Tab, func() Runner {
			runner, err := NewPTYRunner(d.shell)
			if err != nil {
				return &errorRunner{err: err}
			}
			return runner
		})
		result, err := tab.Runner.Read(req.ReadCount)
		if err != nil {
			return Response{Error: err.Error()}
		}
		return Response{Output: result.Output, ExitCode: result.ExitCode}
	case "list":
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
func (r *errorRunner) Command(string) error { return r.err }
func (r *errorRunner) CommandWait(string, time.Duration) (RunResult, error) {
	return RunResult{}, r.err
}
func (r *errorRunner) CommandFollow(string, io.Writer, <-chan struct{}) error {
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
