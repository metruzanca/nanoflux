package httpapi

import (
	"net/http"
	"strconv"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
)

// itemProgress is a resumable player's server-rendered state. Player is a
// core-owned adapter (web.PlayerNative or web.PlayerYouTube), Key is the media
// identity the position is stored under, and Origin is the embedding origin a
// YouTube player needs for its postMessage API.
type itemProgress struct {
	ItemID   int64
	Key      string
	Player   string
	Position int
	Duration int
	Origin   string
}

// resumeSrc is the item's embed src adjusted to resume at the saved position.
func (d itemViewData) resumeSrc() string {
	if d.Progress == nil {
		return d.EmbedSrc
	}
	return web.ResumeEmbedSrc(d.Progress.Player, d.EmbedSrc, d.Progress.Position, d.Progress.Origin)
}

// playbackTarget resolves an item's media identity and core player adapter. A
// plugin's CapPlayback answer wins; otherwise a playable enclosure gets the
// host's item-scoped native adapter. Empty values mean the item is not
// resumable.
func (s *Server) playbackTarget(itemID int64, link string, encs []store.Enclosure) (key, player string) {
	if s.plugins != nil {
		if k, p := s.plugins.PlaybackFor(link); k != "" && web.KnownPlayer(p) {
			return k, p
		}
	}
	for _, e := range encs {
		switch web.EnclosureKind(e) {
		case "video", "audio", "hls":
			return "item:" + strconv.FormatInt(itemID, 10), web.PlayerNative
		}
	}
	return "", ""
}

// loadProgress attaches a user's saved position to the item view when the item
// has a resumable player.
func (s *Server) loadProgress(userID int64, it *store.ItemWithFeed, data *itemViewData, origin string) {
	key, player := s.playbackTarget(it.ID, it.Link, data.Enclosures)
	if key == "" || player == "" {
		return
	}
	p := &itemProgress{
		ItemID: it.ID, Key: key, Player: player,
		Duration: it.DurationSec, Origin: origin,
	}
	if saved, err := s.store.Progress.Get(userID, key); err == nil {
		p.Position = saved.PositionSec
		if saved.DurationSec > 0 {
			p.Duration = saved.DurationSec
		}
	}
	data.Progress = p
}

// itemProgress records a player's position. It is called by the frontend
// roughly every few seconds while media plays, so it stays small: validate the
// item, resolve its media key, drop a finished or restarted video instead of
// storing it, and trim the user's oldest rows.
func (s *Server) itemProgress(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	it, err := s.store.Items.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	position, _ := strconv.Atoi(r.FormValue("position"))
	duration, _ := strconv.Atoi(r.FormValue("duration"))
	if position < 0 || duration < 0 {
		http.Error(w, "invalid position", http.StatusBadRequest)
		return
	}
	encs, _ := s.store.Items.Enclosures(it.ID)
	key, player := s.playbackTarget(it.ID, it.Link, encs)
	if key == "" || player == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Finished media starts over next time, and seeking back to the start is an
	// explicit reset. Both delete the row rather than storing a useless one.
	finished := duration > 0 && (position >= duration-20 || position*100 >= duration*95)
	if position < 10 || finished {
		if err := s.store.Progress.Delete(u.ID, key); err != nil {
			log.Error("clear item progress", "item_id", it.ID, "err", err)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := s.store.Progress.Set(u.ID, it.ID, key, position, duration); err != nil {
		log.Error("save item progress", "item_id", it.ID, "err", err)
		http.Error(w, "could not save progress", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
