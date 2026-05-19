package filter

import (
	"encoding/json"
	"strings"
	"testing"
)

func defaultApplyConfig(t *testing.T) ApplyConfig {
	t.Helper()
	patterns, errs := CompilePatterns([]string{
		`anthropic_key:sk-ant-(?:api|sid)\d+-[A-Za-z0-9_-]+`,
		`email:[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`,
	})
	if len(errs) > 0 {
		t.Fatalf("compile patterns: %v", errs)
	}
	return ApplyConfig{
		Modules: ModuleToggles{
			Redaction:      true,
			Classification: true,
			Extraction:     true,
			BodyText:       true,
		},
		Headers: HeaderRules{
			Allowlist: []string{"content-type", "user-agent"},
			Blocklist: []string{"authorization", "cookie"},
		},
		Redactions:  patterns,
		StorageMode: "raw",
	}
}

func TestApplyCompletionPublicAPI(t *testing.T) {
	body := `{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"hi from test@example.com"}]}`
	rf := &RawFlow{
		EventID:    "evt-1",
		CapturedAt: "2026-05-09T12:00:00Z",
		Request: RawRequest{
			Method:    "POST",
			Host:      "api.anthropic.com",
			Path:      "/v1/messages",
			URL:       "https://api.anthropic.com/v1/messages",
			Headers:   map[string]string{"Content-Type": "application/json", "Authorization": "Bearer secret"},
			Body:      body,
			BodyBytes: len(body),
		},
		Response: RawResponse{
			StatusCode: 200,
			Headers:    map[string]string{"Content-Type": "application/json"},
			Body:       `{"id":"msg_x","stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":7},"content":[{"type":"text","text":"hello"}]}`,
		},
	}

	ev, err := Apply(rf, defaultApplyConfig(t))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	if ev.EndpointType != "completion" {
		t.Errorf("endpoint_type = %q, want completion", ev.EndpointType)
	}
	if _, ok := ev.RequestHeaders["authorization"]; ok {
		t.Errorf("authorization header should be blocked")
	}
	if ev.Completion == nil {
		t.Fatal("expected Completion data")
	}
	if ev.Completion.Model != "claude-3-5-sonnet" {
		t.Errorf("model = %q", ev.Completion.Model)
	}
	if ev.Completion.UserMessage == nil || !strings.Contains(ev.Completion.UserMessage.Text, "[REDACTED:email]") {
		t.Errorf("expected user message text to contain redacted email, got %+v", ev.Completion.UserMessage)
	}
	if ev.Policy.Classification != "flagged_pii" {
		t.Errorf("policy = %q, want flagged_pii", ev.Policy.Classification)
	}
	if ev.Completion.AssistantMessage == nil || ev.Completion.AssistantMessage.Text != "hello" {
		t.Errorf("assistant text = %+v", ev.Completion.AssistantMessage)
	}
}

func TestApplyHashOnlyDropsBodyText(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"sensitive prompt"}]}`
	rf := &RawFlow{
		EventID:    "evt-2",
		CapturedAt: "2026-05-09T12:00:00Z",
		Request: RawRequest{
			Method: "POST", Host: "api.anthropic.com", Path: "/v1/messages",
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    body, BodyBytes: len(body),
		},
		Response: RawResponse{StatusCode: 200, Headers: map[string]string{"Content-Type": "application/json"}, Body: `{}`},
	}
	cfg := defaultApplyConfig(t)
	cfg.StorageMode = "hash_only"
	cfg.Modules.BodyText = false

	ev, err := Apply(rf, cfg)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if ev.RequestBody == nil {
		t.Fatal("expected request body wrapper")
	}
	if ev.RequestBody.Text != "" {
		t.Errorf("Text should be omitted in hash_only mode, got %q", ev.RequestBody.Text)
	}
	if ev.RequestBody.SHA256 == "" || ev.RequestBody.Chars == 0 {
		t.Errorf("expected sha256+chars to be populated, got %+v", ev.RequestBody)
	}
	if ev.Completion != nil && ev.Completion.UserMessage != nil && ev.Completion.UserMessage.Text != "" {
		t.Errorf("user message text should be empty in hash_only mode")
	}
}

func TestApplyRedactsSecretInBody(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"my key is sk-ant-api01-AAAA_bbbb-CCCC dont share"}]}`
	rf := &RawFlow{
		EventID:    "evt-3",
		CapturedAt: "2026-05-09T12:00:00Z",
		Request: RawRequest{
			Method: "POST", Host: "api.anthropic.com", Path: "/v1/messages",
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    body, BodyBytes: len(body),
		},
		Response: RawResponse{StatusCode: 200, Headers: map[string]string{"Content-Type": "application/json"}, Body: `{}`},
	}

	ev, err := Apply(rf, defaultApplyConfig(t))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if strings.Contains(ev.RequestBody.Text, "sk-ant-api01-AAAA_bbbb-CCCC") {
		t.Errorf("secret leaked into request body text")
	}
	if ev.Policy.Classification != "flagged_secret" {
		t.Errorf("policy = %q, want flagged_secret", ev.Policy.Classification)
	}
	var seen bool
	for _, r := range ev.Redactions {
		if r.Pattern == "anthropic_key" && r.Field == "request.body" && r.Count > 0 {
			seen = true
		}
	}
	if !seen {
		raw, _ := json.Marshal(ev.Redactions)
		t.Errorf("expected anthropic_key redaction on request.body, got %s", string(raw))
	}
}

func TestApplyClassificationDisabled(t *testing.T) {
	rf := &RawFlow{
		EventID:    "evt-4",
		CapturedAt: "2026-05-09T12:00:00Z",
		Request:    RawRequest{Method: "POST", Path: "/v1/messages", Headers: map[string]string{}},
		Response:   RawResponse{StatusCode: 200, Headers: map[string]string{}},
	}
	cfg := defaultApplyConfig(t)
	cfg.Modules.Classification = false
	cfg.Modules.Extraction = false

	ev, err := Apply(rf, cfg)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if ev.EndpointType != "unfiltered" {
		t.Errorf("endpoint_type = %q, want unfiltered", ev.EndpointType)
	}
	if ev.Completion != nil {
		t.Errorf("completion should be nil when extraction disabled")
	}
}
