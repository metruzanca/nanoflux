package store

import "testing"

func TestRegistrableDomain(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://www.youtube.com/feeds/videos.xml", "youtube.com"},
		{"https://m.youtube.com/feeds", "youtube.com"},
		{"https://music.youtube.com/channel/UCx", "youtube.com"},
		{"https://youtube.com", "youtube.com"},
		{"http://example.com/feed.xml", "example.com"},
		{"https://www.example.com", "example.com"},
		{"x.com/metruzanca", "x.com"},
		{"https://twitter.com/foo", "twitter.com"},
		{"https://blog.example.com/posts", "example.com"},
		{"https://sub.deep.example.co.uk/feed", "example.co.uk"},
		{"", ""},
		{"   ", ""},
		{"://nonsense", ""},
		{"localhost", "localhost"},
		{"192.168.1.1", "192.168.1.1"},
	}
	for _, c := range cases {
		if got := RegistrableDomain(c.in); got != c.want {
			t.Errorf("RegistrableDomain(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
