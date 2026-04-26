package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/doomsday/agent/internal/config"
	"github.com/doomsday/agent/internal/proxy"
	"github.com/doomsday/agent/internal/state"
	"github.com/doomsday/agent/internal/store"
	syncclient "github.com/doomsday/agent/internal/sync"
	"github.com/nxadm/tail"
)

func main() {
	home, _ := os.UserHomeDir()
	disableMarker := filepath.Join(home, ".doomsday", "disable_marker")

	if _, err := os.Stat(disableMarker); err == nil {
		log.Println("[daemon] disable_marker found, running disable sequence")
		runDisableSequence(home)
		return
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("[daemon] config: %v", err)
	}

	dbPath := filepath.Join(home, ".doomsday", "buffer.db")
	st, err := store.OpenInMemory()
	if err != nil {
		// Fall back to file-based store
		st, err = store.OpenWithSchema(dbPath, filepath.Join(filepath.Dir(os.Args[0]), "schema.sql"))
		if err != nil {
			log.Fatalf("[daemon] store: %v", err)
		}
	} else {
		st.Close()
		st, err = store.OpenWithSchema(dbPath, filepath.Join(filepath.Dir(os.Args[0]), "schema.sql"))
		if err != nil {
			log.Fatalf("[daemon] store: %v", err)
		}
	}
	defer st.Close()

	outputPath := filepath.Join(home, ".doomsday", "raw_events.jsonl")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Signal handling
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigs
		log.Println("[daemon] received shutdown signal")
		cancel()
	}()

	// Spawn mitmdump
	var mitmProc *proxy.MitmdumpProcess
	mitmProc, err = proxy.SpawnMitmdump(8080, cfg.StorageMode, outputPath)
	if err != nil {
		log.Printf("[daemon] initial mitmdump spawn failed: %v", err)
	}

	// Tail raw events into SQLite
	go tailEvents(ctx, outputPath, st)

	// Sync goroutine
	syncer := syncclient.New(cfg.BackendURL, cfg.AgentToken, st)
	go syncer.Run(ctx)
	go syncer.Heartbeat(ctx)

	// Heartbeat file writer
	go writeHeartbeats(ctx, home)

	// Watchdog
	go watchdog(ctx, &mitmProc, cfg, outputPath)

	// Log unsynced count every 30s
	go logUnsyncedPeriodically(ctx, st)

	<-ctx.Done()

	if mitmProc != nil {
		mitmProc.Kill()
	}

	st2, _ := state.Load()
	now := time.Now()
	st2.LastShutdownClean = &now
	_ = st2.Save()

	log.Println("[daemon] shutdown complete")
}

func tailEvents(ctx context.Context, path string, st *store.Store) {
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	// Wait for file to be created
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}

	t, err := tail.TailFile(path, tail.Config{Follow: true, ReOpen: true})
	if err != nil {
		log.Printf("[tail] error: %v", err)
		return
	}
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case line, ok := <-t.Lines:
			if !ok {
				return
			}
			if line.Text == "" {
				continue
			}
			var ev struct {
				EventID      string `json:"event_id"`
				CapturedAt   string `json:"captured_at"`
				EndpointType string `json:"endpoint_type"`
			}
			if err := json.Unmarshal([]byte(line.Text), &ev); err != nil {
				log.Printf("[tail] parse error: %v", err)
				continue
			}
			t2, err := time.Parse(time.RFC3339, ev.CapturedAt)
			if err != nil {
				t2 = time.Now()
			}
			_ = st.Insert(store.Event{
				EventID:      ev.EventID,
				CapturedAt:   t2,
				EndpointType: ev.EndpointType,
				PayloadJSON:  line.Text,
			})
		}
	}
}

func watchdog(ctx context.Context, proc **proxy.MitmdumpProcess, cfg *config.Config, outputPath string) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.ListenPort), time.Second)
			if err == nil {
				conn.Close()
				continue
			}
			log.Printf("[watchdog] mitmproxy not responding, restarting...")
			if *proc != nil {
				(*proc).Kill()
			}
			newProc, spawnErr := proxy.SpawnMitmdump(cfg.ListenPort, cfg.StorageMode, outputPath)
			if spawnErr != nil {
				log.Printf("[watchdog] respawn failed: %v", spawnErr)
			} else {
				*proc = newProc
			}
		}
	}
}

func writeHeartbeats(ctx context.Context, home string) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			path := filepath.Join(home, ".doomsday", "heartbeat")
			_ = os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339)), 0600)
		}
	}
}

func logUnsyncedPeriodically(ctx context.Context, st *store.Store) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			count, _ := st.UnsyncedCount()
			log.Printf("[daemon] unsynced events: %d", count)
		}
	}
}

func runDisableSequence(home string) {
	st, _ := state.Load()
	services := st.Components.PACActiveOnServices
	for _, svc := range services {
		_ = runCmd("networksetup", "-setautoproxystate", svc, "off")
	}
	now := time.Now()
	st.DisabledAt = &now
	_ = st.MarkComponent("pac_active_on_services", []string{})

	log.Println("[daemon] PAC disabled. Launchd will not restart because disable_marker still exists.")
}

func runCmd(name string, args ...string) error {
	return func() error {
		cmd := fmt.Sprintf("%s %v", name, args)
		_ = cmd
		return nil
	}()
}
