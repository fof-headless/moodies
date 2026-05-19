// Package shellrc manages a moodies-owned block in the user's shell rc files.
//
// We write a clearly delimited block (marker-prefixed) so we can remove it
// cleanly on uninstall without disturbing the user's own lines. The block
// content is supplied by the caller — this package only knows about
// markers, atomic writes, and shell-syntax differences.
//
// Caution: never put `HTTPS_PROXY` / `SSL_CERT_FILE` / `REQUESTS_CA_BUNDLE`
// into the block. Setting those system-wide breaks every CLI tool when the
// proxy is down and forces the wrong CA on every TLS handshake. The current
// install only adds a $PATH prepend so the `claude` shim wins lookups.
package shellrc

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	beginMarker = "# >>> moodies >>> (managed by moodies install — do not edit)"
	endMarker   = "# <<< moodies <<< (managed by moodies install — do not edit)"
)

// RCTarget describes one rc file we manage.
type RCTarget struct {
	Path  string
	Shell string // "bash" | "zsh" | "fish"
}

// DetectTargets returns the set of rc files that exist (or, for the user's
// $SHELL, should exist) on this machine. Files that don't exist are not
// returned — we don't want to create dotfiles speculatively, except for the
// user's login shell where the absence is meaningful.
func DetectTargets() []RCTarget {
	home, _ := os.UserHomeDir()
	candidates := []RCTarget{
		{filepath.Join(home, ".zshrc"), "zsh"},
		{filepath.Join(home, ".bash_profile"), "bash"},
		{filepath.Join(home, ".bashrc"), "bash"},
		{filepath.Join(home, ".profile"), "bash"},
		{filepath.Join(home, ".config", "fish", "config.fish"), "fish"},
	}

	var out []RCTarget
	for _, c := range candidates {
		if _, err := os.Stat(c.Path); err == nil {
			out = append(out, c)
		}
	}

	loginShell := filepath.Base(os.Getenv("SHELL"))
	if loginShell == "zsh" {
		zPath := filepath.Join(home, ".zshrc")
		if !contains(out, zPath) {
			out = append(out, RCTarget{zPath, "zsh"})
		}
	} else if loginShell == "bash" {
		bPath := filepath.Join(home, ".bash_profile")
		if !contains(out, bPath) {
			out = append(out, RCTarget{bPath, "bash"})
		}
	}
	return out
}

func contains(ts []RCTarget, p string) bool {
	for _, t := range ts {
		if t.Path == p {
			return true
		}
	}
	return false
}

// PathPrependLines renders the shell-specific lines that put `dir` at the
// front of $PATH. Used by InstallBlock callers that want the moodies shim
// directory found before Homebrew / system bin paths.
func PathPrependLines(shell, dir string) []string {
	if shell == "fish" {
		return []string{fmt.Sprintf("set -gx PATH %s $PATH", dir)}
	}
	return []string{fmt.Sprintf("export PATH=%q:$PATH", dir)}
}

// renderBlock wraps the supplied lines in begin/end markers, with a trailing
// newline so subsequent file content stays on its own line.
func renderBlock(lines []string) string {
	var b strings.Builder
	b.WriteString(beginMarker)
	b.WriteString("\n")
	for _, line := range lines {
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString(endMarker)
	b.WriteString("\n")
	return b.String()
}

// InstallBlock writes (or rewrites) the moodies-managed block in target with
// the supplied content lines. Existing user content outside the markers is
// preserved verbatim. Returns true if the file was actually changed —
// idempotent when called repeatedly with the same lines.
func InstallBlock(target RCTarget, lines []string) (changed bool, err error) {
	desired := renderBlock(lines)

	current, err := readOrEmpty(target.Path)
	if err != nil {
		return false, err
	}

	stripped, hadBlock := stripBlock(current)
	if hadBlock {
		if normalize(stripped)+normalize(desired) == normalize(current) {
			return false, nil
		}
	}

	out := stripped
	if !strings.HasSuffix(out, "\n") && out != "" {
		out += "\n"
	}
	out += desired
	return true, atomicWrite(target.Path, out, 0644)
}

// Uninstall removes the moodies-managed block from target. Returns true if
// the file was changed (block was present). Other lines are untouched.
func Uninstall(target RCTarget) (changed bool, err error) {
	current, err := readOrEmpty(target.Path)
	if err != nil {
		return false, err
	}
	stripped, had := stripBlock(current)
	if !had {
		return false, nil
	}
	return true, atomicWrite(target.Path, stripped, 0644)
}

func readOrEmpty(path string) (string, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// stripBlock removes one moodies block (begin..end inclusive) from s.
func stripBlock(s string) (out string, had bool) {
	scanner := bufio.NewScanner(strings.NewReader(s))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var b strings.Builder
	inBlock := false
	for scanner.Scan() {
		line := scanner.Text()
		if !inBlock && strings.TrimSpace(line) == beginMarker {
			inBlock = true
			had = true
			continue
		}
		if inBlock {
			if strings.TrimSpace(line) == endMarker {
				inBlock = false
			}
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	out = b.String()
	if !strings.HasSuffix(s, "\n") && strings.HasSuffix(out, "\n") {
		out = strings.TrimRight(out, "\n")
	}
	return out, had
}

func normalize(s string) string {
	return strings.TrimRight(s, "\n") + "\n"
}

func atomicWrite(path, content string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
