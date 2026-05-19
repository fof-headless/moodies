package filter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// extractClient infers app/version/platform from a request's headers.
// Mirrors the original sanitizer's extract_client + a fallback for the web app
// (which sends no anthropic-client-* headers but a User-Agent like
// "Claude-Web/...").
func extractClient(headers map[string]string) *ClientInfo {
	// Headers come in case-preserved from mitmproxy; do a case-insensitive get.
	get := func(name string) string {
		lower := strings.ToLower(name)
		for k, v := range headers {
			if strings.ToLower(k) == lower {
				return v
			}
		}
		return ""
	}

	app := get("anthropic-client-app")
	version := get("anthropic-client-version")
	platform := get("anthropic-client-platform")

	if app == "" {
		ua := strings.ToLower(get("user-agent"))
		switch {
		case strings.Contains(ua, "claudefordesktop"):
			app = "com.anthropic.claudefordesktop"
			platform = "desktop_app"
		case strings.Contains(ua, "claude-web") || strings.Contains(ua, "mozilla"):
			// Browser hitting claude.ai/api/...
			app = "web_claude_ai"
			platform = "web"
		case strings.Contains(ua, "python"):
			app = "python_sdk"
		case strings.Contains(ua, "node"):
			app = "node_sdk"
		}
	}

	if app == "" && version == "" && platform == "" {
		return nil
	}
	return &ClientInfo{App: app, Version: version, Platform: platform}
}

