package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/dustinlange/agent-minder/internal/db"
)

// logRefreshMsg triggers periodic re-read of the agent log file.
type logRefreshMsg struct{}

// openLogViewer opens the log viewer overlay for the given task.
func (m *Model) openLogViewer(task *db.AutopilotTask) tea.Cmd {
	if task == nil || task.AgentLog == "" {
		return nil
	}

	m.showLogViewer = true
	m.logViewerTask = task

	// Create viewport for the log content.
	vp := viewport.New()
	vp.KeyMap = safeViewportKeyMap()
	vp.SoftWrap = true
	vp.FillHeight = true
	vp.SetWidth(m.width)
	vp.SetHeight(m.height - 4) // header + separator + footer
	m.logViewerVP = vp

	// Read the initial log content.
	m.loadLogContent()

	// Auto-scroll to bottom.
	m.logViewerVP.GotoBottom()
	m.logViewerAtBottom = true

	// Start auto-refresh tick if task is running.
	if task.Status == "running" {
		return logRefreshTick()
	}
	return nil
}

// closeLogViewer closes the log viewer overlay.
func (m *Model) closeLogViewer() {
	m.showLogViewer = false
	m.logViewerTask = nil
	m.logViewerContent = ""
	m.logViewerFileSize = 0
	m.logViewerAtBottom = true
}

// loadLogContent reads the log file and sets viewport content.
func (m *Model) loadLogContent() {
	if m.logViewerTask == nil || m.logViewerTask.AgentLog == "" {
		m.logViewerContent = "No log available"
		m.logViewerVP.SetContent(m.logViewerContent)
		return
	}

	data, err := os.ReadFile(m.logViewerTask.AgentLog)
	if err != nil {
		m.logViewerContent = fmt.Sprintf("Error reading log: %v", err)
		m.logViewerVP.SetContent(m.logViewerContent)
		return
	}

	m.logViewerFileSize = int64(len(data))
	content := string(data)

	// Auto-detect format and render.
	if isJSONL(content) {
		m.logViewerContent = renderJSONL(content)
	} else {
		m.logViewerContent = renderPlainLog(content)
	}
	m.logViewerVP.SetContent(m.logViewerContent)
}

// refreshLogContent incrementally reads new content if the file has grown.
func (m *Model) refreshLogContent() {
	if m.logViewerTask == nil || m.logViewerTask.AgentLog == "" {
		return
	}

	info, err := os.Stat(m.logViewerTask.AgentLog)
	if err != nil {
		return
	}

	// No change in size — skip.
	if info.Size() == m.logViewerFileSize {
		return
	}

	// Track scroll position before refresh.
	wasAtBottom := m.logViewerAtBottom

	// Re-read the full file (simpler than offset-based for formatted output).
	m.loadLogContent()

	// Restore scroll position.
	if wasAtBottom {
		m.logViewerVP.GotoBottom()
	}
}

// logRefreshTick returns a command that fires a logRefreshMsg after 2 seconds.
func logRefreshTick() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return logRefreshMsg{}
	})
}

// copyLogPath copies the log file path to the clipboard.
func (m *Model) copyLogPath() tea.Cmd {
	if m.logViewerTask == nil || m.logViewerTask.AgentLog == "" {
		return nil
	}

	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(m.logViewerTask.AgentLog)
	if err := cmd.Run(); err != nil {
		m.logViewerStatus = fmt.Sprintf("Copy failed: %v", err)
	} else {
		m.logViewerStatus = fmt.Sprintf("Copied: %s", m.logViewerTask.AgentLog)
	}
	return tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
		return clearLogViewerStatusMsg{}
	})
}

// clearLogViewerStatusMsg clears the log viewer status flash.
type clearLogViewerStatusMsg struct{}

// isJSONL checks if the content looks like JSONL by trying to parse the first non-empty line.
func isJSONL(content string) bool {
	for _, line := range strings.SplitN(content, "\n", 10) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var obj map[string]any
		return json.Unmarshal([]byte(line), &obj) == nil
	}
	return false
}

