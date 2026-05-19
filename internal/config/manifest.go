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

// DefaultManifest is the baseline policy when neither the backend nor the
// on-disk cache is available. v1 ships in passthrough mode: every captured
// flow is forwarded to the backend with no redaction and full body text.
// Real per-org filtering (regex redaction, hash-only storage, per-endpoint
// module toggles) is on the v2 roadmap; the code paths exist in
// internal/filter/ but the defaults exercise none of them.
func DefaultManifest() *Manifest {
	d := Defaults()
	return &Manifest{
		Modules: ModuleToggles{
			Redaction:      false, // passthrough: don't redact anything
			Classification: true,  // still useful for backend indexing
			Extraction:     true,  // still useful for backend search
			BodyText:       true,  // keep full body text
		},
		Filter: FilterConfig{
			TargetHosts: d.Filter.TargetHosts,
			Headers: HeadersConfig{
				// Empty allowlist == every header passes through (after
				// blocklist removal). v1 ships ~zero header redaction.
				Allowlist: nil,
				Blocklist: nil,
			},
		},
		Redaction:           RedactionConfig{Patterns: nil}, // no patterns => no redactions
		StorageMode:         "raw",
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
