package proxy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func MitmproxyCACertPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".mitmproxy", "mitmproxy-ca-cert.pem")
}

// GenerateCA runs mitmdump once to generate the CA cert.
func GenerateCA() error {
	if _, err := os.Stat(MitmproxyCACertPath()); err == nil {
		return nil
	}
	mitmdump, err := resolveMitmdump()
	if err != nil {
		return err
	}
	cmd := exec.Command(mitmdump, "--no-server", "-w", "/dev/null")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
	if _, err := os.Stat(MitmproxyCACertPath()); err != nil {
		return fmt.Errorf("CA cert not generated at %s", MitmproxyCACertPath())
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
