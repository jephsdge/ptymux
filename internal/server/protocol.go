package server

import (
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"
)

const (
	MaxTargetComponentBytes = 64
	MaxActionBytes          = 32
	MaxCommandBytes         = 128 << 10
	MaxReadCount            = 4096
	MaxLocalRequestBytes    = 1 << 20
	MaxRunOutputBytes       = 8 << 20
)

var ErrInvalidRequest = errors.New("invalid request")

type Request struct {
	Action        string `json:"action"`
	Session       string `json:"session,omitempty"`
	Pane          string `json:"pane,omitempty"`
	Tab           string `json:"tab,omitempty"`
	Command       string `json:"command,omitempty"`
	Follow        bool   `json:"follow,omitempty"`
	WaitMillis    int64  `json:"wait_millis,omitempty"`
	ReadCount     int    `json:"read_count,omitempty"`
	StreamVersion int    `json:"stream_version,omitempty"`
	StreamAction  string `json:"stream_action,omitempty"`
}

type Response struct {
	Output    string   `json:"output,omitempty"`
	ExitCode  int      `json:"exit_code,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	Snapshot  Snapshot `json:"snapshot,omitempty"`
	Error     string   `json:"error,omitempty"`
}

type RunResult struct {
	Output    string
	ExitCode  int
	Truncated bool
}

func ValidateAction(action string) error {
	if action == "" {
		return fmt.Errorf("%w: action is required", ErrInvalidRequest)
	}
	if !utf8.ValidString(action) || len(action) > MaxActionBytes {
		return fmt.Errorf("%w: action must be valid UTF-8 and at most %d bytes", ErrInvalidRequest, MaxActionBytes)
	}
	return nil
}

func ValidateTargetComponent(field, value string) error {
	if value == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidRequest, field)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s must be valid UTF-8", ErrInvalidRequest, field)
	}
	if len(value) > MaxTargetComponentBytes {
		return fmt.Errorf("%w: %s must be at most %d bytes", ErrInvalidRequest, field, MaxTargetComponentBytes)
	}
	for _, r := range value {
		if r == '/' || r == 0 || unicode.IsControl(r) {
			return fmt.Errorf("%w: %s contains a reserved character", ErrInvalidRequest, field)
		}
	}
	return nil
}

func ValidateCompleteTarget(session, pane, tab string) error {
	if err := ValidateTargetComponent("session", session); err != nil {
		return err
	}
	if err := ValidateTargetComponent("pane", pane); err != nil {
		return err
	}
	return ValidateTargetComponent("tab", tab)
}

func ValidateTargetPrefix(session, pane, tab string) error {
	if session == "" {
		if pane != "" || tab != "" {
			return fmt.Errorf("%w: target prefix requires a session", ErrInvalidRequest)
		}
		return nil
	}
	if err := ValidateTargetComponent("session", session); err != nil {
		return err
	}
	if pane == "" {
		if tab != "" {
			return fmt.Errorf("%w: target prefix requires a pane", ErrInvalidRequest)
		}
		return nil
	}
	if err := ValidateTargetComponent("pane", pane); err != nil {
		return err
	}
	if tab == "" {
		return nil
	}
	return ValidateTargetComponent("tab", tab)
}

func ValidateCommand(command string) error {
	if !utf8.ValidString(command) {
		return fmt.Errorf("%w: input must be valid UTF-8", ErrInvalidRequest)
	}
	if len(command) > MaxCommandBytes {
		return fmt.Errorf("%w: input must be at most %d bytes", ErrInvalidRequest, MaxCommandBytes)
	}
	return nil
}

func ValidateReadCount(count int) error {
	if count < 0 || count > MaxReadCount {
		return fmt.Errorf("%w: read count must be between 0 and %d", ErrInvalidRequest, MaxReadCount)
	}
	return nil
}

func IsStreamRequest(req Request) bool {
	return req.Action == "follow" || req.Action == "ctrl-c" ||
		(req.Action == "send" && req.Follow) ||
		(req.Action == "command" && req.Follow) ||
		(req.Action == "keys" && req.Follow)
}

func ValidateRequest(req Request) error {
	if err := ValidateAction(req.Action); err != nil {
		return err
	}
	if err := ValidateCommand(req.Command); err != nil {
		return err
	}
	if err := ValidateReadCount(req.ReadCount); err != nil {
		return err
	}
	if req.WaitMillis < 0 {
		return fmt.Errorf("%w: wait duration must not be negative", ErrInvalidRequest)
	}

	switch req.Action {
	case "daemon", "stop":
		return nil
	case "list":
		return ValidateTargetPrefix(req.Session, req.Pane, req.Tab)
	case "kill":
		if req.Session == "" && req.Pane == "" && req.Tab == "" {
			return nil
		}
		return ValidateCompleteTarget(req.Session, req.Pane, req.Tab)
	case "close", "create", "run", "idle", "send", "text", "command", "keys", "read", "follow", "ctrl-c":
		return ValidateCompleteTarget(req.Session, req.Pane, req.Tab)
	default:
		return nil
	}
}
