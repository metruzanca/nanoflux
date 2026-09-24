package httpapi

import (
	"encoding/xml"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/feedparse"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
)

type opmlHead struct {
	Title string `xml:"title"`
}

type opmlOutline struct {
	Type     string        `xml:"type,attr,omitempty"`
	Text     string        `xml:"text,attr"`
	Title    string        `xml:"title,attr,omitempty"`
	XMLURL   string        `xml:"xmlUrl,attr,omitempty"`
	HTMLURL  string        `xml:"htmlUrl,attr,omitempty"`
	Outlines []opmlOutline `xml:"outline"`
}

type opmlDocument struct {
	XMLName xml.Name `xml:"opml"`
	Version string   `xml:"version,attr"`
	Head    opmlHead `xml:"head"`
	Body    struct {
		Outlines []opmlOutline `xml:"outline"`
	} `xml:"body"`
}

// settingsOpmlData carries the import result shown in the settings OPML card.
type settingsOpmlData struct {
	Message string
}

func feedOutline(f store.Feed) opmlOutline {
	return opmlOutline{Type: "rss", Text: f.Title, Title: f.Title, XMLURL: f.FeedURL, HTMLURL: f.HomeURL}
}

// opmlExport writes the user's subscriptions as an OPML 2.0 document, grouped
// into outlines per collection.
func (s *Server) opmlExport(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	feeds, err := s.store.Feeds.List(u.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	collections, _ := s.store.Collections.List(u.ID)

	byColl := make(map[int64][]store.Feed)
	var ungrouped []store.Feed
	for _, f := range feeds {
		ids, _ := s.store.Collections.FeedCollectionIDs(u.ID, f.ID)
		if len(ids) == 0 {
			ungrouped = append(ungrouped, f)
			continue
		}
		// Auto collections are derived from the feed's home url and are not
		// exported; a feed whose only memberships are auto collections is
		// exported ungrouped.
		manual := false
		for _, cid := range ids {
			byColl[cid] = append(byColl[cid], f)
			if !collectionIsAuto(collections, cid) {
				manual = true
			}
		}
		if !manual {
			ungrouped = append(ungrouped, f)
		}
	}

	doc := opmlDocument{Version: "2.0"}
	doc.Head.Title = "nanoflux feeds"
	for _, f := range ungrouped {
		doc.Body.Outlines = append(doc.Body.Outlines, feedOutline(f))
	}
	for _, c := range collections {
		if c.IsAuto || len(byColl[c.ID]) == 0 {
			continue
		}
		group := opmlOutline{Type: "rss", Text: c.Name}
		for _, f := range byColl[c.ID] {
			group.Outlines = append(group.Outlines, feedOutline(f))
		}
		doc.Body.Outlines = append(doc.Body.Outlines, group)
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="nanoflux.opml"`)
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		log.Error("opml export", "err", err)
	}
}

// importEntry is one feed outline from an imported OPML document, tagged with
// the collection group it appeared under ("" = none).
type importEntry struct {
	outline    opmlOutline
	collection string
}

// opmlImport creates feeds from an uploaded OPML document. Each outline is
// validated server-side before saving; feeds whose url already exists are
// skipped. The result summary re-renders the settings card.
func (s *Server) opmlImport(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)

	file, _, err := r.FormFile("file")
	if err != nil {
		writeFormError(w, r, "opml-error", "choose an opml file")
		return
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, 4<<20))
	if err != nil {
		writeFormError(w, r, "opml-error", "could not read the file")
		return
	}
	var doc opmlDocument
	if err := xml.Unmarshal(body, &doc); err != nil {
		writeFormError(w, r, "opml-error", "that file is not valid opml")
		return
	}

	var entries []importEntry
	var walk func(o opmlOutline, coll string)
	walk = func(o opmlOutline, coll string) {
		if o.XMLURL != "" {
			entries = append(entries, importEntry{outline: o, collection: coll})
			return
		}
		for _, child := range o.Outlines {
			walk(child, o.Text)
		}
	}
	for _, o := range doc.Body.Outlines {
		walk(o, "")
	}

	// Existing feed urls are skipped (normalized to a comparable form).
	existing := map[string]bool{}
	for _, f := range s.mustFeeds(u.ID) {
		existing[normalizeFeedKey(f.FeedURL)] = true
	}
	collections, _ := s.store.Collections.List(u.ID)

	client := s.client
	var imported, skipped, failed int
	authorsByName := map[string]int64{}
	for _, e := range entries {
		feedURL := strings.TrimSpace(e.outline.XMLURL)
		if feedURL == "" || existing[normalizeFeedKey(feedURL)] {
			skipped++
			continue
		}
		title := strings.TrimSpace(e.outline.Title)
		homeURL := strings.TrimSpace(e.outline.HTMLURL)
		res, err := feedparse.Fetch(r.Context(), feedURL, client, "", "")
		if err != nil {
			log.Error("opml import feed", "url", feedURL, "err", err)
			failed++
			continue
		}
		if title == "" {
			title = res.Feed.Title
		}
		if title == "" {
			title = feedURL
		}
		if homeURL == "" {
			homeURL = res.Feed.HomeURL
		}
		// Every feed belongs to an author. One author per imported feed, named
		// after the outline (or the feed); feeds sharing an outline name share
		// an author.
		authorName := strings.TrimSpace(e.outline.Title)
		if authorName == "" {
			authorName = strings.TrimSpace(e.outline.Text)
		}
		if authorName == "" {
			authorName = title
		}
		authorID, ok := authorsByName[authorName]
		if !ok {
			a, err := s.store.Authors.Create(u.ID, authorName, "", "")
			if err != nil {
				log.Error("opml import author", "name", authorName, "err", err)
				failed++
				continue
			}
			authorID = a.ID
			authorsByName[authorName] = authorID
		}
		f, err := s.store.Feeds.CreateWithPlugin(u.ID, authorID, title, feedURL, homeURL, "", s.pluginNameFor(feedURL), 900)
		if err != nil {
			log.Error("opml import create", "url", feedURL, "err", err)
			failed++
			continue
		}
		existing[normalizeFeedKey(feedURL)] = true
		imported++
		if err := s.store.Collections.AssignAuto(u.ID, f.ID, homeURL, feedURL); err != nil {
			log.Error("opml assign auto collection", "feed_id", f.ID, "err", err)
		}
		if e.collection != "" {
			if cid := findCollection(collections, e.collection); cid != 0 {
				s.store.Collections.AddFeed(u.ID, cid, f.ID)
			} else if c, err := s.store.Collections.Create(u.ID, e.collection); err == nil {
				collections = append(collections, c)
				s.store.Collections.AddFeed(u.ID, c.ID, f.ID)
			}
		}
	}

	web.Render(w, r, settingsOpml(settingsOpmlData{
		Message: "imported " + strconv.Itoa(imported) + " · skipped " + strconv.Itoa(skipped) + " · failed " + strconv.Itoa(failed),
	}))
}

// mustFeeds lists the user's feeds, tolerating errors (empty on failure).
func (s *Server) mustFeeds(userID int64) []store.Feed {
	feeds, err := s.store.Feeds.List(userID)
	if err != nil {
		log.Error("list feeds", "err", err)
		return nil
	}
	return feeds
}

// normalizeFeedKey makes feed urls comparable across import/export.
func normalizeFeedKey(url string) string {
	return strings.ToLower(strings.TrimSpace(url))
}

// findCollection returns the id of a collection matching name (case
// insensitive), or 0.
func findCollection(cols []store.Collection, name string) int64 {
	for _, c := range cols {
		if strings.EqualFold(c.Name, name) {
			return c.ID
		}
	}
	return 0
}

// collectionIsAuto reports whether the given collection id is an auto
// collection in the provided list.
func collectionIsAuto(cols []store.Collection, id int64) bool {
	for _, c := range cols {
		if c.ID == id {
			return c.IsAuto
		}
	}
	return false
}
