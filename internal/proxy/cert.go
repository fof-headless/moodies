package proxy

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func MitmproxyCACertPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".mitmproxy", "mitmproxy-ca-cert.pem")
}

// GenerateCA runs mitmdump briefly so it creates its CA bundle under
// ~/.mitmproxy/, then kills it. We can't just `cmd.Run()` because mitmdump
// has no natural exit condition — without an input flow file or shutdown
// signal it idles forever even with `--no-server`. The fix: start it,
// poll for the CA file to appear (typically <500ms after first invocation),
// kill it, and verify the file exists. 15s ceiling so a genuinely stuck
// mitmdump still returns an error instead of hanging the installer.
func GenerateCA() error {
	if _, err := os.Stat(MitmproxyCACertPath()); err == nil {
		return nil
	}
	mitmdump, err := resolveMitmdump()
	if err != nil {
		return err
	}

	cmd := exec.Command(mitmdump, "--no-server", "-w", "/dev/null")
	// Discard mitmdump's chatty startup logs so the install output stays
	// readable; if we ever need to debug, swap to os.Stderr.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start mitmdump for CA generation: %w", err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(MitmproxyCACertPath()); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()

	if _, err := os.Stat(MitmproxyCACertPath()); err != nil {
		return fmt.Errorf("CA cert not generated at %s (mitmdump never wrote it)", MitmproxyCACertPath())
	}
	return nil
}

func loginKeychain() string {
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, "Library", "Keychains", "login.keychain-db")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return filepath.Join(home, "Library", "Keychains", "login.keychain")
}

// InstallCA adds the mitmproxy CA cert as trusted in the user's login keychain.
// No admin/osascript required — the login keychain is owned by the user.
func InstallCA() error {
	cmd := exec.Command("security", "add-trusted-cert", "-d", "-r", "trustRoot",
		"-k", loginKeychain(), MitmproxyCACertPath())
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func UninstallCA() error {
	cmd := exec.Command("security", "delete-certificate", "-c", "mitmproxy", loginKeychain())
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
	return nil
}
