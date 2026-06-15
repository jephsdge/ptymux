package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"ptymux/internal/server"
)

type UserConfig struct {
	AutoRelease server.AutoReleaseOptions
}

type rawUserConfig struct {
	AutoRelease *rawAutoReleaseConfig `json:"auto_release"`
}

type rawAutoReleaseConfig struct {
	Enabled           *bool  `json:"enabled"`
	TargetIdleTimeout string `json:"target_idle_timeout"`
	DaemonIdleTimeout string `json:"daemon_idle_timeout"`
}

func LoadUserConfig() (UserConfig, error) {
	cfg := defaultUserConfig()
	home, err := os.UserHomeDir()
	if err != nil {
		return UserConfig{}, err
	}

	path := filepath.Join(home, ".ptymux", "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return UserConfig{}, err
	}

	var raw rawUserConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return UserConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	if raw.AutoRelease == nil {
		return cfg, nil
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

func defaultUserConfig() UserConfig {
	return UserConfig{
		AutoRelease: server.AutoReleaseOptions{
			Enabled:           true,
			TargetIdleTimeout: 8 * time.Hour,
			DaemonIdleTimeout: 30 * time.Minute,
		},
	}
}
