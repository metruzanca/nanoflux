// Package bluesky is the native plugin for Bluesky (AT Protocol) profiles.
//
// The profile RSS feed (bsky.app/profile/{handle}/rss) is not used: it is
// text-only — no item titles, no media, no author — and strips an image or
// video post down to its caption. The AT Protocol exposes the real records, so
// the plugin reads app.bsky.feed.post records straight from the repository and
// rebuilds the entries from them:
//
//   - com.atproto.repo.listRecords returns each post's raw record: text, its
//     rich-text facets (mentions, links, tags), the reply reference, and the
//     embed (images, video, external link card, quote).
//   - com.atproto.sync.getBlob serves a media blob as a real file. A video blob
//     comes back as video/mp4, so it is attached as a playable enclosure, and
//     an image blob as its full-size image.
//
// The feed URL stored on a feed is the profile's /rss shape (what discovery has
// always produced), but its bytes are never parsed: the URL is only the feed's
// identity, and the handle/DID in its path selects the repository to read.
package bluesky

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// Name is the plugin's stable identifier (stored in feeds.plugin_name).
const Name = "bluesky"

// readme is the plugin's Markdown documentation, shown from the admin plugin
// card and from a feed's edit page.
//
//go:embed readme.md
var readme string

// listLimit is how many post records are requested per fetch. The repository is
// newest-first, so this is the recent window; a profile that posts heavily may
// need pagination later.
const listLimit = 50

// baseURLs are the service origins the plugin talks to. The app view answers
// profile reads; the entryway answers repository and blob reads (the app view
// does not implement listRecords). They are vars so tests can point them at a
// mock host.
var (
	// apiBase serves app.bsky.actor.getProfile.
	apiBase = "https://public.api.bsky.app"
	// repoBase serves com.atproto.repo.listRecords.
	repoBase = "https://bsky.social"
	// imgBase serves image blobs as full-size images with no redirect.
	imgBase = "https://cdn.bsky.app"
	// videoBase serves a video's HLS playlist and poster frame.
	videoBase = "https://video.bsky.app"
)

// bskyHosts are the hosts whose profile pages this plugin recognizes.
var bskyHosts = []string{"bsky.app", "www.bsky.app", "bsk.app", "www.bsk.app"}

// Plugin is the Bluesky Fetcher.
type Plugin struct{}

var _ pluginapi.Fetcher = Plugin{}

func (Plugin) Meta() pluginapi.Meta {
	return pluginapi.Meta{
		Name:       Name,
		APIVersion: pluginapi.APIVersion,
		Summary:    "Bluesky profiles: reads posts from the AT Protocol with titles, images, video and link cards",
	}
}

// Docs returns this plugin's Markdown documentation.
func (Plugin) Docs() string { return readme }

// Match handles discovery on a profile page, and fetch for the stored /rss
// feed URL. Fetch deliberately does not claim the bare profile page: generic
// discovery direct-fetches a page URL as a feed, and would otherwise store the
// page URL (not its /rss feed) when a profile is added.
func (Plugin) Match(u *url.URL, cap pluginapi.Capability) bool {
	if u == nil || !isBskyHost(u.Hostname()) {
		return false
	}
	switch cap {
	case pluginapi.CapDiscover:
		_, ok := actorFromPage(u)
		return ok
	case pluginapi.CapFetch:
		_, ok := actorFromFeed(u)
		return ok
	default:
		return false
	}
}

func isBskyHost(host string) bool {
	host = strings.ToLower(host)
	for _, h := range bskyHosts {
		if host == h {
			return true
		}
	}
	return false
}

