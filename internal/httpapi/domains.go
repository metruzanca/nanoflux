package httpapi

import (
	"net/http"
	"sort"
	"strings"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
)

// domainGroup is one registrable domain with its feed count and unread total.
type domainGroup struct {
	Name      string
	FeedCount int
	Unread    int
}

// domains groups the feed library by registrable domain. It is the replacement
// for the old auto collections: nothing is stored, the grouping is derived from
// each feed's home or feed url.
func (s *Server) domains(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	feeds, err := s.store.Feeds.ListWithUnread(u.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	domain := strings.TrimSpace(r.URL.Query().Get("domain"))
	if domain != "" {
		var matched []store.FeedWithUnread
		for _, f := range feeds {
			if domainOf(f.Feed) == domain {
				matched = append(matched, f)
			}
		}
		sort.SliceStable(matched, func(i, j int) bool { return matched[i].Title < matched[j].Title })
		web.Render(w, r, basePage("domain "+domain, u, domainsPage(u, nil, domain, matched)))
		return
	}
	byDomain := make(map[string]*domainGroup)
	for _, f := range feeds {
		name := domainOf(f.Feed)
		g := byDomain[name]
		if g == nil {
			g = &domainGroup{Name: name}
			byDomain[name] = g
		}
		g.FeedCount++
		g.Unread += f.Unread
	}
	groups := make([]domainGroup, 0, len(byDomain))
	for _, g := range byDomain {
		groups = append(groups, *g)
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
	web.Render(w, r, basePage("domains", u, domainsPage(u, groups, "", nil)))
}
