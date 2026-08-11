package server

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

var (
	ErrTargetNotFound  = errors.New("target not found")
	ErrServiceStopping = errors.New("service is stopping")
)

type Service struct {
	store         *Store
	runnerFactory func() (Runner, error)

	lifecycleMu sync.Mutex
	stopping    bool
	operations  sync.WaitGroup
}

func NewService(shell string) *Service {
	return newServiceWithRunnerFactory(func() (Runner, error) {
		return NewPTYRunner(shell)
	})
}

func newServiceWithRunner(factory func() Runner) *Service {
	return newServiceWithRunnerFactory(func() (Runner, error) {
		return factory(), nil
	})
}

func newServiceWithRunnerFactory(factory func() (Runner, error)) *Service {
	return &Service{store: NewStore(), runnerFactory: factory}
}

func (s *Service) Store() *Store {
	return s.store
}

func (s *Service) Create(req Request) Response {
	done, err := s.beginOperation()
	if err != nil {
		return Response{Error: err.Error()}
	}
	defer done()
	if err := ValidateCompleteTarget(req.Session, req.Pane, req.Tab); err != nil {
		return Response{Error: err.Error()}
	}
	return s.create(req)
}

func (s *Service) create(req Request) Response {
	tab, done, err := s.beginTargetUse(req, true)
	if err != nil {
		return Response{Error: err.Error()}
	}
	if tab == nil {
		return Response{Error: ErrTargetNotFound.Error()}
	}
	done()
	return Response{}
}

func (s *Service) Handle(req Request, createMissing bool) Response {
	return s.HandleWithDone(req, createMissing, nil)
}

func (s *Service) HandleWithDone(req Request, createMissing bool, clientDone <-chan struct{}) Response {
	operationDone, err := s.beginOperation()
	if err != nil {
		return Response{Error: err.Error()}
	}
	defer operationDone()
	if err := ValidateRequest(req); err != nil {
		return Response{Error: err.Error()}
	}

	switch req.Action {
	case "create":
		return s.create(req)
	case "run":
		tab, finish, err := s.beginTargetUse(req, createMissing)
		if err != nil {
			return Response{Error: err.Error()}
		}
		if tab == nil {
			return Response{Error: ErrTargetNotFound.Error()}
		}
		defer finish()
		result, err := runWithDone(tab.Runner, req.Command, clientDone)
		if err != nil {
			return Response{Error: err.Error()}
		}
		return Response{Output: result.Output, ExitCode: result.ExitCode, Truncated: result.Truncated}
	case "idle":
		tab, finish, err := s.beginTargetUse(req, createMissing)
		if err != nil {
			return Response{Error: err.Error()}
		}
		if tab == nil {
			return Response{Error: ErrTargetNotFound.Error()}
		}
		defer finish()
		wait := time.Duration(req.WaitMillis) * time.Millisecond
		if wait <= 0 {
			wait = 500 * time.Millisecond
		}
		result, err := tab.Runner.SendWait(req.Command, wait)
		if err != nil {
			return Response{Error: err.Error()}
		}
		return Response{Output: result.Output, ExitCode: result.ExitCode, Truncated: result.Truncated}
	case "send":
		tab, finish, err := s.beginTargetUse(req, createMissing)
		if err != nil {
			return Response{Error: err.Error()}
		}
		if tab == nil {
			return Response{Error: ErrTargetNotFound.Error()}
		}
		defer finish()
		if req.WaitMillis > 0 {
			result, err := tab.Runner.SendWait(req.Command, time.Duration(req.WaitMillis)*time.Millisecond)
			if err != nil {
				return Response{Error: err.Error()}
			}
			return Response{Output: result.Output, ExitCode: result.ExitCode, Truncated: result.Truncated}
		}
		if err := tab.Runner.Send(req.Command); err != nil {
			return Response{Error: err.Error()}
		}
		return Response{}
	case "text":
		tab, finish, err := s.beginTargetUse(req, createMissing)
		if err != nil {
			return Response{Error: err.Error()}
		}
		if tab == nil {
			return Response{Error: ErrTargetNotFound.Error()}
		}
		defer finish()
		if err := tab.Runner.Text(req.Command); err != nil {
			return Response{Error: err.Error()}
		}
		return Response{}
	case "command":
		tab, finish, err := s.beginTargetUse(req, createMissing)
		if err != nil {
			return Response{Error: err.Error()}
		}
		if tab == nil {
			return Response{Error: ErrTargetNotFound.Error()}
		}
		defer finish()
		if req.WaitMillis > 0 {
			result, err := tab.Runner.CommandWait(req.Command, time.Duration(req.WaitMillis)*time.Millisecond)
			if err != nil {
				return Response{Error: err.Error()}
			}
			return Response{Output: result.Output, ExitCode: result.ExitCode, Truncated: result.Truncated}
		}
		if err := tab.Runner.Command(req.Command); err != nil {
			return Response{Error: err.Error()}
		}
		return Response{}
	case "keys":
		tab, finish, err := s.beginTargetUse(req, createMissing)
		if err != nil {
			return Response{Error: err.Error()}
		}
		if tab == nil {
			return Response{Error: ErrTargetNotFound.Error()}
		}
		defer finish()
		if req.WaitMillis > 0 {
			result, err := tab.Runner.KeysWait(req.Command, time.Duration(req.WaitMillis)*time.Millisecond)
			if err != nil {
				return Response{Error: err.Error()}
			}
			return Response{Output: result.Output, ExitCode: result.ExitCode, Truncated: result.Truncated}
		}
		if err := tab.Runner.Keys(req.Command); err != nil {
			return Response{Error: err.Error()}
		}
		return Response{}
	case "read":
		tab, finish, err := s.beginTargetUse(req, createMissing)
		if err != nil {
			return Response{Error: err.Error()}
		}
		if tab == nil {
			return Response{Error: ErrTargetNotFound.Error()}
		}
		defer finish()
		result, err := tab.Runner.Read(req.ReadCount)
		if err != nil {
			return Response{Error: err.Error()}
		}
		return Response{Output: result.Output, ExitCode: result.ExitCode, Truncated: result.Truncated}
	case "list":
		if req.Session != "" && req.Pane != "" && req.Tab != "" {
			s.store.TouchTarget(req.Session, req.Pane, req.Tab)
		}
		return Response{Snapshot: s.store.SnapshotTarget(req.Session, req.Pane, req.Tab)}
	case "kill", "close":
		var err error
		if req.Session == "" {
			err = s.store.CloseAll()
		} else {
			err = s.store.CloseTarget(req.Session, req.Pane, req.Tab)
		}
		if err != nil {
			return Response{Error: err.Error()}
		}
		return Response{}
	default:
		return Response{Error: fmt.Sprintf("unknown action %q", req.Action)}
	}
}

