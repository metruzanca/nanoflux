package killthenewsletter

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/metruzanca/nanoflux/pluginapi"
)

// publicIDFromPath extracts the publicId from a Kill the Newsletter path of the
// forms /feeds/{id} or /feeds/{id}.xml. Returns ("", false) when the path does
// not match.
func publicIDFromPath(path string) (string, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[0] != "feeds" {
		return "", false
	}
	id := strings.TrimSuffix(parts[1], ".xml")
	if !isPublicID(id) {
		return "", false
	}
	return id, true
}

// publicIDFromFeedURL extracts the publicId from a feed URL on the configured
// host, or ("", false) when the URL is not one of this plugin's feeds.
func publicIDFromFeedURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || !isKTNHost(u.Hostname()) {
		return "", false
	}
	return publicIDFromPath(u.Path)
}

// deriveFeed resolves a feed for a Kill the Newsletter page URL with no request.
// Both the feed page (/feeds/{id}) and the Atom URL (/feeds/{id}.xml) map to the
// same canonical feed; the public service rate-limits discovery requests, so the
// candidate is built directly and marked Derived.
func deriveFeed(rawurl string) (pluginapi.Candidate, bool) {
	id, ok := publicIDFromFeedURL(rawurl)
	if !ok {
		return pluginapi.Candidate{}, false
	}
	return pluginapi.Candidate{
		FeedURL: feedURLFor(id),
		Title:   "Kill the Newsletter",
		HomeURL: homeURLFor(id),
		Derived: true,
	}, true
}

// canonicalFeedURL rewrites a Kill the Newsletter URL to the canonical Atom
// shape: https://{host}/feeds/{publicId}.xml. A URL with no publicId is returned
// unchanged.
func canonicalFeedURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || !isKTNHost(u.Hostname()) {
		return raw
	}
	id, ok := publicIDFromPath(u.Path)
	if !ok {
		return raw
	}
	return feedURLFor(id)
}

// feedToken returns the publicId a feed URL represents (the token a feed is
// identified by), or "" when the URL is not one of this plugin's feeds.
func feedToken(feedURL string) string {
	id, _ := publicIDFromFeedURL(feedURL)
	return id
}

// provisionResponse is the JSON body POST /feeds returns when the request asks
// for application/json.
type provisionResponse struct {
	FeedID string `json:"feedId"`
	Email  string `json:"email"`
	Feed   string `json:"feed"`
}

// parseProvisionResponse extracts the publicId, inbox address and feed URL from
// a create response. Any field may be empty when the service omits it; the
// caller fills the email and feed URL from the publicId.
func parseProvisionResponse(body []byte) (id, email, feedURL string) {
	var r provisionResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return "", "", ""
	}
	return r.FeedID, r.Email, r.Feed
}
