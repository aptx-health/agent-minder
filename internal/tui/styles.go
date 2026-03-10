package tui

import (
	"charm.land/lipgloss/v2"
)

var (
	// Colors.
	colorPrimary   = lipgloss.Color("#7C3AED") // purple
	colorSecondary = lipgloss.Color("#06B6D4") // cyan
	colorSuccess   = lipgloss.Color("#10B981") // green
	colorWarning   = lipgloss.Color("#F59E0B") // amber
	colorError     = lipgloss.Color("#EF4444") // red
	colorMuted     = lipgloss.Color("#6B7280") // gray
	colorBg        = lipgloss.Color("#1F2937") // dark gray

	// Styles.
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorPrimary).
			PaddingLeft(1)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorSecondary).
			PaddingLeft(1)

	statusRunning = lipgloss.NewStyle().
			Foreground(colorSuccess).
			Bold(true)

	statusPaused = lipgloss.NewStyle().
			Foreground(colorWarning).
			Bold(true)

	concernWarning = lipgloss.NewStyle().
			Foreground(colorWarning)

	concernInfo = lipgloss.NewStyle().
			Foreground(colorMuted)

	llmResponseStyle = lipgloss.NewStyle().
				PaddingLeft(2).
				Foreground(lipgloss.Color("#E5E7EB"))

	mutedStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	helpStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			PaddingLeft(1)

	errorStyle = lipgloss.NewStyle().
			Foreground(colorError)

	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorMuted).
			Padding(0, 1)
)
