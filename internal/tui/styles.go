package tui

import "github.com/charmbracelet/lipgloss"

var (
	titleStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ffffff")).Background(lipgloss.Color("#2563eb")).Padding(0, 1)
	healthyStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#22c55e"))
	unhealthyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#ef4444"))
	halfOpenStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#f59e0b"))
	headerStyle    = lipgloss.NewStyle().Bold(true).Underline(true)
	tableRowStyle  = lipgloss.NewStyle().Padding(0, 1)
	sectionBorder  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1)
)
