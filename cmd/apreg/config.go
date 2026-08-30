package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type cliConfig struct {
	Registry string `json:"registry"`
	Username string `json:"username"`
	Token    string `json:"token"`
}

func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".apreg", "config.json"), nil
}

func loadConfig() (*cliConfig, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &cliConfig{}, nil
		}
		return nil, err
	}
	var c cliConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func saveConfig(c *cliConfig) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// Config carries an API token, so keep it readable only by the owner.
	return os.WriteFile(path, b, 0o600)
}
