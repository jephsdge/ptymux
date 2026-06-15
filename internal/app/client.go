package app

import (
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

func Run(cfg Config) (server.Response, error) {
	socketPath := cfg.Socket
	if socketPath == "" {
		socketPath = DefaultSocketPath()
	}

	if cfg.Action == ActionDaemon {
		userConfig, err := LoadUserConfig()
		if err != nil {
			return server.Response{}, err
		}
		return server.Response{}, server.NewDaemonWithOptions("", server.DaemonOptions{
			AutoRelease: userConfig.AutoRelease,
		}).Serve(socketPath)
	}

	req := server.Request{
		Action:     string(cfg.Action),
		Session:    cfg.Session,
		Pane:       cfg.Pane,
		Tab:        cfg.Tab,
		Command:    cfg.Command,
		Follow:     cfg.Follow,
		WaitMillis: int64(cfg.Wait / time.Millisecond),
		ReadCount:  cfg.ReadCount,
	}

	if cfg.Action == ActionFollow || cfg.Action == ActionCtrlC || (cfg.Action == ActionSend && cfg.Follow) || (cfg.Action == ActionCommand && cfg.Follow) {
		if err := streamSend(socketPath, req, os.Stdout); err != nil {
			if startErr := startDaemon(socketPath); startErr != nil {
				return server.Response{}, fmt.Errorf("%v; also failed to start daemon: %w", err, startErr)
			}
			if err := streamSend(socketPath, req, os.Stdout); err != nil {
				return server.Response{}, err
			}
		}
		return server.Response{}, nil
	}

	resp, err := send(socketPath, req)
	if err != nil && cfg.Action != ActionStop {
		if startErr := startDaemon(socketPath); startErr != nil {
			return server.Response{}, fmt.Errorf("%v; also failed to start daemon: %w", err, startErr)
		}
		resp, err = send(socketPath, req)
	}
	if err != nil {
		return server.Response{}, err
	}
	if resp.Error != "" {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

func streamSend(socketPath string, req server.Request, output io.Writer) error {
	conn, err := net.DialTimeout("unix", socketPath, time.Second)
	if err != nil {
		return fmt.Errorf("connect daemon at %s: %w", socketPath, err)
	}
	defer conn.Close()

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
	conn, err := net.DialTimeout("unix", socketPath, time.Second)
	if err != nil {
		return server.Response{}, fmt.Errorf("connect daemon at %s: %w", socketPath, err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return server.Response{}, err
	}

	var resp server.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return server.Response{}, err
	}
	return resp, nil
}
