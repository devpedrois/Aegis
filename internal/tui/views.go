package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/user/aegis/internal/metrics"
)

func renderHeader(m Model) string {
	timestamp := m.snapshot.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	uptime := time.Since(m.startTime).Truncate(time.Second)
	header := fmt.Sprintf("%s | Uptime: %s | %s", titleStyle.Render("⚡ AEGIS — Reverse Proxy"), uptime, timestamp.Format("2006-01-02 15:04:05"))
	return sectionBorder.Width(contentWidth(m) - 2).Render(header)
}

func renderBackendsTable(stats []metrics.BackendSnapshot, width int) string {
	rows := []string{headerStyle.Render("URL | Status | Circuit | Req/s | P50 | P95 | P99 | Active | Err%")}
	if len(stats) == 0 {
		rows = append(rows, "No backend metrics yet")
	}

	for _, stat := range stats {
		urlWidth := max(24, width-55)
		row := fmt.Sprintf("%s | %s | %s | %.1f | %s | %s | %s | %d | %s",
			truncate(sanitizeDisplayText(stat.URL), urlWidth),
			renderStatus(stat.Healthy),
			renderCircuit(sanitizeDisplayText(stat.CircuitState)),
			stat.ReqPerSec,
			formatLatency(stat.LatencyP50),
			formatLatency(stat.LatencyP95),
			formatLatency(stat.LatencyP99),
			stat.ActiveRequests,
			renderErrorRate(stat.ErrorRate),
		)
		rows = append(rows, tableRowStyle.Render(row))
	}

	return sectionBorder.Width(width - 2).Render(fitLines(strings.Join(rows, "\n"), width-6))
}

func renderRateLimiter(rate float64, state string, blocked int, width int) string {
	if state == "" {
		state = "normal"
	}
	state = sanitizeDisplayText(state)

	maxRate := 500.0
	barWidth := min(24, max(10, width-60))
	filled := int((rate / maxRate) * float64(barWidth))
	if filled < 0 {
		filled = 0
	}
	if filled > barWidth {
		filled = barWidth
	}

	bar := healthyStyle.Render(strings.Repeat("█", filled)) + strings.Repeat("░", barWidth-filled)
	line := fmt.Sprintf("Rate: %.1f req/s | State: %s | Blocked IPs: %d | %s", rate, state, blocked, bar)
	return sectionBorder.Width(width - 2).Render(fitLines(line, width-6))
}

func renderEvents(events []string, width int) string {
	rows := []string{headerStyle.Render("Recent Events")}
	if len(events) == 0 {
		rows = append(rows, "No warn/error events")
	}

	for index, event := range events {
		if index >= 10 {
			break
		}
		rows = append(rows, colorEvent(truncate(sanitizeDisplayText(event), width-6)))
	}

	return sectionBorder.Width(width - 2).Render(fitLines(strings.Join(rows, "\n"), width-6))
}

func renderStatus(healthy bool) string {
	if healthy {
		return healthyStyle.Render("🟢")
	}

	return unhealthyStyle.Render("🔴")
}

func renderCircuit(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "closed":
		return healthyStyle.Render("Closed")
	case "open":
		return unhealthyStyle.Render("Open")
	case "half-open", "halfopen":
		return halfOpenStyle.Render("Half-Open")
	default:
		return state
	}
}

func renderErrorRate(rate float64) string {
	value := fmt.Sprintf("%.1f%%", rate*100)
	if rate > 0.05 {
		return unhealthyStyle.Render(value)
	}

	return value
}

func formatLatency(value time.Duration) string {
	return fmt.Sprintf("%dms", value.Milliseconds())
}

func colorEvent(event string) string {
	upper := strings.ToUpper(event)
	switch {
	case strings.Contains(upper, "ERROR"):
		return unhealthyStyle.Render(event)
	case strings.Contains(upper, "WARN"):
		return halfOpenStyle.Render(event)
	default:
		return event
	}
}

func fitBlock(value string, width int) string {
	return fitLines(value, width)
}

func fitLines(value string, width int) string {
	if width <= 0 {
		return value
	}

	lines := strings.Split(value, "\n")
	for index, line := range lines {
		lines[index] = truncate(line, width)
	}

	return strings.Join(lines, "\n")
}

func truncate(value string, width int) string {
	if width <= 0 || lipgloss.Width(value) <= width {
		return value
	}
	if width <= 1 {
		return ""
	}

	var builder strings.Builder
	for _, r := range value {
		if lipgloss.Width(builder.String()+string(r)+"…") > width {
			break
		}
		builder.WriteRune(r)
	}
	builder.WriteRune('…')
	return builder.String()
}

func sanitizeDisplayText(value string) string {
	var builder strings.Builder
	for _, r := range strings.TrimSpace(value) {
		// [SECURITY] TUI text is treated as hostile; control and format runes can forge rows or spoof terminal state.
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			break
		}
		builder.WriteRune(r)
	}

	return strings.Join(strings.Fields(builder.String()), " ")
}
