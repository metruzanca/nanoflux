package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
)

// authorAvatarCardData drives the avatar card on the author edit page.
type authorAvatarCardData struct {
	ID            int64
	AvatarURL     string
	AvatarKey     string
	LastFetchedAt string
	Timezone      string
	Flash         string // error rendered inside the swapped card, "" on success
}

func (s *Server) authorAvatarCardData(a store.Author, tz, flash string) authorAvatarCardData {
	return authorAvatarCardData{
		ID: a.ID, AvatarURL: a.AvatarURL, AvatarKey: a.AvatarKey,
		LastFetchedAt: a.LastFetchedAt, Timezone: tz, Flash: flash,
	}
}

// authorAvatarKey returns the deterministic object-storage key for an author's
// cached avatar. It never varies between fetches, so refetching overwrites the
// same object in place — a stale file is never left behind.
func authorAvatarKey(userID, authorID int64) string {
	return "author-avatars/" + strconv.FormatInt(userID, 10) + "/" + strconv.FormatInt(authorID, 10)
}

// fetchAndCacheAuthorAvatar downloads an author's avatar_url and stores the
// bytes under the author's deterministic object key, recording the key. The
// row's avatar_url stays authoritative; errors are returned so callers can
// surface them.
func (s *Server) fetchAndCacheAuthorAvatar(ctx context.Context, a store.Author) error {
	if a.AvatarURL == "" {
		return errors.New("no avatar url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.AvatarURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "nanoflux/0.1")

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		return errors.New("not an image")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAvatarBytes))
	if err != nil {
		return err
	}
	key := authorAvatarKey(a.UserID, a.ID)
	if err := s.files.Put(ctx, key, ct, data); err != nil {
		return err
	}
	return s.store.Authors.SetAvatarKey(a.UserID, a.ID, key, db.Now())
}

// autoCacheAuthorAvatar fetches and caches an author's avatar best-effort,
// bounded so a slow site can't stall the response. Called after create/update.
func (s *Server) autoCacheAuthorAvatar(ctx context.Context, a store.Author) {
	if a.AvatarURL == "" {
		return
	}
	if err := s.fetchAndCacheAuthorAvatar(ctx, a); err != nil {
		log.Error("auto cache author avatar", "author_id", a.ID, "err", err)
	}
}

// clearAuthorAvatar removes an author's cached avatar object and its key.
// Failures are logged, never fatal: the DB key is the source of truth for what
// to purge, and a flaky store must not block the author mutation.
func (s *Server) clearAuthorAvatar(ctx context.Context, a store.Author) {
	if a.AvatarKey != "" {
		if err := s.files.Delete(ctx, a.AvatarKey); err != nil {
			log.Error("delete author avatar object", "key", a.AvatarKey, "err", err)
		}
	}
	if err := s.store.Authors.ClearAvatarKey(a.UserID, a.ID); err != nil {
		log.Error("clear author avatar key", "author_id", a.ID, "err", err)
	}
}

// authorAvatar serves the current user's cached avatar bytes for an author,
// or 404 when none is cached yet (the UI falls back to the /img proxy).
func (s *Server) authorAvatar(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a, err := s.store.Authors.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if a.AvatarKey == "" {
		http.NotFound(w, r)
		return
	}
	ct, data, err := s.files.Get(r.Context(), a.AvatarKey)
	if err != nil {
		log.Error("load author avatar", "key", a.AvatarKey, "err", err)
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(data)
}

// authorAvatarRefresh re-fetches an author's avatar into object storage,
// overwriting the existing object (deterministic key, so no orphaned files),
// and re-renders the avatar card. Errors render inside the swapped card with a
// visible banner.
func (s *Server) authorAvatarRefresh(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	id, err := parseID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a, err := s.store.Authors.ByID(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	render := func(flash string) {
		if flash != "" {
			w.WriteHeader(http.StatusBadRequest)
		}
		web.Render(w, r, authorAvatarFields(s.authorAvatarCardData(a, u.Timezone, flash)))
	}
	if a.AvatarURL == "" {
		render("set an avatar url first")
		return
	}
	if err := s.fetchAndCacheAuthorAvatar(r.Context(), a); err != nil {
		log.Error("refresh author avatar", "author_id", id, "err", err)
		render("could not fetch avatar")
		return
	}
	a, _ = s.store.Authors.ByID(u.ID, id)
	render("")
}
