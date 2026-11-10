package httpapi

import (
	"net/http"
	"sort"
	"time"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
)

// statsData is everything the /stats page renders. All aggregation is done per
// request: the page is deliberately not precached and may be a little slow.
type statsData struct {
	Period      string
	PeriodLabel string
	Overview    store.StatsOverview
	ReadRate    int

	Activity []statsBar
	Heatmap  [7][24]statsCell
	Streak   store.Streak

	Sources     []statsSourceRow
	BestSources []statsSourceRow
	Tags        []statsTagRow

	Aging    []statsBar
	AgingMax int

	Lag      store.ReadLag
	LagTotal int

	Media      []statsMediaRow
	MediaTotal string
	MediaItems int
}

// statsBar is one column in a bar chart. Height is 0..100.
type statsBar struct {
	Label  string
	Value  int
	Height int
}

// statsCell is one heatmap square. Level is 0..4.
type statsCell struct {
	Count int
	Level int
}

// statsSourceRow is one author in the popularity table.
type statsSourceRow struct {
	AuthorID  int64
	Name      string
	Feeds     int
	Total     int
	Read      int
	Favorites int
	Rate      int
}

// statsTagRow is one tag with its read rate.
type statsTagRow struct {
	Tag   string
	Total int
	Read  int
	Rate  int
	Width int
}

// statsMediaRow is one author's watch-time total.
type statsMediaRow struct {
	Name       string
	Watched    string
	Completion int
	Samples    int
}

var statsWeekdays = [7]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}

// statsPage renders the read-only viewing statistics page.
func (s *Server) statsPage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	period, since, label := statsPeriodOf(r)
	loc := web.Location(u.Timezone)
	now := time.Now()
	p := store.StatsPeriod{Start: since}
	items := s.store.Items

	overview, err := items.StatsOverview(u.ID, p)
	if err != nil {
		log.Error("stats overview", "user_id", u.ID, "err", err)
		internalError(w, "load stats", err)
		return
	}

	days, err := items.StatsReadSeries(u.ID, p, loc)
	if err != nil {
		log.Error("stats read series", "user_id", u.ID, "err", err)
	}
	heatmap, err := items.StatsReadHeatmap(u.ID, p, loc)
	if err != nil {
		log.Error("stats heatmap", "user_id", u.ID, "err", err)
	}
	streak, err := items.StatsStreaks(u.ID, loc)
	if err != nil {
		log.Error("stats streaks", "user_id", u.ID, "err", err)
	}
	sources, err := items.StatsTopSources(u.ID, p, 15)
	if err != nil {
		log.Error("stats sources", "user_id", u.ID, "err", err)
	}
	tags, err := items.StatsTags(u.ID, p, 20)
	if err != nil {
		log.Error("stats tags", "user_id", u.ID, "err", err)
	}
	aging, err := items.StatsUnreadAging(u.ID)
	if err != nil {
		log.Error("stats unread aging", "user_id", u.ID, "err", err)
	}
	lag, err := items.StatsReadLag(u.ID, p)
	if err != nil {
		log.Error("stats read lag", "user_id", u.ID, "err", err)
	}
	media, err := items.StatsMedia(u.ID, p, 15)
	if err != nil {
		log.Error("stats media", "user_id", u.ID, "err", err)
	}

	d := statsData{
		Period:      period,
		PeriodLabel: label,
		Overview:    overview,
		Activity:    buildActivityBars(days, since, period, loc, now),
		Heatmap:     heatmapCells(heatmap),
		Streak:      streak,
		Sources:     sourceRows(sources),
		BestSources: bestSourceRows(sources, 5),
		Tags:        tagRows(tags),
		Aging:       bucketBars(aging),
		Lag:         lag,
		Media:       mediaRows(media),
	}
	for _, m := range media {
		d.MediaItems += m.Samples
	}
	d.MediaTotal = formatWatch(mediaWatchSec(media))
	if overview.Total > 0 {
		d.ReadRate = overview.Read * 100 / overview.Total
	}
	for _, b := range d.Aging {
		if b.Value > d.AgingMax {
			d.AgingMax = b.Value
		}
	}
	for _, b := range lag.Buckets {
		d.LagTotal += b.Count
	}

	web.Render(w, r, basePage("stats", u, statsPage(d)))
}

