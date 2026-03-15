package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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

	// Tab bar.
	b.WriteString(m.renderTabBar())
	b.WriteString("\n")

	// Warning banner (persistent, dismiss with 'd').
	if m.warningBanner != "" {
		bannerText := fmt.Sprintf(" ⚠ %s  (press d to dismiss)", m.warningBanner)
		maxW := m.width
		if maxW <= 0 {
			maxW = 80
		}
		banner := warningStyle().Width(maxW).Render(bannerText)
		b.WriteString(banner)
		b.WriteString("\n")
	}

	// Tab content: modal modes overlay tab content.
	if m.mode == "settings" {
		b.WriteString(m.renderSettingsView())
		b.WriteString("\n")
	} else if m.mode == "filter" {
		b.WriteString(m.renderFilterView())
		b.WriteString("\n")
	} else if (m.mode == "track" || m.mode == "untrack") && m.trackStep == trackStepPreview {
		b.WriteString(m.renderTrackPreview())
		b.WriteString("\n")
	} else if m.activeTab == tabOperations {
		b.WriteString(m.renderOperationsTab())
	} else {
		b.WriteString(m.renderAnalysisTab())
	}

	// Bottom bar: input or help.
	b.WriteString(m.renderBottomBar())

	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

// renderTabBar renders the tab selector bar.
func (m Model) renderTabBar() string {
	var b strings.Builder
	b.WriteString(" ")

	opsLabel := " 1: Operations "
	analysisLabel := " 2: Analysis "
	if m.analysisHasNew {
		analysisLabel = " 2: Analysis \u25cf "
	}

	if m.activeTab == tabOperations {
		b.WriteString(tabActiveStyle().Render(opsLabel))
		b.WriteString("  ")
		b.WriteString(tabInactiveStyle().Render(analysisLabel))
	} else {
		b.WriteString(tabInactiveStyle().Render(opsLabel))
		b.WriteString("  ")
		b.WriteString(tabActiveStyle().Render(analysisLabel))
	}

	return b.String()
}

// renderOperationsTab renders the Operations tab content.
func (m Model) renderOperationsTab() string {
	var b strings.Builder

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

	// Autopilot slot status (when running).
	if m.autopilotMode == "running" && m.autopilotSupervisor != nil {
		ap := m.renderAutopilotSlots()
		if ap != "" {
			b.WriteString(ap)
			b.WriteString("\n")
		}
	}

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

	return b.String()
}

// hasAnalysis returns true if an LLM analysis result is available.
func (m Model) hasAnalysis() bool {
	return m.lastPoll != nil && m.lastPoll.LLMResponse() != ""
}

// renderAnalysisTab renders the Analysis tab content.
func (m Model) renderAnalysisTab() string {
	var b strings.Builder

	// Active concerns.
	concerns := m.renderConcerns()
	if concerns != "" {
		b.WriteString(concerns)
		b.WriteString("\n")
	}

	if !m.hasAnalysis() {
		// Prominent call-to-action when no analysis exists yet.
		b.WriteString("\n")
		b.WriteString(headerStyle().Render("  Press R to run analysis"))
		b.WriteString("\n\n")
		b.WriteString(mutedStyle().Render("  Analysis uses the LLM to review recent activity, identify"))
		b.WriteString("\n")
		b.WriteString(mutedStyle().Render("  concerns, and optionally publish to the message bus."))
		b.WriteString("\n")
		return b.String()
	}

	// Analysis section with results.
	expandHint := "e: expand"
	if m.analysisExpanded {
		expandHint = "e: collapse"
	}
	b.WriteString(headerStyle().Render("Last Analysis"))
	b.WriteString("  ")
	b.WriteString(mutedStyle().Render(fmt.Sprintf("[%s]", expandHint)))
	b.WriteString("  ")
	b.WriteString(mutedStyle().Render("[R: rerun]"))
	if m.lastPoll != nil {
		b.WriteString("  ")
		b.WriteString(mutedStyle().Render(m.lastPoll.Duration.Round(time.Millisecond).String()))
	}
	b.WriteString("\n")
	b.WriteString(m.analysisVP.View())
	b.WriteString("\n\n")

	// Poll summary.
	if m.lastPoll != nil {
		statusParts := fmt.Sprintf("  %d commits, %d messages", m.lastPoll.NewCommits, m.lastPoll.NewMessages)
		if m.lastPoll.NewWorktrees > 0 {
			statusParts += fmt.Sprintf(", %d new worktrees", m.lastPoll.NewWorktrees)
		}
		b.WriteString(mutedStyle().Render(statusParts))
		b.WriteString("\n")
	}

	return b.String()
}

