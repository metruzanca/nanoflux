// Package urlmap transforms a user-entered URL into a feed URL using
// per-user "pattern -> template" rules. A pattern is a literal url with
// {name} placeholders (e.g. `abc.com/{user}`), where each placeholder
// matches one path segment; the template references captured values with
// `{name}` too (e.g. `{user}.abc.com/feed`). Everything that is not a
// placeholder matches literally — no regex syntax.
//
// Matching happens against the scheme-stripped host/path of the input
// (trailing slash removed) and is case-insensitive, but captures keep the
// original case of the matched text. A pattern must match the whole
// host/path — it is anchored internally, so substrings never match.
package urlmap

import (
	"fmt"
	"regexp"
	"strings"
)

// nameRe matches a {name} placeholder token.
var nameRe = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)

// Mapping is a compiled pattern/template pair.
type Mapping struct {
	re       *regexp.Regexp
	names    []string // placeholder names, in pattern order (parallel to capture groups)
	hasName  map[string]bool
	template string
}

// Compile validates and compiles a pattern/template pair. The pattern must
// contain at least one {name} placeholder (each matching one non-slash
// segment), and the template may only reference placeholders the pattern
// defines.
func Compile(pattern, template string) (*Mapping, error) {
	pattern = strings.TrimSpace(pattern)
	template = strings.TrimSpace(template)
	if pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}
	if template == "" {
		return nil, fmt.Errorf("feed url is required")
	}

	// Build a case-insensitive, fully anchored regex from the pattern: literal
	// text matches verbatim, each {name} matches one path segment. Captures
	// keep the original case because the input is matched as typed.
	var b strings.Builder
	b.WriteString("(?i)^")
	names := []string{}
	last := 0
	for _, m := range nameRe.FindAllStringSubmatchIndex(pattern, -1) {
		seg := pattern[last:m[0]]
		if strings.ContainsAny(seg, "{}") {
			return nil, fmt.Errorf("invalid placeholder in pattern")
		}
		b.WriteString(regexp.QuoteMeta(seg))
		names = append(names, pattern[m[2]:m[3]])
		b.WriteString("([^/]+)")
		last = m[1]
	}
	seg := pattern[last:]
	if strings.ContainsAny(seg, "{}") {
		return nil, fmt.Errorf("invalid placeholder in pattern")
	}
	b.WriteString(regexp.QuoteMeta(seg))
	b.WriteString("$")
	if len(names) == 0 {
		return nil, fmt.Errorf("pattern must include a placeholder like {user}")
	}
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %v", err)
	}
	hasName := map[string]bool{}
	for _, n := range names {
		hasName[n] = true
	}
	for _, m := range nameRe.FindAllStringSubmatch(template, -1) {
		if !hasName[m[1]] {
			return nil, fmt.Errorf("feed url references {%s} which is not in the pattern", m[1])
		}
	}
	return &Mapping{re: re, names: names, hasName: hasName, template: template}, nil
}

// Apply transforms rawURL into the mapped feed URL. It returns ok=false when
// no pattern matches, so callers can fall back to normal discovery.
func (m *Mapping) Apply(rawURL string) (string, bool) {
	matchable := matchable(rawURL)
	subs := m.re.FindStringSubmatch(matchable)
	if subs == nil {
		return "", false
	}
	captures := map[string]string{}
	for i, name := range m.names {
		if i+1 < len(subs) {
			captures[name] = subs[i+1]
		}
	}
	return expandTemplate(m.template, captures), true
}

// expandTemplate replaces every {name} token with its captured value.
func expandTemplate(template string, captures map[string]string) string {
	return nameRe.ReplaceAllStringFunc(template, func(token string) string {
		name := token[1 : len(token)-1]
		if v, ok := captures[name]; ok {
			return v
		}
		return token
	})
}

// matchable strips the scheme and a trailing slash so patterns describe the
// host/path form users paste into the add-feed box.
func matchable(rawURL string) string {
	s := strings.TrimSpace(rawURL)
	lower := strings.ToLower(s)
	for _, prefix := range []string{"https://", "http://"} {
		if strings.HasPrefix(lower, prefix) {
			s = s[len(prefix):]
			break
		}
	}
	s = strings.TrimSuffix(s, "/")
	return s
}
