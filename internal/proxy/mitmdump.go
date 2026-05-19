package proxy

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

//go:embed mitm_tap.py
var mitmTapSource []byte

type MitmdumpProcess struct {
	cmd     *exec.Cmd
	stopped bool
}

// SpawnOptions are the runtime knobs the daemon passes when starting mitmdump.
type SpawnOptions struct {
	Port        int
	OutputPath  string   // where the tap writes raw flows (JSONL)
	TargetHosts []string // host suffixes (".foo.com") or exact matches passed to the tap via env
}

// AddonPath is where the embedded tap is written before mitmdump loads it.
func AddonPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".doomsday", "_tap.py")
}

// writeAddon flushes the embedded mitm_tap.py to disk so mitmdump can `-s` it.
// Done on every spawn so reinstalls / version bumps always get the bundled copy.
func writeAddon() (string, error) {
	path := AddonPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", fmt.Errorf("addon mkdir: %w", err)
	}
	if err := os.WriteFile(path, mitmTapSource, 0644); err != nil {
		return "", fmt.Errorf("addon write: %w", err)
	}
	return path, nil
}

func SpawnMitmdump(opts SpawnOptions) (*MitmdumpProcess, error) {
	mitmdump, err := resolveMitmdump()
	if err != nil {
		return nil, err
	}

	addonPath, err := writeAddon()
	if err != nil {
		return nil, err
	}

	// Default confdir (~/.mitmproxy) intentionally — that's where install
	// generates and trusts the CA. Overriding confdir here would make
	// mitmdump auto-generate a different (untrusted) CA at runtime.
	args := []string{
		"--listen-port", fmt.Sprint(opts.Port),
		"-s", addonPath,
		"--set", "termlog_verbosity=warn",
		"--set", "flow_detail=0",
	}

	targets := opts.TargetHosts
	if len(targets) == 0 {
		targets = DefaultTargetHosts()
	}

	cmd := exec.Command(mitmdump, args...)
	cmd.Env = append(os.Environ(),
		"DOOMSDAY_OUTPUT="+opts.OutputPath,
		"DOOMSDAY_TARGET_HOSTS="+strings.Join(targets, ","),
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn mitmdump: %w", err)
	}
	return &MitmdumpProcess{cmd: cmd}, nil
}

// DefaultTargetHosts is the conservative built-in list. Config can override.
func DefaultTargetHosts() []string {
	return []string{
		".anthropic.com",
		"claude.ai",
		".claude.ai",
		".claudeusercontent.com",
	}
}

func (m *MitmdumpProcess) Kill() {
	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
	}
	m.stopped = true
}

func (m *MitmdumpProcess) Wait() error {
	return m.cmd.Wait()
}

// RestartWithBackoff restarts mitmdump when it dies, with exponential backoff.
func RestartWithBackoff(opts SpawnOptions, maxBackoff time.Duration) (*MitmdumpProcess, error) {
	backoff := time.Second
	for {
		p, err := SpawnMitmdump(opts)
		if err == nil {
			return p, nil
		}
		time.Sleep(backoff)
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}
