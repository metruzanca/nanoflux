package discover

import (
	"context"
	"net/http"
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

var channelIDRe = regexp.MustCompile(`"channelId"\s*:\s*"([A-Za-z0-9_-]{20,})"`)

// youtubeChannelID fetches a YouTube page and extracts the channel_id embedded
// in its JSON.
func (d *Discoverer) youtubeChannelID(ctx context.Context, pageURL string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "rss/0.1")
	resp, err := d.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return ""
	}
	body := make([]byte, 0, 64<<10)
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		body = append(body, buf[:n]...)
		if len(body) > maxBody {
			return ""
		}
		if err != nil {
			break
		}
	}
	if m := channelIDRe.FindSubmatch(body); len(m) == 2 {
		return string(m[1])
	}
	return ""
}
