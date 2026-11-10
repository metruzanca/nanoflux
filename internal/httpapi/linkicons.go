package httpapi

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/store"
)

// defaultLinkIconBase is the Simple Icons endpoint. Every glyph is a stable
// single-color 24x24 SVG, so it is cached in link_icons after the first lookup.
const defaultLinkIconBase = "https://simpleicons.org/icons/"

// maxLinkIconBytes caps the fetched SVG. Real Simple Icons files are a few KB.
const maxLinkIconBytes = 64 << 10

// maxLinkIconLookups bounds how many domains are resolved concurrently on one
// author page render, so a page with many new links cannot stall the request.
const maxLinkIconLookups = 4

// linkIconRetryCooldown keeps a transient lookup failure from being retried on
// every page view. It is in-memory only: a restart retries immediately, and a
// success or confirmed miss clears the entry.
const linkIconRetryCooldown = 5 * time.Minute

var linkIconRetryAfter sync.Map // domain -> time.Time

// errLinkIconNotFound means a manual slug does not exist in Simple Icons.
var errLinkIconNotFound = errors.New("no such simpleicons icon")

// errLinkIconsDisabled means outbound lookups are turned off (tests).
var errLinkIconsDisabled = errors.New("link icon lookups disabled")

// linkIconAliases maps hosts whose Simple Icons slug differs from any label in
// the domain. The admin card can add more associations manually.
var linkIconAliases = map[string]string{
	"twitter.com":          "x",
	"mobile.twitter.com":   "x",
	"youtu.be":             "youtube",
	"bsky.app":             "bluesky",
	"news.ycombinator.com": "ycombinator",
}