// actorFromPage returns the profile handle/DID from a profile page URL
// (/profile/{actor}), and whether the URL is one.
func actorFromPage(u *url.URL) (string, bool) {
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != "profile" || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

// actorFromFeed returns the profile handle/DID from the stored feed URL shape,
// /profile/{actor}/rss. The bare profile page is not a feed here: discovery
// owns it, and the plugin's feed URL is always the /rss shape.
func actorFromFeed(u *url.URL) (string, bool) {
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "profile" || parts[1] == "" || parts[2] != "rss" {
		return "", false
	}
	return parts[1], true
}

// Discover resolves a profile page to its RSS-shaped feed URL, carrying the
// profile's display name and avatar so the add form shows the real author.
func (p Plugin) Discover(ctx context.Context, pageURL string, h pluginapi.Host) ([]pluginapi.Candidate, error) {
	u, err := url.Parse(pageURL)
	if err != nil || !isBskyHost(u.Hostname()) {
		return nil, pluginapi.ErrUnsupportedCapability
	}
	actor, ok := actorFromPage(u)
	if !ok {
		return nil, pluginapi.ErrUnsupportedCapability
	}
	prof, err := p.profile(ctx, actor, h)
	if err != nil {
		return nil, err
	}
	// The feed URL uses the account's DID: it is the canonical, rename-proof
	// shape, it is what the profile page itself advertises in its RSS <link>,
	// and the /rss endpoint answers it without a redirect. Using the handle
	// here would yield a second candidate for the same feed (the page's own
	// DID link), which discovery would not deduplicate.
	return []pluginapi.Candidate{{
		FeedURL: "https://bsky.app/profile/" + prof.DID + "/rss",
		Title:   feedTitle(prof),
		IconURL: prof.Avatar,
		HomeURL: "https://bsky.app/profile/" + prof.Handle,
	}}, nil
}

// Fetch reads a profile's recent posts from its repository. The handle/DID in
// the feed URL selects the repository; the profile read also supplies the
// account's DID (used to build blob URLs) and display metadata.
func (p Plugin) Fetch(ctx context.Context, req pluginapi.FetchRequest, h pluginapi.Host) (pluginapi.Result, error) {
	u, perr := url.Parse(req.URL)
	if perr != nil {
		return pluginapi.Result{}, perr
	}
	actor, ok := actorFromFeed(u)
	if !ok {
		return pluginapi.Result{}, &pluginapi.StatusError{Code: 400, URL: req.URL}
	}
	prof, err := p.profile(ctx, actor, h)
	if err != nil {
		return pluginapi.Result{}, err
	}
	records, err := p.listRecords(ctx, prof.DID, h)
	if err != nil {
		return pluginapi.Result{}, err
	}
	res := pluginapi.Result{
		Feed: pluginapi.Feed{
			Title:       feedTitle(prof),
			HomeURL:     "https://bsky.app/profile/" + prof.Handle,
			Description: prof.Description,
			ImageURL:    prof.Avatar,
		},
	}
	for _, rec := range records {
		it, ok := p.itemFromRecord(rec, prof)
		if !ok {
			continue
		}
		res.Items = append(res.Items, it)
	}
	return res, nil
}

// profile reads an account's public profile. It accepts a handle or a DID.
func (p Plugin) profile(ctx context.Context, actor string, h pluginapi.Host) (profile, error) {
	body, err := p.get(ctx, apiBase+"/xrpc/app.bsky.actor.getProfile?actor="+url.QueryEscape(actor), h)
	if err != nil {
		return profile{}, err
	}
	var prof profile
	if err := json.Unmarshal(body, &prof); err != nil {
		return profile{}, fmt.Errorf("bluesky getProfile: %w", err)
	}
	if prof.DID == "" {
		return profile{}, errors.New("bluesky getProfile: no did")
	}
	if prof.Handle == "" {
		prof.Handle = actor
	}
	return prof, nil
}

// listRecords returns a repository's most recent app.bsky.feed.post records,
// newest-first.
func (p Plugin) listRecords(ctx context.Context, repo string, h pluginapi.Host) ([]record, error) {
	q := url.Values{
		"repo":       {repo},
		"collection": {"app.bsky.feed.post"},
		"limit":      {fmt.Sprint(listLimit)},
	}
	body, err := p.get(ctx, repoBase+"/xrpc/com.atproto.repo.listRecords?"+q.Encode(), h)
	if err != nil {
		return nil, err
	}
	var resp listRecordsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("bluesky listRecords: %w", err)
	}
	return resp.Records, nil
}

