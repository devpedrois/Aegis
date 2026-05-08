package proxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"time"

	logpkg "github.com/user/aegis/internal/logging"
	securitypkg "github.com/user/aegis/internal/security"
)

func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}

			// [SECURITY] Panic details are logged only on the server side so clients never receive stack traces or internal error strings.
			slog.Error("panic recovered",
				"panic", fmt.Sprint(recovered),
				"path", r.URL.Path,
				"ip", logpkg.MaskIP(securitypkg.ExtractIP(r)),
				"stack", string(debug.Stack()),
			)

			if writer, ok := w.(interface{ Written() bool }); ok && writer.Written() {
				return
			}

			writeBadGateway(w)
		}()

		next.ServeHTTP(w, r)
	})
}

func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(recorder, r)

		// [SECURITY] Only coarse request metadata is logged so secrets, bodies and authentication headers never enter log storage.
		slog.Info("request completed",
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.statusCode,
			"latency_ms", time.Since(start).Milliseconds(),
			"backend", recorder.backend,
			"ip", logpkg.MaskIP(securitypkg.ExtractIP(r)),
		)
	})
}

type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
	backend     string
}

func (w *loggingResponseWriter) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}

	w.wroteHeader = true
	w.statusCode = statusCode
	w.captureInternalHeaders()
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *loggingResponseWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}

	return w.ResponseWriter.Write(body)
}

func (w *loggingResponseWriter) ReadFrom(reader io.Reader) (int64, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}

	if readFrom, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return readFrom.ReadFrom(reader)
	}

	return io.Copy(w.ResponseWriter, reader)
}

func (w *loggingResponseWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}

	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *loggingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter does not support hijacking")
	}

	return hijacker.Hijack()
}

func (w *loggingResponseWriter) Push(target string, opts *http.PushOptions) error {
	if pusher, ok := w.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, opts)
	}

	return http.ErrNotSupported
}

func (w *loggingResponseWriter) Written() bool {
	return w.wroteHeader
}

func (w *loggingResponseWriter) captureInternalHeaders() {
	// [SECURITY] Backend routing metadata is captured for trusted logs and removed before the client response is finalized.
	w.backend = w.Header().Get("X-Aegis-Backend")
	w.Header().Del("X-Aegis-Backend")
}

func writeBadGateway(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad gateway"})
}
