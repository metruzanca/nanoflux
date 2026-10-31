package web

import (
	"net/url"
	"strconv"
)

// Core player adapter names. A plugin's PlaybackInfo.Player must be one of
// these; the host owns how each adapter resumes and reports its position.
const (
	// PlayerNative drives a native <video>/<audio> element (including an HLS
	// playlist attached by hls.js).
	PlayerNative = "native"
	// PlayerYouTube drives a YouTube iframe with its postMessage API.
	PlayerYouTube = "youtube"
)

// KnownPlayer reports whether name is a player adapter the host implements.
func KnownPlayer(name string) bool {
	switch name {
	case PlayerNative, PlayerYouTube:
		return true
	}
	return false
}

// ResumeEmbedSrc returns an embed src adjusted to resume at positionSec for
// player. For YouTube it enables the JS API (required for the postMessage
// control the host uses to save progress) and passes the start offset and
// embedding origin; any other player or a malformed URL is returned unchanged.
func ResumeEmbedSrc(player, src string, positionSec int, origin string) string {
	if player != PlayerYouTube || src == "" {
		return src
	}
	u, err := url.Parse(src)
	if err != nil || u.Host == "" {
		return src
	}
	q := u.Query()
	q.Set("enablejsapi", "1")
	if origin != "" {
		q.Set("origin", origin)
	}
	if positionSec > 0 {
		q.Set("start", strconv.Itoa(positionSec))
	}
	u.RawQuery = q.Encode()
	return u.String()
}
