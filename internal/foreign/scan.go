package foreign

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type ForeignProxy struct {
	Source      string
	Variable    string
	Value       string
	PointsToUs  bool
}

var ourAddr = "127.0.0.1:8080"
var shellRCFiles = []string{".zshrc", ".bashrc", ".bash_profile", ".profile", ".zshenv"}
var exportRegex = regexp.MustCompile(`^\s*export\s+(HTTPS_PROXY|HTTP_PROXY|NODE_EXTRA_CA_CERTS)\s*=\s*(.+)$`)
var claudeConfigFiles = []string{".claude/settings.json", ".claude.json"}

func Scan() []ForeignProxy {
	var results []ForeignProxy
	home, _ := os.UserHomeDir()

	// Shell rc files
	for _, name := range shellRCFiles {
		path := filepath.Join(home, name)
		hits := scanShellFile(path)
		results = append(results, hits...)
	}

	// Claude config JSON files
	for _, name := range claudeConfigFiles {
		path := filepath.Join(home, name)
		hits := scanJSONConfig(path)
		results = append(results, hits...)
	}

	// npm config
	results = append(results, scanCLI("npm config get proxy", "HTTP_PROXY", runCmd("npm", "config", "get", "proxy"))...)
	results = append(results, scanCLI("npm config get https-proxy", "HTTPS_PROXY", runCmd("npm", "config", "get", "https-proxy"))...)

	// git config
	results = append(results, scanCLI("git config --global http.proxy", "HTTP_PROXY", runCmd("git", "config", "--global", "--get", "http.proxy"))...)
	results = append(results, scanCLI("git config --global https.proxy", "HTTPS_PROXY", runCmd("git", "config", "--global", "--get", "https.proxy"))...)

	// Current process env
	for _, envVar := range []string{"HTTPS_PROXY", "HTTP_PROXY", "NODE_EXTRA_CA_CERTS"} {
		if val := os.Getenv(envVar); val != "" {
			results = append(results, ForeignProxy{
				Source:     "process env",
				Variable:   envVar,
				Value:      val,
				PointsToUs: strings.Contains(val, ourAddr),
			})
		}
	}

	return results
}

func scanShellFile(path string) []ForeignProxy {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var results []ForeignProxy
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		m := exportRegex.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		varName := m[1]
		value := strings.Trim(m[2], `"' `)
		results = append(results, ForeignProxy{
			Source:     path,
			Variable:   varName,
			Value:      value,
			PointsToUs: strings.Contains(value, ourAddr),
		})
	}
	return results
}

func scanJSONConfig(path string) []ForeignProxy {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil
	}

	var envBlock map[string]string
	if raw, ok := top["env"]; ok {
		_ = json.Unmarshal(raw, &envBlock)
	}

	var results []ForeignProxy
	for _, varName := range []string{"HTTPS_PROXY", "HTTP_PROXY", "NODE_EXTRA_CA_CERTS"} {
		if val, ok := envBlock[varName]; ok && val != "" {
			results = append(results, ForeignProxy{
				Source:     path,
				Variable:   varName,
				Value:      val,
				PointsToUs: strings.Contains(val, ourAddr),
			})
		}
	}
	return results
}

func scanCLI(source, varName, value string) []ForeignProxy {
	if value == "" || value == "null" || value == "(null)" {
		return nil
	}
	return []ForeignProxy{{
		Source:     source,
		Variable:   varName,
		Value:      value,
		PointsToUs: strings.Contains(value, ourAddr),
	}}
}

func runCmd(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(bytes.TrimSpace(out)))
}

func FormatWarning(proxies []ForeignProxy) string {
	var sb strings.Builder
	sb.WriteString("Doomsday detected existing proxy configuration on this machine:\n\n")
	for _, p := range proxies {
		sb.WriteString(fmt.Sprintf("  %s: %s=%s\n", p.Source, p.Variable, p.Value))
	}
	sb.WriteString(`
These must be removed before installing Doomsday, otherwise Claude Code and
other tools will fail when our proxy starts.

Run ` + "`doomsday doctor --show-foreign-proxies`" + ` to see all locations.

To proceed:
  1. Remove the proxy config from those locations
  2. Run ` + "`doomsday install`" + ` again
`)
	return sb.String()
}
