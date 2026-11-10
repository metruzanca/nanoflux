package store

import (
	"strconv"
	"testing"
	"time"

	"github.com/metruzanca/nanoflux/internal/db"
)

func markReadAt(t *testing.T, s *Store, itemID int64, at time.Time) {
	t.Helper()
	if _, err := s.DB().Exec(`UPDATE items SET read = 1, read_at = ? WHERE id = ?`,
		db.FormatTime(at), itemID); err != nil {
		t.Fatalf("mark read: %v", err)
	}
}

func TestStatsAggregates(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")

	alpha, _ := s.Authors.Create(u.ID, "Alpha", "", "")
	fa, _ := s.Feeds.Create(u.ID, alpha.ID, "Alpha feed", "https://alpha.dev/feed", "", "", 900)
	blocked, _ := s.Authors.Create(u.ID, "Blocked", "", "")
	fb, _ := s.Feeds.Create(u.ID, blocked.ID, "Blocked feed", "https://blocked.dev/feed", "", "", 900)

	now := time.Now().UTC().Truncate(time.Second)
	seed := func(feed int64, guid string, at time.Time, cats ...string) int64 {
		t.Helper()
		if _, err := s.Items.Upsert(feed, Item{
			GUID: guid, Title: guid, Link: "https://x.dev/" + guid,
			Categories: cats, PublishedAt: db.FormatTime(at), FetchedAt: db.Now(),
		}); err != nil {
			t.Fatalf("upsert %s: %v", guid, err)
		}
		id, err := s.Items.ByFeedIdentity(feed, guid)
		if err != nil {
			t.Fatalf("find %s: %v", guid, err)
		}
		return id
	}

	i1 := seed(fa.ID, "i1", now.Add(-48*time.Hour), "r/golang")
	i2 := seed(fa.ID, "i2", now.Add(-24*time.Hour), "r/golang", "r/rust")
	seed(fa.ID, "i3", now, "r/rust")
	seed(fa.ID, "i4", now.Add(-5*24*time.Hour))
	i5 := seed(fb.ID, "i5", now.Add(-2*time.Hour), "r/golang")

	markReadAt(t, s, i1, now.Add(-48*time.Hour).Add(2*time.Hour))
	markReadAt(t, s, i2, now.Add(-24*time.Hour).Add(30*time.Minute))
	markReadAt(t, s, i5, now.Add(-2*time.Hour).Add(time.Hour))
	if err := s.Items.SetFavorite(u.ID, i1, true); err != nil {
		t.Fatal(err)
	}
	if err := s.AuthorFilters.Replace(u.ID, blocked.ID, []FilterRule{{
		Kind: FilterKindFeed, Action: FilterActionHide, Combine: FilterCombineAny,
		Values: []string{strconv.FormatInt(fb.ID, 10)},
	}}); err != nil {
		t.Fatal(err)
	}

	p := StatsPeriod{}
	ov, err := s.Items.StatsOverview(u.ID, p)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if ov.Total != 4 || ov.Read != 2 || ov.Unread != 2 || ov.Favorites != 1 || ov.Bookmarks != 0 {
		t.Fatalf("overview = %+v", ov)
	}

	loc := time.UTC
	series, err := s.Items.StatsReadSeries(u.ID, p, loc)
	if err != nil {
		t.Fatalf("series: %v", err)
	}
	if len(series) != 2 || series[0].Count != 1 || series[1].Count != 1 {
		t.Fatalf("series = %+v", series)
	}

	heat, err := s.Items.StatsReadHeatmap(u.ID, p, loc)
	if err != nil {
		t.Fatalf("heatmap: %v", err)
	}
	total := 0
	for d := 0; d < 7; d++ {
		for h := 0; h < 24; h++ {
			total += heat[d][h]
		}
	}
	if total != 2 {
		t.Fatalf("heatmap total = %d", total)
	}

	sources, err := s.Items.StatsTopSources(u.ID, p, 15)
	if err != nil {
		t.Fatalf("sources: %v", err)
	}
	if len(sources) != 1 || sources[0].Name != "Alpha" || sources[0].Total != 4 || sources[0].Read != 2 || sources[0].Favorites != 1 {
		t.Fatalf("sources = %+v", sources)
	}

	tags, err := s.Items.StatsTags(u.ID, p, 20)
	if err != nil {
		t.Fatalf("tags: %v", err)
	}
	got := map[string]TagStat{}
	for _, tag := range tags {
		got[tag.Tag] = tag
	}
	if got["r/golang"].Total != 2 || got["r/golang"].Read != 2 {
		t.Fatalf("golang tag = %+v", got["r/golang"])
	}
	if got["r/rust"].Total != 2 || got["r/rust"].Read != 1 {
		t.Fatalf("rust tag = %+v", got["r/rust"])
	}

	aging, err := s.Items.StatsUnreadAging(u.ID)
	if err != nil {
		t.Fatalf("aging: %v", err)
	}
	if len(aging) != 4 || aging[0].Count != 1 || aging[1].Count != 1 || aging[2].Count != 0 || aging[3].Count != 0 {
		t.Fatalf("aging = %+v", aging)
	}

	lag, err := s.Items.StatsReadLag(u.ID, p)
	if err != nil {
		t.Fatalf("lag: %v", err)
	}
	if lag.Avg != 75*time.Minute || lag.Median != 75*time.Minute {
		t.Fatalf("lag = %+v", lag)
	}
	if lag.Buckets[0].Count != 1 || lag.Buckets[1].Count != 1 {
		t.Fatalf("lag buckets = %+v", lag.Buckets)
	}

	if err := s.Progress.Set(u.ID, i1, "item:1", 300, 600); err != nil {
		t.Fatalf("progress: %v", err)
	}
	media, err := s.Items.StatsMedia(u.ID, p, 15)
	if err != nil {
		t.Fatalf("media: %v", err)
	}
	if len(media) != 1 || media[0].Name != "Alpha" || media[0].Watched != 300 || media[0].Duration != 600 || media[0].Samples != 1 {
		t.Fatalf("media = %+v", media)
	}
}

