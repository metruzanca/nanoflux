package storage

import (
	"context"
	"testing"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/filestore"
	"github.com/metruzanca/nanoflux/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	sqldb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { sqldb.Close() })
	if err := db.Migrate(sqldb); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store.New(sqldb)
}

func TestAuditAndOrphans(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	u, err := st.Users.Create("alice", "h")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	a, _ := st.Authors.Create(u.ID, "Metru", "", "")
	f, _ := st.Feeds.Create(u.ID, a.ID, "Blog", "https://x.dev/rss.xml", "", "", 900)
	if _, err := st.Items.Upsert(f.ID, store.Item{GUID: "g1", Title: "One", FetchedAt: db.Now()}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	itemID, _ := st.Items.ByFeedIdentity(f.ID, "g1")
	if err := st.Items.SetItemImageCacheKey(itemID, "cache/tumblr/1/0.png", 1000); err != nil {
		t.Fatalf("set cache key: %v", err)
	}

	files := filestore.NewMemory()
	put := func(key string, n int) {
		t.Helper()
		if err := files.Put(ctx, key, "image/png", make([]byte, n)); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
	}
	put("cache/tumblr/1/0.png", 1000) // referenced
	put("cache/feeds/2/0.png", 500)   // orphaned
	put("avatars/1", 10)              // not cache

	rep, err := Audit(ctx, files, st)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if rep.TotalBytes != 1510 || rep.TotalObjects != 3 {
		t.Fatalf("totals = %d bytes / %d objects, want 1510 / 3", rep.TotalBytes, rep.TotalObjects)
	}
	if rep.OrphanBytes != 500 || rep.OrphanObjects != 1 {
		t.Fatalf("orphans = %d bytes / %d objects, want 500 / 1", rep.OrphanBytes, rep.OrphanObjects)
	}
	byLabel := map[string]Group{}
	for _, g := range rep.Groups {
		byLabel[g.Label] = g
	}
	if g := byLabel["cache/tumblr"]; g.Bytes != 1000 || g.OrphanBytes != 0 {
		t.Fatalf("cache/tumblr = %+v", g)
	}
	if g := byLabel["cache/feeds"]; g.Bytes != 500 || g.OrphanBytes != 500 {
		t.Fatalf("cache/feeds = %+v", g)
	}
	if g := byLabel["avatars"]; g.Bytes != 10 || g.OrphanBytes != 0 {
		t.Fatalf("avatars = %+v", g)
	}

	orphans, err := Orphans(ctx, files, st)
	if err != nil {
		t.Fatalf("Orphans: %v", err)
	}
	if len(orphans) != 1 || orphans[0].Key != "cache/feeds/2/0.png" {
		t.Fatalf("orphans = %+v", orphans)
	}
}
