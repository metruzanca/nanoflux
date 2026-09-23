// Package patreon is the native plugin for Patreon creator feeds. Patreon has
// no RSS, but its public web API is credential-free: a campaign is resolved from
// the page's vanity slug, and its posts come from the campaign-posts endpoint
// (cursor-paginated). Treat it as best-effort.
package patreon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// Name is the plugin's stable identifier.
const Name = "patreon"

// apiBase is the base URL for Patreon's public web API. A var so tests can point
// it at a mock server.
var apiBase = "https://www.patreon.com/api"

// hosts lists hosts treated as Patreon creator pages. A var so tests can inject
// a mock host.
var hosts = []string{"patreon.com", "www.patreon.com"}

// postsCount is how many posts to request per page.
const postsCount = 20

var postsRe = regexp.MustCompile(`^/api/campaigns/(\d+)/posts$`)

// Plugin is the Patreon Fetcher.
type Plugin struct{}

var _ pluginapi.Fetcher = Plugin{}

func (Plugin) Meta() pluginapi.Meta {
	return pluginapi.Meta{Name: Name, APIVersion: pluginapi.APIVersion}
}

// Match handles both discover and fetch: creator pages, and the derived
// campaign-posts API URL (the stored feed URL / pagination cursor).
func (Plugin) Match(u *url.URL, _ pluginapi.Capability) bool {
	return isProfileURL(u) || isPostsURL(u)
}

// Discover offers the creator page as its own feed URL (resolved to a campaign
// on fetch), carrying the campaign's name and avatar as preview metadata.
func (p Plugin) Discover(ctx context.Context, pageURL string, h pluginapi.Host) ([]pluginapi.Candidate, error) {
	u, err := url.Parse(pageURL)
	if err != nil || !isProfileURL(u) {
		return nil, pluginapi.ErrUnsupportedCapability
	}
	vanity := vanityOf(u)
	c, err := p.resolveCampaign(ctx, vanity, h)
	if err != nil {
		return nil, err
	}
	return []pluginapi.Candidate{{
		FeedURL: pageURL,
		Title:   c.Name,
		IconURL: c.AvatarURL,
		HomeURL: pageURL,
	}}, nil
}

// Fetch reads a creator's posts. A creator page URL is resolved to its campaign
// first; a campaign-posts API URL (a pagination cursor) is fetched directly.
func (p Plugin) Fetch(ctx context.Context, req pluginapi.FetchRequest, h pluginapi.Host) (pluginapi.Result, error) {
	u, err := url.Parse(req.URL)
	if err != nil {
		return pluginapi.Result{}, err
	}

	postsURL := req.URL
	var campaign campaign
	if !isPostsURL(u) {
		vanity := vanityOf(u)
		if vanity == "" {
			return pluginapi.Result{}, errors.New("not a patreon creator page")
		}
		campaign, err = p.resolveCampaign(ctx, vanity, h)
		if err != nil {
			return pluginapi.Result{}, err
		}
		postsURL = campaign.postsURL()
	}

	body, err := p.get(ctx, postsURL, h)
	if err != nil {
		return pluginapi.Result{}, err
	}
	var resp postsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return pluginapi.Result{}, fmt.Errorf("parse patreon posts: %w", err)
	}
	items, next := itemsFromResponse(resp)
	if len(items) == 0 {
		return pluginapi.Result{}, errors.New("no posts found for patreon campaign")
	}
	return pluginapi.Result{
		Feed: pluginapi.Feed{
			Title:       campaign.Name,
			HomeURL:     campaign.URL,
			Description: campaign.Summary,
			ImageURL:    campaign.AvatarURL,
		},
		Items:       items,
		NextPageURL: next,
	}, nil
}

func isHost(host string) bool {
	host = strings.ToLower(host)
	for _, h := range hosts {
		if host == h {
			return true
		}
	}
	return false
}

