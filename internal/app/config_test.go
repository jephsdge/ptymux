package app

import (
	"bytes"
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
	config := []byte(`{"auto_release":{"target_idle_timeout":"soon"}}`)
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), config, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	if _, err := LoadUserConfig(); err == nil {
		t.Fatal("LoadUserConfig returned nil error, want invalid duration error")
	}
}

func TestLoadUserConfigIgnoresClientsInLocalConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrivateTestFile(t, filepath.Join(home, ".ptymux", "config"), []byte(`{
  "shell":"/bin/bash",
  "clients":{"relay":{"url":"http://old.example","name":"alice"}}
}`))

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig returned error: %v", err)
	}
	if cfg.Shell != "/bin/bash" {
		t.Fatalf("Shell = %q, want /bin/bash", cfg.Shell)
	}
	remoteCfg, err := LoadRemoteConfig()
	if err != nil {
		t.Fatalf("LoadRemoteConfig returned error: %v", err)
	}
	if len(remoteCfg.Clients) != 0 {
		t.Fatalf("remote clients = %+v, want old local clients ignored", remoteCfg.Clients)
	}
}

func TestLoadUserConfigRejectsPublicCanonicalConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".ptymux")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	if _, err := LoadUserConfig(); err == nil {
		t.Fatal("LoadUserConfig returned nil error, want private permission error")
	}
}

func TestLoadRemoteConfigLoadsClientConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrivateTestFile(t, filepath.Join(home, ".ptymux", "client", "config"), []byte(`{
  "clients": {
    "relay": {
      "url": "http://relay.example:8443",
      "token_file": "/tmp/token",
      "name": "alice",
      "password_file": "/tmp/password"
    }
  }
}`))

	cfg, err := LoadRemoteConfig()
	if err != nil {
		t.Fatalf("LoadRemoteConfig returned error: %v", err)
	}
	profile, ok := cfg.Clients["relay"]
	if !ok {
		t.Fatal("relay client alias was not loaded")
	}
	if profile.URL != "http://relay.example:8443" || profile.TokenFile != "/tmp/token" || profile.Name != "alice" || profile.PasswordFile != "/tmp/password" {
		t.Fatalf("profile = %+v, want client config values", profile)
	}
}

func TestLoadRemoteConfigDoesNotFallBackToOldPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrivateTestFile(t, filepath.Join(home, ".ptymux", "config"), []byte(`{"clients":{"relay":{"url":"http://old.example","name":"alice"}}}`))

	cfg, err := LoadRemoteConfig()
	if err != nil {
		t.Fatalf("LoadRemoteConfig returned error: %v", err)
	}
	if len(cfg.Clients) != 0 {
		t.Fatalf("Clients = %+v, want old config ignored", cfg.Clients)
	}
}

func TestLoadRemoteConfigRejectsPublicFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".ptymux", "client", "config")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if _, err := LoadRemoteConfig(); err == nil {
		t.Fatal("LoadRemoteConfig returned nil error, want private permission error")
	}
}

func TestLoadRemoteConfigRejectsSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".ptymux", "client")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "config")); err != nil {
		t.Fatalf("Symlink returned error: %v", err)
	}
	if _, err := LoadRemoteConfig(); err == nil {
		t.Fatal("LoadRemoteConfig returned nil error, want symlink error")
	}
}

func TestResolveRemoteConfigExplicitValuesOverrideAlias(t *testing.T) {
	cfg := Config{
		Mode:       ModeClient,
		Action:     ActionSend,
		Alias:      "relay",
		URL:        "http://explicit.example",
		Token:      "explicit-token",
		ClientName: "bob",
		Password:   "explicit-password",
	}
	remoteConfig := RemoteConfig{Clients: map[string]RemoteProfile{
		"relay": {
			URL:      "http://alias.example",
			Token:    "alias-token",
			Name:     "alice",
			Password: "alias-password",
		},
	}}

	resolved, err := ResolveRemoteConfig(cfg, remoteConfig)
	if err != nil {
		t.Fatalf("ResolveRemoteConfig returned error: %v", err)
	}
	if resolved.URL != cfg.URL || resolved.Token != cfg.Token || resolved.ClientName != cfg.ClientName || resolved.Password != cfg.Password {
		t.Fatalf("resolved = %+v, want explicit values preserved", resolved)
	}
}

func TestResolveRemoteConfigRejectsUnknownAlias(t *testing.T) {
	_, err := ResolveRemoteConfig(Config{Mode: ModeClient, Action: ActionRegister, Alias: "missing"}, RemoteConfig{Clients: map[string]RemoteProfile{}})
	if err == nil {
		t.Fatal("ResolveRemoteConfig returned nil error, want unknown alias error")
	}
}

func TestResolveRemoteConfigReadsPrivateSecretFiles(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	passwordPath := filepath.Join(dir, "password")
	if err := os.WriteFile(tokenPath, []byte("token-value\r\n"), 0o600); err != nil {
		t.Fatalf("WriteFile token returned error: %v", err)
	}
	if err := os.WriteFile(passwordPath, []byte("password-value\n\n"), 0o600); err != nil {
		t.Fatalf("WriteFile password returned error: %v", err)
	}

	resolved, err := ResolveRemoteConfig(Config{
		Mode:         ModeClient,
		Action:       ActionRead,
		URL:          "http://relay.example",
		TokenFile:    tokenPath,
		ClientName:   "alice",
		PasswordFile: passwordPath,
	}, RemoteConfig{})
	if err != nil {
		t.Fatalf("ResolveRemoteConfig returned error: %v", err)
	}
	if resolved.Token != "token-value" || resolved.Password != "password-value" {
		t.Fatalf("resolved secrets = %q/%q, want trimmed file values", resolved.Token, resolved.Password)
	}
	if resolved.TokenFile != "" || resolved.PasswordFile != "" {
		t.Fatalf("resolved file paths = %q/%q, want cleared", resolved.TokenFile, resolved.PasswordFile)
	}
}

