package oembed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDiscover(t *testing.T) {
	var oembedHits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<html><head><link rel="alternate" type="application/json+oembed" href="/oembed?url=page"><link rel="alternate" type="application/rss+xml" href="/rss"></head><body>hi</body></html>`))
		case "/oembed":
			oembedHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"type":"video","version":"1.0","title":"Clip","provider_name":"Mock","thumbnail_url":"https://m/thumb.jpg","width":1080,"height":1920,"html":"<div style='padding-bottom:56%'><iframe src='https://m/embed/abc' width='1080' height='1920' allowfullscreen></iframe></div>"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	r := New(srv.Client(), time.Minute, time.Minute, 10)
	for i := 0; i < 3; i++ {
		emb, err := r.Resolve(context.Background(), srv.URL+"/page")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if emb.Src != "https://m/embed/abc" {
			t.Fatalf("src = %q, want https://m/embed/abc", emb.Src)
		}
		if emb.Provider != "Mock" || emb.Width != 1080 || emb.Height != 1920 {
			t.Fatalf("embed metadata = %+v", emb)
		}
	}
	if n := oembedHits.Load(); n != 1 {
		t.Fatalf("oembed fetched %d times, want 1 (cached)", n)
	}
}

func TestDiscoverNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head><link rel="alternate" type="application/rss+xml" href="/rss"></head></html>`))
	}))
	defer srv.Close()

	r := New(srv.Client(), time.Minute, 5*time.Second, 10)
	if _, err := r.Resolve(context.Background(), srv.URL+"/page"); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestDiscoverNoIframeInOEmbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Write([]byte(`<link rel="alternate" type="application/json+oembed" href="/oembed">`))
		case "/oembed":
			w.Write([]byte(`{"html":"<div>no iframe</div>"}`))
		}
	}))
	defer srv.Close()

	r := New(srv.Client(), time.Minute, 5*time.Second, 10)
	if _, err := r.Resolve(context.Background(), srv.URL+"/page"); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestIframeFromHTMLRejectsNonHTTP(t *testing.T) {
	if src, _, _ := iframeFromHTML(`<iframe src="javascript:alert(1)"></iframe>`); src != "" {
		t.Fatalf("javascript src accepted: %q", src)
	}
	if src, w, h := iframeFromHTML(`<iframe src="https://x.com/embed/1" width="640" height="360"></iframe>`); src != "https://x.com/embed/1" || w != 640 || h != 360 {
		t.Fatalf("iframe = %q %dx%d", src, w, h)
	}
}

func TestDiscoverRejectsBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	r := New(srv.Client(), time.Minute, 5*time.Second, 10)
	if _, err := r.Resolve(context.Background(), srv.URL+"/page"); err == nil || err == ErrNotFound {
		t.Fatalf("err = %v, want a fetch error", err)
	}
}

func TestResolveUsesProvidedClient(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.UserAgent()
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<link rel="alternate" type="application/json+oembed" href="/oembed">`))
		case "/oembed":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"html":""}`))
		}
	}))
	defer srv.Close()

	r := New(srv.Client(), time.Minute, 5*time.Second, 10)
	_, err := r.Resolve(context.Background(), srv.URL+"/page")
	if err != ErrNotFound {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(gotUA, "Mozilla") {
		t.Fatalf("user agent = %q, want a browser UA", gotUA)
	}
}