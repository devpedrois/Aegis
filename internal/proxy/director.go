package proxy

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"

	"github.com/user/aegis/internal/security"
)

func NewDirector(targetURL *url.URL, hostHeader string) func(req *http.Request) {
	return func(req *http.Request) {
		stripHopByHopHeaders(req)
		req.URL.Scheme = targetURL.Scheme
		req.URL.Host = targetURL.Host
		req.Host = hostHeader

		if ip := security.ExtractIP(req); ip != "" {
			// [SECURITY] Forwarded client identity is derived from the TCP peer only so spoofed headers cannot poison downstream trust decisions.
			req.Header.Set("X-Forwarded-For", ip)
		} else {
			// [SECURITY] If RemoteAddr is malformed, preserve zero trust by clearing the forwarding header.
			req.Header.Del("X-Forwarded-For")
		}

		// [SECURITY] Each forwarded request gets a fresh edge-generated identifier so traces cannot be spoofed by client-supplied values.
		req.Header.Set("X-Request-ID", generateUUID())
	}
}

func stripHopByHopHeaders(req *http.Request) {
	if req == nil {
		return
	}

	hopByHop := []string{
		"Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"TE",
		"Trailer",
		"Trailers",
		"Transfer-Encoding",
		"Upgrade",
	}

	if connectionHeaders := req.Header.Get("Connection"); connectionHeaders != "" {
		for _, headerName := range strings.Split(connectionHeaders, ",") {
			trimmed := strings.TrimSpace(headerName)
			if trimmed != "" {
				hopByHop = append(hopByHop, trimmed)
			}
		}
	}

	for _, headerName := range hopByHop {
		// [SECURITY] Hop-by-hop headers are stripped before forwarding to block header smuggling across trust boundaries.
		req.Header.Del(headerName)
	}

	// [SECURITY] Client-supplied request trailers are discarded so delayed hop-by-hop metadata cannot reach trusted upstreams.
	req.Trailer = nil
}

func generateUUID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return ""
	}

	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80

	encoded := hex.EncodeToString(bytes[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