// renderHeader returns the compact 2-line header with inline repo/topic counts.
func (m Model) renderHeader() string {
	var b strings.Builder

	// Line 1: title + status + counts + spinner + theme.
	status := statusRunningStyle().Render("SYNCING")
	if m.autoPaused {
		idleDur := formatDuration(time.Since(m.lastUserInput))
		status = statusPausedStyle().Render(fmt.Sprintf("IDLE (paused %s)", idleDur))
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

// renderConcerns returns wrapped concerns, capped at maxConcernLines total.
// If the wrapped output exceeds the cap, concerns are truncated to fit.
// When concernsExpanded is true, all concerns are shown without caps.
func (m Model) renderConcerns() string {
	concerns, _ := m.store.ActiveConcerns(m.project.ID)
	if len(concerns) == 0 {
		return ""
	}

	const maxConcernLines = 8
	maxConcerns := 5

	var b strings.Builder
	toggleHint := "[c: expand]"
	if m.concernsExpanded {
		toggleHint = "[c: collapse]"
	}
	b.WriteString(headerStyle().Render(fmt.Sprintf("Active Concerns (%d)", len(concerns))))
	b.WriteString(" ")
	b.WriteString(mutedStyle().Render(toggleHint))
	b.WriteString("\n")

	shown := concerns
	if !m.concernsExpanded && len(shown) > maxConcerns {
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

		if !m.concernsExpanded && linesUsed+lineCount > maxConcernLines && concertsShown > 0 {
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

	if !m.concernsExpanded && len(concerns) > maxConcerns {
		b.WriteString(mutedStyle().Render(fmt.Sprintf("  ... +%d more", len(concerns)-maxConcerns)))
		b.WriteString("\n")
	}

	return b.String()
}

// renderTrackedStrip returns a compact tracked items display.
// Shows status tag and dot per item, wrapping to multiple lines for >5 items.
// When trackedExpanded is true, shows one item per line with title.
func (m Model) renderTrackedStrip() string {
	if len(m.trackedItems) == 0 {
		return ""
	}

	var b strings.Builder
	toggleHint := "[x: expand]"
	if m.trackedExpanded {
		toggleHint = "[x: collapse]"
	}
	b.WriteString(headerStyle().Render(fmt.Sprintf("Tracked (%d)", len(m.trackedItems))))
	b.WriteString("  ")
	b.WriteString(mutedStyle().Render(toggleHint))
	b.WriteString("\n")

	if m.trackedExpanded {
		for _, item := range m.trackedItems {
			st := m.effectiveStatus(item)
			dot := statusDot(st)
			ref := fmt.Sprintf("#%d", item.Number)
			status := fmt.Sprintf("[%s]", st)
			ghURL := fmt.Sprintf("https://github.com/%s/%s/issues/%d", item.Owner, item.Repo, item.Number)

			// Truncate title to fit available width.
			// Format: "  ● #123[Status] Title..."
			prefixLen := 2 + 2 + len(ref) + len(status) + 1 // indent + dot + ref + status + space
			maxTitle := m.width - prefixLen - 2
			title := item.Title
			if maxTitle > 0 && len(title) > maxTitle {
				title = title[:maxTitle-3] + "..."
			}

			b.WriteString("  ")
			b.WriteString(dot)
			fmt.Fprintf(&b, "\033]8;;%s\033\\", ghURL)
			b.WriteString(mutedStyle().Render(ref + status))
			b.WriteString("\033]8;;\033\\")
			if title != "" {
				b.WriteString(" ")
				b.WriteString(title)
			}
			b.WriteString("\n")
		}
	} else {
		lineWidth := 2 // indent
		var line strings.Builder
		line.WriteString("  ")

		for i, item := range m.trackedItems {
			st := m.effectiveStatus(item)
			entry := fmt.Sprintf("#%d[%s]", item.Number, st)
			dot := statusDot(st)
			entryLen := len(entry) + 2 // +2 for dot + space
			// Build GitHub URL for OSC 8 hyperlink.
			ghURL := fmt.Sprintf("https://github.com/%s/%s/issues/%d", item.Owner, item.Repo, item.Number)

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
			fmt.Fprintf(&line, "\033]8;;%s\033\\", ghURL)
			line.WriteString(mutedStyle().Render(entry))
			line.WriteString("\033]8;;\033\\")
			lineWidth += entryLen
		}
		b.WriteString(line.String())
		b.WriteString("\n")
	}

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

// renderTrackPreview renders the track/untrack preview in the main content area.
func (m Model) renderTrackPreview() string {
	var b strings.Builder

	action := "Track"
	if m.mode == "untrack" {
		action = "Untrack"
	}
	b.WriteString(headerStyle().Render(fmt.Sprintf("%s Issues", action)))
	b.WriteString("\n\n")

	if len(m.trackPreviewItems) == 0 {
		b.WriteString(mutedStyle().Render("  No items to " + strings.ToLower(action) + "."))
		b.WriteString("\n")
		return b.String()
	}

	b.WriteString(textStyle().Render(fmt.Sprintf("  %d items to %s:", len(m.trackPreviewItems), strings.ToLower(action))))
	b.WriteString("\n\n")

	for _, item := range m.trackPreviewItems {
		dot := statusDot(item.status)
		title := item.title
		if len(title) > 60 {
			title = title[:57] + "..."
		}
		fmt.Fprintf(&b, "  %s %s/%s#%d %s",
			dot,
			item.ref.Owner, item.ref.Repo, item.ref.Number,
			mutedStyle().Render(title))
		b.WriteString("\n")
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
		summary = truncateLine(summary, maxW)
		var style lipgloss.Style
		switch e.Type {
		case "user":
			style = userMsgStyle()
		case "broadcast":
			style = broadcastStyle()
		case "warning":
			style = warningStyle()
		case "error":
			style = errorStyle()
		default:
			style = mutedStyle()
		}
		line := fmt.Sprintf("  %s %s", mutedStyle().Render(fmt.Sprintf("[%s]", ts)), style.Render(fmt.Sprintf("%s: %s", e.Type, summary)))
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
		m.analysisVP.SetContent(mutedStyle().Render("  No analysis yet \u2014 press R to run"))
		return
	}
	response := m.lastPoll.LLMResponse()
	if response == "" {
		m.analysisVP.SetContent(mutedStyle().Render("  No analysis yet \u2014 press R to run"))
		return
	}
	var b strings.Builder
	if m.lastPoll.BusMessageSent != "" {
		b.WriteString(broadcastStyle().Render(fmt.Sprintf("  >> Bus: %s", m.lastPoll.BusMessageSent)))
		b.WriteString("\n")
	}
	b.WriteString(llmResponseStyle().Width(m.width - 2).Render(response))
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
// Only the active tab's viewport gets meaningful space; the other gets a minimum.
func (m Model) computeHeightBudget() (analysisH, eventLogH int) {
	fixed := 2 // header (title + goal)
	fixed += 1 // blank line after header
	fixed += 1 // tab bar
	fixed += 1 // blank line after tab bar
	fixed += m.bottomBarHeight()

	if m.activeTab == tabOperations {
		// Operations tab: tracked + worktrees + autopilot + event log header + VP
		trackedContent := m.renderTrackedStrip()
		if trackedContent != "" {
			fixed += strings.Count(trackedContent, "\n")
			fixed += 1
		}

		if m.showWorktrees {
			wtContent := m.renderWorktrees()
			if wtContent != "" {
				fixed += strings.Count(wtContent, "\n")
				fixed += 1
			}
		}

		if m.autopilotMode == "running" && m.autopilotSupervisor != nil {
			apContent := m.renderAutopilotSlots()
			if apContent != "" {
				fixed += strings.Count(apContent, "\n")
				fixed += 1
			}
		}

		fixed += 1 // event log header
		fixed += 1 // trailing newline

		remaining := m.height - fixed
		if remaining < 4 {
			remaining = 4
		}
		eventLogH = remaining
		analysisH = 2 // minimal for off-screen tab
	} else {
		// Analysis tab: concerns + analysis header + VP + poll summary
		concernContent := m.renderConcerns()
		if concernContent != "" {
			fixed += strings.Count(concernContent, "\n")
			fixed += 1
		}

		fixed += 1 // analysis header
		fixed += 2 // blank lines around VP
		fixed += 1 // poll summary line

		remaining := m.height - fixed
		if remaining < 4 {
			remaining = 4
		}

		if m.analysisExpanded {
			analysisH = remaining
		} else {
			analysisH = 5
			if analysisH > remaining {
				analysisH = remaining
			}
		}
		eventLogH = 2 // minimal for off-screen tab
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
		if m.trackStep == trackStepPreview {
			return 3 // blank + help + empty
		}
		if m.trackStatus != "" || m.trackError {
			return 3 // blank + status + empty
		}
		return len(m.trackRows) + 3 // blank + header + rows + help
	case "filter":
		return 3 // blank + help + empty
	default:
		if m.showHelp {
			return 2 + helpOverlayHeight() + 2 // blank + header + body + close hint + blank
		}
		return 2 // blank + 1 help row
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
		if m.trackStep == trackStepCleanupConfirm {
			b.WriteString(headerStyle().Render(fmt.Sprintf("  Clean up %d done items? (y/n)", m.trackCleanupCount)))
			b.WriteString("\n")
			b.WriteString(helpStyle().Render("y: confirm \u2022 n/esc: cancel"))
			b.WriteString("\n")
		} else if m.trackStep == trackStepPreview {
			// Preview step: help bar only (preview renders in main content area).
			action := "track"
			if m.mode == "untrack" {
				action = "untrack"
			}
			b.WriteString(helpStyle().Render(fmt.Sprintf("enter: %s all • esc: back", action)))
			b.WriteString("\n\n")
		} else if m.trackStatus != "" && !m.trackError {
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
				fmt.Fprintf(&b, "  %s %s: ", cursor, mutedStyle().Render(row.ownerRepo))
				b.WriteString(row.input.View())
				b.WriteString("\n")
			}
			help := "up/down: navigate \u2022 enter: submit \u2022 esc: cancel"
			if m.mode == "untrack" {
				help += " \u2022 c: clean up done"
			}
			b.WriteString(helpStyle().Render(help))
			b.WriteString("\n")
		}
	case "settings":
		if m.settingsStatus != "" {
			b.WriteString(broadcastStyle().Render(fmt.Sprintf("  %s", m.settingsStatus)))
		} else if m.settingsState != nil {
			switch m.settingsState.step {
			case settingsStepSelectField:
				b.WriteString(helpStyle().Render("up/down: select \u2022 enter: edit \u2022 esc: close"))
			case settingsStepEditValue:
				b.WriteString(helpStyle().Render("enter: save \u2022 esc: cancel"))
			case settingsStepEditTextarea:
				b.WriteString(helpStyle().Render("ctrl+d: save \u2022 esc: cancel"))
			}
		}
		b.WriteString("\n\n")
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
		if m.pollConfirm {
			b.WriteString(headerStyle().Render("  Run comprehensive analysis? (Requires LLM tokens)"))
			b.WriteString("\n")
			b.WriteString(helpStyle().Render("y: analyze • n: cancel"))
			b.WriteString("\n")
		} else if m.autopilotMode == "scan-confirm" {
			b.WriteString(headerStyle().Render("  Analyze tracked items for autopilot? (Requires LLM tokens)"))
			b.WriteString("\n")
			b.WriteString(helpStyle().Render("y: analyze • n: cancel"))
			b.WriteString("\n")
		} else if m.autopilotMode == "confirm" {
			b.WriteString(headerStyle().Render(
				fmt.Sprintf("  %d issues found, %d unblocked. Launch %d agents? (y/n)",
					m.autopilotTotal, m.autopilotUnblocked, m.project.AutopilotMaxAgents)))
			b.WriteString("\n")
			b.WriteString(helpStyle().Render("y: launch • n: cancel"))
			b.WriteString("\n")
		} else if m.autopilotMode == "stop-confirm" {
			b.WriteString(concernDangerStyle().Render("  Stop all running agents? (y/n)"))
			b.WriteString("\n")
			b.WriteString(helpStyle().Render("y: stop • n: cancel"))
			b.WriteString("\n")
		} else if m.autopilotStatus != "" {
			b.WriteString(broadcastStyle().Render(fmt.Sprintf("  %s", m.autopilotStatus)))
			b.WriteString("\n")
		}
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
		if m.showHelp {
			b.WriteString(renderHelpOverlay(m.width, m.activeTab))
		} else {
			b.WriteString(renderHelpBar(m.width, m.activeTab))
		}
		b.WriteString("\n")
	}

	return b.String()
}

// renderHelpBar builds a single-line condensed help bar with ? for full list.
func renderHelpBar(width, activeTab int) string {
	keyStyle := helpKeyStyle()
	descStyle := helpStyle()
	sep := descStyle.Render(" \u2022 ")

	type hint struct {
		key  string
		desc string
	}

	var condensed []hint
	if activeTab == tabAnalysis {
		condensed = []hint{
			{"1/2", "tabs"},
			{"R", "analyze"},
			{"b", "batch track"},
			{"u", "user msg"},
			{"m", "broadcast"},
			{"q", "quit"},
			{"?", "help"},
		}
	} else {
		condensed = []hint{
			{"1/2", "tabs"},
			{"p", "pause"},
			{"r", "poll"},
			{"i", "track"},
			{"b", "batch track"},
			{"q", "quit"},
			{"?", "help"},
		}
	}

	var row strings.Builder
	for _, h := range condensed {
		entry := keyStyle.Render(h.key) + descStyle.Render(": "+h.desc)
		if row.Len() > 0 {
			row.WriteString(sep)
		}
		row.WriteString(entry)
	}

	_ = width
	return row.String()
}

type helpHint struct {
	key  string
	desc string
}

// Help hint groups for the columnar overlay.
var (
	globalHints = []helpHint{
		{"1/2/tab", "switch tabs"},
		{"s", "settings"},
		{"t", "theme"},
		{"\u2191/\u2193", "scroll"},
		{"?", "close help"},
		{"q", "quit"},
	}

	opsHints = []helpHint{
		{"p", "pause/resume sync"},
		{"r", "status check"},
		{"i", "track issues"},
		{"I", "untrack issues"},
		{"b", "batch track"},
		{"x", "expand tracked"},
		{"w", "toggle worktrees"},
		{"a", "launch autopilot"},
		{"A", "stop autopilot"},
	}

	analysisHints = []helpHint{
		{"R", "run analysis"},
		{"e", "expand analysis"},
		{"c", "expand concerns"},
		{"u", "user message"},
		{"m", "broadcast"},
		{"o", "onboarding"},
	}
)

// helpOverlayHeight returns the number of lines the help body occupies.
func helpOverlayHeight() int {
	// 3 column headers + max of the three column lengths.
	maxRows := len(globalHints)
	if len(opsHints) > maxRows {
		maxRows = len(opsHints)
	}
	if len(analysisHints) > maxRows {
		maxRows = len(analysisHints)
	}
	return 1 + maxRows // 1 header row + hint rows
}

// renderHelpOverlay renders a three-column help overlay with the active tab highlighted.
func renderHelpOverlay(width, activeTab int) string {
	keyStyle := helpKeyStyle()
	descStyle := helpStyle()

	// Column widths.
	colW := 26
	if width > 0 && width/3 > colW {
		colW = width / 3
	}

	// Build column header labels — highlight the active tab's column.
	globalLabel := headerStyle().Render("Global")
	var opsLabel, analysisLabel string
	if activeTab == tabOperations {
		opsLabel = tabActiveStyle().Render(" Operations ")
		analysisLabel = mutedStyle().Render("Analysis")
	} else {
		opsLabel = mutedStyle().Render("Operations")
		analysisLabel = tabActiveStyle().Render(" Analysis ")
	}

	var b strings.Builder
	b.WriteString(headerStyle().Render("Keybindings"))
	b.WriteString("\n")

	// Column headers.
	fmt.Fprintf(&b, "  %-*s", colW, globalLabel)
	fmt.Fprintf(&b, "%-*s", colW, opsLabel)
	b.WriteString(analysisLabel)
	b.WriteString("\n")

	// Determine row count from longest column.
	maxRows := len(globalHints)
	if len(opsHints) > maxRows {
		maxRows = len(opsHints)
	}
	if len(analysisHints) > maxRows {
		maxRows = len(analysisHints)
	}

	dimStyle := lipgloss.NewStyle().Foreground(currentTheme().Border) // Surface2 — very faded

	fmtHint := func(hints []helpHint, row int, active bool) string {
		if row >= len(hints) {
			return fmt.Sprintf("%-*s", colW, "")
		}
		h := hints[row]
		var k, d string
		if active {
			k = keyStyle.Render(fmt.Sprintf("%7s", h.key))
			d = descStyle.Render(" " + h.desc)
		} else {
			k = dimStyle.Render(fmt.Sprintf("%7s", h.key))
			d = dimStyle.Render(" " + h.desc)
		}
		entry := k + d
		// Pad with spaces to fill column (approximate since ANSI codes add invisible chars).
		pad := colW - len(h.key) - 7 - len(h.desc)
		if pad < 1 {
			pad = 1
		}
		return entry + strings.Repeat(" ", pad)
	}

	for i := 0; i < maxRows; i++ {
		b.WriteString("  ")
		b.WriteString(fmtHint(globalHints, i, true))
		b.WriteString(fmtHint(opsHints, i, activeTab == tabOperations))
		b.WriteString(fmtHint(analysisHints, i, activeTab == tabAnalysis))
		b.WriteString("\n")
	}

	b.WriteString(mutedStyle().Render("  press ? to close"))
	b.WriteString("\n")

	return b.String()
}

// renderAutopilotSlots returns a display of autopilot agent slot status.
func (m Model) renderAutopilotSlots() string {
	if m.autopilotSupervisor == nil {
		return ""
	}

	slots := m.autopilotSupervisor.SlotStatus()
	var b strings.Builder
	b.WriteString(headerStyle().Render("Autopilot Agents"))
	b.WriteString("  ")
	b.WriteString(mutedStyle().Render("[A: stop]"))
	b.WriteString("\n")

	for _, slot := range slots {
		if slot.Status == "idle" {
			b.WriteString(mutedStyle().Render(fmt.Sprintf("  Slot %d: idle", slot.SlotNum)))
		} else {
			elapsed := slot.RunningFor.Round(time.Second)
			title := slot.IssueTitle
			if len(title) > 40 {
				title = title[:37] + "..."
			}
			b.WriteString(statusRunningStyle().Render(fmt.Sprintf("  Slot %d: #%d %s (%s)",
				slot.SlotNum, slot.IssueNumber, title, elapsed)))
		}
		b.WriteString("\n")
	}

	return b.String()
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
