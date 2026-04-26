package proxy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type MitmdumpProcess struct {
	cmd     *exec.Cmd
	stopped bool
}

func SanitizerPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".doomsday", "sanitizer.py")
}

func SpawnMitmdump(port int, storageMode, outputPath string) (*MitmdumpProcess, error) {
	home, _ := os.UserHomeDir()
	confDir := filepath.Join(home, ".doomsday", "mitmproxy")
	if err := os.MkdirAll(confDir, 0700); err != nil {
		return nil, fmt.Errorf("confdir: %w", err)
	}

	mitmdump, err := resolveMitmdump()
	if err != nil {
		return nil, err
	}

	args := []string{
		"--listen-port", fmt.Sprint(port),
		"--set", fmt.Sprintf("confdir=%s", confDir),
		"--set", "ignore_hosts=^(?!.*(anthropic\\.com|claude\\.ai|claudeusercontent\\.com)).*$",
		"-s", SanitizerPath(),
		"--quiet",
	}

	cmd := exec.Command(mitmdump, args...)
	cmd.Env = append(os.Environ(),
		"STORAGE_MODE="+storageMode,
		"DOOMSDAY_OUTPUT="+outputPath,
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn mitmdump: %w", err)
	}
	return &MitmdumpProcess{cmd: cmd}, nil
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
func RestartWithBackoff(port int, storageMode, outputPath string, maxBackoff time.Duration) (*MitmdumpProcess, error) {
	backoff := time.Second
	for {
		p, err := SpawnMitmdump(port, storageMode, outputPath)
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
