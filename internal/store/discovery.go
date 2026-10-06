package store

import (
	"context"
	"strings"
)

// FollowedFeedTokens maps the lowercased URL token of each feed the user
// subscribes to ("u/sam", "r/cats") to that feed's id. It is the set a feed's
// discovery mode matches a post's author against: an item whose author token is
// in the map is one the user already gets through another feed. The polled
// feed's own token is filtered out by the caller so a feed never hides its own
// items.
func (s *Store) FollowedFeedTokens(userID int64) map[string]int64 {
	rows, err := s.q.ListUserFeeds(context.Background(), userID)
	if err != nil {
		return nil
	}
	out := make(map[string]int64, len(rows))
	for _, f := range rows {
		tok := s.Feeds.policy.FeedToken(f.FeedUrl)
		if tok == "" {
			continue
		}
		tok = strings.ToLower(tok)
		if _, ok := out[tok]; !ok {
			out[tok] = f.FeedID
		}
	}
	return out
}

// AuthorTokensFor names the author(s) an item is attributed to, using the
// installed plugin tokenizer for the feed's URL. Empty when no tokenizer is
// installed or the URL has no author rule.
func (s *Store) AuthorTokensFor(feedURL string, categories []string) []string {
	if s.tokenizer == nil {
		return nil
	}
	return s.tokenizer.AuthorTokens(feedURL, categories)
}

// ApplyDiscoveryFilter drops, from feedID, every stored item whose author the
// user already follows through another feed. It is the retroactive form of the
// poller's ingest check: used when discovery mode is turned on, and after a new
// author feed is subscribed so its already-stored posts leave the discovery
// feeds. Best-effort at the caller.
func (s *Store) ApplyDiscoveryFilter(userID, feedID int64) (int, error) {
	f, err := s.Feeds.ByID(userID, feedID)
	if err != nil {
		return 0, err
	}
	if !f.HideFollowedAuthors {
		return 0, nil
	}
	followed := s.FollowedFeedTokens(userID)
	if len(followed) == 0 {
		return 0, nil
	}
	own := strings.ToLower(s.Feeds.policy.FeedToken(f.FeedURL))
	items, err := s.Items.ListFeedItemsForFilter(feedID)
	if err != nil {
		return 0, err
	}
	ids := make([]int64, 0)
	for _, it := range items {
		for _, tok := range s.AuthorTokensFor(f.FeedURL, it.Categories) {
			tok = strings.ToLower(tok)
			if tok == "" || tok == own {
				continue
			}
			if _, ok := followed[tok]; ok {
				ids = append(ids, it.ID)
				break
			}
		}
	}
	return s.Items.RemoveFeedMemberships(userID, feedID, ids)
}

// RefilterDiscoveryForUser re-applies discovery mode to every feed of the user
// that has it on. Called after subscribing to a new feed so its older posts
// stop showing in the user's discovery feeds immediately, rather than waiting
// for each discovery feed's next poll. Best-effort; returns how many items were
// removed.
func (s *Store) RefilterDiscoveryForUser(userID int64) (int, error) {
	feeds, err := s.Feeds.List(userID)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, f := range feeds {
		if !f.HideFollowedAuthors {
			continue
		}
		n, err := s.ApplyDiscoveryFilter(userID, f.ID)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}
