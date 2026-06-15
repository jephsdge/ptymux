package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadUserConfigDefaultsAutoRelease(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig returned error: %v", err)
	}

	if !cfg.AutoRelease.Enabled {
		t.Fatal("AutoRelease.Enabled = false, want true")
	}
	if cfg.AutoRelease.TargetIdleTimeout != 8*time.Hour {
		t.Fatalf("TargetIdleTimeout = %s, want 8h", cfg.AutoRelease.TargetIdleTimeout)
	}
	if cfg.AutoRelease.DaemonIdleTimeout != 30*time.Minute {
		t.Fatalf("DaemonIdleTimeout = %s, want 30m", cfg.AutoRelease.DaemonIdleTimeout)
	}
	if cfg.Shell != "/bin/sh" {
		t.Fatalf("Shell = %q, want /bin/sh", cfg.Shell)
	}
}

func TestDefaultSocketPathUsesPtymuxSocketDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := DefaultSocketPath()
	want := filepath.Join(home, ".ptymux", "sockets", "ptymux-default.sock")
	if got != want {
		t.Fatalf("DefaultSocketPath = %q, want %q", got, want)
	}
}

func TestLoadUserConfigOverridesAutoRelease(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".ptymux")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	config := []byte(`{
  "shell": "/bin/bash",
  "auto_release": {
    "enabled": false,
    "target_idle_timeout": "15m",
    "daemon_idle_timeout": "2h"
  }
}`)
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), config, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig returned error: %v", err)
	}

	if cfg.AutoRelease.Enabled {
		t.Fatal("AutoRelease.Enabled = true, want false")
	}
	if cfg.AutoRelease.TargetIdleTimeout != 15*time.Minute {
		t.Fatalf("TargetIdleTimeout = %s, want 15m", cfg.AutoRelease.TargetIdleTimeout)
	}
	if cfg.AutoRelease.DaemonIdleTimeout != 2*time.Hour {
		t.Fatalf("DaemonIdleTimeout = %s, want 2h", cfg.AutoRelease.DaemonIdleTimeout)
	}
	if cfg.Shell != "/bin/bash" {
		t.Fatalf("Shell = %q, want /bin/bash", cfg.Shell)
	}
}

func TestLoadUserConfigRejectsInvalidDuration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".ptymux")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	config := []byte(`{
  "auto_release": {
    "target_idle_timeout": "soon"
  }
}`)
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), config, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	if _, err := LoadUserConfig(); err == nil {
		t.Fatal("LoadUserConfig returned nil error, want invalid duration error")
	}
}
