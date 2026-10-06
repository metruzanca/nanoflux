// Package imagecache stores a feed's images in nanoflux's own object storage so
// they survive the short-lived, signed URLs some sites serve. It is opt-in per
// feed: a plugin whose site signs image URLs claims CapImageCache (which forces
// caching on and is not user-disableable), and a user may turn it on for any
// individual feed. Cached bytes live under cache/<folder>/<itemID>/<slot>, so a
// plugin's (or the generic "feeds" folder's) cached images can be removed later
// by dropping that prefix.
package imagecache

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/feedparse"
	"github.com/metruzanca/nanoflux/internal/filestore"
	"github.com/metruzanca/nanoflux/internal/imageutil"
	"github.com/metruzanca/nanoflux/internal/safedial"
)

// defaultMaxBytes caps a single cached image. Anything larger is left remote.
const defaultMaxBytes = 10 << 20

// defaultConcurrency bounds simultaneous downloads within one item.
const defaultConcurrency = 4

// Cacher downloads and stores feed images. It is safe for concurrent use. A nil
// *Cacher is a no-op.
type Cacher struct {
	files   filestore.Store
	client  *http.Client
	forced  func(feedURL string) bool
	folder  func(feedURL string) string
	max     int64
	workers int
}

// New builds a Cacher. forced reports whether a feed's plugin forces caching (so
// the user cannot disable it); folder returns the storage folder for a feed's
// plugin ("feeds" for a feed with no plugin). Either callback may be nil.
func New(files filestore.Store, client *http.Client, forced func(feedURL string) bool, folder func(feedURL string) string) *Cacher {
	if client == nil {
		client = safedial.Client(20 * time.Second)
	}
	return &Cacher{files: files, client: client, forced: forced, folder: folder, max: defaultMaxBytes, workers: defaultConcurrency}
}

// Enclosure is one media attachment to consider for caching. Kind is the
// plugin-declared render kind (empty infers from the MIME type/extension).
type Enclosure struct {
	URL      string
	MIMEType string
	Kind     string
}

// Request describes one item's cacheable media. ItemID must be the stored item's
// id; it makes the cache key stable across re-polls (the signed remote URL
// changes every poll, the id does not).
type Request struct {
	Folder     string
	ItemID     int64
	ImageURL   string
	Enclosures []Enclosure
}

// Result carries the object-storage keys of the cached media. Empty means
// "nothing cached" (the caller keeps the remote URL). EnclosureKeys is parallel
// to Request.Enclosures. A size of 0 with a non-empty key means the blob was
// reused from a previous download and its size is unchanged; the caller should
// keep any size it already recorded.
type Result struct {
	ImageKey       string
	ImageBytes     int64
	EnclosureKeys  []string
	EnclosureBytes []int64
}

// Forced reports whether a feed's plugin forces image caching on.
func (c *Cacher) Forced(feedURL string) bool {
	return c != nil && c.forced != nil && c.forced(feedURL)
}

// Folder returns the storage folder for a feed's plugin, or "feeds".
func (c *Cacher) Folder(feedURL string) string {
	if c == nil || c.folder == nil {
		return "feeds"
	}
	if f := c.folder(feedURL); f != "" {
		return f
	}
	return "feeds"
}

// Cache downloads an item's primary image and image-typed enclosures that are
// not already stored, and returns their keys. It is best-effort: a failure logs
// and leaves that URL remote.
func (c *Cacher) Cache(ctx context.Context, req Request) Result {
	res := Result{
		EnclosureKeys:  make([]string, len(req.Enclosures)),
		EnclosureBytes: make([]int64, len(req.Enclosures)),
	}
	if c == nil || c.files == nil || req.ItemID == 0 {
		return res
	}
	folder := req.Folder
	if folder == "" {
		folder = "feeds"
	}

	// The primary image is fetched first so an enclosure that is the same URL
	// (the generic parser mirrors an image enclosure into ImageURL) reuses its
	// key instead of downloading the bytes twice.
	if strings.TrimSpace(req.ImageURL) != "" {
		res.ImageKey, res.ImageBytes = c.cacheOne(ctx, folder, req.ItemID, 0, req.ImageURL)
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, c.workers)
	for i, e := range req.Enclosures {
		if !isImage(e.Kind, e.URL, e.MIMEType) {
			continue
		}
		if e.URL == req.ImageURL && res.ImageKey != "" {
			res.EnclosureKeys[i] = res.ImageKey
			res.EnclosureBytes[i] = res.ImageBytes
			continue
		}
		wg.Add(1)
		go func(i int, e Enclosure) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if key, size := c.cacheOne(ctx, folder, req.ItemID, i+1, e.URL); key != "" {
				res.EnclosureKeys[i] = key
				res.EnclosureBytes[i] = size
			}
		}(i, e)
	}
	wg.Wait()
	return res
}

// cacheOne returns the storage key for rawurl at (folder, itemID, slot) and the
// cached byte size (0 when an already-stored blob was reused), or "" when it is
// not an image or the download failed. An already-stored blob is reused without
// a network request.
func (c *Cacher) cacheOne(ctx context.Context, folder string, itemID int64, slot int, rawurl string) (string, int64) {
	prefix := "cache/" + folder + "/" + strconv.FormatInt(itemID, 10) + "/" + strconv.Itoa(slot)

	// Reuse a previous download for this slot (the URL's signed params rotate
	// every poll but the bytes are the same image).
	if key := c.existing(ctx, prefix); key != "" {
		return key, 0
	}

	u, err := url.Parse(rawurl)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", 0
	}
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(dctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", 0
	}
	httpReq.Header.Set("User-Agent", "nanoflux/0.1")
	resp, err := c.client.Do(httpReq)
	if err != nil {
		log.Debug("image cache: fetch", "url", rawurl, "err", err)
		return "", 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0
	}
	prefix512, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	ct, ok := imageutil.Sniff(resp.Header.Get("Content-Type"), prefix512)
	if !ok {
		return "", 0
	}
	data, err := io.ReadAll(io.LimitReader(io.MultiReader(bytes.NewReader(prefix512), resp.Body), c.max+1))
	if err != nil {
		log.Debug("image cache: read", "url", rawurl, "err", err)
		return "", 0
	}
	if int64(len(data)) > c.max {
		return "", 0
	}
	key := prefix + "." + extFor(ct)
	if err := c.files.Put(dctx, key, ct, data); err != nil {
		log.Error("image cache: store", "key", key, "err", err)
		return "", 0
	}
	return key, int64(len(data))
}

// existing finds a previously stored blob for prefix regardless of its extension
// and returns its key. It probes the extensions this package writes.
func (c *Cacher) existing(ctx context.Context, prefix string) string {
	for _, ext := range cachedExts {
		key := prefix + "." + ext
		ok, err := c.files.Exists(ctx, key)
		if err == nil && ok {
			return key
		}
	}
	return ""
}

var cachedExts = []string{"jpg", "png", "gif", "webp", "avif", "bmp", "svg", "img"}

// extFor maps an image content type to a file extension, defaulting to "img".
func extFor(ct string) string {
	switch strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0])) {
	case "image/jpeg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	case "image/avif":
		return "avif"
	case "image/bmp":
		return "bmp"
	case "image/svg+xml":
		return "svg"
	}
	return "img"
}

// isImage reports whether an enclosure should be cached as an image. It uses
// the host's single enclosure-kind resolver, so a plugin-declared kind wins over
// the MIME type/extension.
func isImage(kind, rawurl, mime string) bool {
	return feedparse.ResolveEnclosureKind(kind, rawurl, mime) == feedparse.EnclosureKindImage
}
