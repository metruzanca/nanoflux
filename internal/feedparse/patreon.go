package feedparse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
)

// Patreon offers no RSS for a creator page, but its public web API is
// credential-free: a campaign is resolved from the page's vanity slug via
// /api/campaigns?filter[vanity]=..., and its posts come from
// /api/campaigns/{id}/posts (cursor-paginated). These posts are read here.
// The API is unofficial, so treat this as best-effort, like the X and
// Instagram scrapers.

// patreonProfileHosts lists hosts treated as Patreon creator pages. It is a
// package var so tests can inject a mock host.
var patreonProfileHosts = []string{"patreon.com", "www.patreon.com"}

// patreonAPIBase is the base URL for Patreon's public web API. It is a package
// var so tests can point it at a mock server.
var patreonAPIBase = "https://www.patreon.com/api"

const patreonBodyCap = 8 << 20

// patreonPostCount is how many posts to request per page. Patreon caps this
// server-side; a cursor URL rides along for "load older items".
const patreonPostCount = 20

var (
	// patreonPostsRe matches a stored feed_url of the form
	// https://www.patreon.com/api/campaigns/{id}/posts (optionally with a
	// cursor query), so "load older items" keeps working.
	patreonPostsRe = regexp.MustCompile(`^/api/campaigns/(\d+)/posts$`)
)

func isPatreonHost(host string) bool {
	host = strings.ToLower(host)
	for _, h := range patreonProfileHosts {
		if host == h {
			return true
		}
	}
	return false
}

// patreonVanity returns the creator's vanity slug for a Patreon page URL
// (/cw/{vanity} or /{vanity}), or "" when u is not a creator page. Post,
// user, api, and other non-creator paths are rejected.
func patreonVanity(u *url.URL) string {
	if !isPatreonHost(u.Hostname()) {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	switch {
	case len(parts) == 2 && parts[0] == "cw" && parts[1] != "":
		return parts[1]
	case len(parts) == 1 && parts[0] != "":
		vanity := parts[0]
		switch vanity {
		case "api", "user", "posts", "login", "signup", "join", "home",
			"explore", "search", "settings", "notifications", "messages":
			return ""
		}
		return vanity
	}
	return ""
}

// isPatreonProfileURL reports whether u is a Patreon creator page.
func isPatreonProfileURL(u *url.URL) bool {
	return patreonVanity(u) != ""
}

// isPatreonPostsURL reports whether u is a Patreon campaign-posts API URL
// (the stored feed_url once a feed is added, or a pagination cursor).
func isPatreonPostsURL(u *url.URL) bool {
	return isPatreonHost(u.Hostname()) && patreonPostsRe.MatchString(u.Path)
}

// isPatreonFeedURL reports whether a stored feed_url should be handled by the
// Patreon reader: either the creator page the user added, or the posts API URL
// derived from it.
func isPatreonFeedURL(feedURL string) bool {
	u, err := url.Parse(feedURL)
	if err != nil {
		return false
	}
	return isPatreonProfileURL(u) || isPatreonPostsURL(u)
}

// fetchPatreon reads a Patreon creator's posts. A creator page URL is resolved
// to its campaign first; a campaign-posts API URL (the pagination cursor) is
// fetched directly.
func fetchPatreon(ctx context.Context, feedURL string, client *http.Client) (Result, error) {
	u, err := url.Parse(feedURL)
	if err != nil {
		return Result{}, err
	}

	postsURL := feedURL
	var campaign patreonCampaign
	if !isPatreonPostsURL(u) {
		vanity := patreonVanity(u)
		if vanity == "" {
			return Result{}, errors.New("not a patreon creator page")
		}
		campaign, err = patreonResolveCampaign(ctx, vanity, client)
		if err != nil {
			return Result{}, err
		}
		postsURL = campaign.postsURL()
	}

	res, err := patreonFetchPosts(ctx, postsURL, client)
	if err != nil {
		return Result{}, err
	}

	feed := Feed{
		Title:       campaign.Name,
		HomeURL:     campaign.URL,
		Description: campaign.Summary,
		ImageURL:    campaign.AvatarURL,
	}
	items, next := patreonItems(res)
	if len(items) == 0 {
		return Result{}, errors.New("no posts found for patreon campaign")
	}
	return Result{Feed: feed, Items: items, NextPageURL: next}, nil
}

// patreonCampaign is the subset of a campaign the reader uses.
type patreonCampaign struct {
	ID        string
	Name      string
	URL       string
	Summary   string
	AvatarURL string
}

func (c patreonCampaign) postsURL() string {
	return patreonAPIBase + "/campaigns/" + c.ID + "/posts?page[count]=" + strconv.Itoa(patreonPostCount)
}

// patreonResolveCampaign resolves a creator's vanity slug to their campaign via
// the public campaigns endpoint.
func patreonResolveCampaign(ctx context.Context, vanity string, client *http.Client) (patreonCampaign, error) {
	endpoint := patreonAPIBase + "/campaigns?filter[vanity]=" + url.QueryEscape(vanity)
	body, err := patreonGet(ctx, endpoint, client)
	if err != nil {
		return patreonCampaign{}, err
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
		return patreonCampaign{}, fmt.Errorf("parse patreon campaign: %w", err)
	}
	if len(payload.Data) == 0 {
		return patreonCampaign{}, fmt.Errorf("patreon campaign %q not found", vanity)
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
	return patreonCampaign{
		ID:        c.ID,
		Name:      name,
		URL:       strings.TrimSpace(c.Attributes.URL),
		Summary:   strings.TrimSpace(c.Attributes.Summary),
		AvatarURL: avatar,
	}, nil
}

// patreonPostsResponse is the JSON shape returned by the campaign-posts
// endpoint: a data array plus a links.next cursor URL.
type patreonPostsResponse struct {
	Data  []patreonPost `json:"data"`
	Links struct {
		Next string `json:"next"`
	} `json:"links"`
}

// patreonPost is the subset of one campaign post the reader uses.
type patreonPost struct {
	ID         string                `json:"id"`
	Attributes patreonPostAttributes `json:"attributes"`
}

// patreonPostAttributes is a post's attributes object.
type patreonPostAttributes struct {
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

// patreonFetchPosts fetches one page of a campaign's posts.
func patreonFetchPosts(ctx context.Context, postsURL string, client *http.Client) (patreonPostsResponse, error) {
	body, err := patreonGet(ctx, postsURL, client)
	if err != nil {
		return patreonPostsResponse{}, err
	}
	var resp patreonPostsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return patreonPostsResponse{}, fmt.Errorf("parse patreon posts: %w", err)
	}
	return resp, nil
}

func patreonGet(ctx context.Context, endpoint string, client *http.Client) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent())
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if isRateLimited(resp) {
		return nil, &RateLimitError{URL: endpoint, Status: resp.StatusCode, RetryAfter: rateLimitBackoff(resp)}
	}
	if resp.StatusCode >= 400 {
		return nil, &StatusError{Code: resp.StatusCode, URL: endpoint}
	}
	return io.ReadAll(io.LimitReader(resp.Body, patreonBodyCap))
}

