package shellrc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallUninstallRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".zshrc")
	original := "# user's own line\nalias ll='ls -la'\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	target := RCTarget{Path: path, Shell: "zsh"}

	lines := PathPrependLines("zsh", "/Users/test/.moodies/bin")
	changed, err := InstallBlock(target, lines)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("first install: changed=false, want true")
	}

	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "# user's own line") {
		t.Errorf("user content lost: %q", string(got))
	}
	if !strings.Contains(string(got), `export PATH="/Users/test/.moodies/bin":$PATH`) {
		t.Errorf("PATH prepend missing: %q", string(got))
	}
	// The CA / proxy env vars must NEVER appear — that was the bug we shipped.
	for _, bad := range []string{"HTTPS_PROXY", "SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "NODE_EXTRA_CA_CERTS"} {
		if strings.Contains(string(got), bad) {
			t.Errorf("forbidden env var %s present: %q", bad, string(got))
		}
	}

	// Idempotent.
	changed2, _ := InstallBlock(target, lines)
	if changed2 {
		t.Error("second install with same lines should be no-op")
	}

	// Re-render with different content (e.g., dir moved) → rewrite.
	newLines := PathPrependLines("zsh", "/elsewhere/bin")
	changed3, _ := InstallBlock(target, newLines)
	if !changed3 {
		t.Error("install with new lines should rewrite")
	}
	got, _ = os.ReadFile(path)
	if !strings.Contains(string(got), "/elsewhere/bin") {
		t.Errorf("new dir not applied: %q", string(got))
	}
	if strings.Contains(string(got), "/Users/test/.moodies/bin") {
		t.Errorf("old dir still present: %q", string(got))
	}

	// Uninstall.
	undid, _ := Uninstall(target)
	if !undid {
		t.Error("uninstall should report changed=true")
	}
	got, _ = os.ReadFile(path)
	if strings.Contains(string(got), "moodies") {
		t.Errorf("uninstall left moodies block behind: %q", string(got))
	}
	if !strings.Contains(string(got), "alias ll='ls -la'") {
		t.Errorf("uninstall ate user content: %q", string(got))
	}

	undidAgain, _ := Uninstall(target)
	if undidAgain {
		t.Error("second uninstall should be no-op")
	}
}

func TestFishUsesSetGx(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.fish")
	_ = os.WriteFile(path, []byte(""), 0644)
	target := RCTarget{Path: path, Shell: "fish"}

	lines := PathPrependLines("fish", "/some/dir/bin")
	if _, err := InstallBlock(target, lines); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if strings.Contains(string(got), "export ") {
		t.Errorf("fish rc should not use export: %q", string(got))
	}
	if !strings.Contains(string(got), "set -gx PATH /some/dir/bin $PATH") {
		t.Errorf("fish block missing set -gx: %q", string(got))
	}
}

func TestInstallOnNonexistentFileCreatesIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".zshrc")
	target := RCTarget{Path: path, Shell: "zsh"}

	changed, err := InstallBlock(target, PathPrependLines("zsh", "/x/bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("install on missing file should create it")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

func TestStripBlockHandlesNoBlock(t *testing.T) {
	in := "line 1\nline 2\n"
	out, had := stripBlock(in)
	if had {
		t.Error("had=true on input without block")
	}
	if out != in {
		t.Errorf("stripBlock mutated input without block: %q != %q", out, in)
	}
}
