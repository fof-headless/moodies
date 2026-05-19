package proxy

import (
	"net"
	"testing"
	"time"
)

// TestIsPortListening exercises the proxy-health probe the watchdog uses to
// decide whether to enable PAC. A live listener should be visible; a closed
// port should not.
func TestIsPortListening(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	if !IsPortListening(port, 500*time.Millisecond) {
		t.Errorf("IsPortListening(%d) = false while listener is live", port)
	}

	_ = ln.Close()
	// Give the kernel a moment to actually free the port.
	time.Sleep(50 * time.Millisecond)

	if IsPortListening(port, 200*time.Millisecond) {
		t.Errorf("IsPortListening(%d) = true after listener closed", port)
	}
}

// TestIsPortListeningTimesOut ensures the probe doesn't hang on a port that
// nobody is listening on (RST or dropped SYN, depending on the OS).
func TestIsPortListeningTimesOut(t *testing.T) {
	// 1 — RFC-reserved; nothing should be listening here. The dial should
	// fail fast (ECONNREFUSED) rather than time out, but either outcome
	// is a "false" return — that's all the watchdog cares about.
	start := time.Now()
	got := IsPortListening(1, 500*time.Millisecond)
	elapsed := time.Since(start)

	if got {
		t.Errorf("IsPortListening(1) = true, want false")
	}
	if elapsed > 800*time.Millisecond {
		t.Errorf("IsPortListening took %v, want < 800ms (timeout failure)", elapsed)
	}
}

// TestListNetworkServices is a smoke test — it just shells out to
// networksetup and confirms the parser produces a non-empty list with the
// help-text and disabled-marker lines filtered out. Skip if networksetup
// isn't on PATH (CI containers).
func TestListNetworkServices(t *testing.T) {
	services, err := ListNetworkServices()
	if err != nil {
		t.Skipf("networksetup unavailable: %v", err)
	}
	if len(services) == 0 {
		t.Errorf("expected at least one network service, got none")
	}
	for _, s := range services {
		if s == "" || s[0] == '*' {
			t.Errorf("ListNetworkServices returned disabled/empty entry: %q", s)
		}
		if len(s) > 8 && s[:8] == "An aster" {
			t.Errorf("ListNetworkServices leaked the help-line: %q", s)
		}
	}
}
