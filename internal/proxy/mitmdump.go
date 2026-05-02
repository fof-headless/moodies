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
	mitmdump, err := resolveMitmdump()
	if err != nil {
		return nil, err
	}

	// Use mitmproxy's default confdir (~/.mitmproxy). The install step
	// (cert.go) generates and trusts the CA cert at that location; if we
	// override confdir here, mitmdump auto-generates a *different* CA at
	// runtime and TLS handshakes present an untrusted cert that browsers
	// reject (especially HSTS-preloaded sites).
	args := []string{
		"--listen-port", fmt.Sprint(port),
		"-s", SanitizerPath(),
		"--set", "termlog_verbosity=warn",
		"--set", "flow_detail=0",
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