func TestResolveRemoteConfigUsesDefaultSecretFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrivateTestFile(t, filepath.Join(home, ".ptymux", "client", "server.token"), []byte("default-token\n"))
	writePrivateTestFile(t, filepath.Join(home, ".ptymux", "client", "alice.password"), []byte("default-password\n"))

	resolved, err := ResolveRemoteConfig(Config{
		Mode:       ModeClient,
		Action:     ActionRead,
		URL:        "http://relay.example",
		ClientName: "alice",
	}, RemoteConfig{})
	if err != nil {
		t.Fatalf("ResolveRemoteConfig returned error: %v", err)
	}
	if resolved.Token != "default-token" || resolved.Password != "default-password" {
		t.Fatalf("resolved secrets = %q/%q, want defaults", resolved.Token, resolved.Password)
	}
}

func TestResolveRemoteConfigAliasSecretsOverrideDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	resolved, err := ResolveRemoteConfig(Config{
		Mode:   ModeClient,
		Action: ActionRead,
		Alias:  "relay",
	}, RemoteConfig{Clients: map[string]RemoteProfile{
		"relay": {
			URL:      "http://relay.example",
			Token:    "alias-token",
			Name:     "alice",
			Password: "alias-password",
		},
	}})
	if err != nil {
		t.Fatalf("ResolveRemoteConfig returned error: %v", err)
	}
	if resolved.Token != "alias-token" || resolved.Password != "alias-password" {
		t.Fatalf("resolved secrets = %q/%q, want alias values", resolved.Token, resolved.Password)
	}
}

func TestResolveRemoteConfigRegisterUsesDefaultTokenWithoutPassword(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrivateTestFile(t, filepath.Join(home, ".ptymux", "client", "server.token"), []byte("token"))

	resolved, err := ResolveRemoteConfig(Config{
		Mode:       ModeClient,
		Action:     ActionRegister,
		URL:        "http://relay.example",
		ClientName: "alice",
	}, RemoteConfig{})
	if err != nil {
		t.Fatalf("ResolveRemoteConfig returned error: %v", err)
	}
	if resolved.Token != "token" || resolved.Password != "" || resolved.PasswordFile != "" {
		t.Fatalf("resolved = %+v, want token without password", resolved)
	}
}

func TestResolveRemoteConfigRejectsInvalidClientName(t *testing.T) {
	_, err := ResolveRemoteConfig(Config{
		Mode:       ModeClient,
		Action:     ActionRegister,
		URL:        "http://relay.example",
		Token:      "token",
		ClientName: "../alice",
	}, RemoteConfig{})
	if err == nil {
		t.Fatal("ResolveRemoteConfig returned nil error, want invalid name error")
	}
}

func TestResolveRemoteConfigRejectsPublicSecretFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("token"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	_, err := ResolveRemoteConfig(Config{
		Mode:       ModeClient,
		Action:     ActionRegister,
		URL:        "http://relay.example",
		TokenFile:  path,
		ClientName: "alice",
	}, RemoteConfig{})
	if err == nil {
		t.Fatal("ResolveRemoteConfig returned nil error, want private permission error")
	}
}

func TestResolveRemoteConfigRejectsSecretSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "token")
	if err := os.WriteFile(target, []byte("token"), 0o600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink returned error: %v", err)
	}

	_, err := ResolveRemoteConfig(Config{
		Mode:       ModeClient,
		Action:     ActionRegister,
		URL:        "http://relay.example",
		TokenFile:  link,
		ClientName: "alice",
	}, RemoteConfig{})
	if err == nil {
		t.Fatal("ResolveRemoteConfig returned nil error, want symlink error")
	}
}

func TestResolveRemoteConfigOperationRequiresDefaultPasswordFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrivateTestFile(t, filepath.Join(home, ".ptymux", "client", "server.token"), []byte("token"))

	_, err := ResolveRemoteConfig(Config{
		Mode:       ModeClient,
		Action:     ActionRead,
		URL:        "http://relay.example",
		ClientName: "alice",
	}, RemoteConfig{})
	if err == nil {
		t.Fatal("ResolveRemoteConfig returned nil error, want missing default password error")
	}
}

func TestLoadRemoteConfigRejectsOversizedFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrivateTestFile(t, filepath.Join(home, ".ptymux", "client", "config"), bytes.Repeat([]byte(" "), maxPrivateConfigSize+1))

	if _, err := LoadRemoteConfig(); err == nil {
		t.Fatal("LoadRemoteConfig returned nil error, want size limit error")
	}
}

func TestResolveRemoteConfigRejectsOversizedSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writePrivateTestFile(t, path, bytes.Repeat([]byte("x"), maxPrivateSecretSize+1))

	_, err := ResolveRemoteConfig(Config{
		Mode:       ModeClient,
		Action:     ActionRegister,
		URL:        "http://relay.example",
		TokenFile:  path,
		ClientName: "alice",
	}, RemoteConfig{})
	if err == nil {
		t.Fatal("ResolveRemoteConfig returned nil error, want size limit error")
	}
}

func writePrivateTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll(%q) returned error: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile(%q) returned error: %v", path, err)
	}
}
