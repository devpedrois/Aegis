package security

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writer := &securityHeaderWriter{ResponseWriter: w}
		next.ServeHTTP(writer, r)
		writer.stripTrailers()
	})
}

type securityHeaderWriter struct {
	http.ResponseWriter
	wroteHeader      bool
	declaredTrailers []string
}

func (w *securityHeaderWriter) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}

	w.wroteHeader = true
	w.applySecurityHeaders()
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *securityHeaderWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}

	return w.ResponseWriter.Write(body)
}

func (w *securityHeaderWriter) ReadFrom(reader io.Reader) (int64, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}

	if readFrom, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return readFrom.ReadFrom(reader)
	}

	return io.Copy(w.ResponseWriter, reader)
}

func (w *securityHeaderWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}

	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *securityHeaderWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter does not support hijacking")
	}

	return hijacker.Hijack()
}

func (w *securityHeaderWriter) Push(target string, opts *http.PushOptions) error {
	if pusher, ok := w.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, opts)
	}

	return http.ErrNotSupported
}

func (w *securityHeaderWriter) applySecurityHeaders() {
	headers := w.Header()
	w.captureDeclaredTrailers(headers)
	// [SECURITY] Response hardening is enforced at the edge so backend misconfiguration cannot silently drop core browser protections.
	headers.Set("X-Content-Type-Options", "nosniff")
	headers.Set("X-Frame-Options", "DENY")
	headers.Set("X-XSS-Protection", "0")
	headers.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	headers.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	// [SECURITY] Internal routing metadata must never escape to clients because it would leak trusted topology details.
	headers.Del("Trailer")
	headers.Del("X-Aegis-Backend")
	headers.Del("X-Powered-By")
	// [SECURITY] Backend identity is replaced with a fixed edge identifier so attackers cannot fingerprint upstream components.
	headers.Set("Server", "aegis")
}

func (w *securityHeaderWriter) captureDeclaredTrailers(headers http.Header) {
	for _, value := range headers.Values("Trailer") {
		for _, trailerName := range strings.Split(value, ",") {
			trimmed := http.CanonicalHeaderKey(strings.TrimSpace(trailerName))
			if trimmed != "" {
				w.declaredTrailers = append(w.declaredTrailers, trimmed)
			}
		}
	}
}

func (w *securityHeaderWriter) stripTrailers() {
	if len(w.declaredTrailers) == 0 {
		return
	}

	headers := w.Header()
	headers.Del("Trailer")
	for _, trailerName := range w.declaredTrailers {
		// [SECURITY] Declared response trailers are removed after handler completion so delayed upstream metadata cannot leak past edge sanitization.
		headers.Del(trailerName)
		headers.Del(http.TrailerPrefix + trailerName)
	}
}
