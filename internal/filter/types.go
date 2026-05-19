package filter

import "encoding/json"

// RawFlow is what the mitmproxy tap (internal/proxy/mitm_tap.py) writes.
// One JSON-line per intercepted HTTP response.
type RawFlow struct {
	EventID    string         `json:"event_id"`
	CapturedAt string         `json:"captured_at"`
	Request    RawRequest     `json:"request"`
	Response   RawResponse    `json:"response"`
	DurationMs int            `json:"duration_ms"`
}

type RawRequest struct {
	Method    string            `json:"method"`
	Scheme    string            `json:"scheme"`
	Host      string            `json:"host"`
	Path      string            `json:"path"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"`
	BodyBytes int               `json:"body_bytes"`
}

type RawResponse struct {
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	BodyBytes  int               `json:"body_bytes"`
}

// Event is the filtered, structured payload the daemon stores in SQLite
// (verbatim) and POSTs to the backend. See SCHEMA.md for the wire contract.
type Event struct {
	EventID    string `json:"event_id"`
	CapturedAt string `json:"captured_at"` // RFC3339 UTC

	// Endpoint classification. Set to "unknown" if no pattern matched
	// (the event is still emitted; backend can decide what to do).
	EndpointType string `json:"endpoint_type"`
	PathTemplate string `json:"path_template,omitempty"`

	Method        string `json:"method"`
	Host          string `json:"host"`
	URL           string `json:"url"`
	StatusCode    int    `json:"status_code"`
	DurationMs    int    `json:"duration_ms"`
	RequestBytes  int    `json:"request_bytes"`
	ResponseBytes int    `json:"response_bytes"`

	RequestHeaders  map[string]string `json:"request_headers"`
	ResponseHeaders map[string]string `json:"response_headers"`

	RequestBody  *Body `json:"request_body,omitempty"`
	ResponseBody *Body `json:"response_body,omitempty"`

	Client  *ClientInfo  `json:"client,omitempty"`
	Account *AccountInfo `json:"account,omitempty"`

	// Endpoint-specific extracted fields. Exactly one of these will be set
	// (matching EndpointType), or none for "unknown".
	Completion        *CompletionData        `json:"completion,omitempty"`
	ConversationFetch *ConversationFetchData `json:"conversation_fetch,omitempty"`
	Upload            *UploadData            `json:"upload,omitempty"`

	Policy     PolicyInfo       `json:"policy"`
	Redactions []RedactionAudit `json:"redactions,omitempty"`
}

type Body struct {
	// SHA256 of the post-redaction body. Always present.
	SHA256 string `json:"sha256"`
	// Character count of the post-redaction body string.
	Chars int `json:"chars"`
	// Decoded body. Omitted entirely when storage_mode = "hash_only".
	Text string `json:"text,omitempty"`
	// MIME content-type as advertised by the response/request header.
	ContentType string `json:"content_type,omitempty"`
}

type ClientInfo struct {
	App      string `json:"app,omitempty"`
	Version  string `json:"version,omitempty"`
	Platform string `json:"platform,omitempty"`
}

type AccountInfo struct {
	OrgUUID     string `json:"org_uuid,omitempty"`
	AccountUUID string `json:"account_uuid,omitempty"`
}

type CompletionData struct {
	Model               string             `json:"model,omitempty"`
	ConversationUUID    string             `json:"conversation_uuid,omitempty"`
	MessageUUID         string             `json:"message_uuid,omitempty"`
	ParentMessageUUID   string             `json:"parent_message_uuid,omitempty"`
	SystemPromptPresent bool               `json:"system_prompt_present"`
	ToolsDeclared       []ToolDeclaration  `json:"tools_declared,omitempty"`
	UserMessage         *Message           `json:"user_message,omitempty"`
	AssistantMessage    *AssistantMessage  `json:"assistant_message,omitempty"`
}

type Message struct {
	Role        string       `json:"role"`
	SHA256      string       `json:"sha256,omitempty"`
	Chars       int          `json:"chars"`
	Text        string       `json:"text,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

type AssistantMessage struct {
	Role         string    `json:"role"`
	SHA256       string    `json:"sha256,omitempty"`
	Chars        int       `json:"chars"`
	Text         string    `json:"text,omitempty"`
	StopReason   string    `json:"stop_reason,omitempty"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	ToolUses     []ToolUse `json:"tool_uses,omitempty"`
}

type Attachment struct {
	FileUUID string `json:"file_uuid,omitempty"`
	Filename string `json:"filename,omitempty"`
	MIME     string `json:"mime,omitempty"`
}

type ToolDeclaration struct {
	Name      string `json:"name"`
	Category  string `json:"category"` // "anthropic_builtin" | "mcp" | "unknown"
	MCPServer string `json:"mcp_server,omitempty"`
}

type ToolUse struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
}

type ConversationFetchData struct {
	Title        string `json:"title,omitempty"`
	MessageCount int    `json:"message_count"`
}

type UploadData struct {
	Filename  string `json:"filename,omitempty"`
	MIMEType  string `json:"mime_type,omitempty"`
	SizeBytes int    `json:"size_bytes"`
	FileSHA256 string `json:"file_sha256,omitempty"`
}

// PolicyInfo summarises which redaction patterns matched (clean if none).
type PolicyInfo struct {
	Classification string   `json:"classification"` // "clean" | "flagged_secret" | "flagged_pii"
	Matches        []string `json:"matches,omitempty"`
}

// RedactionAudit lets the backend (and operators) see what was scrubbed.
type RedactionAudit struct {
	Pattern string `json:"pattern"` // pattern name
	Field   string `json:"field"`   // "request.body" | "response.body" | "request.headers.<name>"
	Count   int    `json:"count"`   // number of replacements
}