// get performs a host-mediated GET and maps status/rate-limit responses to the
// plugin's typed errors so the poller's pacing keeps working.
func (p Plugin) get(ctx context.Context, rawurl string, h pluginapi.Host) ([]byte, error) {
	resp, err := h.Do(ctx, pluginapi.HTTPRequest{Method: "GET", URL: rawurl})
	if err != nil {
		return nil, err
	}
	if resp.RateLimited {
		return nil, &pluginapi.RateLimit{URL: rawurl, Status: resp.Status, RetryAfter: resp.RetryAfter}
	}
	if resp.Status >= 400 {
		return nil, &pluginapi.StatusError{Code: resp.Status, URL: rawurl}
	}
	return resp.Body, nil
}

// itemFromRecord turns one raw post record into an item. A reply is skipped:
// the RSS feed this plugin replaces carried original posts only, and a reader
// does not want a reply with no parent.
func (p Plugin) itemFromRecord(r record, prof profile) (pluginapi.Item, bool) {
	var rec postRecord
	if err := json.Unmarshal(r.Value, &rec); err != nil {
		return pluginapi.Item{}, false
	}
	if rec.Reply != nil {
		return pluginapi.Item{}, false
	}
	rkey := rkeyFromURI(r.URI)
	if rkey == "" {
		return pluginapi.Item{}, false
	}
	var c collected
	collectEmbed(rec.Embed, &c)

	var firstImage string
	it := pluginapi.Item{
		GUID:        r.URI,
		Identity:    r.URI, // the AT URI is immutable across handle renames
		Link:        "https://bsky.app/profile/" + prof.Handle + "/post/" + rkey,
		Summary:     summaryHTML(rec, c),
		PublishedAt: dbTime(rec.CreatedAt),
	}
	for _, im := range c.images {
		cid := im.Image.Ref.Link
		if cid == "" {
			continue
		}
		u := imageURL(prof.DID, cid)
		if firstImage == "" {
			firstImage = u
		}
		mime := im.Image.MIMEType
		if mime == "" {
			mime = "image/jpeg"
		}
		it.Enclosures = append(it.Enclosures, pluginapi.Enclosure{
			URL:      u,
			MIMEType: mime,
			Length:   im.Image.Size,
		})
	}
	if c.video != nil && c.video.Ref.Link != "" {
		cid := c.video.Ref.Link
		// The video is exposed as an HLS playlist, not the raw blob: the raw
		// blob (com.atproto.sync.getBlob) returns the whole file and ignores
		// Range, so seeking is limited, while the HLS segments are CORS-open
		// and Range-capable. The stored URL is the stable master playlist; the
		// browser fetches it at play time, so the session-scoped rendition URLs
		// inside it are always fresh.
		it.Enclosures = append(it.Enclosures, pluginapi.Enclosure{
			URL:      hlsURL(prof.DID, cid),
			MIMEType: "application/vnd.apple.mpegurl",
		})
		if firstImage == "" {
			firstImage = videoThumbURL(prof.DID, cid)
		}
	}
	if firstImage == "" && c.external != nil && c.external.Thumb != nil {
		if cid := c.external.Thumb.Ref.Link; cid != "" {
			firstImage = imageURL(prof.DID, cid)
		}
	}
	it.ImageURL = firstImage
	it.Title = titleFromText(rec.Text)
	if it.Title == "" && c.external != nil {
		it.Title = strings.TrimSpace(c.external.Title)
	}
	if it.Title == "" {
		switch {
		case c.video != nil:
			it.Title = "video"
		case len(c.images) > 0:
			it.Title = "image"
		default:
			it.Title = "post"
		}
	}
	return it, true
}

// imageURL is a blob's full-size image URL. The CDN serves it directly, with
// no redirect and a long cache lifetime.
func imageURL(did, cid string) string {
	return imgBase + "/img/feed_fullsize/plain/" + did + "/" + cid
}

// hlsURL is a video's HLS master playlist. It is the enclosure the plugin
// stores, so the item plays with real seeking. The URL is stable (the playlist
// it returns is fetched fresh at play time and carries the session-scoped
// rendition URLs), so it is safe to store. It redirects to video.cdn.bsky.app;
// the master, rendition playlists and segments are all CORS-open, and the
// segments support Range requests.
func hlsURL(did, cid string) string {
	return videoBase + "/watch/" + url.PathEscape(did) + "/" + cid + "/playlist.m3u8"
}

