package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/doomsday/agent/internal/store"
)

type Client struct {
	BackendURL string
	AgentToken string
	Store      *store.Store
	HTTPClient *http.Client
	Version    string
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

	body, _ := json.Marshal(map[string]any{
		"agent_token": c.AgentToken,
		"events":      payloads,
	})

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

func (c *Client) Heartbeat(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.sendHeartbeat()
		}
	}
}

func (c *Client) sendHeartbeat() {
	hostname, _ := os.Hostname()
	body, _ := json.Marshal(map[string]any{
		"agent_token": c.AgentToken,
		"hostname":    hostname,
		"version":     c.Version,
	})
	resp, err := c.HTTPClient.Post(c.BackendURL+"/api/v1/agent/heartbeat", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("[heartbeat] failed: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("[heartbeat] backend returned %d", resp.StatusCode)
	}
}

func (c *Client) WriteHeartbeatFile() {
	home, _ := os.UserHomeDir()
	path := fmt.Sprintf("%s/.doomsday/heartbeat", home)
	_ = os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339)), 0600)
}
