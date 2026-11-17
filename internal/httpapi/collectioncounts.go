package httpapi

import (
	"sync"
	"time"

	"github.com/metruzanca/nanoflux/internal/store"
)

// collectionCountsTTL is how long a user's per-collection unread/read counts
// are reused.
//
// The collections index is allowed to show approximate counts: computing them
// costs a scan per collection scope plus a scoped query per view-bearing
// collection. A minute of lag after reading items is the accepted trade, and it
// keeps the cache TTL-only like navCountsCache and authorUnreadCache: no
// invalidation hooks to keep in sync with every read marker or membership edit.
const collectionCountsTTL = 60 * time.Second

// collectionCountsCache memoizes the collections index's item counts per user.
type collectionCountsCache struct {
	mu     sync.Mutex
	byUser map[int64]collectionCountsEntry
	ttl    time.Duration
}

type collectionCountsEntry struct {
	// counts maps collection id to its unread/read totals, with the member
	// views' scopes already merged in. Treat it as read-only.
	counts map[int64]store.CollectionCounts
	// tz is the timezone the counts were computed under. A member view may carry
	// a time filter, so a timezone change invalidates the entry.
	tz string
	at time.Time
}

func newCollectionCountsCache() *collectionCountsCache {
	return &collectionCountsCache{byUser: map[int64]collectionCountsEntry{}, ttl: collectionCountsTTL}
}

func (c *collectionCountsCache) get(userID int64, tz string) (map[int64]store.CollectionCounts, bool) {
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

func (c *collectionCountsCache) set(userID int64, tz string, counts map[int64]store.CollectionCounts) {
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
	c.byUser[userID] = collectionCountsEntry{counts: counts, tz: tz, at: time.Now()}
}
