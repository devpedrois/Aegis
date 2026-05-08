package logging

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
)

type Config struct {
	Level  string
	Format string
}

func NewLogger(cfg Config, writer io.Writer) (*slog.Logger, error) {
	if writer == nil {
		writer = os.Stdout
	}

	level, err := parseLevel(cfg.Level)
	if err != nil {
		return nil, err
	}

	options := &slog.HandlerOptions{Level: level}
	switch strings.ToLower(strings.TrimSpace(cfg.Format)) {
	case "", "json":
		return slog.New(slog.NewJSONHandler(writer, options)), nil
	case "text":
		return slog.New(slog.NewTextHandler(writer, options)), nil
	default:
		return nil, fmt.Errorf("unsupported log format %q", cfg.Format)
	}
}

func ConfigureDefault(cfg Config) error {
	logger, err := NewLogger(cfg, os.Stdout)
	if err != nil {
		return err
	}

	slog.SetDefault(logger)
	return nil
}

func MaskIP(ip string) string {
	parsedIP := net.ParseIP(strings.TrimSpace(ip))
	if parsedIP == nil {
		return ip
	}

	if ipv4 := parsedIP.To4(); ipv4 != nil {
		// [SECURITY] IPv4 client addresses are partially masked so logs preserve coarse routing context without retaining full user identifiers.
		return fmt.Sprintf("%d.%d.x.x", ipv4[0], ipv4[1])
	}

	parts := strings.Split(parsedIP.String(), ":")
	if len(parts) >= 2 {
		// [SECURITY] IPv6 client addresses are reduced to the first two segments so logs avoid storing full client identifiers.
		return parts[0] + ":" + parts[1] + ":x:x:x:x:x:x"
	}

	return "masked"
}

func parseLevel(rawLevel string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(rawLevel)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unsupported log level %q", rawLevel)
	}
}
