// Package urlmap transforms a user-entered URL into a feed URL using
// per-user "pattern -> template" rules. A pattern is a Go regular expression
// with named capture groups (e.g. `abc\.com/(?P<user>[^/]+)`); the template
// references captured values with `{name}` (e.g. `{user}.abc.com/feed`).
//
// Matching happens against the scheme-stripped host/path of the input
// (trailing slash removed) and is case-insensitive by default, but captures
// keep the original case of the matched text. A pattern must match the whole
// host/path — it is anchored internally, so substrings never match.
package urlmap

import (
	"fmt"
	"regexp"
	"strings"
)

var nameRe = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)

// Mapping is a compiled pattern/template pair.
type Mapping struct {
	re       *regexp.Regexp
	names    map[string]bool
	template string
}

// Compile validates and compiles a pattern/template pair. The pattern must be
// a valid Go regexp containing at least one named group, and the template may
// only reference groups the pattern defines.
func Compile(pattern, template string) (*Mapping, error) {
	pattern = strings.TrimSpace(pattern)
	template = strings.TrimSpace(template)
	if pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}
	if template == "" {
		return nil, fmt.Errorf("feed url is required")
	}
	// Case-insensitive and fully anchored so the pattern must describe the
	// whole host/path. Users can opt out of case-insensitivity per-section
	// with (?-i:...).
	re, err := regexp.Compile("(?i)^(?:" + pattern + ")$")
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %v", err)
	}
	names := map[string]bool{}
	for _, n := range re.SubexpNames() {
		if n != "" {
			names[n] = true
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("pattern must include a named group like (?P<user>...)")
	}
	for _, match := range nameRe.FindAllStringSubmatch(template, -1) {
		if !names[match[1]] {
			return nil, fmt.Errorf("feed url references {%s} which is not in the pattern", match[1])
		}
	}
	return &Mapping{re: re, names: names, template: template}, nil
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
	for i, name := range m.re.SubexpNames() {
		if name != "" && i < len(subs) {
			captures[name] = subs[i]
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
