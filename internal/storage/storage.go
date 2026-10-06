// Package storage audits the blob store: where bytes live, how they break down
// by kind, and which cached item media is no longer referenced by any row
// (orphaned). It backs `nanoflux storage` and can be reused by the admin UI.
package storage

import (
	"context"
	"sort"
	"strings"

	"github.com/metruzanca/nanoflux/internal/filestore"
	"github.com/metruzanca/nanoflux/internal/store"
)

// cachePrefix is where host-side cached item media lives.
const cachePrefix = "cache/"

// Group is one bucket of storage: a top-level kind ("avatars", "icons", ...) or
// a cache plugin folder ("cache/tumblr").
type Group struct {
	Label         string
	Bytes         int64
	Objects       int
	OrphanBytes   int64
	OrphanObjects int
}

// Report summarizes the object store.
type Report struct {
	TotalBytes    int64
	TotalObjects  int
	OrphanBytes   int64
	OrphanObjects int
	// Groups is sorted by Bytes descending.
	Groups []Group
}

// Audit lists the store and breaks it down, flagging cached blobs whose key is
// referenced by no item or enclosure row (orphans).
func Audit(ctx context.Context, files filestore.Store, st *store.Store) (Report, error) {
	objs, err := files.List(ctx, "")
	if err != nil {
		return Report{}, err
	}
	refs, err := st.Items.AllCacheKeys()
	if err != nil {
		return Report{}, err
	}
	referenced := make(map[string]struct{}, len(refs))
	for _, k := range refs {
		referenced[k] = struct{}{}
	}

	byLabel := map[string]*Group{}
	var report Report
	for _, o := range objs {
		label := classify(o.Key)
		g := byLabel[label]
		if g == nil {
			g = &Group{Label: label}
			byLabel[label] = g
		}
		g.Bytes += o.Size
		g.Objects++
		report.TotalBytes += o.Size
		report.TotalObjects++
		if strings.HasPrefix(o.Key, cachePrefix) {
			if _, ok := referenced[o.Key]; !ok {
				g.OrphanBytes += o.Size
				g.OrphanObjects++
				report.OrphanBytes += o.Size
				report.OrphanObjects++
			}
		}
	}

	report.Groups = make([]Group, 0, len(byLabel))
	for _, g := range byLabel {
		report.Groups = append(report.Groups, *g)
	}
	sort.Slice(report.Groups, func(i, j int) bool {
		return report.Groups[i].Bytes > report.Groups[j].Bytes
	})
	return report, nil
}

// Orphans returns the cached objects that no item or enclosure row references.
func Orphans(ctx context.Context, files filestore.Store, st *store.Store) ([]filestore.Object, error) {
	objs, err := files.List(ctx, cachePrefix)
	if err != nil {
		return nil, err
	}
	refs, err := st.Items.AllCacheKeys()
	if err != nil {
		return nil, err
	}
	referenced := make(map[string]struct{}, len(refs))
	for _, k := range refs {
		referenced[k] = struct{}{}
	}
	var out []filestore.Object
	for _, o := range objs {
		if _, ok := referenced[o.Key]; !ok {
			out = append(out, o)
		}
	}
	return out, nil
}

// classify names the bucket a key belongs to: cache/<plugin> for cached media,
// else the key's first path segment (avatars, author-avatars, icons, ...).
func classify(key string) string {
	if strings.HasPrefix(key, cachePrefix) {
		rest := strings.TrimPrefix(key, cachePrefix)
		folder := rest
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			folder = rest[:i]
		}
		if folder == "" {
			return "cache"
		}
		return cachePrefix + folder
	}
	if i := strings.IndexByte(key, '/'); i >= 0 {
		return key[:i]
	}
	return key
}
