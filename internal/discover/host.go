package discover

import (
	"context"
	"net/url"
	"strings"
)

// hostSpecific applies per-site rules for pages that do not expose feed links
// in their HTML. It returns the candidates it validated, plus the last fetch
// error when a known rule's feed could not be fetched (so a caller can explain
// why — e.g. a rate limit — instead of reporting "no feed"). Sites whose URL
// shapes a plugin owns are handled by that plugin's Discover, not here.
func (d *Discoverer) hostSpecific(ctx context.Context, pageURL string) ([]Candidate, error) {
	u, err := url.Parse(pageURL)
	if err != nil {
		return nil, nil
	}

	var out []Candidate
	var lastErr error
	for _, f := range hostSpecificURLs(u) {
		c, err := d.tryFeed(ctx, f.URL, "host", pageURL)
		if err != nil {
			lastErr = err
			continue
		}
		if f.Title != "" {
			c.Title = f.Title
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, lastErr
	}
	return out, nil
}

// hostFeed is one host-specific candidate: its feed URL and an optional display
// title override ("" uses the feed's own title).
type hostFeed struct {
	URL   string
	Title string
}

// hostSpecificURLs maps a page URL to candidate feed URLs for known sites.
// A site whose feed URL is not derivable from the path alone (its feed needs an
// id scraped from the page) is handled by that site's plugin Discover instead.
func hostSpecificURLs(u *url.URL) []hostFeed {
	host := strings.ToLower(u.Hostname())
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")

	switch {
	case host == "github.com" || host == "www.github.com":
		// Profile: github.com/USERNAME -> the user's activity feed.
		if len(parts) == 1 && parts[0] != "" {
			name := parts[0]
			return []hostFeed{{URL: "https://github.com/" + name + ".atom", Title: name + "'s Github activity"}}
		}
		// Repo: github.com/OWNER/REPO -> releases, commits, or tags to pick from.
		if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
			base := "https://github.com/" + parts[0] + "/" + parts[1]
			return []hostFeed{
				{URL: base + "/releases.atom"},
				{URL: base + "/commits.atom"},
				{URL: base + "/tags.atom"},
			}
		}
	}
	return nil
}
