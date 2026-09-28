package httpapi

import (
	"strconv"
	"strings"

	"github.com/metruzanca/nanoflux/internal/store"
)

// attrPart is one clickable piece of an item's source attribution.
type attrPart struct {
	Text     string
	URL      string
	External bool
}

// itemAttribution returns the "r/cats by u/sam" parts for a reddit post, or nil
// for any other item (whose templates keep the existing author/feed source).
// Both parts are linked: the subreddit to the subscribed sub feed's author page
// (or its feed page, or reddit when unsubscribed), and the poster to an internal
// author when the user also subscribes to that user's feed, otherwise to the
// reddit profile.
func itemAttribution(it store.ItemWithFeed) []attrPart {
	if it.FeedIsSystem {
		return nil
	}
	parts, _ := redditAttribution(it)
	return parts
}

// redditAttribution builds "r/<sub> by u/<user>" from an item's categories. It
// reports false for a non-reddit item (no r/ + u/ category pair).
func redditAttribution(it store.ItemWithFeed) ([]attrPart, bool) {
	var sub, user string
	for _, c := range it.Categories {
		lc := strings.ToLower(c)
		switch {
		case strings.HasPrefix(lc, "r/"):
			sub = c
		case strings.HasPrefix(lc, "u/"):
			user = c
		}
	}
	if sub == "" || user == "" {
		return nil, false
	}

	subURL := subLink(it, sub)
	parts := []attrPart{{Text: sub, URL: subURL, External: !strings.HasPrefix(subURL, "/")}}
	parts = append(parts, attrPart{Text: "by"})
	if url := userLink(it, user); url != "" {
		parts = append(parts, attrPart{Text: user, URL: url, External: !strings.HasPrefix(url, "/")})
	} else {
		parts = append(parts, attrPart{
			Text:     user,
			URL:      "https://www.reddit.com/user/" + strings.TrimPrefix(user, "u/") + "/",
			External: true,
		})
	}
	return parts, true
}

// subLink resolves r/<sub> to the subscribed sub feed's author page, falling
// back to the feed page (both are internal).
func subLink(it store.ItemWithFeed, sub string) string {
	if authorEqual(it.AuthorName, sub) {
		return "/authors/" + strconv.FormatInt(it.AuthorID, 10)
	}
	if feedTitleEqual(it.FeedTitle, sub) {
		return "/feeds/" + strconv.FormatInt(it.FeedID, 10)
	}
	if link, ok := redditLink(it, sub); ok {
		if link.AuthorID != 0 {
			return "/authors/" + strconv.FormatInt(link.AuthorID, 10)
		}
		return "/feeds/" + strconv.FormatInt(link.FeedID, 10)
	}
	for _, src := range it.Sources {
		if authorEqual(src.AuthorName, sub) {
			return "/authors/" + strconv.FormatInt(src.AuthorID, 10)
		}
		if feedTitleEqual(src.FeedTitle, sub) {
			return "/feeds/" + strconv.FormatInt(src.FeedID, 10)
		}
	}
	// The sub feed is not subscribed: link to reddit's subreddit.
	return "https://www.reddit.com/" + strings.TrimPrefix(sub, "/") + "/"
}

// userLink resolves u/<user> to an internal author page when the user also
// subscribes to that reddit user's feed (whose author is named without the u/
// prefix), or "" when there is no internal author.
func userLink(it store.ItemWithFeed, user string) string {
	if authorEqual(it.AuthorName, user) {
		return "/authors/" + strconv.FormatInt(it.AuthorID, 10)
	}
	if link, ok := redditLink(it, user); ok && link.AuthorID != 0 {
		return "/authors/" + strconv.FormatInt(link.AuthorID, 10)
	}
	for _, src := range it.Sources {
		if authorEqual(src.AuthorName, user) {
			return "/authors/" + strconv.FormatInt(src.AuthorID, 10)
		}
	}
	return ""
}

// redditLink finds the subscribed reddit feed a token resolves to, among the
// item's RedditLinks (populated from the user's subscription list). It lets a
// poster link internally even when the post's user feed has not polled it yet.
func redditLink(it store.ItemWithFeed, token string) (store.RedditLink, bool) {
	t := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(token), "/"))
	for _, link := range it.RedditLinks {
		if link.Token == t {
			return link, true
		}
	}
	return store.RedditLink{}, false
}

// authorEqual compares an author name to a reddit "r/x" or "u/x" token,
// tolerating the optional leading slash and, for a user, the absent u/ prefix
// (reddit user feeds are stored under the bare name).
func authorEqual(author, token string) bool {
	if author == "" || token == "" {
		return false
	}
	a := strings.ToLower(strings.TrimPrefix(author, "/"))
	t := strings.ToLower(strings.TrimPrefix(token, "/"))
	if a == t {
		return true
	}
	// A user feed's author is "sam", the token "u/sam".
	if strings.HasPrefix(t, "u/") && a == strings.TrimPrefix(t, "u/") {
		return true
	}
	return false
}

func feedTitleEqual(title, token string) bool {
	if title == "" || token == "" {
		return false
	}
	return strings.EqualFold(strings.TrimPrefix(title, "/"), strings.TrimPrefix(token, "/"))
}