// renderPlainLog renders plain text log with basic styling.
// Dims timestamp-like prefixes and highlights error lines.
func renderPlainLog(content string) string {
	lines := strings.Split(content, "\n")
	var b strings.Builder
	errorStyle := lipgloss.NewStyle().Foreground(currentTheme().Error)
	dimStyle := lipgloss.NewStyle().Foreground(currentTheme().Muted)

	for _, line := range lines {
		if line == "" {
			b.WriteString("\n")
			continue
		}

		// Highlight error-like lines.
		lower := strings.ToLower(line)
		if strings.Contains(lower, "error") || strings.Contains(lower, "panic") ||
			strings.Contains(lower, "fatal") || strings.Contains(lower, "failed") {
			b.WriteString(errorStyle.Render(line))
			b.WriteString("\n")
			continue
		}

		// Dim timestamp prefixes (e.g., "2024-01-15T10:30:00Z" or "[10:30:00]").
		styled := dimTimestamp(line, dimStyle)
		b.WriteString(styled)
		b.WriteString("\n")
	}

	return b.String()
}

// dimTimestamp dims a leading timestamp in a log line.
func dimTimestamp(line string, dimStyle lipgloss.Style) string {
	// ISO timestamp: "2024-01-15T10:30:00Z ..." or "2024-01-15T10:30:00.123Z ..."
	if len(line) > 20 && (line[4] == '-' && line[7] == '-' && line[10] == 'T') {
		// Find the end of the timestamp (space after it).
		for i := 10; i < len(line) && i < 35; i++ {
			if line[i] == ' ' {
				return dimStyle.Render(line[:i]) + line[i:]
			}
		}
	}

	// Bracketed time: "[10:30:00] ..."
	if len(line) > 10 && line[0] == '[' {
		for i := 1; i < len(line) && i < 25; i++ {
			if line[i] == ']' {
				return dimStyle.Render(line[:i+1]) + line[i+1:]
			}
		}
	}

	return line
}

// jsonlEvent represents a parsed JSONL log event from stream-json output.
type jsonlEvent struct {
	Type      string  `json:"type"`       // "assistant", "result", "system", etc.
	Subtype   string  `json:"subtype"`    // "tool_use", "text", etc.
	Tool      string  `json:"tool"`       // tool name for tool_use events
	Input     string  `json:"input"`      // tool input summary
	Text      string  `json:"text"`       // text content
	Turns     int     `json:"num_turns"`  // for result events
	Cost      float64 `json:"total_cost"` // for result events
	Duration  int     `json:"duration_s"` // for result events
	Timestamp string  `json:"timestamp"`  // ISO timestamp
	Error     string  `json:"error"`      // error message
}