// videoThumbURL is a video's poster frame, used as the item thumbnail in lists.
func videoThumbURL(did, cid string) string {
	return videoBase + "/watch/" + url.PathEscape(did) + "/" + cid + "/thumbnail.jpg"
}

// feedTitle renders the feed's display name ("@handle - display name"), the
// same shape the RSS feed used.
func feedTitle(p profile) string {
	if p.DisplayName == "" {
		return "@" + p.Handle
	}
	return "@" + p.Handle + " - " + p.DisplayName
}

// summaryHTML builds an item's body: the post text with its facets rendered as
// links, then a block for each quote, then the external link card when present.
func summaryHTML(rec postRecord, c collected) string {
	var b strings.Builder
	b.WriteString(renderText(rec.Text, rec.Facets))
	for _, q := range c.quotes {
		b.WriteString(quoteBlock(q))
	}
	if c.external != nil {
		b.WriteString(externalBlock(c.external))
	}
	return b.String()
}

// renderText escapes text and wraps its facet ranges in links. Facet offsets
// are byte ranges into the UTF-8 text.
func renderText(text string, facets []facet) string {
	if text == "" {
		return ""
	}
	type span struct {
		start, end int
		feature    facetFeature
	}
	var spans []span
	for _, f := range facets {
		s, e := f.Index.ByteStart, f.Index.ByteEnd
		if s < 0 || e > len(text) || s >= e {
			continue
		}
		for _, ft := range f.Features {
			spans = append(spans, span{s, e, ft})
		}
	}
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].start < spans[j].start })

	var b strings.Builder
	pos := 0
	for _, sp := range spans {
		if sp.start < pos {
			continue // overlapping facet; the earlier one won
		}
		b.WriteString(escapeText(text[pos:sp.start]))
		seg := text[sp.start:sp.end]
		if u := featureURL(sp.feature); u != "" {
			b.WriteString(`<a class="external" href="` + html.EscapeString(u) + `" target="_blank" rel="noopener noreferrer">` + html.EscapeString(seg) + `</a>`)
		} else {
			b.WriteString(html.EscapeString(seg))
		}
		pos = sp.end
	}
	b.WriteString(escapeText(text[pos:]))
	return b.String()
}

// escapeText escapes HTML and keeps the post's newlines visible.
func escapeText(s string) string {
	return strings.ReplaceAll(html.EscapeString(s), "\n", "<br>")
}

// featureURL is the link a facet points at, or "" for an unknown feature type.
// A mention links to the mentioned account by DID (a stable identifier); a link
// uses its target; a tag opens the hashtag page.
func featureURL(f facetFeature) string {
	switch {
	case strings.HasSuffix(f.Type, "#link"):
		return f.URI
	case strings.HasSuffix(f.Type, "#mention"):
		if f.DID != "" {
			return "https://bsky.app/profile/" + f.DID
		}
	case strings.HasSuffix(f.Type, "#tag"):
		if f.Tag != "" {
			return "https://bsky.app/hashtag/" + f.Tag
		}
	}
	return ""
}

// externalBlock renders an external link card: the destination, its title, and
// its description.
func externalBlock(e *externalCard) string {
	if e == nil || e.URI == "" {
		return ""
	}
	title := strings.TrimSpace(e.Title)
	if title == "" {
		title = e.URI
	}
	var b strings.Builder
	b.WriteString(`<p class="ext-card"><a class="external" href="` + html.EscapeString(e.URI) + `" target="_blank" rel="noopener noreferrer">` + html.EscapeString(title) + `</a>`)
	if d := strings.TrimSpace(e.Description); d != "" {
		b.WriteString(` — ` + html.EscapeString(d))
	}
	b.WriteString(`</p>`)
	return b.String()
}

// quoteBlock renders a quoted post as a link to it. The quoted record's own
// content is not fetched (that would be one request per quote); the link
// carries the reader to it.
func quoteBlock(uri string) string {
	actor, rkey := splitATURI(uri)
	if actor == "" || rkey == "" {
		return ""
	}
	link := "https://bsky.app/profile/" + actor + "/post/" + rkey
	return `<blockquote><a class="external" href="` + html.EscapeString(link) + `" target="_blank" rel="noopener noreferrer">quoted post</a></blockquote>`
}

