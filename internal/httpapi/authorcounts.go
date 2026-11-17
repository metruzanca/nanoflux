package httpapi

import (
	"sync"
	"time"
)

// authorUnreadTTL is how long a user's per-author unread counts are reused.
//
// The author index is allowed to show approximate counts: computing them costs
// a pass over every item an author owns plus a scoped query per view-bearing
// author, which is seconds on the small hosts this app targets. A minute of
// lag after marking items read is the accepted trade, and it keeps the cache
// TTL-only like navCountsCache: no invalidation hooks to keep in sync with
// every read marker, feed delete or view edit.
const authorUnreadTTL = 60 * time.Second

// authorUnreadCache memoizes the author index's unread counts per user. Like
// navCountsCache it is TTL-only on purpose.
type authorUnreadCache struct {
	mu     sync.Mutex
	byUser map[int64]authorUnreadEntry
	ttl    time.Duration
}

type authorUnreadEntry struct {
	// counts maps author id to unread items, with each view-bearing author's
	// saved-view scope already merged in. Treat it as read-only.
	counts map[int64]int
	// tz is the timezone the counts were computed under. A saved view may carry
	// a time filter, so a timezone change invalidates the entry.
	tz string
	at time.Time
}

func newAuthorUnreadCache() *authorUnreadCache {
	return &authorUnreadCache{byUser: map[int64]authorUnreadEntry{}, ttl: authorUnreadTTL}
}

func (c *authorUnreadCache) get(userID int64, tz string) (map[int64]int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.byUser[userID]
	if !ok {
		return nil, false
	}
	if e.tz != tz || time.Since(e.at) >= c.ttl {
		delete(c.byUser, userID)
		return nil, false
	}
	return e.counts, true
}

func (c *authorUnreadCache) set(userID int64, tz string, counts map[int64]int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.byUser) >= 4096 {
		now := time.Now()
		for id, e := range c.byUser {
			if now.Sub(e.at) >= c.ttl {
				delete(c.byUser, id)
			}
		}
	}
	c.byUser[userID] = authorUnreadEntry{counts: counts, tz: tz, at: time.Now()}
}
