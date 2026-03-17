package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/dustinlange/agent-minder/internal/autopilot"
	"github.com/dustinlange/agent-minder/internal/db"
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

	// Log viewer overlay: full-screen modal that replaces all tab content.
	if m.showLogViewer {
		b.WriteString(m.renderLogViewerOverlay())
		v := tea.NewView(b.String())
		v.AltScreen = true
		return v
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
	} else if m.activeTab == tabAnalysis {
		b.WriteString(m.renderAnalysisTab())
	} else {
		b.WriteString(m.renderAutopilotTab())
	}

	// Bottom bar: input or help.
	b.WriteString(m.renderBottomBar())

	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

// renderTabBar renders the tab selector bar.
func (m Model) renderTabBar() string {
	type tabDef struct {
		num    int
		label  string
		hasNew bool
	}
	tabs := []tabDef{
		{1, "Operations", false},
		{2, "Analysis", m.analysisHasNew},
		{3, "Autopilot", m.autopilotHasNew},
	}

	var b strings.Builder
	b.WriteString(" ")
	for i, t := range tabs {
		label := fmt.Sprintf(" %d: %s ", t.num, t.label)
		if t.hasNew {
			label = fmt.Sprintf(" %d: %s \u25cf ", t.num, t.label)
		}
		if m.activeTab == i {
			b.WriteString(tabActiveStyle().Render(label))
		} else {
			b.WriteString(tabInactiveStyle().Render(label))
		}
		if i < len(tabs)-1 {
			b.WriteString("  ")
		}
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

// renderAutopilotTab renders the Autopilot tab content.
func (m Model) renderAutopilotTab() string {
	var b strings.Builder

	// Empty state: no autopilot session.
	if m.autopilotMode == "" && m.autopilotSupervisor == nil {
		b.WriteString("\n")
		b.WriteString(headerStyle().Render("  Autopilot"))
		b.WriteString("\n\n")
		b.WriteString(mutedStyle().Render("  No autopilot session — press a to start"))
		b.WriteString("\n")
		return b.String()
	}

	// Settings preview: shown during scan-confirm.
	if m.autopilotMode == "scan-confirm" {
		b.WriteString("\n")
		b.WriteString(headerStyle().Render("  Autopilot — Settings Preview"))
		b.WriteString("\n\n")
		b.WriteString(textStyle().Render(fmt.Sprintf("  Max agents:     %d", m.project.AutopilotMaxAgents)))
		b.WriteString("\n")
		skipLabels := parseSkipLabels(m.project.AutopilotSkipLabel)
		var skipParts []string
		for _, l := range skipLabels {
			skipParts = append(skipParts, fmt.Sprintf("[%s]", l))
		}
		b.WriteString(textStyle().Render(fmt.Sprintf("  Skip label(s):  %s", strings.Join(skipParts, " "))))
		b.WriteString("\n")
		b.WriteString(textStyle().Render(fmt.Sprintf("  Max turns:      %d", m.project.AutopilotMaxTurns)))
		b.WriteString("\n")
		b.WriteString(textStyle().Render(fmt.Sprintf("  Budget/agent:   $%.2f", m.project.AutopilotMaxBudgetUSD)))
		b.WriteString("\n\n")
		b.WriteString(mutedStyle().Render("  Press s to change settings, G to add instructions for the dependency analyzer."))
		b.WriteString("\n")
		if m.autopilotDepGuidance != "" {
			b.WriteString(broadcastStyle().Render(fmt.Sprintf("  Guidance: %q", m.autopilotDepGuidance)))
			b.WriteString("\n")
		}
		return b.String()
	}

	// Dep-select state: carousel through dependency graph options.
	if m.autopilotMode == "dep-select" && len(m.autopilotDepOptions) > 0 {
		idx := m.autopilotDepSelection
		opt := m.autopilotDepOptions[idx]
		n := len(m.autopilotDepOptions)

		// Page indicator: ● ○ ○
		var dots strings.Builder
		for i := 0; i < n; i++ {
			if i == idx {
				dots.WriteString("\u25cf") // ●
			} else {
				dots.WriteString("\u25cb") // ○
			}
			if i < n-1 {
				dots.WriteString(" ")
			}
		}

		b.WriteString("\n")
		b.WriteString(headerStyle().Render(fmt.Sprintf("  Autopilot — %s", opt.Name)))
		b.WriteString("  ")
		b.WriteString(mutedStyle().Render(fmt.Sprintf("(%d/%d  %s)", idx+1, n, dots.String())))
		b.WriteString("\n\n")
		// Wrap rationale to terminal width minus indent.
		rationaleWidth := m.width - 4
		if rationaleWidth < 40 {
			rationaleWidth = 40
		}
		b.WriteString(mutedStyle().Width(rationaleWidth).Render(fmt.Sprintf("  %s", opt.Rationale)))
		b.WriteString("\n")
		b.WriteString(textStyle().Render(fmt.Sprintf("  %d unblocked of %d issues", opt.Unblocked, m.autopilotTotal)))
		b.WriteString("\n")

		depTree := m.renderDepGraphFromOption(opt)
		if depTree != "" {
			b.WriteString("\n")
			b.WriteString(depTree)
		}
		b.WriteString("\n")
		return b.String()
	}

	// Confirm state: issue scan completed, awaiting launch confirmation.
	if m.autopilotMode == "confirm" {
		b.WriteString("\n")
		b.WriteString(headerStyle().Render("  Autopilot — Ready to Launch"))
		b.WriteString("\n\n")
		b.WriteString(textStyle().Render(fmt.Sprintf("  %d issues found, %d unblocked",
			m.autopilotTotal, m.autopilotUnblocked)))
		b.WriteString("\n")
		b.WriteString(textStyle().Render(fmt.Sprintf("  Will launch up to %d agents", m.project.AutopilotMaxAgents)))
		b.WriteString("\n")
		// Show dep graph at confirm time so user can verify before launching.
		depGraph := m.renderDepGraph()
		if depGraph != "" {
			b.WriteString("\n")
			b.WriteString(depGraph)
		}
		b.WriteString("\n")
		b.WriteString(mutedStyle().Render("  Press G to rebuild dependencies with guidance, s to change settings."))
		b.WriteString("\n")
		return b.String()
	}

	// Stop-confirm state.
	if m.autopilotMode == "stop-confirm" {
		b.WriteString(m.renderAutopilotRunningContent())
		return b.String()
	}

	// Running state.
	if m.autopilotMode == "running" {
		b.WriteString(m.renderAutopilotRunningContent())
		return b.String()
	}

	// Completed state: session finished, tasks preserved for inspection.
	if m.autopilotMode == "completed" {
		b.WriteString(m.renderAutopilotCompletedContent())
		return b.String()
	}

	return b.String()
}

// renderAutopilotRunningContent renders slot status and task list for the running/stop-confirm states.
func (m Model) renderAutopilotRunningContent() string {
	var b strings.Builder

	// Slot section (compact, top of tab).
	if m.autopilotSupervisor != nil {
		slots := m.autopilotSupervisor.SlotStatus()
		slotHeader := "Slots"
		if m.autopilotPaused {
			slotHeader = "Slots  " + warningStyle().Render("PAUSED")
		}
		b.WriteString(headerStyle().Render(slotHeader))
		b.WriteString("\n")
		for _, slot := range slots {
			if slot.Status == "idle" {
				label := "idle"
				if m.autopilotPaused {
					label = "idle (paused)"
				}
				b.WriteString(mutedStyle().Render(fmt.Sprintf("  Slot %d: %s", slot.SlotNum, label)))
			} else {
				elapsed := slot.RunningFor.Round(time.Second)
				line := fmt.Sprintf("  Slot %d: #%d  %s  %d steps", slot.SlotNum, slot.IssueNumber, elapsed, slot.StepCount)
				b.WriteString(statusRunningStyle().Render(line))
				if slot.CurrentTool != "" {
					b.WriteString("\n")
					toolLine := fmt.Sprintf("         %s", slot.CurrentTool)
					if slot.ToolInput != "" {
						input := slot.ToolInput
						if len(input) > 60 {
							input = input[:57] + "..."
						}
						toolLine += ": " + input
					}
					b.WriteString(mutedStyle().Render(toolLine))
				}
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")

	// Task list header.
	expandHint := "e: expand"
	if m.autopilotTasksExpanded {
		expandHint = "e: collapse"
	}
	b.WriteString(headerStyle().Render("Tasks"))
	b.WriteString("  ")
	b.WriteString(mutedStyle().Render(fmt.Sprintf("[%s]", expandHint)))
	b.WriteString("\n")
	b.WriteString(m.autopilotTaskVP.View())
	b.WriteString("\n")

	// Task detail panel for selected task.
	detail := m.renderTaskDetail()
	if detail != "" {
		b.WriteString("\n")
		b.WriteString(detail)
	}

	// Dep graph section.
	depGraph := m.renderDepGraph()
	if depGraph != "" {
		b.WriteString("\n")
		b.WriteString(depGraph)
	}

	return b.String()
}

// renderAutopilotCompletedContent renders the task list for a completed autopilot session (no slots).
func (m Model) renderAutopilotCompletedContent() string {
	var b strings.Builder

	b.WriteString(headerStyle().Render("Autopilot — Completed"))
	b.WriteString("  ")
	expandHint := "e: expand"
	if m.autopilotTasksExpanded {
		expandHint = "e: collapse"
	}
	b.WriteString(mutedStyle().Render(fmt.Sprintf("[%s]", expandHint)))
	b.WriteString("\n")
	b.WriteString(m.autopilotTaskVP.View())
	b.WriteString("\n")

	// Task detail panel for selected task.
	detail := m.renderTaskDetail()
	if detail != "" {
		b.WriteString("\n")
		b.WriteString(detail)
	}

	// Dep graph section.
	depGraph := m.renderDepGraph()
	if depGraph != "" {
		b.WriteString("\n")
		b.WriteString(depGraph)
	}

	return b.String()
}

// rebuildAutopilotTaskContent refreshes the task list viewport from the database.
// Stores the sorted task list in m.autopilotTasks and pins cursor by issue number.
func (m *Model) rebuildAutopilotTaskContent() {
	tasks, err := m.store.GetAutopilotTasks(m.project.ID)
	if err != nil || len(tasks) == 0 {
		m.autopilotTasks = nil
		m.autopilotTaskVP.SetContent(mutedStyle().Render("  (no tasks)"))
		return
	}

	// Sort tasks by status priority: running → queued → blocked → manual → review → done → bailed → skipped.
	statusOrder := map[string]int{
		"running": 0,
		"queued":  1,
		"blocked": 2,
		"manual":  3,
		"review":  4,
		"done":    5,
		"bailed":  6,
		"stopped": 7,
		"skipped": 8,
	}
	sortedTasks := make([]db.AutopilotTask, len(tasks))
	copy(sortedTasks, tasks)
	for i := 0; i < len(sortedTasks); i++ {
		for j := i + 1; j < len(sortedTasks); j++ {
			oi := statusOrder[sortedTasks[i].Status]
			oj := statusOrder[sortedTasks[j].Status]
			if oi > oj {
				sortedTasks[i], sortedTasks[j] = sortedTasks[j], sortedTasks[i]
			}
		}
	}
	m.autopilotTasks = sortedTasks

	// Pin cursor by issue number across refreshes.
	if m.autopilotSelectedIssue > 0 {
		found := false
		for i, t := range m.autopilotTasks {
			if t.IssueNumber == m.autopilotSelectedIssue {
				m.autopilotCursor = i
				found = true
				break
			}
		}
		if !found {
			m.autopilotCursor = 0
			if len(m.autopilotTasks) > 0 {
				m.autopilotSelectedIssue = m.autopilotTasks[0].IssueNumber
			}
		}
	} else if len(m.autopilotTasks) > 0 {
		m.autopilotCursor = 0
		m.autopilotSelectedIssue = m.autopilotTasks[0].IssueNumber
	}

	// Clamp cursor.
	if m.autopilotCursor >= len(m.autopilotTasks) {
		m.autopilotCursor = len(m.autopilotTasks) - 1
	}

	var lines []string
	for i, t := range m.autopilotTasks {
		title := t.IssueTitle
		maxTitle := m.width - 34 // extra room for cursor indicator
		if maxTitle < 10 {
			maxTitle = 10
		}
		if len(title) > maxTitle {
			title = title[:maxTitle-3] + "..."
		}

		var extra string
		switch t.Status {
		case "blocked":
			extra = m.taskBlockedContext(t)
		case "review":
			if t.PRNumber > 0 {
				extra = fmt.Sprintf("PR #%d", t.PRNumber)
			}
		case "running":
			if t.StartedAt != "" {
				if started, err := time.Parse(time.RFC3339, t.StartedAt); err == nil {
					extra = time.Since(started).Round(time.Second).String()
				}
			}
		}

		cursor := "  "
		if i == m.autopilotCursor {
			cursor = mutedStyle().Render("▸ ")
		}

		statusStr := m.taskStatusDisplay(t.Status)
		issueRef := fmt.Sprintf("#%-5d", t.IssueNumber)
		if t.Owner != "" && t.Repo != "" {
			ghURL := fmt.Sprintf("https://github.com/%s/%s/issues/%d", t.Owner, t.Repo, t.IssueNumber)
			issueRef = fmt.Sprintf("\033]8;;%s\033\\%s\033]8;;\033\\", ghURL, issueRef)
		}

		// Style the title to match status for pre-lifecycle tasks.
		var styledTitle string
		switch t.Status {
		case "queued":
			styledTitle = textStyle().Render(title)
		case "manual":
			styledTitle = broadcastStyle().Render(title)
		case "skipped":
			styledTitle = errorStyle().Render(title)
		default:
			// Lifecycle statuses (running, done, review, etc.) keep default rendering.
			styledTitle = title
		}

		line := fmt.Sprintf("%s%s %s  %s", cursor, issueRef, statusStr, styledTitle)
		if extra != "" {
			line += "  " + mutedStyle().Render(extra)
		}
		lines = append(lines, line)
	}

	m.autopilotTaskVP.SetContentLines(lines)
}

// renderTaskDetail renders the detail panel for the selected autopilot task.
func (m Model) renderTaskDetail() string {
	task := m.selectedAutopilotTask()
	if task == nil {
		return ""
	}

	var b strings.Builder
	b.WriteString(headerStyle().Render("Detail"))
	b.WriteString("\n")

	// Issue number + full title (clickable link if owner/repo available).
	issueLabel := fmt.Sprintf("#%d", task.IssueNumber)
	if task.Owner != "" && task.Repo != "" {
		ghURL := fmt.Sprintf("https://github.com/%s/%s/issues/%d", task.Owner, task.Repo, task.IssueNumber)
		issueLabel = fmt.Sprintf("\033]8;;%s\033\\#%d\033]8;;\033\\", ghURL, task.IssueNumber)
	}
	detailLine := fmt.Sprintf("  %s  %s", issueLabel, task.IssueTitle)
	switch task.Status {
	case "manual":
		b.WriteString(broadcastStyle().Render(detailLine))
	case "skipped":
		b.WriteString(errorStyle().Render(detailLine))
	case "queued":
		b.WriteString(textStyle().Render(detailLine))
	default:
		b.WriteString(textStyle().Render(detailLine))
	}
	b.WriteString("\n")

	// Status.
	fmt.Fprintf(&b, "  Status: %s", m.taskStatusDisplay(task.Status))
	b.WriteString("\n")

	// Dependencies.
	deps := m.taskBlockedContext(*task)
	if deps != "" {
		b.WriteString(mutedStyle().Render(fmt.Sprintf("  Deps: %s", deps)))
		b.WriteString("\n")
	}
	blocks := m.taskBlocksContext(*task)
	if blocks != "" {
		b.WriteString(mutedStyle().Render(fmt.Sprintf("  %s", blocks)))
		b.WriteString("\n")
	}

	// Worktree path.
	if task.WorktreePath != "" {
		b.WriteString(mutedStyle().Render(fmt.Sprintf("  Worktree: %s", task.WorktreePath)))
		b.WriteString("\n")
	}

	// Branch.
	if task.Branch != "" {
		b.WriteString(mutedStyle().Render(fmt.Sprintf("  Branch: %s", task.Branch)))
		b.WriteString("\n")
	}

	// PR number.
	if task.PRNumber > 0 {
		b.WriteString(broadcastStyle().Render(fmt.Sprintf("  PR #%d", task.PRNumber)))
		b.WriteString("\n")
	}

	// Agent log.
	if task.AgentLog != "" {
		b.WriteString(mutedStyle().Render(fmt.Sprintf("  Log: %s", task.AgentLog)))
		b.WriteString("\n")
	}

	// Runtime.
	if task.Status == "running" && task.StartedAt != "" {
		if started, err := time.Parse(time.RFC3339, task.StartedAt); err == nil {
			elapsed := time.Since(started).Round(time.Second)
			b.WriteString(statusRunningStyle().Render(fmt.Sprintf("  Running for %s", elapsed)))
			b.WriteString("\n")
		}
	}

	return b.String()
}

// taskStatusDisplay returns a styled status string for an autopilot task.
func (m Model) taskStatusDisplay(status string) string {
	switch status {
	case "running":
		return statusRunningStyle().Render("\u25cf running")
	case "queued":
		return textStyle().Render("\u25c9 queued ")
	case "blocked":
		return mutedStyle().Render("\u25cc blocked")
	case "review":
		return broadcastStyle().Render("\u25ce review ")
	case "done":
		return statusRunningStyle().Render("\u2713 done   ")
	case "bailed":
		return errorStyle().Render("\u2717 bailed ")
	case "stopped":
		return errorStyle().Render("\u2717 stopped")
	case "manual":
		return broadcastStyle().Render("\u2691 manual ")
	case "skipped":
		return errorStyle().Render("\u2298 skipped")
	default:
		return mutedStyle().Render(fmt.Sprintf("  %-7s", status))
	}
}

// taskBlockedContext returns a description of what blocks a task.
func (m Model) taskBlockedContext(t db.AutopilotTask) string {
	if t.Dependencies == "" || t.Dependencies == "[]" {
		return ""
	}
	var deps []int
	// Parse JSON array of ints.
	depStr := strings.Trim(t.Dependencies, "[]")
	if depStr == "" {
		return ""
	}
	for _, s := range strings.Split(depStr, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			var n int
			if _, err := fmt.Sscanf(s, "%d", &n); err == nil {
				deps = append(deps, n)
			}
		}
	}
	if len(deps) == 0 {
		return ""
	}
	if len(deps) == 1 {
		return fmt.Sprintf("waiting on #%d", deps[0])
	}
	parts := make([]string, len(deps))
	for i, d := range deps {
		parts[i] = fmt.Sprintf("#%d", d)
	}
	return fmt.Sprintf("waiting on %s", strings.Join(parts, ", "))
}

// taskBlocksContext returns a description of what tasks this one blocks (reverse deps).
func (m Model) taskBlocksContext(t db.AutopilotTask) string {
	var blockedBy []int
	for _, other := range m.autopilotTasks {
		if other.IssueNumber == t.IssueNumber {
			continue
		}
		if other.Dependencies == "" || other.Dependencies == "[]" {
			continue
		}
		depStr := strings.Trim(other.Dependencies, "[]")
		for _, s := range strings.Split(depStr, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				var n int
				if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n == t.IssueNumber {
					blockedBy = append(blockedBy, other.IssueNumber)
					break
				}
			}
		}
	}
	if len(blockedBy) == 0 {
		return ""
	}
	parts := make([]string, len(blockedBy))
	for i, n := range blockedBy {
		parts[i] = fmt.Sprintf("#%d", n)
	}
	return fmt.Sprintf("blocks %s", strings.Join(parts, ", "))
}

// renderDepGraph renders a dependency tree view of all tasks.
func (m Model) renderDepGraph() string {
	if len(m.autopilotTasks) == 0 {
		return ""
	}

	// Parse all deps and check if any exist.
	type taskDep struct {
		issueNumber int
		title       string
		status      string
		deps        []int
	}

	tasks := make([]taskDep, 0, len(m.autopilotTasks))
	hasDeps := false
	taskMap := make(map[int]*taskDep)

	for _, t := range m.autopilotTasks {
		depStr := strings.Trim(t.Dependencies, "[]")
		var deps []int
		if depStr != "" {
			for _, s := range strings.Split(depStr, ",") {
				s = strings.TrimSpace(s)
				if s != "" {
					var n int
					if _, err := fmt.Sscanf(s, "%d", &n); err == nil {
						deps = append(deps, n)
						hasDeps = true
					}
				}
			}
		}
		td := taskDep{
			issueNumber: t.IssueNumber,
			title:       t.IssueTitle,
			status:      t.Status,
			deps:        deps,
		}
		tasks = append(tasks, td)
		taskMap[t.IssueNumber] = &tasks[len(tasks)-1]
	}

	// Helper to render a status icon for a task.
	statusIconFor := func(status string) string {
		switch status {
		case "done":
			return "\u2713" // ✓
		case "bailed", "stopped":
			return "\u2717" // ✗
		case "running":
			return "\u25cf" // ●
		case "blocked":
			return "\u25cc" // ◌
		case "review":
			return "\u25ce" // ◎
		case "skipped":
			return "\u2298" // ⊘
		case "manual":
			return "\u2691" // ⚑
		default:
			return "\u25cb" // ○
		}
	}

	// Helper to render a styled line by status.
	renderStyledLine := func(b *strings.Builder, line, status string) {
		switch status {
		case "done", "running":
			b.WriteString(statusRunningStyle().Render(line))
		case "bailed", "stopped", "skipped":
			b.WriteString(errorStyle().Render(line))
		case "manual":
			b.WriteString(broadcastStyle().Render(line))
		default:
			b.WriteString(mutedStyle().Render(line))
		}
		b.WriteString("\n")
	}

	var b strings.Builder

	// No dependencies: show flat task list so users can still validate what will run.
	if !hasDeps {
		b.WriteString(headerStyle().Render("Tasks"))
		b.WriteString("  ")
		b.WriteString(mutedStyle().Render("(no dependencies)"))
		b.WriteString("\n")
		for _, t := range tasks {
			icon := statusIconFor(t.status)
			title := t.title
			if len(title) > 50 {
				title = title[:47] + "..."
			}
			var suffix string
			switch t.status {
			case "skipped":
				suffix = " [SKIPPED]"
			case "manual":
				suffix = " [MANUAL]"
			}
			line := fmt.Sprintf("  %s #%d  %s%s", icon, t.issueNumber, title, suffix)
			renderStyledLine(&b, line, t.status)
		}
		return b.String()
	}

	// Build children map (parent → children that depend on it).
	children := make(map[int][]int)
	hasParent := make(map[int]bool)
	for _, t := range tasks {
		for _, dep := range t.deps {
			children[dep] = append(children[dep], t.issueNumber)
			hasParent[t.issueNumber] = true
		}
	}

	// Root nodes: tasks with no dependencies.
	var roots []int
	for _, t := range tasks {
		if !hasParent[t.issueNumber] {
			roots = append(roots, t.issueNumber)
		}
	}

	b.WriteString(headerStyle().Render("Dependencies"))
	b.WriteString("\n")

	// Render tree recursively.
	visited := make(map[int]bool)
	var renderNode func(issue int, prefix string, isLast bool)
	renderNode = func(issue int, prefix string, isLast bool) {
		if visited[issue] {
			return
		}
		visited[issue] = true

		connector := "\u251c\u2500 " // ├─
		if isLast {
			connector = "\u2514\u2500 " // └─
		}

		td := taskMap[issue]
		status := ""
		if td != nil {
			status = td.status
		}
		icon := statusIconFor(status)

		title := ""
		if td != nil {
			title = td.title
			maxTitle := 50
			if len(title) > maxTitle {
				title = title[:maxTitle-3] + "..."
			}
		}

		suffix := ""
		if td != nil && td.status == "skipped" {
			suffix = " [SKIPPED]"
		} else if td != nil && td.status == "manual" {
			suffix = " [MANUAL]"
		}
		line := fmt.Sprintf("  %s%s%s #%d  %s%s", prefix, connector, icon, issue, title, suffix)
		renderStyledLine(&b, line, status)

		childPrefix := prefix
		if isLast {
			childPrefix += "   "
		} else {
			childPrefix += "\u2502  " // │
		}

		kids := children[issue]
		for i, kid := range kids {
			renderNode(kid, childPrefix, i == len(kids)-1)
		}
	}

	for i, root := range roots {
		renderNode(root, "", i == len(roots)-1)
	}

	return b.String()
}

// renderDepGraphFromOption renders a dependency tree preview from a DepOption's graph data.
func (m Model) renderDepGraphFromOption(opt autopilot.DepOption) string {
	if len(opt.Graph) == 0 {
		return ""
	}

	type taskDep struct {
		issueNumber int
		title       string
		status      string
		deps        []int
	}

	// Build task data from autopilotTasks (for titles) + option graph (for deps).
	taskTitles := make(map[int]string)
	for _, t := range m.autopilotTasks {
		taskTitles[t.IssueNumber] = t.IssueTitle
	}

	tasks := make([]taskDep, 0, len(opt.Graph))
	hasDeps := false
	taskMap := make(map[int]*taskDep)

	for key, rawDeps := range opt.Graph {
		var issueNum int
		if _, err := fmt.Sscanf(key, "%d", &issueNum); err != nil {
			continue
		}

		// Check for string directives: "skip" or "manual".
		var strVal string
		if json.Unmarshal(rawDeps, &strVal) == nil {
			status := "skipped"
			if strVal == "manual" {
				status = "manual"
			}
			td := taskDep{issueNumber: issueNum, title: taskTitles[issueNum], status: status}
			tasks = append(tasks, td)
			taskMap[issueNum] = &tasks[len(tasks)-1]
			continue
		}

		var deps []int
		if err := json.Unmarshal(rawDeps, &deps); err != nil {
			var strDeps []string
			if json.Unmarshal(rawDeps, &strDeps) == nil {
				for _, sd := range strDeps {
					var n int
					if _, err2 := fmt.Sscanf(sd, "%d", &n); err2 == nil {
						deps = append(deps, n)
					}
				}
			}
		}
		if len(deps) > 0 {
			hasDeps = true
		}

		td := taskDep{issueNumber: issueNum, title: taskTitles[issueNum], status: "queued", deps: deps}
		tasks = append(tasks, td)
		taskMap[issueNum] = &tasks[len(tasks)-1]
	}

	// Sort tasks by issue number for stable rendering.
	for i := 0; i < len(tasks); i++ {
		for j := i + 1; j < len(tasks); j++ {
			if tasks[i].issueNumber > tasks[j].issueNumber {
				tasks[i], tasks[j] = tasks[j], tasks[i]
			}
		}
	}
	// Rebuild taskMap pointers after sort.
	for i := range tasks {
		taskMap[tasks[i].issueNumber] = &tasks[i]
	}

	statusIconFor := func(status string) string {
		switch status {
		case "skipped":
			return "\u2298" // ⊘
		case "manual":
			return "\u2691" // ⚑
		case "blocked":
			return "\u25cc" // ◌
		case "queued":
			return "\u25c9" // ◉
		default:
			return "\u25cb" // ○
		}
	}

	// renderStyledLine applies status-appropriate styling to a line in the dep graph preview.
	renderStyledLine := func(b *strings.Builder, line, status string) {
		switch status {
		case "queued":
			b.WriteString(textStyle().Render(line))
		case "manual":
			b.WriteString(broadcastStyle().Render(line))
		case "skipped":
			b.WriteString(errorStyle().Render(line))
		default:
			b.WriteString(mutedStyle().Render(line))
		}
		b.WriteString("\n")
	}

	var b strings.Builder

	if !hasDeps {
		b.WriteString(headerStyle().Render("  Tasks"))
		b.WriteString("  ")
		b.WriteString(mutedStyle().Render("(no dependencies)"))
		b.WriteString("\n")
		for _, t := range tasks {
			icon := statusIconFor(t.status)
			title := t.title
			if len(title) > 50 {
				title = title[:47] + "..."
			}
			var suffix string
			switch t.status {
			case "skipped":
				suffix = " [SKIPPED]"
			case "manual":
				suffix = " [MANUAL]"
			}
			line := fmt.Sprintf("    %s #%d  %s%s", icon, t.issueNumber, title, suffix)
			renderStyledLine(&b, line, t.status)
		}
		return b.String()
	}

	// Build children map.
	children := make(map[int][]int)
	hasParent := make(map[int]bool)
	for _, t := range tasks {
		for _, dep := range t.deps {
			children[dep] = append(children[dep], t.issueNumber)
			hasParent[t.issueNumber] = true
		}
	}

	var roots []int
	for _, t := range tasks {
		if !hasParent[t.issueNumber] {
			roots = append(roots, t.issueNumber)
		}
	}

	b.WriteString(headerStyle().Render("  Dependencies"))
	b.WriteString("\n")

	visited := make(map[int]bool)
	var renderNode func(issue int, prefix string, isLast bool)
	renderNode = func(issue int, prefix string, isLast bool) {
		if visited[issue] {
			return
		}
		visited[issue] = true

		connector := "\u251c\u2500 " // ├─
		if isLast {
			connector = "\u2514\u2500 " // └─
		}

		td := taskMap[issue]
		status := "queued"
		if td != nil {
			status = td.status
		}
		icon := statusIconFor(status)

		title := ""
		if td != nil {
			title = td.title
			if len(title) > 50 {
				title = title[:47] + "..."
			}
		}

		suffix := ""
		if td != nil && td.status == "skipped" {
			suffix = " [SKIPPED]"
		} else if td != nil && td.status == "manual" {
			suffix = " [MANUAL]"
		}
		line := fmt.Sprintf("    %s%s%s #%d  %s%s", prefix, connector, icon, issue, title, suffix)
		renderStyledLine(&b, line, status)

		childPrefix := prefix
		if isLast {
			childPrefix += "   "
		} else {
			childPrefix += "\u2502  " // │
		}

		kids := children[issue]
		for i, kid := range kids {
			renderNode(kid, childPrefix, i == len(kids)-1)
		}
	}

	for i, root := range roots {
		renderNode(root, "", i == len(roots)-1)
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

	if m.autopilotMode == "running" {
		b.WriteString("  ")
		b.WriteString(statusRunningStyle().Render("\u2708 AUTOPILOT"))
	}

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

// resizeViewports computes the height budget and resizes all viewports.
func (m *Model) resizeViewports() {
	if m.width == 0 || m.height == 0 {
		return
	}

	analysisH, eventLogH, autopilotTaskH := m.computeHeightBudget()

	m.analysisVP.SetWidth(m.width)
	m.analysisVP.SetHeight(analysisH)
	m.eventLogVP.SetWidth(m.width)
	m.eventLogVP.SetHeight(eventLogH)
	m.autopilotTaskVP.SetWidth(m.width)
	m.autopilotTaskVP.SetHeight(autopilotTaskH)
}

// computeHeightBudget calculates the height for analysis, event log, and autopilot task viewports.
// Only the active tab's viewport gets meaningful space; others get a minimum.
func (m Model) computeHeightBudget() (analysisH, eventLogH, autopilotTaskH int) {
	fixed := 2 // header (title + goal)
	fixed += 1 // blank line after header
	fixed += 1 // tab bar
	fixed += 1 // blank line after tab bar
	fixed += m.bottomBarHeight()

	switch m.activeTab {
	case tabOperations:
		// Operations tab: tracked + worktrees + event log header + VP
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

		fixed += 1 // event log header
		fixed += 1 // trailing newline

		remaining := m.height - fixed
		if remaining < 4 {
			remaining = 4
		}
		eventLogH = remaining
		analysisH = 2
		autopilotTaskH = 2

	case tabAnalysis:
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
		eventLogH = 2
		autopilotTaskH = 2

	case tabAutopilot:
		// Autopilot tab: slot section + task list header + VP + detail panel
		isRunning := m.autopilotMode == "running" || m.autopilotMode == "stop-confirm" ||
			m.autopilotMode == "stop-task-confirm" || m.autopilotMode == "restart-confirm" ||
			m.autopilotMode == "review-confirm" || m.autopilotMode == "completed"
		if isRunning {
			// Slot section.
			if m.autopilotSupervisor != nil {
				slots := m.autopilotSupervisor.SlotStatus()
				fixed += 1 // "Slots" header
				fixed += len(slots)
				fixed += 1 // blank line between slots and tasks
			}
			fixed += 1 // "Tasks" header
			fixed += 1 // trailing newline

			// Detail panel: ~8 lines (header + fields).
			detailLines := 8
			fixed += 1 // blank line before detail
			fixed += detailLines

			// Dep graph section: header + one line per task + blank line before.
			if len(m.autopilotTasks) > 0 {
				fixed += 1 // blank line before dep graph
				fixed += 1 // "Dependencies" / "Tasks" header
				fixed += len(m.autopilotTasks)
			}

			remaining := m.height - fixed
			if remaining < 5 {
				remaining = 5
			}

			if m.autopilotTasksExpanded {
				autopilotTaskH = remaining
			} else {
				autopilotTaskH = 5
				if autopilotTaskH > remaining {
					autopilotTaskH = remaining
				}
			}
		} else {
			// Non-running states use static content, no viewport needed.
			autopilotTaskH = 2
		}
		analysisH = 2
		eventLogH = 2
	}

	if analysisH < 2 {
		analysisH = 2
	}
	if eventLogH < 2 {
		eventLogH = 2
	}
	if autopilotTaskH < 2 {
		autopilotTaskH = 2
	}

	return analysisH, eventLogH, autopilotTaskH
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
		// Autopilot action modes have an extra padding line above the controls.
		if m.activeTab == tabAutopilot {
			switch m.autopilotMode {
			case "scan-confirm", "dep-select", "confirm":
				if (m.autopilotMode == "scan-confirm" || m.autopilotMode == "dep-select") && m.polling {
					return 2 // blank + spinner row
				}
				return 3 // blank + padding + help row
			}
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
	case "dep-guidance":
		b.WriteString(headerStyle().Render("  Dependency analyzer guidance:"))
		b.WriteString("\n")
		b.WriteString("  ")
		b.WriteString(m.rebuildDepsInput.View())
		b.WriteString("\n")
		b.WriteString(helpStyle().Render("ctrl+d: save guidance \u2022 esc: cancel"))
		b.WriteString("\n")
	case "rebuild-deps":
		if m.rebuildDepsStatus != "" && m.rebuildDepsStatus != "Rebuilding dependency graph..." {
			b.WriteString(broadcastStyle().Render(fmt.Sprintf("  %s", m.rebuildDepsStatus)))
		} else if m.rebuildDepsStatus == "Rebuilding dependency graph..." {
			b.WriteString("  ")
			b.WriteString(m.spinner.View())
			b.WriteString(" ")
			b.WriteString(broadcastStyle().Render("Rebuilding dependency graph..."))
		} else {
			b.WriteString(headerStyle().Render("  Rebuild dep graph \u2014 add optional guidance:"))
			b.WriteString("\n")
			b.WriteString("  ")
			b.WriteString(m.rebuildDepsInput.View())
		}
		b.WriteString("\n")
		if m.rebuildDepsStatus == "" {
			b.WriteString(helpStyle().Render("ctrl+d: rebuild \u2022 esc: cancel (leave empty to re-analyze with current state)"))
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
		} else if m.activeTab == tabAutopilot && m.autopilotMode == "scan-confirm" {
			if m.polling {
				b.WriteString("  ")
				b.WriteString(m.spinner.View())
				b.WriteString(" ")
				b.WriteString(mutedStyle().Render("Analyzing tracked items..."))
				b.WriteString("\n")
			} else {
				b.WriteString("\n")
				b.WriteString(helpKeyStyle().Render("enter"))
				b.WriteString(helpStyle().Render(": analyze • "))
				b.WriteString(helpKeyStyle().Render("esc"))
				b.WriteString(helpStyle().Render(": cancel"))
				b.WriteString("\n")
			}
		} else if m.activeTab == tabAutopilot && m.autopilotMode == "dep-select" {
			if m.polling {
				b.WriteString("  ")
				b.WriteString(m.spinner.View())
				b.WriteString(" ")
				b.WriteString(mutedStyle().Render("Analyzing tracked items..."))
				b.WriteString("\n")
			} else {
				if m.autopilotStatus != "" {
					b.WriteString(mutedStyle().Render(fmt.Sprintf("  %s", m.autopilotStatus)))
					b.WriteString("\n")
				} else {
					b.WriteString("\n")
				}
				b.WriteString(helpKeyStyle().Render("\u2190/\u2192"))
				b.WriteString(helpStyle().Render(": flip • "))
				b.WriteString(helpKeyStyle().Render("enter"))
				b.WriteString(helpStyle().Render(": select • "))
				b.WriteString(helpKeyStyle().Render("G"))
				b.WriteString(helpStyle().Render(": regenerate with comments • "))
				b.WriteString(helpKeyStyle().Render("esc"))
				b.WriteString(helpStyle().Render(": cancel"))
				b.WriteString("\n")
			}
		} else if m.activeTab == tabAutopilot && m.autopilotMode == "confirm" {
			b.WriteString("\n")
			b.WriteString(helpKeyStyle().Render("enter"))
			b.WriteString(helpStyle().Render(": launch • "))
			b.WriteString(helpKeyStyle().Render("esc"))
			b.WriteString(helpStyle().Render(": cancel"))
			b.WriteString("\n")
		} else if m.activeTab == tabAutopilot && m.autopilotMode == "stop-confirm" {
			b.WriteString(warningStyle().Render("  ⚠ Stop all running agents? "))
			b.WriteString(helpKeyStyle().Render("enter"))
			b.WriteString(helpStyle().Render(": stop • "))
			b.WriteString(helpKeyStyle().Render("esc"))
			b.WriteString(helpStyle().Render(": cancel"))
			b.WriteString("\n")
		} else if m.activeTab == tabAutopilot && m.autopilotMode == "stop-task-confirm" {
			task := m.selectedAutopilotTask()
			issueNum := 0
			if task != nil {
				issueNum = task.IssueNumber
			}
			b.WriteString(warningStyle().Render(fmt.Sprintf("  ⚠ Stop agent on #%d? ", issueNum)))
			b.WriteString(helpKeyStyle().Render("y"))
			b.WriteString(helpStyle().Render(": stop • "))
			b.WriteString(helpKeyStyle().Render("n"))
			b.WriteString(helpStyle().Render(": cancel"))
			b.WriteString("\n")
		} else if m.activeTab == tabAutopilot && m.autopilotMode == "restart-confirm" {
			task := m.selectedAutopilotTask()
			issueNum := 0
			if task != nil {
				issueNum = task.IssueNumber
			}
			b.WriteString(headerStyle().Render(fmt.Sprintf("  Restart agent on #%d? ", issueNum)))
			b.WriteString(helpKeyStyle().Render("y"))
			b.WriteString(helpStyle().Render(": restart • "))
			b.WriteString(helpKeyStyle().Render("n"))
			b.WriteString(helpStyle().Render(": cancel"))
			b.WriteString("\n")
		} else if m.activeTab == tabAutopilot && m.autopilotMode == "review-confirm" {
			task := m.selectedAutopilotTask()
			issueNum := 0
			if task != nil {
				issueNum = task.IssueNumber
			}
			b.WriteString(headerStyle().Render(fmt.Sprintf("  Launch review session for #%d? ", issueNum)))
			b.WriteString(helpKeyStyle().Render("enter"))
			b.WriteString(helpStyle().Render(": launch • "))
			b.WriteString(helpKeyStyle().Render("esc"))
			b.WriteString(helpStyle().Render(": cancel"))
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
			b.WriteString(m.renderHelpBar())
		}
		b.WriteString("\n")
	}

	return b.String()
}

// renderHelpBar builds a single-line condensed help bar with ? for full list.
func (m Model) renderHelpBar() string {
	width := m.width
	activeTab := m.activeTab
	keyStyle := helpKeyStyle()
	descStyle := helpStyle()
	sep := descStyle.Render(" \u2022 ")

	type hint struct {
		key  string
		desc string
	}

	var condensed []hint
	switch activeTab {
	case tabAnalysis:
		condensed = []hint{
			{"1/2/3", "tabs"},
			{"R", "analyze"},
			{"b", "batch track"},
			{"u", "user msg"},
			{"m", "broadcast"},
			{"q", "quit"},
			{"?", "help"},
		}
	case tabAutopilot:
		switch m.autopilotMode {
		case "running":
			condensed = []hint{
				{"↑/↓", "select"},
			}
			// Context-sensitive hints based on selected task.
			task := m.selectedAutopilotTask()
			if task != nil {
				switch task.Status {
				case "running":
					condensed = append(condensed,
						hint{"s", "stop"},
						hint{"l", "log"},
						hint{"c", "copy path"},
					)
				case "bailed", "stopped":
					condensed = append(condensed,
						hint{"r", "restart"},
						hint{"l", "log"},
						hint{"c", "copy path"},
					)
				case "queued", "blocked":
					condensed = append(condensed, hint{"D", "deps"})
				case "review":
					condensed = append(condensed,
						hint{"r", "review"},
						hint{"l", "log"},
						hint{"c", "copy path"},
					)
				case "done":
					condensed = append(condensed,
						hint{"l", "log"},
						hint{"c", "copy path"},
					)
				}
			}
			condensed = append(condensed,
				hint{"P", "pause"},
				hint{"A", "stop all"},
				hint{"?", "help"},
			)
		case "completed":
			condensed = []hint{
				{"↑/↓", "select"},
			}
			task := m.selectedAutopilotTask()
			if task != nil {
				switch task.Status {
				case "bailed", "stopped", "done":
					condensed = append(condensed,
						hint{"l", "log"},
						hint{"c", "copy path"},
					)
				case "review":
					condensed = append(condensed,
						hint{"r", "review"},
						hint{"l", "log"},
						hint{"c", "copy path"},
					)
				case "queued", "blocked":
					condensed = append(condensed, hint{"D", "deps"})
				}
			}
			condensed = append(condensed,
				hint{"a", "new session"},
				hint{"?", "help"},
			)
		default:
			condensed = []hint{
				{"1/2/3", "tabs"},
				{"a", "start"},
				{"A", "stop"},
				{"e", "expand"},
				{"q", "quit"},
				{"?", "help"},
			}
		}
	default:
		condensed = []hint{
			{"1/2/3", "tabs"},
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
		{"1/2/3", "switch tabs"},
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
	}

	analysisHints = []helpHint{
		{"R", "run analysis"},
		{"e", "expand analysis"},
		{"c", "expand concerns"},
		{"u", "user message"},
		{"m", "broadcast"},
		{"o", "onboarding"},
	}

	autopilotHints = []helpHint{
		{"a", "launch autopilot"},
		{"A", "stop all agents"},
		{"s", "stop selected"},
		{"r", "restart/review selected"},
		{"c", "copy worktree path"},
		{"P", "pause/resume slots"},
		{"D", "show dependencies"},
		{"G", "rebuild dep graph"},
		{"l", "view log"},
		{"e", "expand tasks"},
	}
)

// helpOverlayHeight returns the number of lines the help body occupies.
func helpOverlayHeight() int {
	maxRows := len(globalHints)
	for _, h := range [][]helpHint{opsHints, analysisHints, autopilotHints} {
		if len(h) > maxRows {
			maxRows = len(h)
		}
	}
	return 1 + maxRows // 1 header row + hint rows
}

// renderHelpOverlay renders a four-column help overlay with the active tab highlighted.
func renderHelpOverlay(width, activeTab int) string {
	keyStyle := helpKeyStyle()
	descStyle := helpStyle()

	// Column widths.
	colW := 22
	if width > 0 && width/4 > colW {
		colW = width / 4
	}

	// Build column header labels — highlight the active tab's column.
	globalLabel := headerStyle().Render("Global")
	var opsLabel, analysisLabel, autopilotLabel string
	switch activeTab {
	case tabOperations:
		opsLabel = tabActiveStyle().Render(" Operations ")
		analysisLabel = mutedStyle().Render("Analysis")
		autopilotLabel = mutedStyle().Render("Autopilot")
	case tabAnalysis:
		opsLabel = mutedStyle().Render("Operations")
		analysisLabel = tabActiveStyle().Render(" Analysis ")
		autopilotLabel = mutedStyle().Render("Autopilot")
	case tabAutopilot:
		opsLabel = mutedStyle().Render("Operations")
		analysisLabel = mutedStyle().Render("Analysis")
		autopilotLabel = tabActiveStyle().Render(" Autopilot ")
	}

	var b strings.Builder
	b.WriteString(headerStyle().Render("Keybindings"))
	b.WriteString("\n")

	// Column headers.
	fmt.Fprintf(&b, "  %-*s", colW, globalLabel)
	fmt.Fprintf(&b, "%-*s", colW, opsLabel)
	fmt.Fprintf(&b, "%-*s", colW, analysisLabel)
	b.WriteString(autopilotLabel)
	b.WriteString("\n")

	// Determine row count from longest column.
	maxRows := len(globalHints)
	for _, h := range [][]helpHint{opsHints, analysisHints, autopilotHints} {
		if len(h) > maxRows {
			maxRows = len(h)
		}
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
		b.WriteString(fmtHint(autopilotHints, i, activeTab == tabAutopilot))
		b.WriteString("\n")
	}

	b.WriteString(mutedStyle().Render("  press ? to close"))
	b.WriteString("\n")

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
