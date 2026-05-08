package security

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"

	logpkg "github.com/user/aegis/internal/logging"
)

type RequestValidationConfig struct {
	MaxBodyBytes int64
	AllowedHosts []string
}

var allowedMethods = map[string]struct{}{
	http.MethodDelete:  {},
	http.MethodGet:     {},
	http.MethodHead:    {},
	http.MethodOptions: {},
	http.MethodPatch:   {},
	http.MethodPost:    {},
	http.MethodPut:     {},
}

func ValidateRequest(cfg RequestValidationConfig) func(http.Handler) http.Handler {
	allowedHosts := buildAllowedHostSet(cfg.AllowedHosts)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cfg.MaxBodyBytes > 0 {
				// [SECURITY] Large bodies are rejected at the edge to reduce memory pressure and backend amplification risk.
				if r.ContentLength > cfg.MaxBodyBytes {
					writeJSONError(w, http.StatusRequestEntityTooLarge, "request body too large")
					return
				}

				if r.Body != nil {
					r.Body = http.MaxBytesReader(w, r.Body, cfg.MaxBodyBytes)
				}
			}

			if hasTransferEncoding(r) && hasContentLength(r) {
				// [SECURITY] Requests with both Content-Length and Transfer-Encoding are ambiguous and therefore rejected to block smuggling.
				writeJSONError(w, http.StatusBadRequest, "ambiguous request")
				slog.Warn("request smuggling rejected", "ip", logpkg.MaskIP(ExtractIP(r)))
				return
			}

			if _, ok := allowedMethods[r.Method]; !ok {
				// [SECURITY] Only explicit HTTP verbs are allowed so unexpected tunneling methods fail closed.
				writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}

			if len(allowedHosts) > 0 && !isAllowedHost(r.Host, allowedHosts) {
				// [SECURITY] Public Host validation is optional and fail-closed when configured so hostile hosts cannot blend into trusted domains.
				writeJSONError(w, http.StatusBadRequest, "invalid host")
				slog.Warn("host validation rejected", "host", r.Host, "ip", logpkg.MaskIP(ExtractIP(r)))
				return
			}

			cleanPath, traversalAttempt, err := normalizePath(r)
			if err != nil {
				writeJSONError(w, http.StatusBadRequest, "invalid path")
				return
			}

			if traversalAttempt {
				// [SECURITY] Dot-dot traversal is blocked before routing so upstream handlers never see hostile filesystem-like paths.
				writeJSONError(w, http.StatusBadRequest, "invalid path")
				slog.Warn("path traversal rejected", "path", r.URL.Path, "ip", logpkg.MaskIP(ExtractIP(r)))
				return
			}

			r.URL.Path = cleanPath
			r.URL.RawPath = ""
			next.ServeHTTP(w, r)
		})
	}
}

func normalizePath(r *http.Request) (string, bool, error) {
	if r == nil || r.URL == nil {
		return "/", false, nil
	}

	rawPath := rawRequestPath(r)
	if rawPath == "" {
		rawPath = r.URL.EscapedPath()
	}

	if err := validateEscapedPath(rawPath); err != nil {
		return "", false, err
	}

	decodedPath, err := url.PathUnescape(rawPath)
	if err != nil {
		return "", false, err
	}

	traversalAttempt := hasDotDotSegment(decodedPath) || hasEncodedTraversal(decodedPath)
	cleanPath := path.Clean(decodedPath)
	if cleanPath == "." {
		cleanPath = "/"
	}

	return cleanPath, traversalAttempt, nil
}

func rawRequestPath(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}

	if r.URL.RawPath != "" {
		return r.URL.RawPath
	}

	if r.RequestURI == "" {
		return r.URL.Path
	}

	if parsedURL, err := url.ParseRequestURI(r.RequestURI); err == nil && parsedURL != nil {
		if parsedURL.RawPath != "" {
			return parsedURL.RawPath
		}

		if parsedURL.Path != "" {
			return parsedURL.Path
		}
	}

	return r.URL.Path
}

func hasDotDotSegment(rawPath string) bool {
	for _, segment := range strings.Split(rawPath, "/") {
		if segment == ".." {
			return true
		}
	}

	return false
}

func hasEncodedTraversal(rawPath string) bool {
	current := rawPath
	for i := 0; i < 2; i++ {
		decoded, err := url.PathUnescape(current)
		if err != nil {
			return true
		}

		if decoded == current {
			return false
		}

		if hasDotDotSegment(decoded) {
			return true
		}

		current = decoded
	}

	return false
}

func buildAllowedHostSet(hosts []string) map[string]struct{} {
	if len(hosts) == 0 {
		return nil
	}

	set := make(map[string]struct{}, len(hosts))
	for _, host := range hosts {
		normalized := normalizeHost(host)
		if normalized == "" {
			continue
		}

		set[normalized] = struct{}{}
	}

	return set
}

func isAllowedHost(host string, allowedHosts map[string]struct{}) bool {
	if len(allowedHosts) == 0 {
		return true
	}

	normalized := normalizeHost(host)
	if normalized == "" {
		return false
	}

	_, ok := allowedHosts[normalized]
	return ok
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" {
		return ""
	}

	if strings.Contains(host, ":") {
		if parsedURL, err := url.Parse("http://" + host); err == nil {
			return strings.ToLower(parsedURL.Hostname())
		}
	}

	return host
}

func hasTransferEncoding(r *http.Request) bool {
	if r == nil {
		return false
	}

	if len(r.TransferEncoding) > 0 {
		return true
	}

	return strings.TrimSpace(r.Header.Get("Transfer-Encoding")) != ""
}

func hasContentLength(r *http.Request) bool {
	if r == nil {
		return false
	}

	if len(r.TransferEncoding) == 0 && r.ContentLength > 0 {
		return true
	}

	return strings.TrimSpace(r.Header.Get("Content-Length")) != ""
}

func validateEscapedPath(rawPath string) error {
	for i := 0; i < len(rawPath); i++ {
		if rawPath[i] != '%' {
			continue
		}

		if i+2 >= len(rawPath) || !isHex(rawPath[i+1]) || !isHex(rawPath[i+2]) {
			return fmt.Errorf("invalid URL escape in path")
		}
		i += 2
	}

	return nil
}

func isHex(char byte) bool {
	switch {
	case char >= '0' && char <= '9':
		return true
	case char >= 'a' && char <= 'f':
		return true
	case char >= 'A' && char <= 'F':
		return true
	default:
		return false
	}
}

func writeJSONError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
