package feedparse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestIsInstagramProfileURL(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://www.instagram.com/candlesan", true},
		{"https://www.instagram.com/candlesan/", true},
		{"https://instagram.com/candlesan", true},
		{"https://m.instagram.com/candlesan", true},
		{"https://www.instagram.com/candlesan/?hl=en", true},
		{"https://www.instagram.com/@candlesan", true},
		{"https://www.instagram.com/p/Ddj3Q8EgSku/", false},
		{"https://www.instagram.com/reel/Ddj3Q8EgSku/", false},
		{"https://www.instagram.com/stories/candlesan/", false},
		{"https://www.instagram.com/explore/tags/gamedesign/", false},
		{"https://www.instagram.com/accounts/login/", false},
		{"https://www.instagram.com/", false},
		{"https://example.com/candlesan", false},
		{"https://www.instagram.com/candlesan/extra", false},
	}
	for _, c := range cases {
		u, err := url.Parse(c.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", c.raw, err)
		}
		if got := isInstagramProfileURL(u); got != c.want {
			t.Errorf("isInstagramProfileURL(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

func TestInstagramShortcode(t *testing.T) {
	cases := []struct{ pk, want string }{
		{"3991276751350212910", "Ddj3Q8EgSku"},
		{"3986203195176837489", "DdR1q-rCuFx"},
		{"3976055985379538616", "DctydpHnPa4"},
		{"", ""},
		{"notanumber", ""},
		{"0", ""},
	}
	for _, c := range cases {
		if got := instagramShortcode(c.pk); got != c.want {
			t.Errorf("instagramShortcode(%q) = %q, want %q", c.pk, got, c.want)
		}
	}
}

func TestInstagramMediaTime(t *testing.T) {
	// pk 3991276751350212910 -> 2026-09-21 (time is approximate).
	got := instagramMediaTime("3991276751350212910")
	want := time.Date(2026, 9, 21, 19, 1, 27, 650e6, time.UTC)
	if !got.Equal(want) {
		t.Errorf("instagramMediaTime = %v, want %v", got, want)
	}
	if !instagramMediaTime("notanumber").IsZero() {
		t.Error("invalid id should give zero time")
	}
}

func instagramFixture() string {
	// Two posts: a reel with a caption containing braces, and an image whose
	// caption is null. The same reel also appears in a second preloader to
	// exercise dedup.
	reel := `{"__typename":"XIGPolarisVideoMedia","is_timeline_pinned":false,"pk":"3991276751350212910","image_versions2":{"candidates":[{"height":1080,"url":"https:\/\/scontent.example\/reel.jpg"}]},"caption":{"pk":"1","text":"mit = Armor/{Armor+K}\nsecond line"},"media_type":2,"product_type":"clips","id":"POLARIS_3991276751350212910"}`
	image := `{"__typename":"XIGPolarisImageMedia","is_timeline_pinned":false,"pk":"3986203195176837489","image_versions2":{"candidates":[{"height":900,"url":"https:\/\/scontent.example\/image.jpg"}]},"caption":null,"media_type":1,"product_type":"feed","id":"POLARIS_3986203195176837489"}`
	var b strings.Builder
	b.WriteString(`<html><head><title>Wyatt Cheng (&#064;candlesan) &#x2022; Instagram photos and videos</title></head><body>`)
	b.WriteString(`{"__bbox":{"result":{"data":{"xig_user_by_igid_v2":{"full_name":"Wyatt Cheng","username":"candlesan","polaris_timeline_connection":{"edges":[{"node":`)
	b.WriteString(reel)
	b.WriteString(`},{"node":`)
	b.WriteString(image)
	b.WriteString(`}],"page_info":{"has_next_page":true}}}}}}}`)
	// Duplicate preloader for the reel.
	b.WriteString(`{"__bbox":{"result":{"data":{"polaris_timeline_connection":{"edges":[{"node":`)
	b.WriteString(reel)
	b.WriteString(`}]}}}}`)
	b.WriteString(`</body></html>`)
	return b.String()
}

func TestFetchInstagramProfile(t *testing.T) {
	old := instagramProfileHosts
	instagramProfileHosts = append([]string(nil), old...)
	instagramProfileHosts = append(instagramProfileHosts, "127.0.0.1")
	defer func() { instagramProfileHosts = old }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); !strings.Contains(ua, "Googlebot") {
			t.Errorf("request user-agent = %q, want a crawler identity", ua)
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(instagramFixture()))
	}))
	defer srv.Close()

	res, err := Fetch(context.Background(), srv.URL+"/candlesan", srv.Client(), "", "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Feed.Title != "Wyatt Cheng" {
		t.Errorf("title = %q, want Wyatt Cheng", res.Feed.Title)
	}
	if res.Feed.HomeURL != srv.URL+"/candlesan" {
		t.Errorf("home = %q", res.Feed.HomeURL)
	}
	if len(res.Items) != 2 {
		t.Fatalf("items = %d, want 2 (dedup keeps one reel)", len(res.Items))
	}

	reel := res.Items[0]
	if reel.GUID != "instagram:3991276751350212910" {
		t.Errorf("guid = %q", reel.GUID)
	}
	if reel.Link != "https://www.instagram.com/reel/Ddj3Q8EgSku/" {
		t.Errorf("link = %q", reel.Link)
	}
	if reel.Title != "mit = Armor/{Armor+K}" {
		t.Errorf("title = %q", reel.Title)
	}
	if !strings.Contains(reel.Summary, "second line") {
		t.Errorf("summary = %q", reel.Summary)
	}
	if reel.ImageURL != "https://www.instagram.com/p/Ddj3Q8EgSku/media/?size=l" {
		t.Errorf("image = %q", reel.ImageURL)
	}
	if reel.PublishedAt != "2026-09-21 19:01:27" {
		t.Errorf("published = %q", reel.PublishedAt)
	}

	image := res.Items[1]
	if image.Link != "https://www.instagram.com/p/DdR1q-rCuFx/" {
		t.Errorf("image link = %q", image.Link)
	}
	if image.Title != "" || image.Summary != "" {
		t.Errorf("null caption should stay empty, got title=%q summary=%q", image.Title, image.Summary)
	}
}

func TestInstagramDisplayNameFallback(t *testing.T) {
	body := []byte(`<html><head><title>Wyatt Cheng (&#064;candlesan) &#x2022; Instagram photos and videos</title></head><body></body></html>`)
	if got := instagramDisplayName(body, "candlesan"); got != "Wyatt Cheng" {
		t.Errorf("title fallback = %q, want Wyatt Cheng", got)
	}
	if got := instagramDisplayName([]byte(`<html></html>`), "candlesan"); got != "candlesan" {
		t.Errorf("handle fallback = %q", got)
	}
}

func TestFetchInstagramProfileNoPosts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><head><title>someone</title></head><body>nothing</body></html>`))
	}))
	defer srv.Close()

	if _, err := fetchInstagramProfile(context.Background(), srv.URL+"/someone", srv.Client()); err == nil {
		t.Fatal("expected error when no posts are embedded")
	}
}
