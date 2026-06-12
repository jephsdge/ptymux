package server

import (
	"encoding/json"
	"errors"
	"fmt"
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

	resp := d.Handle(req)
	_ = json.NewEncoder(conn).Encode(resp)
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
	case "list":
		return Response{Snapshot: d.store.SnapshotTarget(req.Session, req.Pane, req.Tab)}
	case "kill":
		if err := d.store.CloseAll(); err != nil {
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
func (r *errorRunner) Close() error                  { return nil }
