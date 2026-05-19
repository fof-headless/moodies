package filter

import (
	"encoding/json"
	"sort"
	"strings"
)

// ParsedSSE is the accumulated state from walking an Anthropic SSE response.
type ParsedSSE struct {
	ContentText  string
	ToolUses     []ToolUse
	StopReason   string
	InputTokens  int
	OutputTokens int
}

// ParseSSE walks `data: ...` lines and accumulates assistant text deltas,
// tool_use blocks, stop_reason, and usage tokens. Tracks SSE shape used by
// both /v1/messages and chat_conversations completion endpoints.
func ParseSSE(raw string) ParsedSSE {
	var textParts []string
	toolBuf := map[int]*toolBuilder{}
	out := ParsedSSE{}

	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := line[6:]
		if payload == "[DONE]" {
			break
		}
		var data map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &data); err != nil {
			continue
		}
		var etype string
		if v, ok := data["type"]; ok {
			_ = json.Unmarshal(v, &etype)
		}

		switch etype {
		case "content_block_start":
			var idx int
			if v, ok := data["index"]; ok {
				_ = json.Unmarshal(v, &idx)
			}
			var block map[string]json.RawMessage
			if v, ok := data["content_block"]; ok {
				_ = json.Unmarshal(v, &block)
			}
			var btype, name, id string
			if v, ok := block["type"]; ok {
				_ = json.Unmarshal(v, &btype)
			}
			if btype == "tool_use" {
				if v, ok := block["name"]; ok {
					_ = json.Unmarshal(v, &name)
				}
				if v, ok := block["id"]; ok {
					_ = json.Unmarshal(v, &id)
				}
				toolBuf[idx] = &toolBuilder{Name: name, ID: id}
			}

		case "content_block_delta":
			var idx int
			if v, ok := data["index"]; ok {
				_ = json.Unmarshal(v, &idx)
			}
			var delta map[string]json.RawMessage
			if v, ok := data["delta"]; ok {
				_ = json.Unmarshal(v, &delta)
			}
			var dtype string
			if v, ok := delta["type"]; ok {
				_ = json.Unmarshal(v, &dtype)
			}
			switch dtype {
			case "text_delta":
				var text string
				if v, ok := delta["text"]; ok {
					_ = json.Unmarshal(v, &text)
				}
				textParts = append(textParts, text)
			case "input_json_delta":
				if tb, ok := toolBuf[idx]; ok {
					var partial string
					if v, ok := delta["partial_json"]; ok {
						_ = json.Unmarshal(v, &partial)
					}
					tb.Input.WriteString(partial)
				}
			}

		case "message_delta":
			var delta map[string]json.RawMessage
			if v, ok := data["delta"]; ok {
				_ = json.Unmarshal(v, &delta)
			}
			if v, ok := delta["stop_reason"]; ok {
				var sr string
				if json.Unmarshal(v, &sr) == nil && sr != "" {
					out.StopReason = sr
				}
			}
			if v, ok := data["usage"]; ok {
				var usage struct {
					OutputTokens int `json:"output_tokens"`
				}
				if json.Unmarshal(v, &usage) == nil {
					out.OutputTokens = usage.OutputTokens
				}
			}

		case "message_start":
			var msg map[string]json.RawMessage
			if v, ok := data["message"]; ok {
				_ = json.Unmarshal(v, &msg)
			}
			if v, ok := msg["usage"]; ok {
				var usage struct {
					InputTokens int `json:"input_tokens"`
				}
				if json.Unmarshal(v, &usage) == nil {
					out.InputTokens = usage.InputTokens
				}
			}
		}
	}

	out.ContentText = strings.Join(textParts, "")

	keys := make([]int, 0, len(toolBuf))
	for k := range toolBuf {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, k := range keys {
		tb := toolBuf[k]
		var input json.RawMessage
		raw := tb.Input.String()
		if raw != "" {
			// Try to parse into a generic value; if invalid, send under _raw.
			var anyVal interface{}
			if err := json.Unmarshal([]byte(raw), &anyVal); err == nil {
				input, _ = json.Marshal(anyVal)
			} else {
				input, _ = json.Marshal(map[string]string{"_raw": raw})
			}
		}
		out.ToolUses = append(out.ToolUses, ToolUse{
			ToolName:  tb.Name,
			ToolInput: input,
			ToolUseID: tb.ID,
		})
	}
	return out
}

type toolBuilder struct {
	Name  string
	ID    string
	Input strings.Builder
}
