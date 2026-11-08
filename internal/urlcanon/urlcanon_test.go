package urlcanon

import "testing"

func TestLink(t *testing.T) {
	cases := []struct{ in, want string }{
		// A LAN/tailnet host plus an explicit non-default port is preserved.
		{"http://nixos:8090/Path", "http://nixos:8090/Path"},
		{"HTTP://NixOS:8090/Path", "http://nixos:8090/Path"},
		// Scheme and host are case-insensitive; the path is not.
		{"HTTPS://Example.COM/Some/Path?Q=1#Frag", "https://example.com/Some/Path?Q=1#Frag"},
		// Default ports are dropped.
		{"HTTP://Example.COM:80/A", "http://example.com/A"},
		{"https://example.com:443/A", "https://example.com/A"},
		// A missing scheme defaults to https.
		{"example.com/a", "https://example.com/a"},
		{"  example.com/a  ", "https://example.com/a"},
		// IPv6 literals keep their brackets and port.
		{"https://[::1]:8090/x", "https://[::1]:8090/x"},
		{"https://[::1]/x", "https://[::1]/x"},
		// Empty stays empty; no trailing slash is invented.
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		if got := Link(c.in); got != c.want {
			t.Errorf("Link(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