// extractCompletion parses both the public (/v1/messages) and the web app
// (chat_conversations/.../completion) request shapes into a CompletionData.
// Falls back gracefully on unknown shapes — fields are best-effort.
func extractCompletion(rf *RawFlow, storeText bool) *CompletionData {
	out := &CompletionData{}

	var reqBody map[string]json.RawMessage
	if rf.Request.Body != "" {
		_ = json.Unmarshal([]byte(rf.Request.Body), &reqBody)
	}

	// Model — same field on both shapes.
	if v, ok := reqBody["model"]; ok {
		var s string
		_ = json.Unmarshal(v, &s)
		out.Model = s
	}

	// system prompt presence.
	if _, ok := reqBody["system"]; ok {
		out.SystemPromptPresent = true
	} else if _, ok := reqBody["system_prompt"]; ok {
		out.SystemPromptPresent = true
	}

	// Tools declared.
	if v, ok := reqBody["tools"]; ok {
		var tools []map[string]json.RawMessage
		if json.Unmarshal(v, &tools) == nil {
			for _, t := range tools {
				name := ""
				if nv, ok := t["name"]; ok {
					_ = json.Unmarshal(nv, &name)
				}
				if name == "" {
					if fnRaw, ok := t["function"]; ok {
						var fn map[string]json.RawMessage
						if json.Unmarshal(fnRaw, &fn) == nil {
							if nv, ok := fn["name"]; ok {
								_ = json.Unmarshal(nv, &name)
							}
						}
					}
				}
				out.ToolsDeclared = append(out.ToolsDeclared, ToolDeclaration{
					Name:      name,
					Category:  ClassifyTool(name),
					MCPServer: ExtractMCPServer(name),
				})
			}
		}
	}

	// Conversation UUID — try URL first, then body fields.
	out.ConversationUUID = ExtractConversationUUID(rf.Request.Path)

	// User message — try multiple shapes:
	// 1) public API: messages[] with role=user, content=string|blocks
	// 2) web app: top-level "prompt" or "text" string
	// 3) web app variant: "completion" wrapper with prompt inside
	userText, attachments := extractUserText(reqBody)
	if userText != "" || len(attachments) > 0 {
		um := &Message{Role: "user"}
		if userText != "" {
			um.Chars = len(userText)
			um.SHA256 = sha256hex(userText)
			if storeText {
				um.Text = userText
			}
		}
		if len(attachments) > 0 {
			um.Attachments = attachments
		}
		out.UserMessage = um
	}

	// Response — depends on content-type.
	asst := &AssistantMessage{Role: "assistant"}
	contentType := lookupHeader(rf.Response.Headers, "content-type")

	if strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		sse := ParseSSE(rf.Response.Body)
		asst.StopReason = sse.StopReason
		asst.InputTokens = sse.InputTokens
		asst.OutputTokens = sse.OutputTokens
		asst.ToolUses = sse.ToolUses
		if sse.ContentText != "" {
			asst.Chars = len(sse.ContentText)
			asst.SHA256 = sha256hex(sse.ContentText)
			if storeText {
				asst.Text = sse.ContentText
			}
		}
	} else {
		// JSON response (public API non-streaming).
		var respBody map[string]json.RawMessage
		_ = json.Unmarshal([]byte(rf.Response.Body), &respBody)
		if v, ok := respBody["id"]; ok {
			var s string
			_ = json.Unmarshal(v, &s)
			out.MessageUUID = s
		}
		if v, ok := respBody["stop_reason"]; ok {
			var s string
			_ = json.Unmarshal(v, &s)
			asst.StopReason = s
		}
		if v, ok := respBody["usage"]; ok {
			var u struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			}
			_ = json.Unmarshal(v, &u)
			asst.InputTokens = u.InputTokens
			asst.OutputTokens = u.OutputTokens
		}
		var asstText strings.Builder
		if v, ok := respBody["content"]; ok {
			var blocks []map[string]json.RawMessage
			if json.Unmarshal(v, &blocks) == nil {
				for _, blk := range blocks {
					var btype string
					if bv, ok := blk["type"]; ok {
						_ = json.Unmarshal(bv, &btype)
					}
					switch btype {
					case "text":
						if tv, ok := blk["text"]; ok {
							var s string
							_ = json.Unmarshal(tv, &s)
							asstText.WriteString(s)
						}
					case "tool_use":
						tu := ToolUse{}
						if nv, ok := blk["name"]; ok {
							_ = json.Unmarshal(nv, &tu.ToolName)
						}
						if iv, ok := blk["input"]; ok {
							tu.ToolInput = iv
						}
						if idv, ok := blk["id"]; ok {
							_ = json.Unmarshal(idv, &tu.ToolUseID)
						}
						asst.ToolUses = append(asst.ToolUses, tu)
					}
				}
			}
		}
		t := asstText.String()
		if t != "" {
			asst.Chars = len(t)
			asst.SHA256 = sha256hex(t)
			if storeText {
				asst.Text = t
			}
		}
	}

	if asst.Chars > 0 || asst.StopReason != "" || len(asst.ToolUses) > 0 ||
		asst.InputTokens > 0 || asst.OutputTokens > 0 {
		out.AssistantMessage = asst
	}

	return out
}

