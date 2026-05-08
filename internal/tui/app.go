package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/user/aegis/internal/metrics"
)

const defaultRefreshInterval = time.Second

type tickMsg struct {
	t time.Time
}

type Model struct {
	snapshot    metrics.MetricsSnapshot
	events      []string
	startTime   time.Time
	width       int
	height      int
	getSnapshot func() metrics.MetricsSnapshot

	refreshInterval time.Duration
	getEvents       func() []string
}

func NewModel(getSnapshot func() metrics.MetricsSnapshot, events []string, refreshInterval time.Duration) Model {
	return NewModelWithEventSource(getSnapshot, func() []string {
		return append([]string(nil), events...)
	}, refreshInterval)
}

func NewModelWithEventSource(getSnapshot func() metrics.MetricsSnapshot, getEvents func() []string, refreshInterval time.Duration) Model {
	if getSnapshot == nil {
		getSnapshot = func() metrics.MetricsSnapshot { return metrics.MetricsSnapshot{Timestamp: time.Now()} }
	}
	if getEvents == nil {
		getEvents = func() []string { return nil }
	}
	if refreshInterval <= 0 {
		refreshInterval = defaultRefreshInterval
	}

	return Model{
		snapshot:        getSnapshot(),
		events:          getEvents(),
		startTime:       time.Now(),
		width:           80,
		height:          24,
		getSnapshot:     getSnapshot,
		refreshInterval: refreshInterval,
		getEvents:       getEvents,
	}
}

func (m Model) Init() tea.Cmd {
	return m.nextTick()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "r":
			m.refresh()
			return m, nil
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tickMsg:
		_ = msg.t
		m.refresh()
		return m, m.nextTick()
	}

	return m, nil
}

func (m Model) View() string {
	width := contentWidth(m)
	sections := []string{
		fitBlock(renderHeader(m), width),
		fitBlock(renderBackendsTable(m.snapshot.BackendStats, width), width),
		fitBlock(renderRateLimiter(m.snapshot.CurrentRate, m.snapshot.AdaptiveState, m.snapshot.BlockedIPs, width), width),
		fitBlock(renderEvents(m.events, width), width),
	}

	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

func (m *Model) refresh() {
	m.snapshot = m.getSnapshot()
	if m.snapshot.Timestamp.IsZero() {
		m.snapshot.Timestamp = time.Now()
	}
	m.events = m.getEvents()
}

func (m Model) nextTick() tea.Cmd {
	return tea.Tick(m.refreshInterval, func(t time.Time) tea.Msg {
		return tickMsg{t: t}
	})
}

func contentWidth(m Model) int {
	if m.width <= 0 {
		return 80
	}
	if m.width < 20 {
		return 20
	}

	return m.width
}
