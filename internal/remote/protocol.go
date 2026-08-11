package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"ptymux/internal/server"
)

const (
	ProtocolVersion   = 1
	MaxRequestIDBytes = 128
)

const (
	ErrorAuthenticationFailed  = "authentication_failed"
	ErrorBadRequest            = "bad_request"
	ErrorClientNameUnavailable = "client_name_unavailable"
	ErrorConnectionLimit       = "connection_limit"
	ErrorInternal              = "internal_error"
	ErrorInvalidOperation      = "invalid_operation"
	ErrorOriginNotAllowed      = "origin_not_allowed"
	ErrorRateLimited           = "rate_limited"
	ErrorServerShuttingDown    = "server_shutting_down"
	ErrorSlowConnection        = "slow_connection"
	ErrorTargetLimit           = "target_limit"
	ErrorTargetNotFound        = "target_not_found"
)

type ProtocolError struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

func (e *ProtocolError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

type Operation string

const (
	OperationCreate Operation = "create"
	OperationList   Operation = "list"
	OperationSend   Operation = "send"
	OperationText   Operation = "text"
	OperationKeys   Operation = "keys"
	OperationRead   Operation = "read"
	OperationFollow Operation = "follow"
	OperationClose  Operation = "close"
)

type ResponseType string

const (
	ResponseTypeResponse      ResponseType = "response"
	ResponseTypeFollowStarted ResponseType = "follow_started"
	ResponseTypeFollowEnded   ResponseType = "follow_ended"
)

type Target struct {
	Session string `json:"session,omitempty"`
	Pane    string `json:"pane,omitempty"`
	Tab     string `json:"tab,omitempty"`
}

type Request struct {
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	Operation Operation `json:"operation"`
	Target    Target    `json:"target,omitempty"`
	Input     string    `json:"input,omitempty"`
	ReadCount int       `json:"read_count,omitempty"`
}

type Response struct {
	Version  int             `json:"version"`
	Type     ResponseType    `json:"type"`
	ID       string          `json:"id,omitempty"`
	Output   string          `json:"output,omitempty"`
	ExitCode int             `json:"exit_code,omitempty"`
	Snapshot server.Snapshot `json:"snapshot,omitempty"`
	Error    *ProtocolError  `json:"error,omitempty"`
}

type RegisterRequest struct {
	Name string `json:"name"`
}

type Registration struct {
	Version  int          `json:"version"`
	Client   ClientRecord `json:"client"`
	Password string       `json:"password"`
}

type Rotation struct {
	Version   int       `json:"version"`
	Principal Principal `json:"principal"`
	Password  string    `json:"password"`
}

type ManagementResponse struct {
	Version int            `json:"version"`
	Error   *ProtocolError `json:"error,omitempty"`
}

func validRequestID(id string) bool {
	return id != "" && utf8.ValidString(id) && len(id) <= MaxRequestIDBytes
}

func validateRequest(request Request) *ProtocolError {
	if request.Version != ProtocolVersion {
		return &ProtocolError{Code: ErrorBadRequest, Message: "unsupported protocol version"}
	}
	if !validRequestID(request.ID) {
		return &ProtocolError{Code: ErrorBadRequest, Message: "request ID must be valid UTF-8 and at most 128 bytes"}
	}
	if !utf8.ValidString(string(request.Operation)) || len(request.Operation) > server.MaxActionBytes {
		return &ProtocolError{Code: ErrorInvalidOperation}
	}
	if err := server.ValidateCommand(request.Input); err != nil {
		return &ProtocolError{Code: ErrorBadRequest, Message: "input exceeds the allowed limit or is invalid"}
	}
	if err := server.ValidateReadCount(request.ReadCount); err != nil {
		return &ProtocolError{Code: ErrorBadRequest, Message: "read count is outside the allowed range"}
	}

	var targetErr error
	switch request.Operation {
	case OperationList:
		targetErr = server.ValidateTargetPrefix(request.Target.Session, request.Target.Pane, request.Target.Tab)
	case OperationCreate, OperationSend, OperationText, OperationKeys, OperationRead, OperationFollow, OperationClose:
		targetErr = server.ValidateCompleteTarget(request.Target.Session, request.Target.Pane, request.Target.Tab)
	default:
		return &ProtocolError{Code: ErrorInvalidOperation}
	}
	if targetErr != nil {
		return &ProtocolError{Code: ErrorBadRequest, Message: "invalid target"}
	}
	return nil
}

func decodeStrictJSON(reader io.Reader, value interface{}) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("remote: trailing JSON data")
		}
		return fmt.Errorf("remote: decode trailing JSON: %w", err)
	}
	return nil
}
