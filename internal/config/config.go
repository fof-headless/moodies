package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	BackendURL  string `toml:"backend_url"`
	AgentToken  string `toml:"agent_token"`
	StorageMode string `toml:"storage_mode"`
	ListenPort  int    `toml:"listen_port"`
}

func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".doomsday", "config.toml")
}

func Load() (*Config, error) {
	path := DefaultPath()
	cfg := &Config{
		BackendURL:  "http://localhost:4000",
		AgentToken:  "changeme-set-in-env",
		StorageMode: "raw",
		ListenPort:  8080,
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return cfg, nil
	}
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, fmt.Errorf("config decode: %w", err)
	}
	return cfg, nil
}

func (c *Config) Save() error {
	path := DefaultPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(c)
}

func (c *Config) Set(key, value string) error {
	switch key {
	case "backend_url":
		c.BackendURL = value
	case "agent_token":
		c.AgentToken = value
	case "storage_mode":
		c.StorageMode = value
	default:
		return fmt.Errorf("unknown config key: %s", key)
	}
	return c.Save()
}
