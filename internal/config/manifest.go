package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Manifest is the daemon-side mirror of the backend Manifest. The two structs
// MUST stay in sync — Version is sha256(config_json) so any drift will surface
// the next time the daemon refreshes.
type Manifest struct {
	Version             string          `json:"version"`
	Modules             ModuleToggles   `json:"modules"`
	Filter              FilterConfig    `json:"filter"`
	Redaction           RedactionConfig `json:"redaction"`
	StorageMode         string          `json:"storage_mode"`
	RefreshIntervalSecs int             `json:"refresh_interval_seconds"`
	ExpiresAt           string          `json:"expires_at,omitempty"`
}

type ModuleToggles struct {
	Redaction      bool `json:"redaction"`
	Classification bool `json:"classification"`
	Extraction     bool `json:"extraction"`
	BodyText       bool `json:"body_text"`
}

// ManifestPath is where the last-known-good manifest is cached. The daemon
// reads this on startup if the backend handshake fails so policy survives
// short outages.
func ManifestPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".doomsday", "manifest.json")
}

// DefaultManifest is the baseline policy used when both the backend and the
// on-disk cache are unavailable. Mirrors Defaults() so a never-handshaked
// daemon still produces useful events.
func DefaultManifest() *Manifest {
	d := Defaults()
	return &Manifest{
		Modules: ModuleToggles{
			Redaction:      true,
			Classification: true,
			Extraction:     true,
			BodyText:       true,
		},
		Filter: FilterConfig{
			TargetHosts: d.Filter.TargetHosts,
			Headers:     d.Filter.Headers,
		},
		Redaction:           RedactionConfig{Patterns: d.Redaction.Patterns},
		StorageMode:         d.StorageMode,
		RefreshIntervalSecs: 300,
	}
}

func LoadCachedManifest() (*Manifest, error) {
	b, err := os.ReadFile(ManifestPath())
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("manifest decode: %w", err)
	}
	return &m, nil
}

func (m *Manifest) SaveCache() error {
	path := ManifestPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
