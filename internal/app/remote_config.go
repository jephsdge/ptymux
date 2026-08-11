package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"ptymux/internal/remote"
)

type RemoteConfig struct {
	Clients map[string]RemoteProfile `json:"clients"`
}

type RemoteProfile struct {
	URL          string `json:"url"`
	Token        string `json:"token,omitempty"`
	TokenFile    string `json:"token_file,omitempty"`
	Name         string `json:"name"`
	Password     string `json:"password,omitempty"`
	PasswordFile string `json:"password_file,omitempty"`
}

func LoadRemoteConfig() (RemoteConfig, error) {
	path, err := defaultClientConfigPath()
	if err != nil {
		return RemoteConfig{}, err
	}
	data, err := readPrivateFile(path, maxPrivateConfigSize)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RemoteConfig{Clients: make(map[string]RemoteProfile)}, nil
		}
		return RemoteConfig{}, err
	}
	var cfg RemoteConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return RemoteConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	if cfg.Clients == nil {
		cfg.Clients = make(map[string]RemoteProfile)
	}
	return cfg, nil
}

func ResolveRemoteConfig(cfg Config, remoteConfig RemoteConfig) (Config, error) {
	if cfg.Mode != ModeClient {
		return cfg, nil
	}
	if cfg.Alias != "" {
		profile, ok := remoteConfig.Clients[cfg.Alias]
		if !ok {
			return Config{}, fmt.Errorf("unknown client alias %q", cfg.Alias)
		}
		applyRemoteProfile(&cfg, profile)
	}
	if cfg.URL == "" {
		return Config{}, errors.New("client URL is required")
	}
	if cfg.ClientName == "" {
		return Config{}, errors.New("client name is required")
	}
	if !remote.ValidClientName(cfg.ClientName) {
		return Config{}, fmt.Errorf("invalid client name %q", cfg.ClientName)
	}

	if cfg.Token == "" && cfg.TokenFile == "" {
		path, err := defaultClientTokenPath()
		if err != nil {
			return Config{}, err
		}
		cfg.TokenFile = path
	}
	token, err := resolveSecret("token", cfg.Token, cfg.TokenFile)
	if err != nil {
		return Config{}, err
	}
	cfg.Token = token
	cfg.TokenFile = ""

	if cfg.Action != ActionRegister {
		if cfg.Password == "" && cfg.PasswordFile == "" {
			path, err := defaultClientPasswordPath(cfg.ClientName)
			if err != nil {
				return Config{}, err
			}
			cfg.PasswordFile = path
		}
		password, err := resolveSecret("password", cfg.Password, cfg.PasswordFile)
		if err != nil {
			return Config{}, err
		}
		cfg.Password = password
		cfg.PasswordFile = ""
	}
	return cfg, nil
}

func applyRemoteProfile(cfg *Config, profile RemoteProfile) {
	if cfg.URL == "" {
		cfg.URL = profile.URL
	}
	if cfg.Token == "" && cfg.TokenFile == "" {
		cfg.Token = profile.Token
		cfg.TokenFile = profile.TokenFile
	}
	if cfg.ClientName == "" {
		cfg.ClientName = profile.Name
	}
	if cfg.Password == "" && cfg.PasswordFile == "" {
		cfg.Password = profile.Password
		cfg.PasswordFile = profile.PasswordFile
	}
}

func resolveSecret(label, inline, path string) (string, error) {
	if inline != "" && path != "" {
		return "", fmt.Errorf("%s and %s file cannot both be set", label, label)
	}
	if inline != "" {
		if bytes.IndexByte([]byte(inline), 0) >= 0 {
			return "", fmt.Errorf("%s contains a NUL byte", label)
		}
		return inline, nil
	}
	if path == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	data, err := readPrivateFile(path, maxPrivateSecretSize)
	if err != nil {
		return "", fmt.Errorf("read %s file: %w", label, err)
	}
	data = bytes.TrimRight(data, "\r\n")
	if len(data) == 0 {
		return "", fmt.Errorf("%s is empty", label)
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", fmt.Errorf("%s contains a NUL byte", label)
	}
	return string(data), nil
}