// renderJSONL renders JSONL log content with formatted, color-coded display.
func renderJSONL(content string) string {
	lines := strings.Split(content, "\n")
	theme := currentTheme()

	toolStyle := lipgloss.NewStyle().Foreground(theme.Secondary)
	textStyle := lipgloss.NewStyle().Foreground(theme.Text)
	resultStyle := lipgloss.NewStyle().Foreground(theme.Success)
	errorStyle := lipgloss.NewStyle().Foreground(theme.Error)
	dimStyle := lipgloss.NewStyle().Foreground(theme.Muted)
	turnStyle := lipgloss.NewStyle().Foreground(theme.Muted).Width(4).Align(lipgloss.Right)

	var b strings.Builder
	turnNum := 0

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var evt jsonlEvent
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			// Malformed line — render dimmed.
			b.WriteString(dimStyle.Render(truncateLine(line, 120)))
			b.WriteString("\n")
			continue
		}

		ts := formatTimestamp(evt.Timestamp)

		switch evt.Type {
		case "assistant":
			if evt.Subtype == "tool_use" || evt.Tool != "" {
				turnNum++
				toolName := evt.Tool
				if toolName == "" {
					toolName = "tool"
				}
				detail := evt.Input
				if len(detail) > 80 {
					detail = detail[:77] + "..."
				}
				fmt.Fprintf(&b, "%s  %s  %s  %s\n",
					dimStyle.Render(ts),
					turnStyle.Render(fmt.Sprintf("%d", turnNum)),
					toolStyle.Render(fmt.Sprintf("%-12s", toolName)),
					textStyle.Render(detail))
			} else if evt.Text != "" {
				text := evt.Text
				if len(text) > 80 {
					text = text[:77] + "..."
				}
				fmt.Fprintf(&b, "%s       %s\n",
					dimStyle.Render(ts),
					textStyle.Render(text))
			}

		case "result":
			fmt.Fprintf(&b, "%s       %s\n",
				dimStyle.Render(ts),
				resultStyle.Render(fmt.Sprintf("✓ Completed — %d turns, $%.2f, %ds",
					evt.Turns, evt.Cost, evt.Duration)))

		case "error":
			msg := evt.Error
			if msg == "" {
				msg = evt.Text
			}
			if msg == "" {
				msg = "unknown error"
			}
			fmt.Fprintf(&b, "%s       %s\n",
				dimStyle.Render(ts),
				errorStyle.Render("✗ "+msg))

		default:
			// Other event types — single dim line.
			summary := evt.Type
			if evt.Subtype != "" {
				summary += "/" + evt.Subtype
			}
			if evt.Text != "" {
				text := evt.Text
				if len(text) > 60 {
					text = text[:57] + "..."
				}
				summary += ": " + text
			}
			fmt.Fprintf(&b, "%s       %s\n",
				dimStyle.Render(ts),
				dimStyle.Render(summary))
		}
	}

	return b.String()
}

// formatTimestamp extracts a compact time from an ISO timestamp.
func formatTimestamp(ts string) string {
	if ts == "" {
		return "        "
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		// Try other common formats.
		t, err = time.Parse("2006-01-02T15:04:05.000Z", ts)
		if err != nil {
			if len(ts) > 8 {
				return ts[:8]
			}
			return ts
		}
	}
	return t.Format("15:04:05")
}

// renderLogViewerOverlay renders the full-screen log viewer.
func (m Model) renderLogViewerOverlay() string {
	if m.logViewerTask == nil {
		return ""
	}

	var b strings.Builder

	// Header.
	title := fmt.Sprintf("Agent Log: #%d — %s",
		m.logViewerTask.IssueNumber,
		truncateLine(m.logViewerTask.IssueTitle, m.width-40))
	closeHint := mutedStyle().Render("[q: close]")

	headerText := headerStyle().Render(title)
	// Right-align close hint.
	padding := m.width - lipgloss.Width(headerText) - lipgloss.Width(closeHint) - 2
	if padding < 2 {
		padding = 2
	}
	b.WriteString(headerText)
	b.WriteString(strings.Repeat(" ", padding))
	b.WriteString(closeHint)
	b.WriteString("\n")

	// Separator.
	sep := strings.Repeat("─", m.width)
	b.WriteString(mutedStyle().Render(sep))
	b.WriteString("\n")

	// Viewport content.
	b.WriteString(m.logViewerVP.View())
	b.WriteString("\n")

	// Footer.
	var footer strings.Builder
	path := mutedStyle().Render(m.logViewerTask.AgentLog)
	footer.WriteString("  ")
	footer.WriteString(path)

	hints := mutedStyle().Render("  c: copy path  ↑/↓/pgup/pgdn: scroll")
	footer.WriteString(hints)

	if m.logViewerStatus != "" {
		footer.WriteString("  ")
		footer.WriteString(broadcastStyle().Render(m.logViewerStatus))
	}

	b.WriteString(footer.String())

	return b.String()
}
