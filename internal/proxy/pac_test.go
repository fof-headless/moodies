package proxy_test

import (
	"strings"
	"testing"

	"github.com/doomsday/agent/internal/proxy"
)

func TestPACGeneration(t *testing.T) {
	pac := proxy.GeneratePAC(8080)
	if !strings.Contains(pac, "PROXY 127.0.0.1:8080") {
		t.Errorf("PAC missing proxy line: %s", pac)
	}
	if !strings.Contains(pac, "*.anthropic.com") {
		t.Error("PAC missing anthropic.com")
	}
	if !strings.Contains(pac, "*.claude.ai") {
		t.Error("PAC missing claude.ai")
	}
	if !strings.Contains(pac, "return \"DIRECT\"") {
		t.Error("PAC missing DIRECT fallback")
	}
}
