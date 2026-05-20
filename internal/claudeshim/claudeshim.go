// Package claudeshim injects HTTPS_PROXY + NODE_EXTRA_CA_CERTS into
// /Applications/Claude.app's Info.plist LSEnvironment so the Electron app
// honours the moodies proxy on every Dock-click launch — no shell rc edits,
// no system-wide setenv blast radius, no user habit changes.
//
// Why LSEnvironment: macOS LaunchServices reads this dict every time it
// spawns the app (Dock click, Spotlight, `open -a`, subprocess launches all
// go through it). Setting env via the user's shell rc doesn't reach
// LaunchServices-launched apps; setting it via `launchctl setenv` works
// but affects every GUI app launched after login. LSEnvironment is the
// surgical option that only touches the app we're modifying.
//
// Why a watchdog: Claude.app self-updates via Squirrel, which overwrites
// the entire .app bundle including Info.plist. Without re-application,
// capture would silently turn off after every Anthropic release. The
// daemon's watchdog loop re-applies on each tick if the keys are missing
// or wrong, so the user never has to notice.
package claudeshim

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	// DefaultAppPath is where /Applications/Claude.app lives on a stock
	// macOS install. Exported so callers can override during tests.
	DefaultAppPath = "/Applications/Claude.app"

	plistBuddy = "/usr/libexec/PlistBuddy"
	lsRegister = "/System/Library/Frameworks/CoreServices.framework/Versions/A/Frameworks/LaunchServices.framework/Versions/A/Support/lsregister"

	keyProxy = "HTTPS_PROXY"
	keyCA    = "NODE_EXTRA_CA_CERTS"
)

// Config describes what to inject. Builds from the daemon's runtime config
// + the user's home — kept tiny on purpose so the public API doesn't pull
// in moodies-side packages.
type Config struct {
	AppPath  string // defaults to DefaultAppPath when empty
	ProxyURL string // e.g. "http://127.0.0.1:8080"
	CAPath   string // e.g. "/Users/x/.mitmproxy/mitmproxy-ca-cert.pem"
}

func (c Config) appPath() string {
	if c.AppPath == "" {
		return DefaultAppPath
	}
	return c.AppPath
}

func (c Config) plistPath() string {
	return filepath.Join(c.appPath(), "Contents", "Info.plist")
}

// AppInstalled reports whether Claude.app exists at the configured path.
// Callers should skip Apply when this returns false rather than treating
// it as a failure — the user just doesn't have the desktop app.
func AppInstalled(c Config) bool {
	_, err := os.Stat(c.plistPath())
	return err == nil
}

// IsApplied returns true when both keys are already present and match the
// configured values. Lets the watchdog skip the (cheap but noisy) re-apply
// path on every tick.
func IsApplied(c Config) bool {
	if !AppInstalled(c) {
		return false
	}
	gotProxy, _ := readLSEnvKey(c.plistPath(), keyProxy)
	gotCA, _ := readLSEnvKey(c.plistPath(), keyCA)
	return gotProxy == c.ProxyURL && gotCA == c.CAPath
}

// ErrSIPProtected is returned when Claude.app's Info.plist can't be
// written because macOS App Bundle Protection (com.apple.provenance xattr
// + System Integrity Protection) refuses the write at the kernel level.
// Disabling SIP would let us write, but that's a Recovery-Mode reboot
// procedure we won't ask of users.
var ErrSIPProtected = fmt.Errorf("Claude.app is SIP-protected (notarized bundle); LSEnvironment injection is not possible without disabling System Integrity Protection")

// Apply injects the env keys into Claude.app's LSEnvironment dict. Verifies
// after the write that the values actually landed, because PlistBuddy
// returns exit code 0 on "Operation not permitted" errors against
// SIP-protected files — meaning we can't trust its exit status alone.
//
// Returns ErrSIPProtected when the write was refused. Caller should
// treat that as a permanent failure mode (not a transient one) and stop
// retrying.
func Apply(c Config) (changed bool, err error) {
	if !AppInstalled(c) {
		return false, nil
	}
	plist := c.plistPath()

	// Make sure LSEnvironment dict exists. Anthropic ships one with
	// MallocNanoZone=0, but a future build might drop the dict entirely.
	_ = run(plistBuddy, "-c", "Add :LSEnvironment dict", plist)

	_ = setKey(plist, keyProxy, c.ProxyURL)
	_ = setKey(plist, keyCA, c.CAPath)

	// Verify the writes actually landed. PlistBuddy reports success on
	// SIP-blocked writes, so the only reliable check is reading back.
	gotProxy, _ := readLSEnvKey(plist, keyProxy)
	gotCA, _ := readLSEnvKey(plist, keyCA)
	if gotProxy != c.ProxyURL || gotCA != c.CAPath {
		return false, ErrSIPProtected
	}
	changed = true

	// Refresh LaunchServices cache so the next launch reads the new dict.
	_ = run(lsRegister, "-f", c.appPath())

	// Kill any running Claude.app so the user's next Dock click respawns
	// with the new env.
	_ = exec.Command("pkill", "-x", "Claude").Run()

	return changed, nil
}

// Remove strips both env keys from LSEnvironment. The LSEnvironment dict
// itself is preserved if it still has Anthropic's own keys (e.g.,
// MallocNanoZone) — we delete entries, not the container.
func Remove(c Config) error {
	if !AppInstalled(c) {
		return nil
	}
	plist := c.plistPath()
	_ = run(plistBuddy, "-c", "Delete :LSEnvironment:"+keyProxy, plist)
	_ = run(plistBuddy, "-c", "Delete :LSEnvironment:"+keyCA, plist)
	_ = run(lsRegister, "-f", c.appPath())
	_ = exec.Command("pkill", "-x", "Claude").Run()
	return nil
}

// setKey writes a single string-typed LSEnvironment entry. PlistBuddy's
// Set fails if the key doesn't exist; Add fails if it does. We try Set
// first (cheap if the key was there before), then fall back to Add.
func setKey(plist, key, value string) error {
	path := ":LSEnvironment:" + key
	if err := run(plistBuddy, "-c", "Set "+path+" "+value, plist); err == nil {
		return nil
	}
	if err := run(plistBuddy, "-c", "Add "+path+" string "+value, plist); err != nil {
		return fmt.Errorf("plistbuddy add %s: %w", key, err)
	}
	return nil
}

// readLSEnvKey shells out to PlistBuddy to read a single string. Empty
// string + nil error when the key isn't present (PlistBuddy returns
// non-zero in that case; we treat absence as the empty value).
func readLSEnvKey(plist, key string) (string, error) {
	out, err := exec.Command(plistBuddy, "-c", "Print :LSEnvironment:"+key, plist).Output()
	if err != nil {
		return "", nil
	}
	return strings.TrimRight(string(out), "\n"), nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s (%w)", name, strings.TrimSpace(string(out)), err)
	}
	return nil
}