// patreonItems converts a page of posts into items and returns the cursor URL
// for the next page ("" when exhausted). Posts are kept in the API's order
// (newest first).
func patreonItems(resp patreonPostsResponse) ([]Item, string) {
	out := make([]Item, 0, len(resp.Data))
	for i := range resp.Data {
		it, ok := patreonItem(&resp.Data[i])
		if !ok {
			continue
		}
		out = append(out, it)
	}
	return out, resp.Links.Next
}

// patreonItem converts one post into an Item. Posts with no title fall back to
// a title derived from their body; a member-locked post (no body) falls back to
// its teaser. It returns ok=false only when the post has no id or link.
func patreonItem(p *patreonPost) (Item, bool) {
	if p.ID == "" {
		return Item{}, false
	}
	a := &p.Attributes

	body := patreonDocText(a.ContentJSON)
	if body == "" {
		body = strings.TrimSpace(a.Content)
	}
	summary := body
	if summary == "" {
		// A member-locked post ships no body; its teaser is all there is.
		summary = patreonDocText(a.TeaserJSON)
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

	return Item{
		GUID:        "patreon:" + p.ID,
		Title:       title,
		Link:        link,
		Summary:     summary,
		ImageURL:    patreonImage(a),
		PublishedAt: patreonTime(when),
	}, true
}

// patreonTime converts Patreon's RFC3339 timestamp to the app's stored UTC
// format, returning "" when it cannot be parsed.
func patreonTime(s string) string {
	if s == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return ""
	}
	return db.FormatTime(t)
}

// patreonImage picks a post thumbnail: the largest image URL available, falling
// back to the post file (video poster) or thumbnail object.
func patreonImage(a *patreonPostAttributes) string {
	for _, raw := range []json.RawMessage{a.Image, a.PostFile, a.Thumbnail} {
		if u := patreonImageURL(raw); u != "" {
			return u
		}
	}
	return ""
}

// patreonImageURL reads a URL out of one of Patreon's image shapes: either a
// plain string or an object with a "url"/"large_url"/"default_large"/
// "default" field (the shapes vary by post type).
func patreonImageURL(raw json.RawMessage) string {
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

// patreonDocText extracts plain text from Patreon's ProseMirror content JSON
// ({"type":"doc","content":[{"type":"paragraph","content":[{"type":"text",
// "text":"…"}]}]}). Paragraphs and list items are separated by newlines.
func patreonDocText(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw[0] != '{' {
		return ""
	}
	var doc patreonDocNode
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return ""
	}
	var b strings.Builder
	patreonDocWalk(&doc, &b)
	return strings.TrimSpace(b.String())
}

// patreonDocNode is one ProseMirror node (recursively nested via Content).
type patreonDocNode struct {
	Type    string           `json:"type"`
	Text    string           `json:"text"`
	Content []patreonDocNode `json:"content"`
}

// patreonDocWalk writes a node's text, adding line breaks between block-level
// children (paragraphs, headings, list items).
func patreonDocWalk(n *patreonDocNode, b *strings.Builder) {
	switch n.Type {
	case "text":
		b.WriteString(n.Text)
		return
	case "hardBreak":
		b.WriteByte('\n')
		return
	}
	for i := range n.Content {
		patreonDocWalk(&n.Content[i], b)
	}
	switch n.Type {
	case "paragraph", "heading", "listItem", "blockquote", "codeBlock":
		b.WriteByte('\n')
	}
}