// extractUserText walks every known shape and returns the most recent user
// prompt + any attachments.
func extractUserText(reqBody map[string]json.RawMessage) (string, []Attachment) {
	// Shape 1: messages[] (public API).
	if v, ok := reqBody["messages"]; ok {
		var msgs []map[string]json.RawMessage
		if json.Unmarshal(v, &msgs) == nil {
			for i := len(msgs) - 1; i >= 0; i-- {
				m := msgs[i]
				var role string
				if r, ok := m["role"]; ok {
					_ = json.Unmarshal(r, &role)
				}
				if role != "user" {
					continue
				}
				cv, ok := m["content"]
				if !ok {
					continue
				}
				// content can be a string or an array of blocks.
				var s string
				if json.Unmarshal(cv, &s) == nil && s != "" {
					return s, nil
				}
				var blocks []map[string]json.RawMessage
				if json.Unmarshal(cv, &blocks) == nil {
					var parts []string
					var atts []Attachment
					for _, blk := range blocks {
						var bt string
						if bv, ok := blk["type"]; ok {
							_ = json.Unmarshal(bv, &bt)
						}
						switch bt {
						case "text":
							var t string
							if tv, ok := blk["text"]; ok {
								_ = json.Unmarshal(tv, &t)
							}
							parts = append(parts, t)
						case "image", "document":
							a := Attachment{}
							if nv, ok := blk["name"]; ok {
								_ = json.Unmarshal(nv, &a.Filename)
							}
							var src map[string]string
							if sv, ok := blk["source"]; ok {
								_ = json.Unmarshal(sv, &src)
							}
							a.MIME = src["media_type"]
							a.FileUUID = src["url"]
							atts = append(atts, a)
						}
					}
					return strings.Join(parts, "\n"), atts
				}
			}
		}
	}
	// Shape 2: top-level "prompt" string (web app sometimes).
	if v, ok := reqBody["prompt"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil && s != "" {
			return s, nil
		}
	}
	// Shape 3: top-level "text".
	if v, ok := reqBody["text"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil && s != "" {
			return s, nil
		}
	}
	// Shape 4: nested under "completion" (older web shape).
	if v, ok := reqBody["completion"]; ok {
		var sub map[string]json.RawMessage
		if json.Unmarshal(v, &sub) == nil {
			if pv, ok := sub["prompt"]; ok {
				var s string
				if json.Unmarshal(pv, &s) == nil && s != "" {
					return s, nil
				}
			}
		}
	}
	return "", nil
}

func extractConversationFetch(rf *RawFlow) *ConversationFetchData {
	var body map[string]json.RawMessage
	_ = json.Unmarshal([]byte(rf.Response.Body), &body)
	out := &ConversationFetchData{}
	if v, ok := body["name"]; ok {
		var s string
		_ = json.Unmarshal(v, &s)
		out.Title = s
	}
	if out.Title == "" {
		if v, ok := body["title"]; ok {
			var s string
			_ = json.Unmarshal(v, &s)
			out.Title = s
		}
	}
	if v, ok := body["chat_messages"]; ok {
		var msgs []json.RawMessage
		_ = json.Unmarshal(v, &msgs)
		out.MessageCount = len(msgs)
	}
	return out
}

func extractAccount(rf *RawFlow) *AccountInfo {
	out := &AccountInfo{
		OrgUUID: ExtractOrgUUID(rf.Request.Path),
	}
	var body map[string]json.RawMessage
	_ = json.Unmarshal([]byte(rf.Response.Body), &body)
	for _, key := range []string{"uuid", "id", "account_id"} {
		if v, ok := body[key]; ok {
			var s string
			if json.Unmarshal(v, &s) == nil && s != "" {
				out.AccountUUID = s
				break
			}
		}
	}
	if v, ok := body["organizations"]; ok {
		var orgs []map[string]json.RawMessage
		if json.Unmarshal(v, &orgs) == nil && len(orgs) > 0 {
			if ov, ok := orgs[0]["uuid"]; ok {
				var s string
				if json.Unmarshal(ov, &s) == nil && s != "" {
					out.OrgUUID = s
				}
			}
		}
	}
	return out
}

func extractUpload(rf *RawFlow) *UploadData {
	contentType := lookupHeader(rf.Request.Headers, "content-type")
	contentLength := lookupHeader(rf.Request.Headers, "content-length")
	size := atoi(contentLength)
	if size == 0 {
		size = rf.Request.BodyBytes
	}
	out := &UploadData{
		MIMEType:  contentType,
		SizeBytes: size,
	}
	// Hash bodies under 10 MiB only.
	if size > 0 && size < 10*1024*1024 && rf.Request.Body != "" {
		out.FileSHA256 = sha256hex(rf.Request.Body)
	}
	return out
}

func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func lookupHeader(h map[string]string, name string) string {
	low := strings.ToLower(name)
	for k, v := range h {
		if strings.ToLower(k) == low {
			return v
		}
	}
	return ""
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}
