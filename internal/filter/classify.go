package filter

import (
	"regexp"
	"strings"
)

// EndpointDef matches one Anthropic API endpoint shape.
type EndpointDef struct {
	Method  string
	Pattern *regexp.Regexp
	Type    string
}

// Endpoints — derived from the original Python sanitizer's table.
// fullmatch semantics: a path must match the entire pattern.
var Endpoints = []EndpointDef{
	{"POST", regexp.MustCompile(`^/v1/messages$`), "completion"},
	{"POST", regexp.MustCompile(`^/api/organizations/[^/]+/chat_conversations/[^/]+/completion$`), "completion"},
	{"POST", regexp.MustCompile(`^/api/organizations/[^/]+/chat_conversations/[^/]+/retry_completion$`), "retry_completion"},
	{"GET", regexp.MustCompile(`^/api/organizations/[^/]+/chat_conversations$`), "conversation_list"},
	{"GET", regexp.MustCompile(`^/api/organizations/[^/]+/chat_conversations/[^/]+$`), "conversation_fetch"},
	{"POST", regexp.MustCompile(`^/api/organizations/[^/]+/upload$`), "upload"},
	{"GET", regexp.MustCompile(`^/api/account$`), "account"},
}

// Classify returns the endpoint type and the regex source string (for the
// path_template field). Returns "unknown" / "" if nothing matched.
func Classify(method, path string) (epType, template string) {
	// Strip query string.
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	for _, e := range Endpoints {
		if e.Method == method && e.Pattern.MatchString(path) {
			// Use FindStringSubmatchIndex with full pattern to mimic fullmatch.
			loc := e.Pattern.FindStringIndex(path)
			if loc != nil && loc[0] == 0 && loc[1] == len(path) {
				return e.Type, e.Pattern.String()
			}
		}
	}
	return "unknown", ""
}

// Tool classification mirrors the Python sanitizer's classify_tool helper.
var builtinPrefixes = []string{
	"web_", "bash", "computer_", "str_replace_", "text_editor_", "str_edit_", "file_",
}

func ClassifyTool(name string) string {
	for _, p := range builtinPrefixes {
		if strings.HasPrefix(name, p) || name == strings.TrimRight(p, "_") {
			return "anthropic_builtin"
		}
	}
	if strings.Contains(name, ":") {
		return "mcp"
	}
	return "unknown"
}

func ExtractMCPServer(name string) string {
	if i := strings.IndexByte(name, ':'); i > 0 {
		return name[:i]
	}
	return ""
}

// ExtractOrgUUID pulls the org UUID out of paths shaped like
// /api/organizations/<uuid>/... and returns "" if not present.
func ExtractOrgUUID(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if p == "organizations" && i+1 < len(parts) {
			// Strip query string in case the segment was the last one.
			seg := parts[i+1]
			if j := strings.IndexByte(seg, '?'); j >= 0 {
				seg = seg[:j]
			}
			return seg
		}
	}
	return ""
}

// ExtractConversationUUID for paths like /chat_conversations/<uuid>(/...)?
func ExtractConversationUUID(path string) string {
	const marker = "/chat_conversations/"
	i := strings.Index(path, marker)
	if i < 0 {
		return ""
	}
	rest := path[i+len(marker):]
	// Stop at next slash or query.
	for k, c := range rest {
		if c == '/' || c == '?' {
			return rest[:k]
		}
	}
	return rest
}
