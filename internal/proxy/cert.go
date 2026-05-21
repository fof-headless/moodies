package proxy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/doomsday/agent/internal/mitm"
)

// CABundlePath is the private-key+certificate PEM bundle written by GenerateCA.
// It is read by the proxy at startup to load the signing CA.
func CABundlePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".doomsday", "ca.pem")
}

// CACertPath is the certificate-only PEM written by GenerateCA.
// This is the file that goes into the login keychain and NODE_EXTRA_CA_CERTS.
func CACertPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".doomsday", "ca-cert.pem")
}

// MitmproxyCACertPath returns the legacy mitmproxy cert path. Kept for
// backward-compatibility references in doctorCmd and uninstall.
func MitmproxyCACertPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".mitmproxy", "mitmproxy-ca-cert.pem")
}

// GenerateCA creates the moodies CA in pure Go (no mitmproxy required).
// Writes the key+cert bundle to CABundlePath and the cert-only PEM to
// CACertPath. No-op if the bundle already exists (idempotent).
func GenerateCA() error {
	return mitm.GenerateCA(CABundlePath(), CACertPath())
}

func loginKeychain() string {
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, "Library", "Keychains", "login.keychain-db")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return filepath.Join(home, "Library", "Keychains", "login.keychain")
}

// InstallCA adds the moodies CA cert as trusted in the user's login keychain.
// No admin / osascript required — the login keychain is owned by the user.
func InstallCA() error {
	cmd := exec.Command("security", "add-trusted-cert", "-d", "-r", "trustRoot",
		"-k", loginKeychain(), CACertPath())
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// UninstallCA removes the moodies CA cert from the login keychain.
// Also removes the legacy mitmproxy cert if present (migration cleanup).
func UninstallCA() error {
	// New CA (moodies CA)
	_ = exec.Command("security", "delete-certificate", "-c", "moodies CA", loginKeychain()).Run()
	// Legacy CA (mitmproxy CA from old installs)
	_ = exec.Command("security", "delete-certificate", "-c", "mitmproxy", loginKeychain()).Run()
	return nil
}

// CAInstalled reports whether any moodies CA cert is present in the keychain.
func CAInstalled() bool {
	// Check for new CA name first
	out, err := exec.Command("security", "find-certificate", "-c", "moodies CA").Output()
	if err == nil && len(out) > 0 {
		return true
	}
	// Fall back to legacy mitmproxy name (existing installs)
	out, err = exec.Command("security", "find-certificate", "-c", "mitmproxy").Output()
	return err == nil && len(out) > 0
}

// LoadCA returns the CA for use by the proxy, preferring the new path and
// falling back to the legacy mitmproxy CA so existing installs keep working
// without requiring a reinstall.
func LoadCA() (*mitm.CA, error) {
	newPath := CABundlePath()
	if _, err := os.Stat(newPath); err == nil {
		return mitm.LoadCA(newPath)
	}
	// Backward compat: use mitmproxy CA if the new one hasn't been generated yet.
	legacy := filepath.Join(func() string { h, _ := os.UserHomeDir(); return h }(), ".mitmproxy", "mitmproxy-ca.pem")
	if _, err := os.Stat(legacy); err == nil {
		ca, err := mitm.LoadCA(legacy)
		if err != nil {
			return nil, fmt.Errorf("load legacy mitmproxy CA: %w", err)
		}
		return ca, nil
	}
	return nil, fmt.Errorf("no CA found at %s (run 'moodies install')", newPath)
}
