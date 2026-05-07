package proxy

import (
	"net"
	"net/http"
	"net/url"
)

func NewDirector(targetURL *url.URL, hostHeader string) func(req *http.Request) {
	return func(req *http.Request) {
		req.URL.Scheme = targetURL.Scheme
		req.URL.Host = targetURL.Host
		req.Host = hostHeader

		host, _, err := net.SplitHostPort(req.RemoteAddr)
		if err == nil {
			// [SECURITY] X-Forwarded-For is derived from RemoteAddr only and overwrites any client-supplied value.
			req.Header.Set("X-Forwarded-For", host)
		} else {
			// [SECURITY] If RemoteAddr is malformed, preserve zero trust by clearing the forwarding header.
			req.Header.Del("X-Forwarded-For")
		}
	}
}
