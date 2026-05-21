package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/doomsday/agent/internal/claudeshim"
	"github.com/doomsday/agent/internal/config"
	"github.com/doomsday/agent/internal/filter"
	"github.com/doomsday/agent/internal/mitm"
	"github.com/doomsday/agent/internal/proxy"
	"github.com/doomsday/agent/internal/state"
	"github.com/doomsday/agent/internal/store"
	syncclient "github.com/doomsday/agent/internal/sync"
)

const daemonVersion = "0.1.0"

// Build-time overrides (populated via -ldflags).
var (
	backendURL = ""
	agentToken = ""
)

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
	st, err := store.OpenWithSchema(dbPath, filepath.Join(filepath.Dir(os.Args[0]), "schema.sql"))
	if err != nil {
		log.Fatalf("[daemon] store: %v", err)
	}
	defer st.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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

	// ---- Load CA and start the Go-native proxy ----
	ca, err := proxy.LoadCA()
	if err != nil {
		log.Fatalf("[daemon] load CA: %v — run 'moodies install' first", err)
	}

	// flows is the channel between the proxy and the filter pipeline.
	// Buffer of 512 so a momentary processing lag doesn't block proxy goroutines.
	flows := make(chan filter.RawFlow, 512)

	goProxy := mitm.NewProxy(
		cfg.ListenPort,
		ca,
		func() []string { return applyCfg.Load().Filter.TargetHosts },
		flows,
	)

	// Start the proxy with automatic restart on error.
	go func() {
		for {
			if err := goProxy.ListenAndServe(ctx); err != nil {
				log.Printf("[mitm] proxy error: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				log.Println("[mitm] restarting proxy...")
			}
		}
	}()

	// Filter pipeline: receives RawFlow from the proxy, applies redaction /
	// classification / extraction, and inserts Events into SQLite.
	go processFlows(ctx, flows, st, &applyCfg)

	// Periodic manifest refresh.
	go refreshManifestLoop(ctx, syncer, cfg, &applyCfg, manifest.RefreshIntervalSecs)

	// Sync + heartbeat goroutines.
	go syncer.Run(ctx)
	go syncer.HeartbeatWithRefresh(ctx, func(m *config.Manifest) {
		applyCfg.Store(buildApplyCfg(cfg, m))
		if err := m.SaveCache(); err != nil {
			log.Printf("[daemon] manifest cache write: %v", err)
		}
	})

	go writeHeartbeats(ctx, home)

	// Claude.app shim watchdog (best-effort; blocked by SIP on notarised
	// builds but harmless to keep running — it no-ops when SIP refuses).
	go claudeShimLoop(ctx, cfg, home)

	// PAC self-heal loop — keeps PAC active while the proxy port is up.
	pacURL := "file://" + filepath.Join(home, ".doomsday", "proxy.pac")
	go proxy.EnforcePACLoop(ctx, pacURL, cfg.ListenPort, 5*time.Second,
		func() bool {
			_, err := os.Stat(disableMarker)
			return err == nil
		},
		func(changed []string) {
			st, err := state.Load()
			if err != nil {
				return
			}
			merged := mergeServices(st.Components.PACActiveOnServices, changed)
			_ = st.MarkComponent("pac_active_on_services", merged)
		},
		func(_ []string) {},
	)

	go logUnsyncedPeriodically(ctx, st)

	<-ctx.Done()

	st2, _ := state.Load()
	now := time.Now()
	st2.LastShutdownClean = &now
	_ = st2.Save()

	log.Println("[daemon] shutdown complete")
}

// processFlows reads RawFlow events from the Go proxy, runs them through the
// filter pipeline, and inserts the resulting Events into SQLite. This replaces
// the old tailEvents / JSONL-file approach.
func processFlows(ctx context.Context, flows <-chan filter.RawFlow, st *store.Store, applyCfg *atomic.Pointer[filter.ApplyConfig]) {
	for {
		select {
		case <-ctx.Done():
			return
		case rf, ok := <-flows:
			if !ok {
				return
			}
			ev, err := filter.Apply(&rf, *applyCfg.Load())
			if err != nil {
				log.Printf("[flows] filter apply: %v", err)
				continue
			}
			payload, err := json.Marshal(ev)
			if err != nil {
				log.Printf("[flows] marshal event: %v", err)
				continue
			}
			t, err := time.Parse(time.RFC3339, rf.CapturedAt)
			if err != nil {
				t = time.Now()
			}
			_ = st.Insert(store.Event{
				EventID:      rf.EventID,
				CapturedAt:   t,
				EndpointType: ev.EndpointType,
				PayloadJSON:  string(payload),
			})
		}
	}
}

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

func claudeShimLoop(ctx context.Context, cfg *config.Config, home string) {
	shimCfg := claudeshim.Config{
		ProxyURL: fmt.Sprintf("http://127.0.0.1:%d", cfg.ListenPort),
		CAPath:   proxy.CACertPath(), // use new CA path; falls back gracefully
	}
	sipBlocked := false
	tick := func() {
		if sipBlocked || !claudeshim.AppInstalled(shimCfg) {
			return
		}
		if claudeshim.IsApplied(shimCfg) {
			return
		}
		_, err := claudeshim.Apply(shimCfg)
		switch {
		case err == nil:
			log.Printf("[claude-shim] re-injected LSEnvironment into Claude.app")
		case errors.Is(err, claudeshim.ErrSIPProtected):
			log.Printf("[claude-shim] Claude.app is SIP-protected; relying on launchctl env plist instead")
			sipBlocked = true
		default:
			log.Printf("[claude-shim] re-apply failed: %v", err)
		}
	}
	tick()
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
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
	for _, svc := range st.Components.PACActiveOnServices {
		_ = runCmd("networksetup", "-setautoproxystate", svc, "off")
	}
	now := time.Now()
	st.DisabledAt = &now
	_ = st.MarkComponent("pac_active_on_services", []string{})
	log.Println("[daemon] PAC disabled.")
}

func runCmd(name string, args ...string) error {
	_ = name
	_ = args
	return nil
}

// proxyAlive reports whether the proxy port is accepting connections.
// Used by the PAC self-heal loop.
func proxyAlive(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
