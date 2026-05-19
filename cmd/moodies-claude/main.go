// Command moodies-claude is the Claude Code shim. It's installed under
// `~/.moodies/bin/claude` (which is prepended to $PATH by moodies install),
// so it wins binary lookup over the real `claude`. It then exec's the real
// `claude` with HTTPS_PROXY + NODE_EXTRA_CA_CERTS injected into the env —
// scoped to that one process, not the whole shell.
//
// Why this design instead of system-wide $HTTPS_PROXY in .zshrc:
//   - When the moodies daemon is down, system-wide $HTTPS_PROXY breaks every
//     CLI tool (gh, git, npm, curl, pip, the user's own scripts). The shim
//     contains the breakage to `claude` invocations only.
//   - The user's own Python/Go/Node code that talks to api.anthropic.com is
//     untouched and goes direct. That's the right default — they don't want
//     their app traffic captured by a dev tool.
//   - The shim catches `claude` whether it's typed at the prompt OR launched
//     as a subprocess by another tool (IDE extensions, npm scripts), because
//     PATH lookup is honored by both shell parsing and execvp().
//
// Graceful fallback: if the proxy port isn't listening, exec the real claude
// *without* the proxy env so the user's command still works. A one-line
// stderr warning makes the loss-of-capture visible without being noisy.
package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	defaultProxyHost = "127.0.0.1"
	defaultProxyPort = "8080"
)

func main() {
	realClaude, err := findRealClaude()
	if err != nil {
		fmt.Fprintf(os.Stderr, "moodies-claude: %v\n", err)
		os.Exit(127)
	}

	env := os.Environ()

	proxyURL := proxyURLFromEnv()
	if probeProxy(proxyURL) {
		caPath := caPathFromEnv()
		env = appendOrReplace(env, "HTTPS_PROXY", proxyURL)
		env = appendOrReplace(env, "HTTP_PROXY", proxyURL)
		// NODE_EXTRA_CA_CERTS is additive in Node — it's appended to Node's
		// built-in CA bundle rather than replacing it. Safe for the shim
		// to set unconditionally; existing CAs (including the user's own)
		// keep working.
		if caPath != "" {
			env = appendOrReplace(env, "NODE_EXTRA_CA_CERTS", caPath)
		}
	} else {
		// Daemon down — exec with the caller's env unchanged so claude
		// still works. Print one stderr line so the loss-of-capture isn't
		// silent. Suppress the warning if MOODIES_QUIET=1.
		if os.Getenv("MOODIES_QUIET") != "1" {
			fmt.Fprintf(os.Stderr, "moodies-claude: proxy %s unreachable, running claude direct (not captured)\n", proxyURL)
		}
	}

	// Exec rather than fork+wait so signals, stdio, tty allocation are
	// transparent. The shim disappears from the process tree.
	if err := syscall.Exec(realClaude, append([]string{"claude"}, os.Args[1:]...), env); err != nil {
		fmt.Fprintf(os.Stderr, "moodies-claude: exec %s: %v\n", realClaude, err)
		os.Exit(126)
	}
}

// findRealClaude walks $PATH looking for the next `claude` binary that
// isn't our shim. Re-resolved on every invocation so Claude Code updates
// that move the binary path are picked up without reinstalling the shim.
func findRealClaude() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate self: %w", err)
	}
	selfResolved, _ := filepath.EvalSymlinks(self)
	selfDir := filepath.Dir(selfResolved)

	for _, p := range strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)) {
		if p == "" {
			continue
		}
		if p == selfDir {
			continue
		}
		cand := filepath.Join(p, "claude")
		info, err := os.Stat(cand)
		if err != nil || info.IsDir() {
			continue
		}
		// Skip if it's a symlink back to us.
		resolved, _ := filepath.EvalSymlinks(cand)
		if resolved == selfResolved {
			continue
		}
		if info.Mode()&0111 == 0 {
			continue
		}
		return cand, nil
	}
	return "", fmt.Errorf("real `claude` not found on PATH (the shim resolved itself)")
}

func proxyURLFromEnv() string {
	host := os.Getenv("MOODIES_PROXY_HOST")
	if host == "" {
		host = defaultProxyHost
	}
	port := os.Getenv("MOODIES_PROXY_PORT")
	if port == "" {
		port = defaultProxyPort
	}
	return fmt.Sprintf("http://%s:%s", host, port)
}

func caPathFromEnv() string {
	if p := os.Getenv("MOODIES_CA_CERT"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".mitmproxy", "mitmproxy-ca-cert.pem")
}

// probeProxy returns true if the proxy port accepts a TCP connection within
// 300ms. Quick enough that the shim adds no perceptible startup delay.
func probeProxy(proxyURL string) bool {
	hostPort := strings.TrimPrefix(proxyURL, "http://")
	hostPort = strings.TrimPrefix(hostPort, "https://")
	if i := strings.IndexByte(hostPort, '/'); i >= 0 {
		hostPort = hostPort[:i]
	}
	conn, err := net.DialTimeout("tcp", hostPort, 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// appendOrReplace returns env with the given KEY=VAL pair, replacing any
// existing assignment of KEY.
func appendOrReplace(env []string, key, val string) []string {
	prefix := key + "="
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			env[i] = prefix + val
			return env
		}
	}
	return append(env, prefix+val)
}
