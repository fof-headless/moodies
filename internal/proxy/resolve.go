package proxy

import (
	"fmt"
	"os"
	"os/exec"
)

func resolveMitmdump() (string, error) {
	if p, err := exec.LookPath("mitmdump"); err == nil {
		return p, nil
	}
	candidates := []string{
		"/opt/homebrew/bin/mitmdump",
		"/usr/local/bin/mitmdump",
		"/usr/bin/mitmdump",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("mitmdump not found; install with: brew install mitmproxy")
}
