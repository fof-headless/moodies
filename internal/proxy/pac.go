package proxy

import (
	"fmt"
	"os"
	"path/filepath"
)

// pacTemplate routes all major AI-provider domains through the local proxy.
// The list must be kept in sync with DefaultTargetHosts in mitmdump.go and
// the DOOMSDAY_TARGET_HOSTS env consumed by the Go proxy's matchTarget logic.
const pacTemplate = `function FindProxyForURL(url, host) {
  // Anthropic / Claude
  if (shExpMatch(host, "*.anthropic.com") ||
      shExpMatch(host, "anthropic.com") ||
      shExpMatch(host, "*.claude.ai") ||
      shExpMatch(host, "claude.ai") ||
      shExpMatch(host, "*.claudeusercontent.com")) {
    return "PROXY 127.0.0.1:%d";
  }
  // OpenAI
  if (shExpMatch(host, "api.openai.com") ||
      shExpMatch(host, "openai.com") ||
      shExpMatch(host, "*.openai.com") ||
      shExpMatch(host, "*.openai.azure.com")) {
    return "PROXY 127.0.0.1:%d";
  }
  // Google Gemini
  if (shExpMatch(host, "generativelanguage.googleapis.com") ||
      shExpMatch(host, "*.generativelanguage.googleapis.com") ||
      shExpMatch(host, "gemini.google.com") ||
      shExpMatch(host, "aistudio.google.com")) {
    return "PROXY 127.0.0.1:%d";
  }
  // Mistral
  if (shExpMatch(host, "api.mistral.ai") ||
      shExpMatch(host, "*.mistral.ai")) {
    return "PROXY 127.0.0.1:%d";
  }
  // Cohere
  if (shExpMatch(host, "api.cohere.ai") ||
      shExpMatch(host, "api.cohere.com")) {
    return "PROXY 127.0.0.1:%d";
  }
  // Groq
  if (shExpMatch(host, "api.groq.com")) {
    return "PROXY 127.0.0.1:%d";
  }
  // Together AI
  if (shExpMatch(host, "api.together.xyz") ||
      shExpMatch(host, "api.together.ai")) {
    return "PROXY 127.0.0.1:%d";
  }
  return "DIRECT";
}`

func GeneratePAC(port int) string {
	// Each return statement needs the port — count and fill them all.
	return fmt.Sprintf(pacTemplate,
		port, port, port, port, port, port, port, // 7 AI provider blocks
	)
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
	if err := os.WriteFile(path, []byte(GeneratePAC(port)), 0644); err != nil {
		return "", err
	}
	return path, nil
}
