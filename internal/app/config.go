package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"ptymux/internal/remote"
	"ptymux/internal/server"
)

const (
	maxPrivateConfigSize = 1 << 20
	maxPrivateSecretSize = 64 << 10
)

type UserConfig struct {
	Shell       string
	AutoRelease server.AutoReleaseOptions
}

type rawUserConfig struct {
	Shell       string                `json:"shell"`
	AutoRelease *rawAutoReleaseConfig `json:"auto_release"`
}

type rawAutoReleaseConfig struct {
	Enabled           *bool  `json:"enabled"`
	TargetIdleTimeout string `json:"target_idle_timeout"`
	DaemonIdleTimeout string `json:"daemon_idle_timeout"`
}

type serverDefaultPaths struct {
	TokenFile      string
	ClientRegistry string
}

func LoadUserConfig() (UserConfig, error) {
	cfg := defaultUserConfig()
	path, err := ptymuxHomePath("config")
	if err != nil {
		return UserConfig{}, err
	}
	data, err := readPrivateFile(path, maxPrivateConfigSize)
	if err != nil && errors.Is(err, os.ErrNotExist) {
		path, err = ptymuxHomePath("config.json")
		if err != nil {
			return UserConfig{}, err
		}
		data, err = readRegularFile(path, maxPrivateConfigSize, false)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return UserConfig{}, err
	}

	var raw rawUserConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return UserConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	if raw.AutoRelease == nil {
		if raw.Shell != "" {
			cfg.Shell = raw.Shell
		}
		return cfg, nil
	}

	if raw.Shell != "" {
		cfg.Shell = raw.Shell
	}
	if raw.AutoRelease.Enabled != nil {
		cfg.AutoRelease.Enabled = *raw.AutoRelease.Enabled
	}
	if raw.AutoRelease.TargetIdleTimeout != "" {
		timeout, err := time.ParseDuration(raw.AutoRelease.TargetIdleTimeout)
		if err != nil {
			return UserConfig{}, fmt.Errorf("invalid auto_release.target_idle_timeout: %w", err)
		}
		cfg.AutoRelease.TargetIdleTimeout = timeout
	}
	if raw.AutoRelease.DaemonIdleTimeout != "" {
		timeout, err := time.ParseDuration(raw.AutoRelease.DaemonIdleTimeout)
		if err != nil {
			return UserConfig{}, fmt.Errorf("invalid auto_release.daemon_idle_timeout: %w", err)
		}
		cfg.AutoRelease.DaemonIdleTimeout = timeout
	}
	return cfg, nil
}

func defaultServerPaths() (serverDefaultPaths, error) {
	dir, err := ptymuxHomePath("server")
	if err != nil {
		return serverDefaultPaths{}, err
	}
	return serverDefaultPaths{
		TokenFile:      filepath.Join(dir, "token"),
		ClientRegistry: filepath.Join(dir, "clients.json"),
	}, nil
}

func defaultClientConfigPath() (string, error) {
	return ptymuxHomePath("client", "config")
}

func defaultClientTokenPath() (string, error) {
	return ptymuxHomePath("client", "server.token")
}

func defaultClientPasswordPath(name string) (string, error) {
	if !remote.ValidClientName(name) {
		return "", fmt.Errorf("invalid client name %q", name)
	}
	return ptymuxHomePath("client", name+".password")
}

func ptymuxHomePath(parts ...string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	pathParts := append([]string{home, ".ptymux"}, parts...)
	return filepath.Join(pathParts...), nil
}

func readPrivateFile(path string, maxSize int64) ([]byte, error) {
	return readRegularFile(path, maxSize, true)
}

func readRegularFile(path string, maxSize int64, private bool) ([]byte, error) {
	flags := syscall.O_RDONLY | syscall.O_CLOEXEC | syscall.O_NONBLOCK
	if private {
		flags |= syscall.O_NOFOLLOW
	}
	fd, err := syscall.Open(path, flags, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file", path)
	}
	if private && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s must not be accessible by group or other users", path)
	}
	if info.Size() > maxSize {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, maxSize)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if int64(len(data)) > maxSize {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, maxSize)
	}
	return data, nil
}

func defaultUserConfig() UserConfig {
	return UserConfig{
		Shell: "/bin/sh",
		AutoRelease: server.AutoReleaseOptions{
			Enabled:           true,
			TargetIdleTimeout: 8 * time.Hour,
			DaemonIdleTimeout: 30 * time.Minute,
		},
	}
}
