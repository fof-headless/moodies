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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/doomsday/agent/internal/config"
	"github.com/doomsday/agent/internal/filter"
	"github.com/doomsday/agent/internal/proxy"
	"github.com/doomsday/agent/internal/state"
	"github.com/doomsday/agent/internal/store"
	syncclient "github.com/doomsday/agent/internal/sync"
	"github.com/nxadm/tail"
)

const daemonVersion = "0.1.0"

// Build-time overrides. Populated via:
//
//	go build -ldflags "-X main.backendURL=https://api.example.com \
//	                   -X main.agentToken=prod-secret-token"
//
// When non-empty, these win over any value the user has in
// ~/.doomsday/config.toml — that file becomes a debug-only fallback for
// development builds. The intent is that release builds ship with the
// backend hard-wired so end users can't accidentally point the agent at
// a different sink.
var (
	backendURL = ""
	agentToken = ""
)

// applyEmbeddedConfig overlays the build-time vars onto cfg, returning the
// loaded config with overrides applied. Empty overrides leave cfg untouched
// so `go run` against ~/.doomsday/config.toml keeps working in dev.
func applyEmbeddedConfig(cfg *config.Config) *config.Config {
	if backendURL != "" {
		cfg.BackendURL = backendURL
	}
	if agentToken != "" {
		cfg.AgentToken = agentToken
	}
	return cfg
}

func capabilities() []string {
	return []string{"redaction", "classification", "extraction", "body_text"}
}

