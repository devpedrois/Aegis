package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"unicode"
)

const maxEventLength = 240

type EventBuffer struct {
	entries []string
	next    int
	full    bool
	// EventBuffer is shared by slog handlers and the TUI, so all state is protected by this mutex.
	mu sync.Mutex
}

func NewEventBuffer(capacity int) *EventBuffer {
	if capacity <= 0 {
		capacity = 10
	}

	return &EventBuffer{entries: make([]string, 0, capacity)}
}

func (b *EventBuffer) Add(event string) {
	if b == nil {
		return
	}

	event = sanitizeEvent(event)
	if event == "" {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.entries) < cap(b.entries) {
		b.entries = append(b.entries, event)
		return
	}

	b.entries[b.next] = event
	b.next = (b.next + 1) % cap(b.entries)
	b.full = true
}

func (b *EventBuffer) Events() []string {
	if b == nil {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.entries) == 0 {
		return nil
	}

	events := make([]string, 0, len(b.entries))
	if !b.full {
		for i := len(b.entries) - 1; i >= 0; i-- {
			events = append(events, b.entries[i])
		}
		return events
	}

	for offset := 0; offset < len(b.entries); offset++ {
		index := (b.next - 1 - offset + len(b.entries)) % len(b.entries)
		events = append(events, b.entries[index])
	}
	return events
}

type EventHandler struct {
	next   slog.Handler
	buffer *EventBuffer
}

func NewEventHandler(next slog.Handler, buffer *EventBuffer) slog.Handler {
	if next == nil {
		next = slog.NewTextHandler(io.Discard, nil)
	}

	return EventHandler{next: next, buffer: buffer}
}

func NewLoggerWithEvents(cfg Config, writer io.Writer, buffer *EventBuffer) (*slog.Logger, error) {
	if writer == nil {
		writer = os.Stdout
	}

	logger, err := NewLogger(cfg, writer)
	if err != nil {
		return nil, err
	}

	return slog.New(NewEventHandler(logger.Handler(), buffer)), nil
}

func ConfigureDefaultWithEvents(cfg Config, buffer *EventBuffer, writer io.Writer) error {
	logger, err := NewLoggerWithEvents(cfg, writer, buffer)
	if err != nil {
		return err
	}

	slog.SetDefault(logger)
	return nil
}

func (h EventHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h EventHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Level >= slog.LevelWarn && h.buffer != nil {
		// [SECURITY] TUI events store only level and fixed log message text, never slog attributes that may contain client metadata.
		h.buffer.Add(fmt.Sprintf("%s %s %s", record.Time.Format("15:04:05"), strings.ToUpper(record.Level.String()), record.Message))
	}

	return h.next.Handle(ctx, record)
}

func (h EventHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return EventHandler{next: h.next.WithAttrs(attrs), buffer: h.buffer}
}

func (h EventHandler) WithGroup(name string) slog.Handler {
	return EventHandler{next: h.next.WithGroup(name), buffer: h.buffer}
}

func sanitizeEvent(event string) string {
	event = terminalSafePrefix(event)
	event = strings.Join(strings.Fields(event), " ")
	runes := []rune(event)
	if len(runes) <= maxEventLength {
		return event
	}

	return string(runes[:maxEventLength-1]) + "…"
}

func terminalSafePrefix(value string) string {
	var builder strings.Builder
	for _, r := range strings.TrimSpace(value) {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			break
		}
		builder.WriteRune(r)
	}

	return builder.String()
}
