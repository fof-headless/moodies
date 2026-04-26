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
	cmd := exec.Command("mitmdump", "--no-server", "-w", "/dev/null")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
	if _, err := os.Stat(MitmproxyCACertPath()); err != nil {
		return fmt.Errorf("CA cert not generated at %s", MitmproxyCACertPath())
	}
	return nil
}

// InstallCAScript returns an osascript command to install the CA cert as trusted.
func InstallCAScript() string {
	cert := MitmproxyCACertPath()
	return fmt.Sprintf(
		`do shell script "security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain %s" with administrator privileges`,
		cert,
	)
}

func InstallCA() error {
	script := InstallCAScript()
	cmd := exec.Command("osascript", "-e", script)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func UninstallCA() error {
	script := `do shell script "security delete-certificate -c mitmproxy /Library/Keychains/System.keychain" with administrator privileges`
	cmd := exec.Command("osascript", "-e", script)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
