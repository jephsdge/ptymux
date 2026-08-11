package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"ptymux/internal/server"
)

func DefaultSocketPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "ptymux-default.sock")
	}
	return filepath.Join(home, ".ptymux", "sockets", "ptymux-default.sock")
}

func serverRequestFromConfig(cfg Config) server.Request {
	return server.Request{
		Action:     string(cfg.Action),
		Session:    cfg.Session,
		Pane:       cfg.Pane,
		Tab:        cfg.Tab,
		Command:    cfg.Command,
		Follow:     cfg.Follow,
		WaitMillis: int64(cfg.Wait / time.Millisecond),
		ReadCount:  cfg.ReadCount,
	}
}

func runLocal(cfg Config) (server.Response, error) {
	return runLocalContext(context.Background(), cfg)
}

func runLocalContext(ctx context.Context, cfg Config) (server.Response, error) {
	socketPath := cfg.Socket
	if socketPath == "" {
		socketPath = DefaultSocketPath()
	}

	if cfg.Action == ActionDaemon {
		userConfig, err := LoadUserConfig()
		if err != nil {
			return server.Response{}, err
		}
		return server.Response{}, server.NewDaemonWithOptions(userConfig.Shell, server.DaemonOptions{
			AutoRelease: userConfig.AutoRelease,
		}).Serve(socketPath)
	}

	req := serverRequestFromConfig(cfg)

	if server.IsStreamRequest(req) {
		if err := streamSendContext(ctx, socketPath, req, os.Stdout); err != nil {
			var connectErr *daemonConnectError
			if !errors.As(err, &connectErr) || ctx.Err() != nil {
				return server.Response{}, err
			}
			if startErr := startDaemon(socketPath); startErr != nil {
				return server.Response{}, fmt.Errorf("%v; also failed to start daemon: %w", err, startErr)
			}
			if err := streamSendContext(ctx, socketPath, req, os.Stdout); err != nil {
				return server.Response{}, err
			}
		}
		return server.Response{}, nil
	}

	resp, err := sendContext(ctx, socketPath, req)
	var connectErr *daemonConnectError
	if err != nil && cfg.Action != ActionStop && errors.As(err, &connectErr) && ctx.Err() == nil {
		if startErr := startDaemon(socketPath); startErr != nil {
			return server.Response{}, fmt.Errorf("%v; also failed to start daemon: %w", err, startErr)
		}
		resp, err = sendContext(ctx, socketPath, req)
	}
	if err != nil {
		return server.Response{}, err
	}
	if resp.Error != "" {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

type daemonConnectError struct {
	socketPath string
	err        error
}

func (e *daemonConnectError) Error() string {
	return fmt.Sprintf("connect daemon at %s: %v", e.socketPath, e.err)
}

func (e *daemonConnectError) Unwrap() error {
	return e.err
}

const localStreamNegotiationTimeout = 5 * time.Second

func streamSend(socketPath string, req server.Request, output io.Writer) error {
	return streamSendContext(context.Background(), socketPath, req, output)
}

func streamSendContext(ctx context.Context, socketPath string, req server.Request, output io.Writer) error {
	return streamSendWithNegotiationTimeoutContext(ctx, socketPath, req, output, localStreamNegotiationTimeout)
}

func streamSendWithNegotiationTimeout(socketPath string, req server.Request, output io.Writer, negotiationTimeout time.Duration) error {
	return streamSendWithNegotiationTimeoutContext(context.Background(), socketPath, req, output, negotiationTimeout)
}

func streamSendWithNegotiationTimeoutContext(ctx context.Context, socketPath string, req server.Request, output io.Writer, negotiationTimeout time.Duration) (returnErr error) {
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", socketPath)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &daemonConnectError{socketPath: socketPath, err: err}
	}
	defer conn.Close()
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancel()
	defer func() {
		if ctx.Err() != nil {
			returnErr = ctx.Err()
		}
	}()
	if negotiationTimeout > 0 {
		if err := conn.SetDeadline(time.Now().Add(negotiationTimeout)); err != nil {
			return fmt.Errorf("set local stream negotiation deadline: %w", err)
		}
	}

	envelope := req
	envelope.Action = server.LocalStreamEnvelopeAction
	envelope.StreamVersion = server.LocalStreamVersion
	envelope.StreamAction = req.Action
	if err := json.NewEncoder(conn).Encode(envelope); err != nil {
		return err
	}

	reader := bufio.NewReader(conn)
	prefix, err := reader.Peek(len(server.LocalStreamMagic))
	if err != nil {
		return fmt.Errorf("read local stream acknowledgement: %w", err)
	}
	if string(prefix) != server.LocalStreamMagic {
		var response server.Response
		if err := json.NewDecoder(reader).Decode(&response); err != nil {
			return fmt.Errorf("decode local stream negotiation: %w", err)
		}
		expected := fmt.Sprintf("unknown action %q", server.LocalStreamEnvelopeAction)
		if response.Error != expected {
			if response.Error != "" {
				return errors.New(response.Error)
			}
			return errors.New("daemon did not acknowledge local stream protocol")
		}
		_ = conn.Close()
		return streamSendLegacyContext(ctx, socketPath, req, output)
	}

	started := false
	for {
		frameType, payload, err := server.ReadLocalStreamFrame(reader)
		if err != nil {
			return fmt.Errorf("read local stream frame: %w", err)
		}
		switch frameType {
		case server.LocalStreamFrameStarted:
			if started {
				return errors.New("duplicate local stream acknowledgement")
			}
			if negotiationTimeout > 0 {
				if err := conn.SetDeadline(time.Time{}); err != nil {
					return fmt.Errorf("clear local stream negotiation deadline: %w", err)
				}
			}
			started = true
		case server.LocalStreamFrameData:
			if !started {
				return errors.New("local stream data arrived before acknowledgement")
			}
			n, err := output.Write(payload)
			if err != nil {
				return err
			}
			if n != len(payload) {
				return io.ErrShortWrite
			}
		case server.LocalStreamFrameError:
			if !started {
				return errors.New("local stream error arrived before acknowledgement")
			}
			return errors.New(string(payload))
		case server.LocalStreamFrameEnd:
			if !started {
				return errors.New("local stream ended before acknowledgement")
			}
			return nil
		}
	}
}

func streamSendLegacy(socketPath string, req server.Request, output io.Writer) error {
	return streamSendLegacyContext(context.Background(), socketPath, req, output)
}

func streamSendLegacyContext(ctx context.Context, socketPath string, req server.Request, output io.Writer) (returnErr error) {
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", socketPath)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("legacy stream fallback could not connect daemon at %s: %v", socketPath, err)
	}
	defer conn.Close()
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancel()
	defer func() {
		if ctx.Err() != nil {
			returnErr = ctx.Err()
		}
	}()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return err
	}
	_, err = io.Copy(output, conn)
	return err
}

func startDaemon(socketPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}

	cmd := exec.Command(exe, "daemon", "--socket", socketPath)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", socketPath, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not create socket %s", socketPath)
}

func send(socketPath string, req server.Request) (server.Response, error) {
	return sendContext(context.Background(), socketPath, req)
}

func sendContext(ctx context.Context, socketPath string, req server.Request) (resp server.Response, returnErr error) {
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", socketPath)
	if err != nil {
		if ctx.Err() != nil {
			return server.Response{}, ctx.Err()
		}
		return server.Response{}, &daemonConnectError{socketPath: socketPath, err: err}
	}
	defer conn.Close()
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancel()
	defer func() {
		if ctx.Err() != nil {
			resp = server.Response{}
			returnErr = ctx.Err()
		}
	}()

	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return server.Response{}, err
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return server.Response{}, err
	}
	if err := conn.SetWriteDeadline(time.Time{}); err != nil {
		return server.Response{}, err
	}

	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return server.Response{}, err
	}
	return resp, nil
}
