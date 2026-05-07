package security

import (
	"net"
	"net/http"
)

// [SECURITY] IP spoofing prevention:
// [SECURITY] Never trust X-Forwarded-For, X-Real-IP, or any other client-sent identity header.
// [SECURITY] Those headers are trivially forgeable by an attacker before the request reaches Aegis.
// [SECURITY] RemoteAddr is assigned by the TCP stack for the connection peer and is the only accepted signal here.
func ExtractIP(r *http.Request) string {
	if r == nil {
		return ""
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}
