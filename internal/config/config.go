package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config is the on-disk shape of ~/.doomsday/config.toml.
//
// Sections introduced after v0.1.0 (Filter, Headers, Redaction) are optional —
// missing fields fall back to package defaults so old config files keep working.
type Config struct {
	BackendURL  string `toml:"backend_url"`
	AgentToken  string `toml:"agent_token"`
	StorageMode string `toml:"storage_mode"` // "raw" | "hash_only"
	ListenPort  int    `toml:"listen_port"`

	Filter    FilterConfig    `toml:"filter"`
	Redaction RedactionConfig `toml:"redaction"`
}

type FilterConfig struct {
	// Suffix-match for entries starting with '.', exact match otherwise.
	TargetHosts []string      `toml:"target_hosts"`
	Headers     HeadersConfig `toml:"headers"`
}

type HeadersConfig struct {
	// If non-empty, only these (lowercased) header names pass through.
	// Empty allowlist == all headers pass (after blocklist removal).
	Allowlist []string `toml:"allowlist"`
	// Always dropped before allowlist check.
	Blocklist []string `toml:"blocklist"`
}

// RedactionConfig holds named regex patterns. Format: "name:regex".
// Compiled in internal/filter at runtime; bad patterns log warning + skip.
type RedactionConfig struct {
	Patterns []string `toml:"patterns"`
}

func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".doomsday", "config.toml")
}

// Defaults returns a Config seeded with built-in defaults.
// Anything in config.toml overrides these on a per-field basis.
func Defaults() *Config {
	return &Config{
		BackendURL:  "https://moodies-backshot-production.up.railway.app",
		AgentToken:  "changeme-set-in-env",
		StorageMode: "raw",
		ListenPort:  8080,
		Filter: FilterConfig{
			TargetHosts: []string{
				".anthropic.com",
				"claude.ai",
				".claude.ai",
				".claudeusercontent.com",
			},
			Headers: HeadersConfig{
				Allowlist: []string{
					"content-type",
					"user-agent",
					"anthropic-client-app",
					"anthropic-client-version",
					"anthropic-client-platform",
					"x-stainless-package-version",
				},
				Blocklist: []string{
					"cookie",
					"set-cookie",
					"authorization",
					"x-api-key",
					"sessionkey",
					"routinghint",
					"cf_clearance",
				},
			},
		},
		Redaction: RedactionConfig{
			Patterns: []string{
				`anthropic_key:sk-ant-(?:api|sid)\d+-[A-Za-z0-9_-]+`,
				`aws_key:AKIA[0-9A-Z]{16}`,
				`github_pat:gh[psoru]_[A-Za-z0-9_]{36,255}`,
				`email:[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`,
				`us_ssn:\b\d{3}-\d{2}-\d{4}\b`,
			},
		},
	}
}

func Load() (*Config, error) {
	path := DefaultPath()
	cfg := Defaults()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return cfg, nil
	}
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, fmt.Errorf("config decode: %w", err)
	}
	// Backfill missing slices with defaults so a partial config.toml doesn't
	// produce an effectively empty filter.
	d := Defaults()
	if len(cfg.Filter.TargetHosts) == 0 {
		cfg.Filter.TargetHosts = d.Filter.TargetHosts
	}
	if len(cfg.Filter.Headers.Allowlist) == 0 {
		cfg.Filter.Headers.Allowlist = d.Filter.Headers.Allowlist
	}
	if len(cfg.Filter.Headers.Blocklist) == 0 {
		cfg.Filter.Headers.Blocklist = d.Filter.Headers.Blocklist
	}
	if len(cfg.Redaction.Patterns) == 0 {
		cfg.Redaction.Patterns = d.Redaction.Patterns
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

// ManifestOrConfig picks the manifest values when present, otherwise falls
// back to the local Config (so a never-handshaked daemon keeps working).
// The result is consumed by filter.Apply.
func MergeForFilter(cfg *Config, m *Manifest) (modules ModuleToggles, headers HeadersConfig, redactions []string, targetHosts []string, storageMode string) {
	if m != nil {
		modules = m.Modules
		headers = m.Filter.Headers
		redactions = m.Redaction.Patterns
		targetHosts = m.Filter.TargetHosts
		storageMode = m.StorageMode
		if storageMode == "" {
			storageMode = cfg.StorageMode
		}
		return
	}
	// Passthrough defaults — see DefaultManifest in manifest.go. The local
	// Config still supplies target hosts + header rules so a fully offline
	// daemon (no backend, no cache) still captures Anthropic traffic.
	modules = ModuleToggles{
		Redaction:      false,
		Classification: true,
		Extraction:     true,
		BodyText:       true,
	}
	headers = cfg.Filter.Headers
	redactions = nil
	targetHosts = cfg.Filter.TargetHosts
	storageMode = "raw"
	return
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
