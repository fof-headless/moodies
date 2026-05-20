package claudeshim

import (
	"os"
	"path/filepath"
	"testing"
)

// TestApplyRemoveRoundTrip writes a fake .app bundle layout (just Info.plist
// inside Contents/) and verifies Apply -> IsApplied -> Remove -> !IsApplied.
// Skips if PlistBuddy isn't on the system (non-macOS CI).
func TestApplyRemoveRoundTrip(t *testing.T) {
	if _, err := os.Stat(plistBuddy); err != nil {
		t.Skipf("PlistBuddy unavailable (%v); skipping", err)
	}
	dir := t.TempDir()
	appPath := filepath.Join(dir, "FakeClaude.app")
	contents := filepath.Join(appPath, "Contents")
	if err := os.MkdirAll(contents, 0755); err != nil {
		t.Fatal(err)
	}
	// Minimal valid Info.plist with an existing LSEnvironment that mimics
	// what Anthropic ships (MallocNanoZone=0), so we can verify Apply
	// preserves it.
	initial := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleIdentifier</key><string>com.fake.claude</string>
  <key>CFBundleName</key><string>FakeClaude</string>
  <key>LSEnvironment</key>
  <dict>
    <key>MallocNanoZone</key><string>0</string>
  </dict>
</dict>
</plist>`
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		AppPath:  appPath,
		ProxyURL: "http://127.0.0.1:8080",
		CAPath:   "/tmp/ca.pem",
	}

	if !AppInstalled(cfg) {
		t.Fatal("AppInstalled=false on a freshly-staged fake .app")
	}
	if IsApplied(cfg) {
		t.Fatal("IsApplied=true before Apply ran")
	}

	if _, err := Apply(cfg); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !IsApplied(cfg) {
		t.Fatal("IsApplied=false right after Apply")
	}

	// Idempotency — Apply twice doesn't error.
	if _, err := Apply(cfg); err != nil {
		t.Fatalf("second Apply: %v", err)
	}

	// Anthropic's pre-existing MallocNanoZone key must survive.
	got, _ := readLSEnvKey(cfg.plistPath(), "MallocNanoZone")
	if got != "0" {
		t.Errorf("Apply nuked MallocNanoZone; got %q", got)
	}

	// Remove strips our keys but leaves the dict.
	if err := Remove(cfg); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if IsApplied(cfg) {
		t.Fatal("IsApplied=true after Remove")
	}
	got, _ = readLSEnvKey(cfg.plistPath(), "MallocNanoZone")
	if got != "0" {
		t.Errorf("Remove nuked MallocNanoZone; got %q", got)
	}
}

func TestIsAppliedDetectsValueDrift(t *testing.T) {
	if _, err := os.Stat(plistBuddy); err != nil {
		t.Skipf("PlistBuddy unavailable (%v); skipping", err)
	}
	dir := t.TempDir()
	appPath := filepath.Join(dir, "FakeClaude.app")
	contents := filepath.Join(appPath, "Contents")
	_ = os.MkdirAll(contents, 0755)
	_ = os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict/></plist>`), 0644)

	cfg := Config{AppPath: appPath, ProxyURL: "http://127.0.0.1:8080", CAPath: "/tmp/ca.pem"}
	_, _ = Apply(cfg)
	if !IsApplied(cfg) {
		t.Fatal("expected applied")
	}
	// Change the proxy port the daemon wants — IsApplied must report
	// false so the watchdog will rewrite.
	cfg.ProxyURL = "http://127.0.0.1:9090"
	if IsApplied(cfg) {
		t.Fatal("expected IsApplied=false after value drift")
	}
}
