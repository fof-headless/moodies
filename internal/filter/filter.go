package filter

import (
	"strings"
)

// ModuleToggles enables/disables stages of the pipeline. Mirrors the manifest
// shape so a backend-pushed manifest can flip behaviour without restarts.
type ModuleToggles struct {
	Redaction      bool
	Classification bool
	Extraction     bool
	BodyText       bool
}

// HeaderRules controls which request/response headers are kept on the Event.
// Allowlist (lowercased) wins when non-empty; otherwise everything passes
// except names in Blocklist.
type HeaderRules struct {
	Allowlist []string
	Blocklist []string
}

// FilterRules is the host-routing piece. mitmdump's tap reads this via env;
// the Go pipeline doesn't enforce it again (the tap already dropped non-
// matching hosts), but it's carried alongside for SpawnMitmdump.
type FilterRules struct {
	TargetHosts []string
}

// ApplyConfig is the runtime config the pipeline uses for one event.
// Held in an atomic.Pointer in the daemon so manifest refreshes hot-swap it.
type ApplyConfig struct {
	Modules     ModuleToggles
	Headers     HeaderRules
	Redactions  []CompiledPattern
	StorageMode string // "raw" | "hash_only"
	Filter      FilterRules
}

// Apply runs the configured pipeline against one RawFlow and returns the
// structured Event ready for SQLite + sync.
func Apply(rf *RawFlow, ac ApplyConfig) (*Event, error) {
	e := &Event{
		EventID:       rf.EventID,
		CapturedAt:    rf.CapturedAt,
		Method:        rf.Request.Method,
		Host:          rf.Request.Host,
		URL:           rf.Request.URL,
		StatusCode:    rf.Response.StatusCode,
		DurationMs:    rf.DurationMs,
		RequestBytes:  rf.Request.BodyBytes,
		ResponseBytes: rf.Response.BodyBytes,
	}

	e.RequestHeaders = filterHeaders(rf.Request.Headers, ac.Headers)
	e.ResponseHeaders = filterHeaders(rf.Response.Headers, ac.Headers)

	var allRedactions []RedactionAudit

	reqBody, respBody := rf.Request.Body, rf.Response.Body

	if ac.Modules.Redaction && len(ac.Redactions) > 0 {
		var hits []match
		reqBody, hits = RedactString(reqBody, ac.Redactions)
		for _, h := range hits {
			allRedactions = append(allRedactions, RedactionAudit{
				Pattern: h.Name, Field: "request.body", Count: h.Count,
			})
		}
		respBody, hits = RedactString(respBody, ac.Redactions)
		for _, h := range hits {
			allRedactions = append(allRedactions, RedactionAudit{
				Pattern: h.Name, Field: "response.body", Count: h.Count,
			})
		}
		for k, v := range e.RequestHeaders {
			nv, hits := RedactString(v, ac.Redactions)
			if len(hits) > 0 {
				e.RequestHeaders[k] = nv
				for _, h := range hits {
					allRedactions = append(allRedactions, RedactionAudit{
						Pattern: h.Name, Field: "request.headers." + k, Count: h.Count,
					})
				}
			}
		}
		for k, v := range e.ResponseHeaders {
			nv, hits := RedactString(v, ac.Redactions)
			if len(hits) > 0 {
				e.ResponseHeaders[k] = nv
				for _, h := range hits {
					allRedactions = append(allRedactions, RedactionAudit{
						Pattern: h.Name, Field: "response.headers." + k, Count: h.Count,
					})
				}
			}
		}
	}

	if ac.Modules.Classification {
		e.EndpointType, e.PathTemplate = Classify(rf.Request.Method, rf.Request.Path)
	} else {
		e.EndpointType = "unfiltered"
	}

	storeText := ac.StorageMode == "raw" && ac.Modules.BodyText

	if ac.Modules.Extraction {
		// Per-endpoint extractors read body off the RawFlow; feed them the
		// post-redaction copy so extracted text never leaks pre-redaction data.
		rfCopy := *rf
		rfCopy.Request.Body = reqBody
		rfCopy.Response.Body = respBody

		e.Client = extractClient(rf.Request.Headers)
		switch e.EndpointType {
		case "completion", "retry_completion":
			e.Completion = extractCompletion(&rfCopy, storeText)
		case "conversation_fetch":
			e.ConversationFetch = extractConversationFetch(&rfCopy)
		case "account":
			e.Account = extractAccount(&rfCopy)
		case "upload":
			e.Upload = extractUpload(&rfCopy)
		}
		if e.Account == nil {
			if org := ExtractOrgUUID(rf.Request.Path); org != "" {
				e.Account = &AccountInfo{OrgUUID: org}
			}
		}
	}

	e.RequestBody = makeBody(reqBody, lookupHeader(e.RequestHeaders, "content-type"), storeText)
	e.ResponseBody = makeBody(respBody, lookupHeader(e.ResponseHeaders, "content-type"), storeText)

	e.Redactions = allRedactions
	e.Policy = ClassifyMatches(allRedactions)

	return e, nil
}

// filterHeaders applies blocklist (drops matching names) then allowlist
// (keeps only matching names if non-empty). Comparison is case-insensitive;
// keys come back lowercased so downstream lookups are predictable.
func filterHeaders(in map[string]string, rules HeaderRules) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	block := make(map[string]bool, len(rules.Blocklist))
	for _, b := range rules.Blocklist {
		block[strings.ToLower(b)] = true
	}
	var allow map[string]bool
	if len(rules.Allowlist) > 0 {
		allow = make(map[string]bool, len(rules.Allowlist))
		for _, a := range rules.Allowlist {
			allow[strings.ToLower(a)] = true
		}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		lk := strings.ToLower(k)
		if block[lk] {
			continue
		}
		if allow != nil && !allow[lk] {
			continue
		}
		out[lk] = v
	}
	return out
}

func makeBody(text, contentType string, storeText bool) *Body {
	if text == "" {
		return nil
	}
	b := &Body{
		SHA256:      sha256hex(text),
		Chars:       len(text),
		ContentType: contentType,
	}
	if storeText {
		b.Text = text
	}
	return b
}
