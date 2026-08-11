package server

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const socketProbeTimeout = 200 * time.Millisecond

func prepareSocketPath(socketPath string) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(socketPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect socket path: %w", err)
	}
	if err := validateOwnedSocket(info); err != nil {
		return fmt.Errorf("refusing to replace %s: %w", socketPath, err)
	}

	conn, dialErr := net.DialTimeout("unix", socketPath, socketProbeTimeout)
	if dialErr == nil {
		_ = conn.Close()
		return fmt.Errorf("daemon is already listening at %s", socketPath)
	}
	if !errors.Is(dialErr, syscall.ECONNREFUSED) {
		return fmt.Errorf("cannot prove socket at %s is stale: %w", socketPath, dialErr)
	}

	current, err := os.Lstat(socketPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reinspect socket path: %w", err)
	}
	if err := validateOwnedSocket(current); err != nil {
		return fmt.Errorf("refusing to replace %s: %w", socketPath, err)
	}
	if !os.SameFile(info, current) {
		return fmt.Errorf("refusing to replace changed socket path %s", socketPath)
	}
	if err := os.Remove(socketPath); err != nil {
		return fmt.Errorf("remove stale socket %s: %w", socketPath, err)
	}
	return nil
}

func validateOwnedSocket(info os.FileInfo) error {
	if info.Mode()&os.ModeSocket == 0 {
		return errors.New("path is not a Unix socket")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("socket ownership is unavailable")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return errors.New("socket is not owned by the current user")
	}
	return nil
}

func cleanupSocketPath(socketPath string, created os.FileInfo) error {
	current, err := os.Lstat(socketPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect socket during cleanup: %w", err)
	}
	if validateOwnedSocket(current) != nil || !os.SameFile(created, current) {
		return nil
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove daemon socket: %w", err)
	}
	return nil
}