// linkIconSlugRe keeps a derived slug path-safe.
var linkIconSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._+-]*$`)

// linkIconSlugs returns the Simple Icons slugs to try for a domain, most
// specific first. It strips "www." and tries the host's leading label and then
// the registrable label (so news.ycombinator.com tries "news", then
// "ycombinator").
func linkIconSlugs(domain string) []string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	domain = strings.TrimPrefix(domain, "www.")
	if domain == "" {
		return nil
	}
	var out []string
	seen := make(map[string]bool, 2)
	add := func(slug string) {
		if slug == "" || seen[slug] || !linkIconSlugRe.MatchString(slug) {
			return
		}
		seen[slug] = true
		out = append(out, slug)
	}
	if alias, ok := linkIconAliases[domain]; ok {
		// A known alias is authoritative; trying the domain labels too would
		// only waste a guaranteed 404.
		if linkIconSlugRe.MatchString(alias) {
			return []string{alias}
		}
	}
	labels := strings.Split(domain, ".")
	if len(labels) > 0 {
		add(labels[0])
	}
	if len(labels) > 1 {
		add(labels[len(labels)-2])
	}
	return out
}

// normalizeLinkIconDomain normalizes a link URL (or bare domain) to the key used
// in link_icons. The "www." prefix is dropped so the same site shares one entry.
func normalizeLinkIconDomain(raw string) string {
	return strings.TrimPrefix(normalizeDomain(raw), "www.")
}

// authorLinkIcons resolves the glyphs for an author's links, keyed by normalized
// domain. Cached domains are read in one query; unseen domains are looked up
// concurrently and persisted. A transient failure leaves the domain uncached so
// the next render retries it.
func (s *Server) authorLinkIcons(ctx context.Context, links []store.AuthorLink) map[string][]string {
	domains := make([]string, 0, len(links))
	seen := make(map[string]bool, len(links))
	for _, l := range links {
		d := normalizeLinkIconDomain(l.URL)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		domains = append(domains, d)
	}
	if len(domains) == 0 {
		return nil
	}

	icons := make(map[string][]string, len(domains))
	cached, err := s.store.LinkIcons.GetMany(domains)
	if err != nil {
		log.Error("load link icons", "err", err)
		return nil
	}
	var unknown []string
	for _, d := range domains {
		if ic, ok := cached[d]; ok {
			if ic.Found && len(ic.Paths) > 0 {
				icons[d] = ic.Paths
			}
			continue
		}
		unknown = append(unknown, d)
	}
	if len(unknown) == 0 {
		return icons
	}

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	sem := make(chan struct{}, maxLinkIconLookups)
	for _, domain := range unknown {
		if linkIconCooling(domain) {
			continue
		}
		wg.Add(1)
		go func(domain string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ic, err := s.resolveLinkIcon(ctx, domain, "")
			if err != nil {
				if !errors.Is(err, errLinkIconsDisabled) && !errors.Is(err, context.Canceled) {
					linkIconRetryAfter.Store(domain, time.Now().Add(linkIconRetryCooldown))
					log.Error("resolve link icon", "domain", domain, "err", err)
				}
				return
			}
			linkIconRetryAfter.Delete(domain)
			if ic.Found && len(ic.Paths) > 0 {
				mu.Lock()
				icons[domain] = ic.Paths
				mu.Unlock()
			}
		}(domain)
	}
	wg.Wait()
	return icons
}

// linkIconCooling reports whether a domain failed recently and should be skipped
// until its retry window passes.
func linkIconCooling(domain string) bool {
	v, ok := linkIconRetryAfter.Load(domain)
	if !ok {
		return false
	}
	until, ok := v.(time.Time)
	if !ok || time.Now().After(until) {
		linkIconRetryAfter.Delete(domain)
		return false
	}
	return true
}

// resolveLinkIcon looks a domain up and caches the result. A confirmed miss is
// cached too; a transient network error is returned without writing anything.
// With slugOverride, only that slug is tried and a miss is not cached (the admin
// card reports it so the mapping can be corrected).
func (s *Server) resolveLinkIcon(ctx context.Context, domain, slugOverride string) (store.LinkIcon, error) {
	slugs := linkIconSlugs(domain)
	if slugOverride != "" {
		slugs = []string{slugOverride}
	}
	for _, slug := range slugs {
		paths, found, err := s.fetchLinkIcon(ctx, slug)
		if err != nil {
			return store.LinkIcon{}, err
		}
		if !found {
			continue
		}
		if err := s.store.LinkIcons.UpsertFound(domain, slug, paths); err != nil {
			log.Error("cache link icon", "domain", domain, "slug", slug, "err", err)
		}
		linkIconRetryAfter.Delete(domain)
		return store.LinkIcon{Domain: domain, Slug: slug, Paths: paths, Found: true}, nil
	}
	if slugOverride != "" {
		return store.LinkIcon{}, errLinkIconNotFound
	}
	if err := s.store.LinkIcons.UpsertMissing(domain); err != nil {
		log.Error("cache missing link icon", "domain", domain, "err", err)
	}
	linkIconRetryAfter.Delete(domain)
	return store.LinkIcon{Domain: domain}, nil
}

// fetchLinkIcon downloads and parses one Simple Icons SVG. found=false is a
// confirmed 404; err is a transient failure.
func (s *Server) fetchLinkIcon(ctx context.Context, slug string) (paths []string, found bool, err error) {
	if s.linkIconBase == "" {
		return nil, false, errLinkIconsDisabled
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		s.linkIconBase+url.PathEscape(slug)+".svg", nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", "nanoflux/0.1")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("simpleicons status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxLinkIconBytes))
	if err != nil {
		return nil, false, err
	}
	paths, err = parseLinkIconSVG(data)
	if err != nil {
		// A 200 with unusable content is a permanent condition for this slug:
		// cache the miss rather than retrying it on every page view.
		log.Error("parse link icon", "slug", slug, "err", err)
		return nil, false, nil
	}
	return paths, true, nil
}

// parseLinkIconSVG extracts the path data from a Simple Icons SVG. Only <path>
// data is kept, and every other drawing or active element is rejected, so the
// remote document can never inject markup into the page.
func parseLinkIconSVG(data []byte) ([]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var (
		root  bool
		paths []string
	)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		name := strings.ToLower(start.Name.Local)
		if !root {
			if name != "svg" {
				return nil, errors.New("not an svg")
			}
			root = true
			continue
		}
		switch name {
		case "title", "g":
			// Presentation-only wrappers are skipped; only path data is kept.
		case "path":
			var d string
			for _, attr := range start.Attr {
				if strings.EqualFold(attr.Name.Local, "d") {
					d = strings.TrimSpace(attr.Value)
					break
				}
			}
			if d == "" {
				continue
			}
			if !validLinkIconPath(d) {
				return nil, errors.New("invalid path data")
			}
			paths = append(paths, d)
		default:
			return nil, fmt.Errorf("unexpected svg element %q", name)
		}
	}
	if !root || len(paths) == 0 {
		return nil, errors.New("no path data")
	}
	return paths, nil
}

// validLinkIconPath accepts only SVG path grammar characters. It cannot carry
// markup, an event handler or a URL.
func validLinkIconPath(d string) bool {
	if len(d) > 32<<10 {
		return false
	}
	for _, r := range d {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r == ' ', r == ',', r == '.', r == '-', r == '+':
		default:
			return false
		}
	}
	return true
}