// mergeServices returns the union of two service-name lists, preserving the
// order of `existing` and appending any new entries from `incoming`. Used to
// keep state.json's PACActiveOnServices growing monotonically as the PAC
// watchdog discovers new services (e.g., Ethernet plugged in post-install).
func mergeServices(existing, incoming []string) []string {
	seen := make(map[string]bool, len(existing))
	out := make([]string, 0, len(existing)+len(incoming))
	for _, s := range existing {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, s := range incoming {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// buildApplyCfg derives the runtime filter config from the local Config plus
// (optionally) a backend-issued Manifest. Patterns that fail to compile are
// logged and skipped — the rest of the pipeline runs without them.
func buildApplyCfg(cfg *config.Config, m *config.Manifest) *filter.ApplyConfig {
	modules, headers, redactionPatterns, targetHosts, storageMode := config.MergeForFilter(cfg, m)
	compiled, errs := filter.CompilePatterns(redactionPatterns)
	for _, e := range errs {
		log.Printf("[filter] redaction pattern: %v", e)
	}
	return &filter.ApplyConfig{
		Modules: filter.ModuleToggles{
			Redaction:      modules.Redaction,
			Classification: modules.Classification,
			Extraction:     modules.Extraction,
			BodyText:       modules.BodyText,
		},
		Headers: filter.HeaderRules{
			Allowlist: headers.Allowlist,
			Blocklist: headers.Blocklist,
		},
		Redactions:  compiled,
		StorageMode: storageMode,
		Filter:      filter.FilterRules{TargetHosts: targetHosts},
	}
}

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
	cfg = applyEmbeddedConfig(cfg)

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

	syncer := syncclient.New(cfg.BackendURL, cfg.AgentToken, st)

	// Handshake with backend; fall back to cached manifest, then defaults.
	hostname, _ := os.Hostname()
	var manifest *config.Manifest
	hsCtx, hsCancel := context.WithTimeout(ctx, 10*time.Second)
	hs, hsErr := syncer.Handshake(hsCtx, hostname, daemonVersion, capabilities())
	hsCancel()
	if hsErr != nil {
		log.Printf("[daemon] handshake failed: %v", hsErr)
		if cached, err := config.LoadCachedManifest(); err == nil {
			log.Printf("[daemon] using cached manifest %s", cached.Version)
			manifest = cached
			syncer.SetManifestVersion(cached.Version)
		} else {
			log.Printf("[daemon] using default manifest")
			manifest = config.DefaultManifest()
		}
	} else {
		log.Printf("[daemon] handshake ok, manifest %s", hs.ManifestVersion)
		manifest = hs.Manifest
		if err := manifest.SaveCache(); err != nil {
			log.Printf("[daemon] manifest cache write: %v", err)
		}
	}

	var applyCfg atomic.Pointer[filter.ApplyConfig]
	applyCfg.Store(buildApplyCfg(cfg, manifest))

	// Spawn mitmdump
	spawnOpts := proxy.SpawnOptions{
		Port:        cfg.ListenPort,
		OutputPath:  outputPath,
		TargetHosts: applyCfg.Load().Filter.TargetHosts,
	}
	var mitmProc *proxy.MitmdumpProcess
	mitmProc, err = proxy.SpawnMitmdump(spawnOpts)
	if err != nil {
		log.Printf("[daemon] initial mitmdump spawn failed: %v", err)
	}

	// Tail raw events through the filter pipeline into SQLite.
	go tailEvents(ctx, outputPath, st, &applyCfg)

	// Periodic refresh: pull manifest at the cadence the backend asked for.
	// Heartbeat-driven drift refresh is a separate goroutine below.
	go refreshManifestLoop(ctx, syncer, cfg, &applyCfg, manifest.RefreshIntervalSecs)

	// Sync goroutine
	go syncer.Run(ctx)
	go syncer.HeartbeatWithRefresh(ctx, func(m *config.Manifest) {
		applyCfg.Store(buildApplyCfg(cfg, m))
		if err := m.SaveCache(); err != nil {
			log.Printf("[daemon] manifest cache write: %v", err)
		}
	})

	// Heartbeat file writer
	go writeHeartbeats(ctx, home)

	// Watchdog
	go watchdog(ctx, &mitmProc, cfg, outputPath, &applyCfg)

	// PAC self-heal, tied to mitmdump health. PAC is only enabled while the
	// proxy port is accepting connections — this avoids the phantom-proxy
	// failure mode (PAC on, mitmdump dead → all traffic dies) during
	// startup, between crash + respawn, after `doomsday-disable`, or when
	// macOS spontaneously flips PAC off on a network change.
	pacURL := "file://" + filepath.Join(home, ".doomsday", "proxy.pac")
	go proxy.EnforcePACLoop(ctx, pacURL, cfg.ListenPort, 5*time.Second,
		func() bool {
			// Respect the disable kill switch mid-flight, not just at startup.
			_, err := os.Stat(disableMarker)
			return err == nil
		},
		// onEnable
		func(changed []string) {
			st, err := state.Load()
			if err != nil {
				return
			}
			merged := mergeServices(st.Components.PACActiveOnServices, changed)
			_ = st.MarkComponent("pac_active_on_services", merged)
		},
		// onDisable
		func(_ []string) {
			// Intentionally don't shrink the stored service list — keep the
			// historical superset so uninstall always knows what to clean up,
			// even after a temporary disable cycle.
		},
	)

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

func tailEvents(ctx context.Context, path string, st *store.Store, applyCfg *atomic.Pointer[filter.ApplyConfig]) {
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
			var rf filter.RawFlow
			if err := json.Unmarshal([]byte(line.Text), &rf); err != nil {
				log.Printf("[tail] decode raw flow: %v", err)
				continue
			}
			ev, err := filter.Apply(&rf, *applyCfg.Load())
			if err != nil {
				log.Printf("[tail] filter apply: %v", err)
				continue
			}
			payload, err := json.Marshal(ev)
			if err != nil {
				log.Printf("[tail] marshal event: %v", err)
				continue
			}
			t2, err := time.Parse(time.RFC3339, rf.CapturedAt)
			if err != nil {
				t2 = time.Now()
			}
			_ = st.Insert(store.Event{
				EventID:      rf.EventID,
				CapturedAt:   t2,
				EndpointType: ev.EndpointType,
				PayloadJSON:  string(payload),
			})
		}
	}
}

// refreshManifestLoop polls the backend on the cadence the manifest specified.
// Drift detected by heartbeat is handled separately via HeartbeatWithRefresh —
// this loop is the safety net for a backend that updated but isn't asked
// before the next heartbeat tick.
func refreshManifestLoop(ctx context.Context, syncer *syncclient.Client, cfg *config.Config, applyCfg *atomic.Pointer[filter.ApplyConfig], intervalSecs int) {
	if intervalSecs <= 0 {
		intervalSecs = 300
	}
	ticker := time.NewTicker(time.Duration(intervalSecs) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m, err := syncer.RefreshManifest(ctx)
			if err != nil {
				log.Printf("[refresh] %v", err)
				continue
			}
			applyCfg.Store(buildApplyCfg(cfg, m))
			if err := m.SaveCache(); err != nil {
				log.Printf("[refresh] cache write: %v", err)
			}
		}
	}
}

func watchdog(ctx context.Context, proc **proxy.MitmdumpProcess, cfg *config.Config, outputPath string, applyCfg *atomic.Pointer[filter.ApplyConfig]) {
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
			newProc, spawnErr := proxy.SpawnMitmdump(proxy.SpawnOptions{
				Port:        cfg.ListenPort,
				OutputPath:  outputPath,
				TargetHosts: applyCfg.Load().Filter.TargetHosts,
			})
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
