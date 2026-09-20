package discover

import (
	"context"
	"io"
	"net/url"
	"regexp"
	"strings"
)

// hostSpecific applies per-site rules for pages that do not expose feed links
// in their HTML.
func (d *Discoverer) hostSpecific(ctx context.Context, pageURL string) []Candidate {
	u, err := url.Parse(pageURL)
	if err != nil {
		return nil
	}

	var feeds []string
	if isYouTube(u.Hostname()) {
		if id := d.youtubeChannelID(ctx, pageURL); id != "" {
			feeds = append(feeds, "https://www.youtube.com/feeds/videos.xml?channel_id="+id)
		}
	} else {
		feeds = hostSpecificURLs(u)
	}

	var out []Candidate
	for _, f := range feeds {
		if c, ok := d.tryFeed(ctx, f, "host", pageURL); ok {
			out = append(out, c)
		}
	}
	return out
}

func isYouTube(host string) bool {
	switch host {
	case "youtube.com", "www.youtube.com", "m.youtube.com":
		return true
	}
	return false
}

// hostSpecificURLs maps a page URL to candidate feed URLs for known sites.
// YouTube is excluded: its feed requires the channel_id, scraped separately.
func hostSpecificURLs(u *url.URL) []string {
	host := strings.ToLower(u.Hostname())
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")

	switch {
	case strings.HasSuffix(host, "bsky.app"), strings.HasSuffix(host, "bsk.app"):
		// bsk.app/profile/{handle} -> the profile's RSS feed.
		if len(parts) >= 2 && parts[0] == "profile" {
			return []string{"https://" + u.Host + "/profile/" + parts[1] + "/rss"}
		}

	case host == "reddit.com" || host == "www.reddit.com" || host == "old.reddit.com":
		// Subreddits expose /r/{sub}.rss.
		if len(parts) >= 2 && parts[0] == "r" {
			return []string{"https://www.reddit.com/r/" + parts[1] + "/.rss"}
		}

	case host == "github.com" || host == "www.github.com":
		// Repos expose releases.atom.
		if len(parts) >= 2 {
			return []string{"https://github.com/" + parts[0] + "/" + parts[1] + "/releases.atom"}
		}
	}
	return nil
}

var (
	channelIDRe    = regexp.MustCompile(`"channelId"\s*:\s*"(UC[A-Za-z0-9_-]{20,})"`)
	externalIDRe   = regexp.MustCompile(`"externalId"\s*:\s*"(UC[A-Za-z0-9_-]{20,})"`)
	browseIDRe     = regexp.MustCompile(`"browseId"\s*:\s*"(UC[A-Za-z0-9_-]{20,})"`)
	canonicalTagRe = regexp.MustCompile(`<link[^>]*\brel=["']canonical["'][^>]*>`)
	hrefAttrRe     = regexp.MustCompile(`href=["']([^"']+)["']`)
)

// youtubeChannelID resolves the channel id (e.g. "UC5--wS0Ljbin1TjWQX6eafA")
// for a YouTube channel URL. It handles /channel/, /user/, /c/ and /@handle
// forms. /channel/ URLs carry the id in the path; everything else is resolved
// from the page's canonical link or its embedded JSON.
func (d *Discoverer) youtubeChannelID(ctx context.Context, pageURL string) string {
	u, err := url.Parse(pageURL)
	if err != nil {
		return ""
	}
	if id := channelIDFromPath(u.Path); id != "" {
		return id
	}
	body, _, err := d.openPage(ctx, pageURL)
	if err != nil {
		return ""
	}
	defer body.Close()
	data, _ := io.ReadAll(io.LimitReader(body, maxBody))

	if id := canonicalChannelID(data); id != "" {
		return id
	}
	for _, re := range []*regexp.Regexp{externalIDRe, browseIDRe, channelIDRe} {
		if m := re.FindSubmatch(data); len(m) == 2 {
			return string(m[1])
		}
	}
	return ""
}

// channelIDFromPath extracts a channel id from a YouTube path like
// "/channel/UC5--wS0Ljbin1TjWQX6eafA".
func channelIDFromPath(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) < 2 || parts[0] != "channel" {
		return ""
	}
	id := parts[1]
	if strings.HasPrefix(id, "UC") && len(id) >= 20 {
		return id
	}
	return ""
}

// canonicalChannelID reads a channel id out of the page's canonical link
// (<link rel="canonical" href="https://www.youtube.com/channel/UC...">).
func canonicalChannelID(data []byte) string {
	tag := canonicalTagRe.Find(data)
	if tag == nil {
		return ""
	}
	m := hrefAttrRe.FindSubmatch(tag)
	if len(m) != 2 {
		return ""
	}
	u, err := url.Parse(string(m[1]))
	if err != nil || !isYouTube(u.Hostname()) {
		return ""
	}
	return channelIDFromPath(u.Path)
}
