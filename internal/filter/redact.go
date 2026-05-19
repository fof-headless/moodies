package filter

import (
	"fmt"
	"regexp"
	"strings"
)

// CompiledPattern is a parsed entry from RedactionConfig.Patterns ("name:regex").
type CompiledPattern struct {
	Name string
	RE   *regexp.Regexp
}

// CompilePatterns parses raw "name:regex" strings. Lines that fail to compile
// are silently skipped — the daemon logs a warning at startup.
func CompilePatterns(raw []string) ([]CompiledPattern, []error) {
	var out []CompiledPattern
	var errs []error
	for _, line := range raw {
		idx := strings.IndexByte(line, ':')
		if idx <= 0 || idx == len(line)-1 {
			errs = append(errs, fmt.Errorf("malformed pattern (missing name:regex): %q", line))
			continue
		}
		name := strings.TrimSpace(line[:idx])
		expr := line[idx+1:]
		re, err := regexp.Compile(expr)
		if err != nil {
			errs = append(errs, fmt.Errorf("compile %s: %w", name, err))
			continue
		}
		out = append(out, CompiledPattern{Name: name, RE: re})
	}
	return out, errs
}

// RedactString runs every compiled pattern against s, returning the redacted
// string and a slice of which named patterns matched (with counts). The
// replacement marker is "[REDACTED:<name>]".
func RedactString(s string, patterns []CompiledPattern) (string, []match) {
	if s == "" || len(patterns) == 0 {
		return s, nil
	}
	out := s
	var hits []match
	for _, p := range patterns {
		count := len(p.RE.FindAllStringIndex(out, -1))
		if count == 0 {
			continue
		}
		out = p.RE.ReplaceAllString(out, "[REDACTED:"+p.Name+"]")
		hits = append(hits, match{Name: p.Name, Count: count})
	}
	return out, hits
}

type match struct {
	Name  string
	Count int
}

// SECRET_PATTERN_NAMES classifies which named patterns count as "secrets" vs
// "pii" for PolicyInfo. Anything not in either set is just a redaction with
// no policy bump.
var SecretPatternNames = map[string]bool{
	"anthropic_key": true,
	"aws_key":       true,
	"github_pat":    true,
}

var PIIPatternNames = map[string]bool{
	"email":  true,
	"us_ssn": true,
}

// ClassifyMatches reduces a list of redaction matches to a PolicyInfo.
func ClassifyMatches(all []RedactionAudit) PolicyInfo {
	if len(all) == 0 {
		return PolicyInfo{Classification: "clean"}
	}
	hasSecret, hasPII := false, false
	seen := map[string]bool{}
	var names []string
	for _, m := range all {
		if !seen[m.Pattern] {
			seen[m.Pattern] = true
			names = append(names, m.Pattern)
		}
		if SecretPatternNames[m.Pattern] {
			hasSecret = true
		}
		if PIIPatternNames[m.Pattern] {
			hasPII = true
		}
	}
	cls := "clean"
	if hasSecret {
		cls = "flagged_secret"
	} else if hasPII {
		cls = "flagged_pii"
	}
	return PolicyInfo{Classification: cls, Matches: names}
}