// titleFromText is a post's display title: its first non-empty line, clamped so
// a long line does not dominate a card.
func titleFromText(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return clampRunes(line, 100)
		}
	}
	return ""
}

// clampRunes truncates s to at most n runes, appending an ellipsis when cut.
func clampRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}

// rkeyFromURI returns the record key of an at:// URI (its last path segment).
func rkeyFromURI(uri string) string {
	parts := strings.Split(strings.TrimPrefix(uri, "at://"), "/")
	if len(parts) < 3 {
		return ""
	}
	return parts[len(parts)-1]
}

// splitATURI returns the repo (DID/handle) and rkey of an at:// URI.
func splitATURI(uri string) (actor, rkey string) {
	parts := strings.Split(strings.TrimPrefix(uri, "at://"), "/")
	if len(parts) < 3 {
		return "", ""
	}
	return parts[0], parts[len(parts)-1]
}

const dbTimeFormat = "2006-01-02 15:04:05"

// dbTime formats an RFC 3339 timestamp as the host's stored UTC form.
func dbTime(ts string) string {
	if ts == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ""
	}
	return t.UTC().Format(dbTimeFormat)
}

// collected is an embed's flattened media: every image, the first video, the
// first external card, and every quoted record URI. A recordWithMedia nests its
// media under a second embed, so the walk recurses.
type collected struct {
	images   []embedImage
	video    *blob
	external *externalCard
	quotes   []string
}

func collectEmbed(e *embed, c *collected) {
	if e == nil {
		return
	}
	c.images = append(c.images, e.Images...)
	if e.Video != nil && e.Video.Ref.Link != "" && c.video == nil {
		c.video = e.Video
	}
	if e.External != nil && e.External.URI != "" && c.external == nil {
		c.external = e.External
	}
	if e.Record != nil && e.Record.Record.URI != "" {
		c.quotes = append(c.quotes, e.Record.Record.URI)
	}
	collectEmbed(e.Media, c)
}

// ---- XRPC response shapes ----

type profile struct {
	DID         string `json:"did"`
	Handle      string `json:"handle"`
	DisplayName string `json:"displayName"`
	Avatar      string `json:"avatar"`
	Description string `json:"description"`
}

type record struct {
	URI   string          `json:"uri"`
	CID   string          `json:"cid"`
	Value json.RawMessage `json:"value"`
}

type listRecordsResponse struct {
	Records []record `json:"records"`
	Cursor  string   `json:"cursor"`
}

type postRecord struct {
	Type      string    `json:"$type"`
	Text      string    `json:"text"`
	CreatedAt string    `json:"createdAt"`
	Reply     *replyRef `json:"reply"`
	Facets    []facet   `json:"facets"`
	Embed     *embed    `json:"embed"`
}

type replyRef struct {
	Root   *strongRef `json:"root"`
	Parent *strongRef `json:"parent"`
}

type strongRef struct {
	URI string `json:"uri"`
	CID string `json:"cid"`
}

type facet struct {
	Index    facetIndex     `json:"index"`
	Features []facetFeature `json:"features"`
}

type facetIndex struct {
	ByteStart int `json:"byteStart"`
	ByteEnd   int `json:"byteEnd"`
}

type facetFeature struct {
	Type string `json:"$type"`
	URI  string `json:"uri"`
	DID  string `json:"did"`
	Tag  string `json:"tag"`
}

type embed struct {
	Type     string          `json:"$type"`
	Images   []embedImage    `json:"images"`
	Video    *blob           `json:"video"`
	External *externalCard   `json:"external"`
	Record   *embeddedRecord `json:"record"`
	Media    *embed          `json:"media"`
}

type embedImage struct {
	Alt         string       `json:"alt"`
	AspectRatio *aspectRatio `json:"aspectRatio"`
	Image       blob         `json:"image"`
}

type blob struct {
	Ref      blobRef `json:"ref"`
	MIMEType string  `json:"mimeType"`
	Size     int64   `json:"size"`
}

type blobRef struct {
	Link string `json:"$link"`
}

type aspectRatio struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type externalCard struct {
	URI         string `json:"uri"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Thumb       *blob  `json:"thumb"`
}

type embeddedRecord struct {
	Record strongRef `json:"record"`
}
