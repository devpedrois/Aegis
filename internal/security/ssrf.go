package security

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

type HostLookupFunc func(string) ([]string, error)

type ResolvedBackend struct {
	PinnedAddress string
	ServerName    string
	OriginalHost  string
}

func ValidateBackendURL(rawURL string, lookupHost HostLookupFunc, allowLoopback bool) (ResolvedBackend, error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return ResolvedBackend{}, fmt.Errorf("parse backend URL: %w", err)
	}

	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return ResolvedBackend{}, fmt.Errorf("unsupported scheme")
	}

	if parsedURL.User != nil {
		return ResolvedBackend{}, fmt.Errorf("backend URL must not contain credentials")
	}

	hostname := parsedURL.Hostname()
	if hostname == "" {
		return ResolvedBackend{}, fmt.Errorf("backend URL must include a host")
	}

	port := parsedURL.Port()
	if port == "" {
		port = defaultPortForScheme(parsedURL.Scheme)
	}

	resolved := ResolvedBackend{
		ServerName:   hostname,
		OriginalHost: parsedURL.Host,
	}

	if parsedIP := net.ParseIP(hostname); parsedIP != nil {
		// [SECURITY] Direct IP backends are validated up front so internal services cannot be reached through config tampering.
		if IsPrivateIP(parsedIP) {
			if allowLoopback && parsedIP.IsLoopback() {
				resolved.PinnedAddress = net.JoinHostPort(parsedIP.String(), port)
				return resolved, nil
			}

			return ResolvedBackend{}, fmt.Errorf("backend URL resolves to private/reserved IP: %s", parsedIP.String())
		}

		resolved.PinnedAddress = net.JoinHostPort(parsedIP.String(), port)
		return resolved, nil
	}

	addresses, err := lookupHost(hostname)
	if err != nil {
		return ResolvedBackend{}, fmt.Errorf("resolve backend host %q: %w", hostname, err)
	}

	for _, address := range addresses {
		ip := net.ParseIP(address)
		if ip == nil {
			return ResolvedBackend{}, fmt.Errorf("resolved backend host %q to invalid IP %q", hostname, address)
		}

		// [SECURITY] DNS answers are treated as untrusted input and blocked if they point to private or metadata ranges.
		if IsPrivateIP(ip) {
			if allowLoopback && ip.IsLoopback() {
				if resolved.PinnedAddress == "" {
					// [SECURITY] The development bypass is loopback-only and still pins the validated address to reduce DNS rebinding risk.
					resolved.PinnedAddress = net.JoinHostPort(ip.String(), port)
				}

				continue
			}

			return ResolvedBackend{}, fmt.Errorf("backend URL resolves to private/reserved IP: %s", ip.String())
		}

		if resolved.PinnedAddress == "" {
			// [SECURITY] The first validated routable address is pinned at startup so runtime DNS rebinding cannot retarget upstream traffic.
			resolved.PinnedAddress = net.JoinHostPort(ip.String(), port)
		}
	}

	if resolved.PinnedAddress == "" {
		return ResolvedBackend{}, fmt.Errorf("resolved backend host %q to no routable IPs", hostname)
	}

	return resolved, nil
}

func IsPrivateIP(ip net.IP) bool {
	if ip == nil {
		return false
	}

	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() || ip.IsUnspecified() {
		return true
	}

	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 10:
			return true
		case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
			return true
		case ip4[0] == 192 && ip4[1] == 168:
			return true
		case ip4[0] == 169 && ip4[1] == 254:
			// [SECURITY] Link-local and cloud metadata ranges are blocked to prevent credential exfiltration via SSRF.
			return true
		}
	}

	if strings.EqualFold(ip.String(), "::1") {
		return true
	}

	return false
}

func defaultPortForScheme(scheme string) string {
	if scheme == "https" {
		return "443"
	}

	return "80"
}