// statsPeriodOf parses ?period= into a store period plus its display label.
func statsPeriodOf(r *http.Request) (string, time.Time, string) {
	now := time.Now()
	switch r.URL.Query().Get("period") {
	case "30d":
		return "30d", now.AddDate(0, 0, -30), "last 30 days"
	case "1y":
		return "1y", now.AddDate(-1, 0, 0), "last year"
	case "all":
		return "all", time.Time{}, "all time"
	default:
		return "90d", now.AddDate(0, 0, -90), "last 90 days"
	}
}

// buildActivityBars turns per-day read counts into chart columns. Bars are daily
// for 30/90 days, weekly for a year, and monthly for all time, so the chart never
// collapses into unreadable slivers.
func buildActivityBars(days []store.DayCount, since time.Time, period string, loc *time.Location, now time.Time) []statsBar {
	byDay := make(map[string]int, len(days))
	var first time.Time
	for _, d := range days {
		byDay[d.Day] += d.Count
		if t, err := time.ParseInLocation("2006-01-02", d.Day, loc); err == nil {
			if first.IsZero() || t.Before(first) {
				first = t
			}
		}
	}

	start := since.In(loc)
	if since.IsZero() {
		if first.IsZero() {
			return nil
		}
		start = first
	}
	start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc)
	end := now.In(loc)

	switch period {
	case "1y":
		// Weeks starting Monday.
		start = start.AddDate(0, 0, -((int(start.Weekday()) + 6) % 7))
		var out []statsBar
		for d := start; !d.After(end); d = d.AddDate(0, 0, 7) {
			sum := 0
			for i := 0; i < 7; i++ {
				sum += byDay[d.AddDate(0, 0, i).Format("2006-01-02")]
			}
			out = append(out, statsBar{Label: "week of " + d.Format("Jan 2"), Value: sum})
		}
		return normalizeBars(out)
	case "all":
		start = time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, loc)
		var out []statsBar
		for d := start; !d.After(end); d = d.AddDate(0, 1, 0) {
			prefix := d.Format("2006-01")
			sum := 0
			for day, n := range byDay {
				if len(day) >= 7 && day[:7] == prefix {
					sum += n
				}
			}
			out = append(out, statsBar{Label: d.Format("Jan 2006"), Value: sum})
		}
		return normalizeBars(out)
	default:
		var out []statsBar
		for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
			out = append(out, statsBar{Label: d.Format("Mon, Jan 2"), Value: byDay[d.Format("2006-01-02")]})
		}
		return normalizeBars(out)
	}
}

// normalizeBars scales values to a 0..100 height and gives any non-zero bar a
// visible minimum.
func normalizeBars(bars []statsBar) []statsBar {
	max := 0
	for _, b := range bars {
		if b.Value > max {
			max = b.Value
		}
	}
	if max == 0 {
		return bars
	}
	for i := range bars {
		bars[i].Height = bars[i].Value * 100 / max
		if bars[i].Value > 0 && bars[i].Height < 4 {
			bars[i].Height = 4
		}
	}
	return bars
}

// heatmapCells converts raw counts into 0..4 intensity levels.
func heatmapCells(grid [7][24]int) [7][24]statsCell {
	var out [7][24]statsCell
	max := 0
	for d := 0; d < 7; d++ {
		for h := 0; h < 24; h++ {
			if grid[d][h] > max {
				max = grid[d][h]
			}
		}
	}
	for d := 0; d < 7; d++ {
		for h := 0; h < 24; h++ {
			n := grid[d][h]
			level := 0
			if n > 0 && max > 0 {
				level = 1 + (n-1)*3/max
				if level > 4 {
					level = 4
				}
			}
			out[d][h] = statsCell{Count: n, Level: level}
		}
	}
	return out
}

