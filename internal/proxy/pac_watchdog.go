package proxy

import (
	"context"
	"fmt"
	"log"
	"net"
	"os/exec"
	"strings"
	"time"
)

// PACServiceState is what `networksetup -getautoproxyurl <service>` reports.
type PACServiceState struct {
	Service string
	URL     string
	Enabled bool
}

// ListNetworkServices returns the names of every network service macOS knows
// about, minus the leading "An asterisk..." help line and any service marked
// disabled (prefixed with '*'). The list is live — plugging in Ethernet
// shows up on the next call.
func ListNetworkServices() ([]string, error) {
	out, err := exec.Command("networksetup", "-listallnetworkservices").Output()
	if err != nil {
		return nil, fmt.Errorf("listallnetworkservices: %w", err)
	}
	var services []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "An asterisk") || strings.HasPrefix(line, "*") {
			continue
		}
		services = append(services, line)
	}
	return services, nil
}

// GetPACState parses `networksetup -getautoproxyurl <service>`. macOS reports
// a (URL, Enabled) pair even if PAC was never configured (URL comes back
// empty in that case). Returns a zero-value PACServiceState on parse errors
// rather than failing — the watchdog should keep going.
func GetPACState(service string) PACServiceState {
	st := PACServiceState{Service: service}
	out, err := exec.Command("networksetup", "-getautoproxyurl", service).Output()
	if err != nil {
		return st
	}
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "URL: "):
			st.URL = strings.TrimSpace(strings.TrimPrefix(line, "URL: "))
		case strings.HasPrefix(line, "Enabled: "):
			st.Enabled = strings.TrimSpace(strings.TrimPrefix(line, "Enabled: ")) == "Yes"
		}
	}
	return st
}

// EnsurePAC walks every live network service and brings PAC into the
// expected state (URL = pacURL, Enabled = Yes). Services whose state already
// matches are left alone — this is idempotent and safe to call every few
// seconds. Returns the names of services that were modified this tick.
func EnsurePAC(pacURL string) ([]string, error) {
	services, err := ListNetworkServices()
	if err != nil {
		return nil, err
	}
	var changed []string
	for _, svc := range services {
		cur := GetPACState(svc)
		if cur.URL == pacURL && cur.Enabled {
			continue
		}
		// `-setautoproxyurl` both writes the URL and enables PAC in one call,
		// so a single command covers "URL drifted" and "Enabled flipped to No".
		if err := exec.Command("networksetup", "-setautoproxyurl", svc, pacURL).Run(); err != nil {
			// Some pseudo-services (e.g. "Bluetooth PAN") may reject the
			// command. Skip and continue rather than aborting the whole tick.
			continue
		}
		changed = append(changed, svc)
	}
	return changed, nil
}

// DisablePAC turns PAC off on every service that currently has it enabled.
// Used by the watchdog when mitmdump isn't accepting connections — we'd
// rather route traffic direct than to a dead proxy. Idempotent. Returns
// the names of services that were touched this tick.
func DisablePAC() ([]string, error) {
	services, err := ListNetworkServices()
	if err != nil {
		return nil, err
	}
	var changed []string
	for _, svc := range services {
		cur := GetPACState(svc)
		if !cur.Enabled {
			continue
		}
		if err := exec.Command("networksetup", "-setautoproxystate", svc, "off").Run(); err != nil {
			continue
		}
		changed = append(changed, svc)
	}
	return changed, nil
}

// IsPortListening returns true if a TCP connection to 127.0.0.1:<port>
// succeeds within `timeout`. Used as the proxy-health probe for the watchdog.
func IsPortListening(port int, timeout time.Duration) bool {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// EnforcePACLoop ties PAC state to mitmdump health: PAC is only enabled
// while the proxy port is accepting connections, and is disabled whenever
// it isn't. This eliminates the "phantom proxy" failure mode where PAC is
// active but the listener is dead (during startup, between crash + respawn,
// after `moodies-disable` etc.). Users always have working network
// connectivity — either via the proxy or direct — never broken.
//
// shouldStop is consulted every tick; if it returns true (e.g., the
// disable_marker file appears mid-run), the loop disables PAC and exits.
//
// onEnable / onDisable are called only on state transitions so callers can
// log noisily without per-tick spam, and so state.json gets updated with
// the actual list of services that were touched.
func EnforcePACLoop(
	ctx context.Context,
	pacURL string,
	proxyPort int,
	interval time.Duration,
	shouldStop func() bool,
	onEnable func(services []string),
	onDisable func(services []string),
) {
	tick := func() {
		if shouldStop != nil && shouldStop() {
			disabled, _ := DisablePAC()
			if len(disabled) > 0 {
				log.Printf("[pac] disable_marker present, turning PAC off on: %s", strings.Join(disabled, ", "))
				if onDisable != nil {
					onDisable(disabled)
				}
			}
			return
		}

		if IsPortListening(proxyPort, 500*time.Millisecond) {
			changed, err := EnsurePAC(pacURL)
			if err != nil {
				log.Printf("[pac] ensure: %v", err)
				return
			}
			if len(changed) > 0 {
				log.Printf("[pac] enabled on: %s (proxy up)", strings.Join(changed, ", "))
				if onEnable != nil {
					onEnable(changed)
				}
			}
			return
		}

		// Proxy is not listening. Turn PAC off so the user's traffic goes
		// direct rather than into a black hole. The mitmdump watchdog will
		// (re)spawn the proxy on its own ticker; we'll re-enable next tick.
		changed, err := DisablePAC()
		if err != nil {
			log.Printf("[pac] disable: %v", err)
			return
		}
		if len(changed) > 0 {
			log.Printf("[pac] disabled on: %s (proxy down — routing direct)", strings.Join(changed, ", "))
			if onDisable != nil {
				onDisable(changed)
			}
		}
	}

	tick()

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
			if shouldStop != nil && shouldStop() {
				return
			}
		}
	}
}
