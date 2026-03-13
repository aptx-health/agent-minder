package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// View renders the TUI dashboard with height-budgeted sections.
func (m Model) View() tea.View {
	if m.width == 0 {
		return tea.NewView("Loading...")
	}

	var b strings.Builder

	// Header (2 lines: title row + goal row).
	b.WriteString(m.renderHeader())
	b.WriteString("\n")

	// Info detail (repos/topics) — only when toggled.
	if m.showInfo {
		b.WriteString(m.renderInfoDetail())
		b.WriteString("\n")
	}

	// Active concerns.
	concerns := m.renderConcerns()
	if concerns != "" {
		b.WriteString(concerns)
		b.WriteString("\n")
	}

	// Tracked items strip.
	tracked := m.renderTrackedStrip()
	if tracked != "" {
		b.WriteString(tracked)
		b.WriteString("\n")
	}

	// Worktree display.
	if m.showWorktrees {
		wt := m.renderWorktrees()
		if wt != "" {
			b.WriteString(wt)
			b.WriteString("\n")
		}
	}

	// Main content area: filter mode replaces analysis+event log.
	if m.mode == "filter" {
		b.WriteString(m.renderFilterView())
		b.WriteString("\n")
	} else {
		// Analysis section.
		expandHint := "e: expand"
		if m.analysisExpanded {
			expandHint = "e: collapse"
		}
		b.WriteString(headerStyle().Render("Last Analysis"))
		b.WriteString("  ")
		b.WriteString(mutedStyle().Render(fmt.Sprintf("[%s]", expandHint)))
		if m.lastPoll != nil {
			b.WriteString("  ")
			b.WriteString(mutedStyle().Render(m.lastPoll.Duration.Round(time.Millisecond).String()))
		}
		b.WriteString("\n")
		b.WriteString(m.analysisVP.View())
		b.WriteString("\n\n")

		// Event log section.
		b.WriteString(headerStyle().Render("Event Log"))
		scrollHint := ""
		if !m.eventLogVP.AtTop() || !m.eventLogVP.AtBottom() {
			pct := m.eventLogVP.ScrollPercent()
			scrollHint = fmt.Sprintf("  [%.0f%%]", pct*100)
		}
		if scrollHint != "" {
			b.WriteString(mutedStyle().Render(scrollHint))
		}
		b.WriteString("\n")
		b.WriteString(m.eventLogVP.View())
		b.WriteString("\n")
	}

	// Bottom bar: input or help.
	b.WriteString(m.renderBottomBar())

	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

// renderHeader returns the compact 2-line header with inline repo/topic counts.
func (m Model) renderHeader() string {
	var b strings.Builder

	// Line 1: title + status + counts + spinner + theme.
	status := statusRunningStyle().Render("RUNNING")
	if m.autoPaused {
		idleDur := formatDuration(time.Since(m.lastUserInput))
		status = statusPausedStyle().Render(fmt.Sprintf("AUTO-PAUSED (idle %s)", idleDur))
	} else if m.poller.IsPaused() {
		status = statusPausedStyle().Render("PAUSED")
	}
	b.WriteString(titleStyle().Render(fmt.Sprintf("agent-minder: %s", m.project.Name)))
	b.WriteString("  ")
	b.WriteString(status)

	repos, _ := m.store.GetRepos(m.project.ID)
	topics, _ := m.store.GetTopics(m.project.ID)
	b.WriteString("  ")
	b.WriteString(mutedStyle().Render(fmt.Sprintf("%d repos", len(repos))))
	b.WriteString(mutedStyle().Render(" \u2022 "))
	b.WriteString(mutedStyle().Render(fmt.Sprintf("%d topics", len(topics))))

	if m.polling {
		b.WriteString("  ")
		b.WriteString(m.spinner.View())
		b.WriteString(" ")
		b.WriteString(mutedStyle().Render("polling..."))
	}
	b.WriteString("  ")
	b.WriteString(mutedStyle().Render(fmt.Sprintf("[%s]", currentTheme().Name)))
	b.WriteString("\n")

	// Line 2: goal.
	goalText := fmt.Sprintf("  %s \u2014 %s", m.project.GoalType, m.project.GoalDescription)
	b.WriteString(mutedStyle().Width(m.width).Render(goalText))
	b.WriteString("\n")

	return b.String()
}

// renderInfoDetail returns the expanded repos/topics listing (shown when showInfo is true).
func (m Model) renderInfoDetail() string {
	var b strings.Builder

	repos, _ := m.store.GetRepos(m.project.ID)
	b.WriteString(headerStyle().Render("Repos"))
	b.WriteString("\n")
	for _, r := range repos {
		b.WriteString(textStyle().Render(fmt.Sprintf("  %s (%s)", r.ShortName, r.Path)))
		b.WriteString("\n")
	}

	topics, _ := m.store.GetTopics(m.project.ID)
	if len(topics) > 0 {
		b.WriteString(headerStyle().Render("Topics"))
		b.WriteString("\n")
		for _, t := range topics {
			b.WriteString(textStyle().Render(fmt.Sprintf("  %s", t.Name)))
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")

	return b.String()
}

// renderConcerns returns wrapped concerns, capped at maxConcernLines total.
// If the wrapped output exceeds the cap, concerns are truncated to fit.
func (m Model) renderConcerns() string {
	concerns, _ := m.store.ActiveConcerns(m.project.ID)
	if len(concerns) == 0 {
		return ""
	}

	const maxConcernLines = 8
	maxConcerns := 5

	var b strings.Builder
	b.WriteString(headerStyle().Render(fmt.Sprintf("Active Concerns (%d)", len(concerns))))
	b.WriteString("\n")

	shown := concerns
	if len(shown) > maxConcerns {
		shown = shown[:maxConcerns]
	}

	// Render each concern with wrapping, tracking total lines used.
	linesUsed := 0
	concertsShown := 0
	for _, c := range shown {
		prefix := "INFO"
		style := concernInfoStyle()
		switch c.Severity {
		case "warning":
			style = concernWarningStyle()
			prefix = "WARN"
		case "danger":
			style = concernDangerStyle()
			prefix = "DANGER"
		}
		rendered := style.Width(m.width - 2).Render(fmt.Sprintf("  [%s] %s", prefix, c.Message))
		lineCount := strings.Count(rendered, "\n") + 1

		if linesUsed+lineCount > maxConcernLines && concertsShown > 0 {
			remaining := len(concerns) - concertsShown
			b.WriteString(mutedStyle().Render(fmt.Sprintf("  ... +%d more", remaining)))
			b.WriteString("\n")
			return b.String()
		}

		b.WriteString(rendered)
		b.WriteString("\n")
		linesUsed += lineCount
		concertsShown++
	}

	if len(concerns) > maxConcerns {
		b.WriteString(mutedStyle().Render(fmt.Sprintf("  ... +%d more", len(concerns)-maxConcerns)))
		b.WriteString("\n")
	}

	return b.String()
}

// renderTrackedStrip returns a compact tracked items display.
// Shows status tag and dot per item, wrapping to multiple lines for >5 items.
func (m Model) renderTrackedStrip() string {
	if len(m.trackedItems) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(headerStyle().Render(fmt.Sprintf("Tracked (%d)", len(m.trackedItems))))
	b.WriteString("\n")

	lineWidth := 2 // indent
	var line strings.Builder
	line.WriteString("  ")

	for i, item := range m.trackedItems {
		entry := fmt.Sprintf("#%d[%s]", item.Number, item.LastStatus)
		dot := statusDot(item.LastStatus)
		entryLen := len(entry) + 2 // +2 for dot + space

		// Wrap to next line if needed (allow ~5 items per line).
		if i > 0 && lineWidth+entryLen+2 > m.width-2 {
			b.WriteString(line.String())
			b.WriteString("\n")
			line.Reset()
			line.WriteString("  ")
			lineWidth = 2
		}

		if lineWidth > 2 {
			line.WriteString("  ")
			lineWidth += 2
		}
		line.WriteString(dot)
		line.WriteString(mutedStyle().Render(entry))
		lineWidth += entryLen
	}
	b.WriteString(line.String())
	b.WriteString("\n")

	return b.String()
}

// renderWorktrees returns a compact worktree display grouped by repo.
func (m Model) renderWorktrees() string {
	if len(m.worktrees) == 0 {
		return headerStyle().Render("Worktrees") + "  " + mutedStyle().Render("[w: hide]") + "\n" + mutedStyle().Render("  (none)") + "\n"
	}

	var b strings.Builder
	b.WriteString(headerStyle().Render(fmt.Sprintf("Worktrees (%d)", len(m.worktrees))))
	b.WriteString("  ")
	b.WriteString(mutedStyle().Render("[w: hide]"))
	b.WriteString("\n")

	// Group by repo short name.
	grouped := make(map[string][]string)
	var repoOrder []string
	for _, wt := range m.worktrees {
		if _, seen := grouped[wt.RepoShortName]; !seen {
			repoOrder = append(repoOrder, wt.RepoShortName)
		}
		branch := wt.Branch
		if branch == "" {
			branch = "(detached)"
		}
		grouped[wt.RepoShortName] = append(grouped[wt.RepoShortName], branch)
	}

	const maxRenderedLines = 6
	linesUsed := 0
	width := m.width
	if width <= 0 {
		width = 80
	}

	for i, repo := range repoOrder {
		branches := grouped[repo]
		line := fmt.Sprintf("  %s: %s", repo, strings.Join(branches, ", "))
		// Estimate wrapped line count based on terminal width.
		lineCount := (len(line) + width - 1) / width
		if lineCount < 1 {
			lineCount = 1
		}
		if linesUsed+lineCount > maxRenderedLines && linesUsed > 0 {
			// Count remaining worktrees across skipped repos.
			remaining := 0
			for _, r := range repoOrder[i:] {
				remaining += len(grouped[r])
			}
			b.WriteString(mutedStyle().Render(fmt.Sprintf("  ... +%d more worktrees across %d repos", remaining, len(repoOrder)-i)))
			b.WriteString("\n")
			break
		}
		b.WriteString(textStyle().Render(line))
		b.WriteString("\n")
		linesUsed += lineCount
	}

	return b.String()
}

// rebuildEventLogContent sets the event log viewport content with single-line entries.
func (m *Model) rebuildEventLogContent() {
	lines := make([]string, 0, len(m.events))
	for i := len(m.events) - 1; i >= 0; i-- {
		e := m.events[i]
		ts := e.Time.Format("15:04:05")
		summary := strings.ReplaceAll(e.Summary, "\n", " ")
		summary = strings.Join(strings.Fields(summary), " ")
		maxW := m.width - 22
		if maxW < 20 {
			maxW = 20
		}
		line := fmt.Sprintf("  [%s] %s: %s", ts, e.Type, truncateLine(summary, maxW))
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		lines = []string{"  (no events yet)"}
	}
	m.eventLogVP.SetContentLines(lines)
}

// rebuildAnalysisContent sets the analysis viewport content from the last poll.
func (m *Model) rebuildAnalysisContent() {
	if m.lastPoll == nil {
		m.analysisVP.SetContent(mutedStyle().Render("  Waiting for first poll..."))
		return
	}
	var b strings.Builder
	b.WriteString(mutedStyle().Render(fmt.Sprintf("  %d commits, %d messages",
		m.lastPoll.NewCommits, m.lastPoll.NewMessages)))
	b.WriteString("\n")
	if m.lastPoll.BusMessageSent != "" {
		b.WriteString(broadcastStyle().Render(fmt.Sprintf("  >> Bus: %s", m.lastPoll.BusMessageSent)))
		b.WriteString("\n")
	}
	response := m.lastPoll.LLMResponse()
	if response != "" {
		b.WriteString(llmResponseStyle().Width(m.width - 2).Render(response))
	}
	m.analysisVP.SetContent(b.String())
}

// resizeViewports computes the height budget and resizes both viewports.
func (m *Model) resizeViewports() {
	if m.width == 0 || m.height == 0 {
		return
	}

	analysisH, eventLogH := m.computeHeightBudget()

	m.analysisVP.SetWidth(m.width)
	m.analysisVP.SetHeight(analysisH)
	m.eventLogVP.SetWidth(m.width)
	m.eventLogVP.SetHeight(eventLogH)
}

// computeHeightBudget calculates the height for analysis and event log viewports.
func (m Model) computeHeightBudget() (analysisH, eventLogH int) {
	fixed := 2 // header (title + goal)
	fixed += 1 // blank line after header

	if m.showInfo {
		repos, _ := m.store.GetRepos(m.project.ID)
		topics, _ := m.store.GetTopics(m.project.ID)
		fixed += 1 + len(repos) // "Repos" header + repo lines
		if len(topics) > 0 {
			fixed += 1 + len(topics) // "Topics" header + topic lines
		}
		fixed += 1 // blank line after info
	}

	// Count actual rendered concern lines (matches renderConcerns logic).
	concernContent := m.renderConcerns()
	if concernContent != "" {
		fixed += strings.Count(concernContent, "\n")
		fixed += 1 // blank line after concerns
	}

	trackedContent := m.renderTrackedStrip()
	if trackedContent != "" {
		fixed += strings.Count(trackedContent, "\n")
		fixed += 1 // blank line after tracked
	}

	if m.showWorktrees {
		wtContent := m.renderWorktrees()
		if wtContent != "" {
			fixed += strings.Count(wtContent, "\n")
			fixed += 1 // blank line after worktrees
		}
	}

	fixed += 1 // analysis header
	fixed += 1 // event log header
	fixed += 3 // blank lines (after analysis VP, after event log VP, before bottom bar)
	fixed += m.bottomBarHeight()

	remaining := m.height - fixed
	if remaining < 6 {
		remaining = 6
	}

	if m.analysisExpanded {
		analysisH = remaining * 60 / 100
		eventLogH = remaining - analysisH
	} else {
		analysisH = 3
		eventLogH = remaining - 3
	}

	if analysisH < 2 {
		analysisH = 2
	}
	if eventLogH < 2 {
		eventLogH = 2
	}

	return analysisH, eventLogH
}

// bottomBarHeight returns the number of terminal lines the bottom bar will occupy.
func (m Model) bottomBarHeight() int {
	switch m.mode {
	case "broadcast":
		if m.broadcastStatus != "" {
			return 3 // blank + status + empty
		}
		return 6 // blank + textarea(3) + help + empty
	case "usermsg":
		if m.userMsgStatus != "" {
			return 3
		}
		return 6
	case "onboard":
		if m.onboardStatus != "" {
			return 3
		}
		return 7 // blank + header + textarea(3) + help + empty
	case "track", "untrack":
		if m.trackStatus != "" || m.trackError {
			return 3 // blank + status + empty
		}
		return len(m.trackRows) + 3 // blank + header + rows + help
	case "filter":
		return 3 // blank + help + empty
	default:
		return 3 // blank + 2 help rows
	}
}

// renderBottomBar renders the input area or help bar depending on mode.
func (m Model) renderBottomBar() string {
	var b strings.Builder
	b.WriteString("\n")

	switch m.mode {
	case "broadcast":
		if m.broadcastStatus == "Sending..." {
			b.WriteString("  ")
			b.WriteString(m.spinner.View())
			b.WriteString(" ")
			b.WriteString(broadcastStyle().Render("Sending broadcast..."))
		} else if m.broadcastStatus != "" {
			b.WriteString(broadcastStyle().Render(fmt.Sprintf("  %s", m.broadcastStatus)))
		} else {
			b.WriteString("  ")
			b.WriteString(m.broadcastInput.View())
		}
		b.WriteString("\n")
		if m.broadcastStatus == "" {
			b.WriteString(helpStyle().Render("ctrl+d: send \u2022 esc: cancel"))
		}
		b.WriteString("\n")
	case "usermsg":
		if m.userMsgStatus == "Posting..." {
			b.WriteString("  ")
			b.WriteString(m.spinner.View())
			b.WriteString(" ")
			b.WriteString(userMsgStyle().Render("Posting message..."))
		} else if m.userMsgStatus != "" {
			b.WriteString(userMsgStyle().Render(fmt.Sprintf("  %s", m.userMsgStatus)))
		} else {
			b.WriteString("  ")
			b.WriteString(m.userMsgInput.View())
		}
		b.WriteString("\n")
		if m.userMsgStatus == "" {
			b.WriteString(helpStyle().Render("ctrl+d: send \u2022 esc: cancel"))
		}
		b.WriteString("\n")
	case "onboard":
		if m.onboardStatus != "" && m.onboardStatus != "Generating onboarding message..." {
			b.WriteString(broadcastStyle().Render(fmt.Sprintf("  %s", m.onboardStatus)))
		} else if m.onboardStatus == "Generating onboarding message..." {
			b.WriteString("  ")
			b.WriteString(m.spinner.View())
			b.WriteString(" ")
			b.WriteString(broadcastStyle().Render("Generating onboarding message..."))
		} else {
			b.WriteString(headerStyle().Render("  Onboard \u2014 optional guidance for the new agent:"))
			b.WriteString("\n")
			b.WriteString("  ")
			b.WriteString(m.onboardInput.View())
		}
		b.WriteString("\n")
		if m.onboardStatus == "" {
			b.WriteString(helpStyle().Render("ctrl+d: generate & publish \u2022 esc: cancel (leave empty for generic onboarding)"))
		}
		b.WriteString("\n")
	case "track", "untrack":
		if m.trackStatus != "" && !m.trackError {
			b.WriteString("  ")
			b.WriteString(m.spinner.View())
			b.WriteString(" ")
			b.WriteString(mutedStyle().Render(m.trackStatus))
			b.WriteString("\n\n")
		} else if m.trackError {
			b.WriteString(errorStyle().Render(fmt.Sprintf("  %s", m.trackStatus)))
			b.WriteString("\n\n")
		} else {
			label := "Track issues"
			if m.mode == "untrack" {
				label = "Untrack issues (remove numbers to untrack)"
			}
			b.WriteString(headerStyle().Render(fmt.Sprintf("  %s:", label)))
			b.WriteString("\n")
			for i, row := range m.trackRows {
				cursor := " "
				if i == m.trackFocus {
					cursor = "\u25b8"
				}
				b.WriteString(fmt.Sprintf("  %s %s: ", cursor, mutedStyle().Render(row.ownerRepo)))
				b.WriteString(row.input.View())
				b.WriteString("\n")
			}
			help := "up/down: navigate \u2022 enter: submit \u2022 esc: cancel"
			b.WriteString(helpStyle().Render(help))
			b.WriteString("\n")
		}
	case "filter":
		if m.filterStatus != "" {
			b.WriteString(broadcastStyle().Render(fmt.Sprintf("  %s", m.filterStatus)))
		} else if m.filterState != nil {
			switch m.filterState.step {
			case filterStepSelectRepo:
				b.WriteString(helpStyle().Render("up/down: select \u2022 enter: confirm \u2022 esc: cancel"))
			case filterStepSelectType:
				b.WriteString(helpStyle().Render("l: label \u2022 m: milestone \u2022 p: project \u2022 a: assignee \u2022 esc: back"))
			case filterStepInputValue:
				b.WriteString(helpStyle().Render("enter: search \u2022 esc: back"))
			case filterStepFetching:
				// no help, spinner is shown in filter view
			case filterStepPreview:
				if m.filterState.err != nil || m.filterState.results == nil || len(m.filterState.results.Items) == 0 {
					b.WriteString(helpStyle().Render("esc: back"))
				} else {
					b.WriteString(helpStyle().Render("enter: add all \u2022 esc: back"))
				}
			case filterStepConflict:
				b.WriteString(helpStyle().Render("a: append \u2022 c: clear & replace \u2022 esc: back"))
			}
		}
		b.WriteString("\n\n")
	default:
		if m.broadcastStatus != "" {
			b.WriteString(broadcastStyle().Render(fmt.Sprintf("  %s", m.broadcastStatus)))
			b.WriteString("\n")
		}
		if m.userMsgStatus != "" {
			b.WriteString(userMsgStyle().Render(fmt.Sprintf("  %s", m.userMsgStatus)))
			b.WriteString("\n")
		}
		if m.filterStatus != "" {
			b.WriteString(broadcastStyle().Render(fmt.Sprintf("  %s", m.filterStatus)))
			b.WriteString("\n")
		}
		b.WriteString(renderHelpBar(m.width))
		b.WriteString("\n")
	}

	return b.String()
}

// renderHelpBar builds a two-row help bar with styled key hints.
func renderHelpBar(width int) string {
	keyStyle := helpKeyStyle()
	descStyle := helpStyle()
	sep := descStyle.Render(" \u2022 ")

	type hint struct {
		key  string
		desc string
	}

	hints := []hint{
		{"p", "pause/resume"},
		{"r", "poll now"},
		{"e", "expand"},
		{"w", "worktrees"},
		{"i", "track"},
		{"I", "untrack"},
		{"f", "filter"},
		{"u", "user msg"},
		{"m", "broadcast"},
		{"o", "onboard"},
		{"d", "details"},
		{"t", "theme"},
		{"q", "quit"},
	}

	var row1, row2 strings.Builder
	for idx, h := range hints {
		entry := keyStyle.Render(h.key) + descStyle.Render(": "+h.desc)
		target := &row1
		if idx >= 8 {
			target = &row2
		}
		if target.Len() > 0 {
			target.WriteString(sep)
		}
		target.WriteString(entry)
	}

	return row1.String() + "\n" + row2.String()
}

// truncateLine truncates a string to maxWidth characters, adding "..." if truncated.
func truncateLine(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return s
	}
	// Flatten to single line first.
	s = strings.ReplaceAll(s, "\n", " ")
	runes := []rune(s)
	if len(runes) <= maxWidth {
		return s
	}
	if maxWidth <= 3 {
		return string(runes[:maxWidth])
	}
	return string(runes[:maxWidth-3]) + "..."
}
