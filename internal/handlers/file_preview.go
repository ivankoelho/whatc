package handlers

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/valyala/fasthttp"
)

// Viewing a file in the browser is not the same as downloading it, and it is the riskier of the two: the
// bytes are interpreted by the browser inside the application's origin. These helpers decide, from the BYTES
// (never from the extension or the client's label), whether a file may be shown inline, and set the
// headers that keep what is shown inert:
//   - only images, PDF, plain text and audio/video for the media element may be inline;
//   - HTML, SVG, XML and every other type are always a download, served as an opaque octet-stream;
//   - nosniff, a CSP that allows nothing (default-src 'none'), same-origin resource policy, no caching.

// inlineMime: what may be shown inside the application.
func inlineMime(m string) bool {
	switch m {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf", "text/plain":
		return true
	}
	return false
}

// validatedMime returns the type the server is willing to serve for these bytes, given what the record or
// the extension claims. It never returns a type the bytes do not support, and never an active type.
func validatedMime(data []byte, claimed string) string {
	claimed = strings.ToLower(strings.TrimSpace(strings.SplitN(claimed, ";", 2)[0]))
	sniffed := strings.SplitN(http.DetectContentType(data), ";", 2)[0]
	switch claimed {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf":
		if sniffed == claimed {
			return claimed
		}
	case "text/plain":
		// DetectContentType calls UTF-8 text "text/plain"; anything with markup is still plain TEXT to the
		// browser once served as text/plain with nosniff, but a NUL byte or invalid UTF-8 is not text.
		if sniffed == "text/plain" && utf8.Valid(data) {
			return claimed
		}
	}
	if strings.HasPrefix(claimed, "audio/") || strings.HasPrefix(claimed, "video/") {
		// Container sniffing is unreliable for audio/video, and they are only decoded by a media element.
		switch {
		case strings.HasPrefix(sniffed, "audio/"), strings.HasPrefix(sniffed, "video/"), sniffed == "application/ogg", sniffed == "application/octet-stream":
			return claimed
		}
	}
	return "application/octet-stream"
}

// setSafeFileHeaders sets the headers of a file response. inline only when the (validated) type allows it.
func setSafeFileHeaders(h *fasthttp.ResponseHeader, mime string) {
	h.Set("Content-Type", mime)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Cache-Control", "private, no-cache")
}
