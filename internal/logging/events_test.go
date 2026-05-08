package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestEventBufferKeepsNewestTenEvents(t *testing.T) {
	t.Parallel()

	buffer := NewEventBuffer(10)
	for i := 0; i < 12; i++ {
		buffer.Add("WARN event")
	}

	events := buffer.Events()
	if len(events) != 10 {
		t.Fatalf("len(Events()) = %d, want 10", len(events))
	}
}

func TestEventBufferReturnsNewestFirst(t *testing.T) {
	t.Parallel()

	buffer := NewEventBuffer(10)
	buffer.Add("WARN older")
	buffer.Add("ERROR newer")

	events := buffer.Events()
	if len(events) != 2 {
		t.Fatalf("len(Events()) = %d, want 2", len(events))
	}
	if events[0] != "ERROR newer" {
		t.Fatalf("events[0] = %q, want newest first", events[0])
	}
}

func TestEventHandlerCapturesWarnAndErrorOnly(t *testing.T) {
	t.Parallel()

	buffer := NewEventBuffer(10)
	var output bytes.Buffer
	logger, err := NewLoggerWithEvents(Config{Level: "info", Format: "text"}, &output, buffer)
	if err != nil {
		t.Fatalf("NewLoggerWithEvents() error = %v", err)
	}

	logger.Info("info ignored")
	logger.Warn("warn captured")
	logger.Error("error captured")

	events := buffer.Events()
	if len(events) != 2 {
		t.Fatalf("len(Events()) = %d, want 2", len(events))
	}
	if !strings.Contains(events[0], "ERROR error captured") {
		t.Fatalf("events[0] = %q, want error event", events[0])
	}
	if !strings.Contains(events[1], "WARN warn captured") {
		t.Fatalf("events[1] = %q, want warn event", events[1])
	}
}

func TestEventHandlerSanitizesControlCharacters(t *testing.T) {
	t.Parallel()

	buffer := NewEventBuffer(10)
	handler := NewEventHandler(slog.NewTextHandler(&bytes.Buffer{}, nil), buffer)
	logger := slog.New(handler)

	logger.Warn("bad\nmessage\twith\rcontrols")

	events := buffer.Events()
	if len(events) != 1 {
		t.Fatalf("len(Events()) = %d, want 1", len(events))
	}
	if strings.ContainsAny(events[0], "\n\r\t") {
		t.Fatalf("event = %q, want no control characters", events[0])
	}
}

func TestEventHandlerRemovesTerminalSpoofingRunes(t *testing.T) {
	t.Parallel()

	buffer := NewEventBuffer(10)
	handler := NewEventHandler(slog.NewTextHandler(&bytes.Buffer{}, nil), buffer)
	logger := slog.New(handler)

	logger.Warn("safe\u202eexe.txt\u200b\x1b]0;owned\a")

	events := buffer.Events()
	if len(events) != 1 {
		t.Fatalf("len(Events()) = %d, want 1", len(events))
	}
	if strings.ContainsAny(events[0], "\u202e\u200b\x1b\a") {
		t.Fatalf("event = %q, want no terminal spoofing runes", events[0])
	}
}

func TestEventHandlerDoesNotCaptureSensitiveAttributes(t *testing.T) {
	t.Parallel()

	buffer := NewEventBuffer(10)
	handler := NewEventHandler(slog.NewTextHandler(&bytes.Buffer{}, nil), buffer)
	logger := slog.New(handler)

	logger.Warn("request rejected", "Authorization", "Bearer secret-token", "Cookie", "session=secret")

	events := buffer.Events()
	if len(events) != 1 {
		t.Fatalf("len(Events()) = %d, want 1", len(events))
	}
	if strings.Contains(events[0], "secret") || strings.Contains(events[0], "Authorization") || strings.Contains(events[0], "Cookie") {
		t.Fatalf("event = %q, want no sensitive attributes", events[0])
	}
}

func TestEventBufferTruncatesUnicodeSafely(t *testing.T) {
	t.Parallel()

	buffer := NewEventBuffer(10)
	buffer.Add(strings.Repeat("界", 300))

	events := buffer.Events()
	if len(events) != 1 {
		t.Fatalf("len(Events()) = %d, want 1", len(events))
	}
	if !strings.HasSuffix(events[0], "…") {
		t.Fatalf("event = %q, want ellipsis suffix", events[0])
	}
}