// vanityOf returns the creator's vanity slug for /cw/{vanity} or /{vanity}, or
// "" for non-creator paths.
func vanityOf(u *url.URL) string {
	if u == nil || !isHost(u.Hostname()) {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	switch {
	case len(parts) == 2 && parts[0] == "cw" && parts[1] != "":
		return parts[1]
	case len(parts) == 1 && parts[0] != "":
		switch parts[0] {
		case "api", "user", "posts", "login", "signup", "join", "home",
			"explore", "search", "settings", "notifications", "messages":
			return ""
		}
		return parts[0]
	}
	return ""
}

func isProfileURL(u *url.URL) bool { return vanityOf(u) != "" }

func isPostsURL(u *url.URL) bool {
	return u != nil && isHost(u.Hostname()) && postsRe.MatchString(u.Path)
}

type campaign struct {
	ID        string
	Name      string
	URL       string
	Summary   string
	AvatarURL string
}

func (c campaign) postsURL() string {
	return apiBase + "/campaigns/" + c.ID + "/posts?page[count]=" + strconv.Itoa(postsCount)
}

func (p Plugin) resolveCampaign(ctx context.Context, vanity string, h pluginapi.Host) (campaign, error) {
	endpoint := apiBase + "/campaigns?filter[vanity]=" + url.QueryEscape(vanity)
	body, err := p.get(ctx, endpoint, h)
	if err != nil {
		return campaign{}, err
	}
	var payload struct {
		Data []struct {
			ID         string `json:"id"`
			Attributes struct {
				Name       string `json:"name"`
				URL        string `json:"url"`
				Summary    string `json:"summary"`
				AvatarURL  string `json:"avatar_photo_url"`
				ImageSmall string `json:"image_small_url"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return campaign{}, fmt.Errorf("parse patreon campaign: %w", err)
	}
	if len(payload.Data) == 0 {
		return campaign{}, fmt.Errorf("patreon campaign %q not found", vanity)
	}
	c := payload.Data[0]
	name := strings.TrimSpace(c.Attributes.Name)
	if name == "" {
		name = vanity
	}
	avatar := c.Attributes.AvatarURL
	if avatar == "" {
		avatar = c.Attributes.ImageSmall
	}
	return campaign{
		ID:        c.ID,
		Name:      name,
		URL:       strings.TrimSpace(c.Attributes.URL),
		Summary:   strings.TrimSpace(c.Attributes.Summary),
		AvatarURL: avatar,
	}, nil
}

// get performs a host-mediated GET with an Accept: application/json header.
func (p Plugin) get(ctx context.Context, endpoint string, h pluginapi.Host) ([]byte, error) {
	resp, err := h.Do(ctx, pluginapi.HTTPRequest{
		Method:  "GET",
		URL:     endpoint,
		Headers: map[string]string{"Accept": "application/json"},
	})
	if err != nil {
		return nil, err
	}
	if resp.RateLimited {
		return nil, &pluginapi.RateLimit{URL: endpoint, Status: resp.Status, RetryAfter: resp.RetryAfter}
	}
	if resp.Status >= 400 {
		return nil, &pluginapi.StatusError{Code: resp.Status, URL: endpoint}
	}
	return resp.Body, nil
}

type postsResponse struct {
	Data  []post `json:"data"`
	Links struct {
		Next string `json:"next"`
	} `json:"links"`
}

type post struct {
	ID         string         `json:"id"`
	Attributes postAttributes `json:"attributes"`
}

type postAttributes struct {
	Title       string          `json:"title"`
	Content     string          `json:"content"`
	ContentJSON string          `json:"content_json_string"`
	Teaser      string          `json:"teaser_text"`
	TeaserJSON  string          `json:"teaser_text_json_string"`
	URL         string          `json:"url"`
	PublishedAt string          `json:"published_at"`
	CreatedAt   string          `json:"created_at"`
	Image       json.RawMessage `json:"image"`
	Thumbnail   json.RawMessage `json:"thumbnail"`
	PostFile    json.RawMessage `json:"post_file"`
}

func itemsFromResponse(resp postsResponse) ([]pluginapi.Item, string) {
	out := make([]pluginapi.Item, 0, len(resp.Data))
	for i := range resp.Data {
		if it, ok := itemFor(&resp.Data[i]); ok {
			out = append(out, it)
		}
	}
	return out, resp.Links.Next
}

func itemFor(p *post) (pluginapi.Item, bool) {
	if p.ID == "" {
		return pluginapi.Item{}, false
	}
	a := &p.Attributes

	body := docText(a.ContentJSON)
	if body == "" {
		body = strings.TrimSpace(a.Content)
	}
	summary := body
	if summary == "" {
		summary = docText(a.TeaserJSON)
		if summary == "" {
			summary = strings.TrimSpace(a.Teaser)
		}
	}
	title := strings.TrimSpace(a.Title)
	if title == "" {
		title = postTitle(summary)
	}

	link := strings.TrimSpace(a.URL)
	if link == "" {
		link = "https://www.patreon.com/posts/" + p.ID
	}

	when := a.PublishedAt
	if when == "" {
		when = a.CreatedAt
	}

	return pluginapi.Item{
		GUID:        "patreon:" + p.ID,
		Title:       title,
		Link:        link,
		Summary:     summary,
		ImageURL:    image(a),
		PublishedAt: formatTime(when),
	}, true
}

func formatTime(s string) string {
	if s == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04:05")
}

func image(a *postAttributes) string {
	for _, raw := range []json.RawMessage{a.Image, a.PostFile, a.Thumbnail} {
		if u := imageURL(raw); u != "" {
			return u
		}
	}
	return ""
}

func imageURL(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	for _, key := range []string{"large_url", "default_large", "url", "default", "thumb_url"} {
		if v, ok := obj[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func postTitle(text string) string {
	title := text
	if i := strings.IndexByte(title, '\n'); i >= 0 {
		title = title[:i]
	}
	title = strings.TrimSpace(title)
	if len([]rune(title)) > 100 {
		title = string([]rune(title)[:99]) + "…"
	}
	return title
}

// docText extracts plain text from Patreon's ProseMirror content JSON.
func docText(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw[0] != '{' {
		return ""
	}
	var doc docNode
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return ""
	}
	var b strings.Builder
	docWalk(&doc, &b)
	return strings.TrimSpace(b.String())
}

type docNode struct {
	Type    string    `json:"type"`
	Text    string    `json:"text"`
	Content []docNode `json:"content"`
}

func docWalk(n *docNode, b *strings.Builder) {
	switch n.Type {
	case "text":
		b.WriteString(n.Text)
		return
	case "hardBreak":
		b.WriteByte('\n')
		return
	}
	for i := range n.Content {
		docWalk(&n.Content[i], b)
	}
	switch n.Type {
	case "paragraph", "heading", "listItem", "blockquote", "codeBlock":
		b.WriteByte('\n')
	}
}
