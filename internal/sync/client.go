package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/doomsday/agent/internal/config"
	"github.com/doomsday/agent/internal/store"
)

type Client struct {
	BackendURL string
	AgentToken string
	Store      *store.Store
	HTTPClient *http.Client
	Version    string

	mu               sync.Mutex
	sessionToken     string
	sessionExpiresAt time.Time
	manifestVersion  string
}

func New(backendURL, agentToken string, st *store.Store) *Client {
	return &Client{
		BackendURL: backendURL,
		AgentToken: agentToken,
		Store:      st,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
		Version:    "0.1.0",
	}
}

func (c *Client) Run(ctx context.Context) error {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			c.tick()
		}
	}
}

func (c *Client) tick() {
	events, err := c.Store.Unsynced(100)
	if err != nil || len(events) == 0 {
		return
	}

	payloads := make([]json.RawMessage, len(events))
	for i, e := range events {
		payloads[i] = json.RawMessage(e.PayloadJSON)
	}

	c.mu.Lock()
	tok := c.sessionToken
	c.mu.Unlock()

	reqBody := map[string]any{
		"agent_token": c.AgentToken,
		"events":      payloads,
	}
	if tok != "" {
		reqBody["session_token"] = tok
	}
	body, _ := json.Marshal(reqBody)

	req, err := http.NewRequest("POST", c.BackendURL+"/api/v1/agent/events", bytes.NewReader(body))
	if err != nil {
		log.Printf("[sync] build request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		log.Printf("[sync] request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		ids := make([]string, len(events))
		for i, e := range events {
			ids[i] = e.EventID
		}
		if err := c.Store.MarkSynced(ids); err != nil {
			log.Printf("[sync] mark synced: %v", err)
		}
		log.Printf("[sync] pushed %d events", len(ids))
	} else {
		log.Printf("[sync] backend returned %d", resp.StatusCode)
	}
}

// Heartbeat runs the basic heartbeat loop without reacting to manifest drift.
// Kept for callers that don't have a refresh callback to plug in.
func (c *Client) Heartbeat(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := c.sendHeartbeat(); err != nil {
				log.Printf("[heartbeat] %v", err)
			}
		}
	}
}

// HeartbeatResult holds whatever the heartbeat endpoint reports back. Right
// now that's just the latest manifest version (sha), used for cheap drift
// detection without re-downloading the full manifest.
type HeartbeatResult struct {
	ManifestVersion string
}

func (c *Client) sendHeartbeat() (*HeartbeatResult, error) {
	hostname, _ := os.Hostname()
	body, _ := json.Marshal(map[string]any{
		"agent_token": c.AgentToken,
		"hostname":    hostname,
		"version":     c.Version,
	})
	resp, err := c.HTTPClient.Post(c.BackendURL+"/api/v1/agent/heartbeat", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("heartbeat status %d", resp.StatusCode)
	}
	var parsed struct {
		ManifestVersion string `json:"manifest_version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&parsed)
	return &HeartbeatResult{ManifestVersion: parsed.ManifestVersion}, nil
}

// HeartbeatWithRefresh runs the heartbeat loop, comparing returned manifest
// versions against the cached one and triggering onDrift when they differ.
// onDrift is invoked synchronously so the caller controls how the new manifest
// is applied (atomic.Pointer swap, cache write, etc.).
func (c *Client) HeartbeatWithRefresh(ctx context.Context, onDrift func(*config.Manifest)) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			res, err := c.sendHeartbeat()
			if err != nil {
				log.Printf("[heartbeat] %v", err)
				continue
			}
			if res.ManifestVersion == "" || res.ManifestVersion == c.ManifestVersion() {
				continue
			}
			log.Printf("[heartbeat] manifest drift detected (have %q want %q), refreshing",
				c.ManifestVersion(), res.ManifestVersion)
			m, err := c.RefreshManifest(ctx)
			if err != nil {
				log.Printf("[heartbeat] refresh failed: %v", err)
				continue
			}
			if onDrift != nil {
				onDrift(m)
			}
		}
	}
}

// HandshakeResult is everything the daemon needs after a successful handshake:
// the session token (used as a cheap auth on subsequent requests), the
// manifest the backend currently wants this agent to enforce, and how often
// to refresh it.
type HandshakeResult struct {
	SessionToken     string
	SessionExpiresAt time.Time
	Manifest         *config.Manifest
	ManifestVersion  string
}

// Handshake POSTs /api/v1/agent/handshake with the agent's identity and reads
// back the manifest + session token. Returns an error on any non-200; the
// caller is expected to fall back to a cached or default manifest in that
// case so traffic capture continues.
func (c *Client) Handshake(ctx context.Context, hostname, version string, capabilities []string) (*HandshakeResult, error) {
	body, _ := json.Marshal(map[string]any{
		"agent_token":  c.AgentToken,
		"hostname":     hostname,
		"version":      version,
		"capabilities": capabilities,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", c.BackendURL+"/api/v1/agent/handshake", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("handshake: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("handshake status %d: %s", resp.StatusCode, string(b))
	}
	var parsed struct {
		SessionToken           string           `json:"session_token"`
		SessionExpiresAt       string           `json:"session_expires_at"`
		ManifestVersion        string           `json:"manifest_version"`
		Manifest               *config.Manifest `json:"manifest"`
		RefreshIntervalSeconds int              `json:"refresh_interval_seconds"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("handshake decode: %w", err)
	}
	if parsed.Manifest == nil {
		return nil, fmt.Errorf("handshake: empty manifest")
	}
	if parsed.Manifest.Version == "" {
		parsed.Manifest.Version = parsed.ManifestVersion
	}
	if parsed.Manifest.RefreshIntervalSecs == 0 && parsed.RefreshIntervalSeconds > 0 {
		parsed.Manifest.RefreshIntervalSecs = parsed.RefreshIntervalSeconds
	}
	exp, _ := time.Parse(time.RFC3339, parsed.SessionExpiresAt)

	c.mu.Lock()
	c.sessionToken = parsed.SessionToken
	c.sessionExpiresAt = exp
	c.manifestVersion = parsed.ManifestVersion
	c.mu.Unlock()

	return &HandshakeResult{
		SessionToken:     parsed.SessionToken,
		SessionExpiresAt: exp,
		Manifest:         parsed.Manifest,
		ManifestVersion:  parsed.ManifestVersion,
	}, nil
}

// RefreshManifest fetches the current manifest. Used by the periodic refresh
// loop and on heartbeat-driven drift detection.
func (c *Client) RefreshManifest(ctx context.Context) (*config.Manifest, error) {
	c.mu.Lock()
	tok := c.sessionToken
	c.mu.Unlock()

	body, _ := json.Marshal(map[string]any{
		"agent_token":   c.AgentToken,
		"session_token": tok,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", c.BackendURL+"/api/v1/agent/config", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("refresh status %d: %s", resp.StatusCode, string(b))
	}
	var m config.Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("refresh decode: %w", err)
	}

	c.mu.Lock()
	c.manifestVersion = m.Version
	c.mu.Unlock()
	return &m, nil
}

// ManifestVersion returns the last-seen sha. Used by the heartbeat loop to
// short-circuit RefreshManifest when nothing has changed.
func (c *Client) ManifestVersion() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.manifestVersion
}

func (c *Client) SetManifestVersion(v string) {
	c.mu.Lock()
	c.manifestVersion = v
	c.mu.Unlock()
}

func (c *Client) WriteHeartbeatFile() {
	home, _ := os.UserHomeDir()
	path := fmt.Sprintf("%s/.doomsday/heartbeat", home)
	_ = os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339)), 0600)
}
