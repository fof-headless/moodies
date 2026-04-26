package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type ComponentStatus struct {
	CACertTrusted       bool     `json:"ca_cert_trusted"`
	PACFileWritten      bool     `json:"pac_file_written"`
	PACActiveOnServices []string `json:"pac_active_on_services"`
	LaunchdLoaded       bool     `json:"launchd_loaded"`
	SQLiteInitialized   bool     `json:"sqlite_initialized"`
}

type State struct {
	SchemaVersion            int              `json:"schema_version"`
	InstallStartedAt         *time.Time       `json:"install_started_at"`
	InstallCompletedAt       *time.Time       `json:"install_completed_at"`
	Components               ComponentStatus  `json:"components"`
	LastUninstallAttemptedAt *time.Time       `json:"last_uninstall_attempted_at"`
	LastUninstallCompletedAt *time.Time       `json:"last_uninstall_completed_at"`
	LastShutdownClean        *time.Time       `json:"last_shutdown_clean"`
	DisabledAt               *time.Time       `json:"disabled_at"`
}

func statePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".doomsday", "state.json")
}

func Load() (*State, error) {
	path := statePath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &State{SchemaVersion: 1}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("state read: %w", err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("state parse: %w", err)
	}
	return &s, nil
}

func (s *State) Save() error {
	path := statePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// Atomic write via tmp + rename
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *State) MarkComponent(name string, val any) error {
	switch name {
	case "ca_cert_trusted":
		s.Components.CACertTrusted = val.(bool)
	case "pac_file_written":
		s.Components.PACFileWritten = val.(bool)
	case "pac_active_on_services":
		s.Components.PACActiveOnServices = val.([]string)
	case "launchd_loaded":
		s.Components.LaunchdLoaded = val.(bool)
	case "sqlite_initialized":
		s.Components.SQLiteInitialized = val.(bool)
	default:
		return fmt.Errorf("unknown component: %s", name)
	}
	return s.Save()
}

func (s *State) Delete() error {
	return os.Remove(statePath())
}
