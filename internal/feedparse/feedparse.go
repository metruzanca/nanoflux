// Package feedparse fetches and normalizes RSS/Atom/JSON feeds into the
// app's item model.
package feedparse

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/mmcdole/gofeed"
)

// ErrNotModified is returned when the server answers 304 for a conditional GET.
var ErrNotModified = errors.New("not modified")

// Feed carries normalized feed-level metadata.
type Feed struct {
	Title       string
	HomeURL     string
	Description string
	ImageURL    string
}

// Item is one normalized entry.
type Item struct {
	GUID        string
	Title       string
	Link        string
	Summary     string
	ImageURL    string
	PublishedAt string // "" when unknown
}

// Result is the normalized output of a successful fetch.
type Result struct {
	Feed         Feed
	Items        []Item
	ETag         string
	LastModified string
}

// Fetch retrieves and parses feedURL. When etag or lastModified are non-empty
// they are sent as conditional-GET headers; a 304 returns ErrNotModified.
func Fetch(ctx context.Context, feedURL string, client *http.Client, etag, lastModified string) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("User-Agent", "nanoflux/0.1")
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/feed+json, application/xml, text/xml, */*")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}

	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("get %s: %w", feedURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return Result{}, ErrNotModified
	}
	if resp.StatusCode >= 400 {
		return Result{}, fmt.Errorf("get %s: status %d", feedURL, resp.StatusCode)
	}

	parsed, err := gofeed.NewParser().Parse(resp.Body)
	if err != nil {
		return Result{}, fmt.Errorf("parse %s: %w", feedURL, err)
	}
	if parsed == nil {
		return Result{}, errors.New("empty feed")
	}

	res := Result{
		Feed: Feed{
			Title:       parsed.Title,
			HomeURL:     parsed.Link,
			Description: parsed.Description,
			ImageURL:    imageURL(parsed.Image),
		},
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
	}
	for _, it := range parsed.Items {
		res.Items = append(res.Items, normalizeItem(it))
	}
	return res, nil
}

func normalizeItem(it *gofeed.Item) Item {
	out := Item{
		Title: it.Title,
		Link:  it.Link,
	}
	out.GUID = it.GUID
	if out.GUID == "" {
		out.GUID = it.Link
	}
	if out.GUID == "" {
		// Last resort: a stable hash so dedup still works.
		sum := sha1.Sum([]byte(it.Title + it.Content))
		out.GUID = hex.EncodeToString(sum[:])
	}
	if d := strings.TrimSpace(it.Description); d != "" {
		out.Summary = d
	} else {
		out.Summary = it.Content
	}
	if it.Image != nil {
		out.ImageURL = it.Image.URL
	}
	switch {
	case it.PublishedParsed != nil:
		out.PublishedAt = db.FormatTime(*it.PublishedParsed)
	case it.UpdatedParsed != nil:
		out.PublishedAt = db.FormatTime(*it.UpdatedParsed)
	}
	return out
}

func imageURL(img *gofeed.Image) string {
	if img == nil {
		return ""
	}
	return img.URL
}
