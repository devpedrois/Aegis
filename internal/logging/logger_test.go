package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestMaskIPMasksLastTwoOctets(t *testing.T) {
	t.Parallel()

	if got := MaskIP("192.168.10.20"); got != "192.168.x.x" {
		t.Fatalf("MaskIP() = %q, want %q", got, "192.168.x.x")
	}
}

func TestMaskIPKeepsNonIPv4InputsStable(t *testing.T) {
	t.Parallel()

	if got := MaskIP("not-an-ip"); got != "not-an-ip" {
		t.Fatalf("MaskIP() = %q, want original value", got)
	}
}

func TestNewLoggerRejectsUnknownFormat(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	_, err := NewLogger(Config{Level: "info", Format: "xml"}, &buffer)
	if err == nil {
		t.Fatal("NewLogger() error = nil, want invalid format error")
	}
}

func TestNewLoggerRespectsConfiguredLevel(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(Config{Level: "warn", Format: "text"}, &buffer)
	if err != nil {
		t.Fatalf("NewLogger() error = %v", err)
	}

	logger.LogAttrs(nil, slog.LevelInfo, "info message")
	logger.LogAttrs(nil, slog.LevelWarn, "warn message")

	output := buffer.String()
	if strings.Contains(output, "info message") {
		t.Fatalf("output = %q, want info filtered at warn level", output)
	}

	if !strings.Contains(output, "warn message") {
		t.Fatalf("output = %q, want warn message present", output)
	}
}