func ValidateStreamRequest(req Request) error {
	if err := ValidateRequest(req); err != nil {
		return err
	}
	switch req.Action {
	case "command":
		_, err := parseKeySequence(req.Command)
		return err
	case "keys":
		_, err := parseKeySequenceNoEnter(req.Command)
		return err
	default:
		return nil
	}
}

func (s *Service) Stream(req Request, output io.Writer, done <-chan struct{}, createMissing bool) error {
	finishOperation, err := s.beginOperation()
	if err != nil {
		return err
	}
	defer finishOperation()
	if err := ValidateStreamRequest(req); err != nil {
		return err
	}

	tab, finish, err := s.beginTargetUse(req, createMissing)
	if err != nil {
		return err
	}
	if tab == nil {
		return ErrTargetNotFound
	}
	defer finish()

	switch req.Action {
	case "ctrl-c":
		return tab.Runner.CtrlCFollow(output, done)
	case "follow":
		return tab.Runner.Follow(output, done)
	case "command":
		return tab.Runner.CommandFollow(req.Command, output, done)
	case "keys":
		return tab.Runner.KeysFollow(req.Command, output, done)
	case "send":
		return tab.Runner.SendFollow(req.Command, output, done)
	default:
		return fmt.Errorf("action %q is not streaming", req.Action)
	}
}

func (s *Service) CloseExisting(req Request) Response {
	done, err := s.beginOperation()
	if err != nil {
		return Response{Error: err.Error()}
	}
	defer done()
	if err := ValidateCompleteTarget(req.Session, req.Pane, req.Tab); err != nil {
		return Response{Error: err.Error()}
	}
	found, err := s.store.CloseExistingTarget(req.Session, req.Pane, req.Tab)
	if err != nil {
		return Response{Error: err.Error()}
	}
	if !found {
		return Response{Error: ErrTargetNotFound.Error()}
	}
	return Response{}
}

func (s *Service) CloseAll() error {
	return s.store.CloseAll()
}

func (s *Service) StopAdmission() {
	s.lifecycleMu.Lock()
	s.stopping = true
	s.lifecycleMu.Unlock()
}

func (s *Service) WaitOperations() {
	s.operations.Wait()
}

func (s *Service) CloseIdleTargets(now time.Time, idleFor time.Duration) error {
	return s.store.CloseIdleTargets(now, idleFor)
}

func (s *Service) Empty() bool {
	return s.store.Empty()
}

func (s *Service) TargetExists(session, pane, tab string) bool {
	return s.store.targetExists(session, pane, tab)
}

func (s *Service) TargetCount() int {
	return s.store.targetCount()
}

func (s *Service) beginOperation() (func(), error) {
	s.lifecycleMu.Lock()
	if s.stopping {
		s.lifecycleMu.Unlock()
		return nil, ErrServiceStopping
	}
	s.operations.Add(1)
	s.lifecycleMu.Unlock()
	return s.operations.Done, nil
}

type runnerWithCancellation interface {
	RunWithDone(command string, done <-chan struct{}) (RunResult, error)
}

func runWithDone(runner Runner, command string, done <-chan struct{}) (RunResult, error) {
	if cancellable, ok := runner.(runnerWithCancellation); ok {
		return cancellable.RunWithDone(command, done)
	}
	return runner.Run(command)
}

func (s *Service) beginTargetUse(req Request, createMissing bool) (*Tab, func(), error) {
	if createMissing {
		s.lifecycleMu.Lock()
		if s.stopping {
			s.lifecycleMu.Unlock()
			return nil, nil, ErrServiceStopping
		}
		tab, done, err := s.store.BeginUseWithError(req.Session, req.Pane, req.Tab, s.runnerFactory)
		s.lifecycleMu.Unlock()
		return tab, done, err
	}
	tab, done := s.store.BeginExistingUse(req.Session, req.Pane, req.Tab)
	return tab, done, nil
}
