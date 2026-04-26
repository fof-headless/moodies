package proxy

import (
	"fmt"
	"os"
	"path/filepath"
)

const pacTemplate = `function FindProxyForURL(url, host) {
  if (shExpMatch(host, "*.anthropic.com") ||
      shExpMatch(host, "anthropic.com") ||
      shExpMatch(host, "*.claude.ai") ||
      shExpMatch(host, "claude.ai") ||
      shExpMatch(host, "*.claudeusercontent.com")) {
    return "PROXY 127.0.0.1:%d";
  }
  return "DIRECT";
}`

func GeneratePAC(port int) string {
	return fmt.Sprintf(pacTemplate, port)
}

func WritePAC(port int) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".doomsday")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "proxy.pac")
	content := GeneratePAC(port)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", err
	}
	return path, nil
}
