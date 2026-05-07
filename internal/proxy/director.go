package proxy

import (
	"net"
	"net/http"
)

func newDirector(target Target) func(req *http.Request) {
	return func(req *http.Request) {
		req.URL.Scheme = target.URL.Scheme
		req.URL.Host = target.URL.Host
		req.Host = target.HostHeader

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