func TestStatsStreaks(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "A", "", "")
	f, _ := s.Feeds.Create(u.ID, a.ID, "F", "https://a.dev/feed", "", "", 900)

	loc := time.UTC
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, loc)

	var reads []time.Time
	// Current run: today, yesterday, the day before.
	for i := 0; i < 3; i++ {
		reads = append(reads, today.AddDate(0, 0, -i))
	}
	// Longest run: five consecutive days, two weeks back.
	for i := 0; i < 5; i++ {
		reads = append(reads, today.AddDate(0, 0, -10-i))
	}
	for i, at := range reads {
		guid := "g" + strconv.Itoa(i)
		if _, err := s.Items.Upsert(f.ID, Item{
			GUID: guid, Title: guid, Link: "https://a.dev/" + guid,
			PublishedAt: db.FormatTime(at.Add(-time.Hour)), FetchedAt: db.Now(),
		}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		id, err := s.Items.ByFeedIdentity(f.ID, guid)
		if err != nil {
			t.Fatalf("find: %v", err)
		}
		markReadAt(t, s, id, at)
	}

	streak, err := s.Items.StatsStreaks(u.ID, loc)
	if err != nil {
		t.Fatalf("streaks: %v", err)
	}
	if streak.Current != 3 || streak.Longest != 5 {
		t.Fatalf("streak = %+v, want current 3 longest 5", streak)
	}
}

func TestStatsPeriodFiltersItems(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	a, _ := s.Authors.Create(u.ID, "A", "", "")
	f, _ := s.Feeds.Create(u.ID, a.ID, "F", "https://a.dev/feed", "", "", 900)

	now := time.Now().UTC().Truncate(time.Second)
	for _, it := range []struct {
		guid string
		at   time.Time
	}{{"old", now.AddDate(0, 0, -100)}, {"new", now.AddDate(0, 0, -1)}} {
		if _, err := s.Items.Upsert(f.ID, Item{
			GUID: it.guid, Title: it.guid, Link: "https://a.dev/" + it.guid,
			PublishedAt: db.FormatTime(it.at), FetchedAt: db.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	recent := StatsPeriod{Start: now.AddDate(0, 0, -30)}
	ov, err := s.Items.StatsOverview(u.ID, recent)
	if err != nil {
		t.Fatal(err)
	}
	if ov.Total != 1 {
		t.Fatalf("recent total = %d, want 1", ov.Total)
	}
	all, err := s.Items.StatsOverview(u.ID, StatsPeriod{})
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 2 {
		t.Fatalf("all total = %d, want 2", all.Total)
	}
}
