package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/user/aegis/internal/metrics"
)

func TestModelViewWithEmptySnapshotDoesNotPanic(t *testing.T) {
	t.Parallel()

	model := NewModel(func() metrics.MetricsSnapshot {
		return metrics.MetricsSnapshot{Timestamp: time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)}
	}, nil, time.Second)

	view := model.View()
	if !strings.Contains(view, "AEGIS") {
		t.Fatalf("View() = %q, want header", view)
	}
}

func TestModelViewRendersThreeBackends(t *testing.T) {
	t.Parallel()

	model := NewModel(func() metrics.MetricsSnapshot {
		return metrics.MetricsSnapshot{
			Timestamp: time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC),
			BackendStats: []metrics.BackendSnapshot{
				{URL: "http://backend-1.example", Healthy: true, CircuitState: "closed"},
				{URL: "http://backend-2.example", Healthy: true, CircuitState: "closed"},
				{URL: "http://backend-3.example", Healthy: true, CircuitState: "closed"},
			},
		}
	}, nil, time.Second)

	view := model.View()
	for _, backend := range []string{"backend-1", "backend-2", "backend-3"} {
		if !strings.Contains(view, backend) {
			t.Fatalf("View() missing %q in %q", backend, view)
		}
	}
}

func TestModelViewRendersUnhealthyStatus(t *testing.T) {
	t.Parallel()

	model := NewModel(func() metrics.MetricsSnapshot {
		return metrics.MetricsSnapshot{
			Timestamp: time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC),
			BackendStats: []metrics.BackendSnapshot{
				{URL: "http://backend.example", Healthy: false, CircuitState: "open"},
			},
		}
	}, nil, time.Second)

	if view := model.View(); !strings.Contains(view, "🔴") {
		t.Fatalf("View() = %q, want unhealthy marker", view)
	}
}

func TestModelQuitKeyReturnsQuitCommand(t *testing.T) {
	t.Parallel()

	model := NewModel(func() metrics.MetricsSnapshot { return metrics.MetricsSnapshot{} }, nil, time.Second)
	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("Update(q) cmd = nil, want tea.Quit")
	}

	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("Update(q) cmd() = %T, want tea.QuitMsg", cmd())
	}
}

func TestModelViewFitsEightyColumns(t *testing.T) {
	t.Parallel()

	model := NewModel(func() metrics.MetricsSnapshot {
		return metrics.MetricsSnapshot{
			Timestamp:   time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC),
			CurrentRate: 100,
			BackendStats: []metrics.BackendSnapshot{
				{URL: "http://very-long-backend-name.example:8081", Healthy: true, CircuitState: "closed"},
			},
		}
	}, []string{"WARN degraded backend"}, time.Second)
	model.width = 80
	model.height = 24

	for _, line := range strings.Split(model.View(), "\n") {
		if width := lipgloss.Width(line); width > 80 {
			t.Fatalf("line width = %d, want <= 80: %q", width, line)
		}
	}
}

func TestModelWindowResizeConstrainsViewWidth(t *testing.T) {
	t.Parallel()

	model := NewModel(func() metrics.MetricsSnapshot {
		return metrics.MetricsSnapshot{
			Timestamp: time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC),
			BackendStats: []metrics.BackendSnapshot{
				{URL: "http://very-long-backend-name.example:8081", Healthy: true, CircuitState: "closed"},
			},
		}
	}, nil, time.Second)

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	resized := updated.(Model)
	for _, line := range strings.Split(resized.View(), "\n") {
		if width := lipgloss.Width(line); width > 40 {
			t.Fatalf("line width = %d, want <= 40: %q", width, line)
		}
	}
}

func TestRefreshKeyUpdatesSnapshotImmediately(t *testing.T) {
	t.Parallel()

	rate := 10.0
	model := NewModel(func() metrics.MetricsSnapshot {
		return metrics.MetricsSnapshot{CurrentRate: rate}
	}, nil, time.Second)

	rate = 99
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd != nil {
		t.Fatal("Update(r) cmd != nil, want immediate refresh without command")
	}

	refreshed := updated.(Model)
	if refreshed.snapshot.CurrentRate != 99 {
		t.Fatalf("CurrentRate = %.1f, want 99", refreshed.snapshot.CurrentRate)
	}
}

func TestModelViewSanitizesBackendDisplayText(t *testing.T) {
	t.Parallel()

	model := NewModel(func() metrics.MetricsSnapshot {
		return metrics.MetricsSnapshot{
			Timestamp: time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC),
			BackendStats: []metrics.BackendSnapshot{
				{URL: "http://safe.example\nERROR forged row\x1b[31m", Healthy: true, CircuitState: "closed"},
			},
		}
	}, nil, time.Second)

	view := model.View()
	if strings.Contains(view, "forged row") {
		t.Fatalf("View() = %q, want forged backend text removed", view)
	}
	if strings.Contains(view, "\x1b") {
		t.Fatalf("View() contains escape characters: %q", view)
	}
}

func TestModelViewSanitizesInjectedEvents(t *testing.T) {
	t.Parallel()

	model := NewModel(func() metrics.MetricsSnapshot {
		return metrics.MetricsSnapshot{Timestamp: time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)}
	}, []string{"WARN safe\nERROR forged row\x1b]0;owned\a"}, time.Second)

	view := model.View()
	if strings.Contains(view, "forged row") {
		t.Fatalf("View() = %q, want forged event line removed", view)
	}
	if strings.ContainsAny(view, "\x1b\a") {
		t.Fatalf("View() contains terminal control characters: %q", view)
	}
}

func TestTickRefreshesSnapshotAndEvents(t *testing.T) {
	t.Parallel()

	model := NewModel(func() metrics.MetricsSnapshot {
		return metrics.MetricsSnapshot{CurrentRate: 42}
	}, []string{"ERROR backend failed"}, time.Second)

	updated, cmd := model.Update(tickMsg{t: time.Now()})
	if cmd == nil {
		t.Fatal("Update(tickMsg) cmd = nil, want next tick")
	}

	next := updated.(Model)
	if next.snapshot.CurrentRate != 42 {
		t.Fatalf("snapshot.CurrentRate = %.2f, want 42", next.snapshot.CurrentRate)
	}
	if len(next.events) != 1 || next.events[0] != "ERROR backend failed" {
		t.Fatalf("events = %#v, want latest event", next.events)
	}
}
