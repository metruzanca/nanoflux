package imagecache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/metruzanca/nanoflux/internal/filestore"
)

func TestCacheStoresAndReuses(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("\x89PNG\r\n\x1a\nfake"))
	}))
	defer srv.Close()

	files := filestore.NewMemory()
	c := New(files, srv.Client(), nil, nil)
	req := Request{
		Folder:   "pics",
		ItemID:   7,
		ImageURL: srv.URL + "/photo.png?sig=abc",
		Enclosures: []Enclosure{
			{URL: srv.URL + "/one.png?sig=1", MIMEType: "image/png"},
			{URL: srv.URL + "/clip.mp4", MIMEType: "video/mp4"},
		},
	}

	res := c.Cache(context.Background(), req)
	if res.ImageKey != "cache/pics/7/0.png" {
		t.Fatalf("ImageKey = %q, want cache/pics/7/0.png", res.ImageKey)
	}
	if res.EnclosureKeys[0] != "cache/pics/7/1.png" {
		t.Fatalf("EnclosureKeys[0] = %q", res.EnclosureKeys[0])
	}
	if res.EnclosureKeys[1] != "" {
		t.Fatalf("a video enclosure must not be cached, got %q", res.EnclosureKeys[1])
	}
	if _, data, err := files.Get(context.Background(), res.ImageKey); err != nil || len(data) == 0 {
		t.Fatalf("cached image missing: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("downloads = %d, want 2 (image + one image enclosure)", got)
	}

	// A second poll with a freshly signed URL reuses the stored bytes: same key,
	// no new request.
	hits.Store(0)
	res2 := c.Cache(context.Background(), Request{
		Folder:   "pics",
		ItemID:   7,
		ImageURL: srv.URL + "/photo.png?sig=rotated",
	})
	if res2.ImageKey != res.ImageKey {
		t.Fatalf("reuse key = %q, want %q", res2.ImageKey, res.ImageKey)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("reuse made %d requests, want 0", got)
	}
}

func TestCacheSkipsNonImageAndFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/notimage" {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html>"))
			return
		}
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()

	files := filestore.NewMemory()
	c := New(files, srv.Client(), nil, nil)
	res := c.Cache(context.Background(), Request{
		Folder:     "feeds",
		ItemID:     3,
		ImageURL:   srv.URL + "/notimage",
		Enclosures: []Enclosure{{URL: srv.URL + "/missing.png", MIMEType: "image/png"}},
	})
	if res.ImageKey != "" {
		t.Fatalf("non-image should not be cached, got %q", res.ImageKey)
	}
	if res.EnclosureKeys[0] != "" {
		t.Fatalf("failed download should not be cached, got %q", res.EnclosureKeys[0])
	}
}

func TestForcedAndFolder(t *testing.T) {
	c := New(filestore.NewMemory(), nil,
		func(u string) bool { return u == "https://pics.example/blog/x" },
		func(u string) string {
			if u == "https://pics.example/blog/x" {
				return "pics"
			}
			return ""
		},
	)
	if !c.Forced("https://pics.example/blog/x") {
		t.Fatal("forced feed not reported")
	}
	if c.Forced("https://example.com/feed") {
		t.Fatal("non-forced feed reported as forced")
	}
	if got := c.Folder("https://pics.example/blog/x"); got != "pics" {
		t.Fatalf("Folder = %q", got)
	}
	if got := c.Folder("https://example.com/feed"); got != "feeds" {
		t.Fatalf("Folder = %q, want feeds", got)
	}
}
