// Package pty provides a narrow, transport-independent PTY target service.
package pty

import (
	"context"
	"errors"
	"fmt"
	"io"

	"ptymux/internal/server"
)

var (
	ErrInvalidRequest  = server.ErrInvalidRequest
	ErrTargetNotFound  = server.ErrTargetNotFound
	ErrServiceStopping = server.ErrServiceStopping
)

// Target identifies a PTY target. Empty trailing components form a prefix for
// Targets; all other operations require a complete target.
type Target struct {
	Session string
	Pane    string
	Tab     string
}

// Result is the output returned by a PTY operation.
type Result struct {
	Output    string
	ExitCode  int
	Truncated bool
}

// Service owns an isolated set of PTY targets.
type Service struct {
	service *server.Service
}

// New creates a service whose targets run shell.
func New(shell string) *Service {
	return &Service{service: server.NewService(shell)}
}

// Create creates target if it does not already exist.
func (s *Service) Create(target Target) error {
	return responseError(s.service.Create(request(target, "", "", 0)))
}

// Exists reports whether a complete target exists.
func (s *Service) Exists(target Target) bool {
	return s.service.TargetExists(target.Session, target.Pane, target.Tab)
}

// Targets lists targets below an optional prefix. Passing no prefix lists all
// complete targets. At most one prefix may be supplied.
func (s *Service) Targets(prefix ...Target) ([]Target, error) {
	if len(prefix) > 1 {
		return nil, fmt.Errorf("%w: at most one target prefix is allowed", ErrInvalidRequest)
	}
	var target Target
	if len(prefix) == 1 {
		target = prefix[0]
	}
	resp := s.service.Handle(request(target, "list", "", 0), false)
	if err := responseError(resp); err != nil {
		return nil, err
	}
	return flatten(resp.Snapshot), nil
}

// Send writes input to an existing target.
func (s *Service) Send(target Target, input string) error {
	return responseError(s.service.Handle(request(target, "send", input, 0), false))
}

// Text types literal text into an existing target.
func (s *Service) Text(target Target, input string) error {
	return responseError(s.service.Handle(request(target, "text", input, 0), false))
}

// Keys sends a terminal key sequence to an existing target.
func (s *Service) Keys(target Target, keys string) error {
	return responseError(s.service.Handle(request(target, "keys", keys, 0), false))
}

// Read renders the current screen or recent transcript lines.
func (s *Service) Read(target Target, count int) (Result, error) {
	resp := s.service.Handle(request(target, "read", "", count), false)
	if err := responseError(resp); err != nil {
		return Result{}, err
	}
	return Result{Output: resp.Output, ExitCode: resp.ExitCode, Truncated: resp.Truncated}, nil
}

// Follow forwards future raw PTY output until ctx is canceled or the target
// closes.
func (s *Service) Follow(ctx context.Context, target Target, output io.Writer) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ErrInvalidRequest)
	}
	return s.service.Stream(request(target, "follow", "", 0), output, ctx.Done(), false)
}

// Close closes an existing target.
func (s *Service) Close(target Target) error {
	return responseError(s.service.CloseExisting(request(target, "", "", 0)))
}

// StopAdmission prevents new operations from starting.
func (s *Service) StopAdmission() {
	s.service.StopAdmission()
}

// Wait waits for admitted operations to finish.
func (s *Service) Wait() {
	s.service.WaitOperations()
}

// CloseAll closes all targets without changing admission state.
func (s *Service) CloseAll() error {
	return s.service.CloseAll()
}

func request(target Target, action, input string, readCount int) server.Request {
	return server.Request{
		Action:    action,
		Session:   target.Session,
		Pane:      target.Pane,
		Tab:       target.Tab,
		Command:   input,
		ReadCount: readCount,
	}
}

func responseError(resp server.Response) error {
	if resp.Err != nil {
		return resp.Err
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	return nil
}

func flatten(snapshot server.Snapshot) []Target {
	var targets []Target
	for _, session := range snapshot.Sessions {
		if len(session.Panes) == 0 {
			targets = append(targets, Target{Session: session.Name})
			continue
		}
		for _, pane := range session.Panes {
			if len(pane.Tabs) == 0 {
				targets = append(targets, Target{Session: session.Name, Pane: pane.Name})
				continue
			}
			for _, tab := range pane.Tabs {
				targets = append(targets, Target{Session: session.Name, Pane: pane.Name, Tab: tab.Name})
			}
		}
	}
	return targets
}