func sourceRows(stats []store.SourceStat) []statsSourceRow {
	out := make([]statsSourceRow, 0, len(stats))
	for _, s := range stats {
		rate := 0
		if s.Total > 0 {
			rate = s.Read * 100 / s.Total
		}
		out = append(out, statsSourceRow{
			AuthorID: s.AuthorID, Name: s.Name, Feeds: s.Feeds,
			Total: s.Total, Read: s.Read, Favorites: s.Favorites, Rate: rate,
		})
	}
	return out
}

// bestSourceRows ranks sources by read rate, requiring a minimum volume so a
// one-post feed that happened to be read cannot top the list.
func bestSourceRows(stats []store.SourceStat, limit int) []statsSourceRow {
	const minPosts = 20
	var eligible []store.SourceStat
	for _, s := range stats {
		if s.Total >= minPosts {
			eligible = append(eligible, s)
		}
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		ri := eligible[i].Read * 100 / eligible[i].Total
		rj := eligible[j].Read * 100 / eligible[j].Total
		if ri != rj {
			return ri > rj
		}
		return eligible[i].Read > eligible[j].Read
	})
	if len(eligible) > limit {
		eligible = eligible[:limit]
	}
	return sourceRows(eligible)
}

func tagRows(stats []store.TagStat) []statsTagRow {
	if len(stats) == 0 {
		return nil
	}
	max := 0
	for _, s := range stats {
		if s.Total > max {
			max = s.Total
		}
	}
	out := make([]statsTagRow, 0, len(stats))
	for _, s := range stats {
		rate, width := 0, 0
		if s.Total > 0 {
			rate = s.Read * 100 / s.Total
			width = s.Total * 100 / max
		}
		out = append(out, statsTagRow{Tag: s.Tag, Total: s.Total, Read: s.Read, Rate: rate, Width: width})
	}
	return out
}

func bucketBars(buckets []store.Bucket) []statsBar {
	max := 0
	for _, b := range buckets {
		if b.Count > max {
			max = b.Count
		}
	}
	out := make([]statsBar, 0, len(buckets))
	for _, b := range buckets {
		height := 0
		if max > 0 {
			height = b.Count * 100 / max
			if b.Count > 0 && height < 4 {
				height = 4
			}
		}
		out = append(out, statsBar{Label: b.Label, Value: b.Count, Height: height})
	}
	return out
}

func mediaRows(stats []store.MediaStat) []statsMediaRow {
	out := make([]statsMediaRow, 0, len(stats))
	for _, s := range stats {
		completion := 0
		if s.Duration > 0 {
			completion = int(s.Watched * 100 / s.Duration)
			if completion > 100 {
				completion = 100
			}
		}
		out = append(out, statsMediaRow{
			Name: s.Name, Watched: formatWatch(s.Watched), Completion: completion, Samples: s.Samples,
		})
	}
	return out
}

// formatWatch renders a watch-time total: "0:00" for none, otherwise the shared
// duration format.
func formatWatch(sec int64) string {
	if sec <= 0 {
		return "0:00"
	}
	return web.FormatDuration(int(sec))
}

// mediaWatchSec sums the per-author breakdown's watch time, so the headline
// matches the table (the overview total also counts positions whose item is gone
// or hidden).
func mediaWatchSec(stats []store.MediaStat) int64 {
	var total int64
	for _, m := range stats {
		total += m.Watched
	}
	return total
}

// statsPeriodHref builds a period link, preserving nothing else (the page has no
// other query state).
func statsPeriodHref(period string) string {
	if period == "90d" {
		return "/stats"
	}
	return "/stats?period=" + period
}

// statsBarTitle is the accessible label for one activity bar.
func statsBarTitle(b statsBar) string {
	return b.Label + ": " + readsText(b.Value)
}
