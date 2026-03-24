// Package remotetui provides a bubbletea TUI that connects to a remote
// deploy daemon's HTTP API, giving a k9s-like live dashboard for monitoring
// deploy watch operations.
package remotetui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/dustinlange/agent-minder/internal/api"
)

// Tab indices.
const (
	tabTasks    = 0
	tabDepGraph = 1
	tabAnalysis = 2
	tabLog      = 3
)

var tabNames = []string{"Tasks", "Dep Graph", "Analysis", "Log"}

// Polling intervals.
const (
	defaultTaskPollInterval     = 5 * time.Second
	defaultAnalysisPollInterval = 30 * time.Second
	logPollInterval             = 3 * time.Second
)

// Model is the root bubbletea model for the remote TUI.
type Model struct {
	client *api.Client
	remote string // display address

	// Polling config.
	taskPollInterval     time.Duration
	analysisPollInterval time.Duration

	// UI state.
	activeTab int
	width     int
	height    int
	cursor    int // task list cursor
	expanded  bool
	flash     string

	// Data fetched from API.
	status   *api.StatusResponse
	tasks    []api.TaskResponse
	depGraph *api.DepGraphResponse
	analyses []api.AnalysisResponse
	taskLog  string // log content for selected task

	// Log viewer state.
	logTaskID   int // issue number of task whose log is shown
	logOffset   int // scroll offset into log lines
	logLines    []string
	logAutoTail bool

	// Error tracking.
	lastErr error
}

// New creates a new remote TUI model.
func New(client *api.Client, remote string, taskPoll, analysisPoll time.Duration) Model {
	if taskPoll == 0 {
		taskPoll = defaultTaskPollInterval
	}
	if analysisPoll == 0 {
		analysisPoll = defaultAnalysisPollInterval
	}
	return Model{
		client:               client,
		remote:               remote,
		taskPollInterval:     taskPoll,
		analysisPollInterval: analysisPoll,
		logAutoTail:          true,
	}
}

// --- Tea messages ---

type taskTickMsg struct{}
type analysisTickMsg struct{}
type logTickMsg struct{}

type statusMsg struct {
	status *api.StatusResponse
	err    error
}

type tasksMsg struct {
	tasks []api.TaskResponse
	err   error
}

type depGraphMsg struct {
	graph *api.DepGraphResponse
	err   error
}

type analysisMsg struct {
	analyses []api.AnalysisResponse
	err      error
}

type taskLogMsg struct {
	log string
	err error
}

type triggerPollMsg struct{ err error }

// --- Init ---

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.fetchStatus(),
		m.fetchTasks(),
		m.fetchDepGraph(),
		m.fetchAnalysis(),
		m.taskTick(),
		m.analysisTick(),
	)
}

// --- Update ---

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case statusMsg:
		if msg.err != nil {
			m.lastErr = msg.err
		} else {
			m.status = msg.status
			m.lastErr = nil
		}

	case tasksMsg:
		if msg.err != nil {
			m.lastErr = msg.err
		} else {
			m.tasks = msg.tasks
			m.lastErr = nil
			if m.cursor >= len(m.tasks) {
				m.cursor = max(0, len(m.tasks)-1)
			}
		}

	case depGraphMsg:
		if msg.err == nil {
			m.depGraph = msg.graph
		}

	case analysisMsg:
		if msg.err == nil {
			m.analyses = msg.analyses
		}

	case taskLogMsg:
		if msg.err == nil {
			m.taskLog = msg.log
			m.logLines = strings.Split(msg.log, "\n")
			if m.logAutoTail && len(m.logLines) > 0 {
				visible := m.height - 8
				if visible < 1 {
					visible = 20
				}
				if len(m.logLines) > visible {
					m.logOffset = len(m.logLines) - visible
				}
			}
		}

	case triggerPollMsg:
		if msg.err != nil {
			m.flash = fmt.Sprintf("Poll trigger failed: %v", msg.err)
		} else {
			m.flash = "Analysis poll triggered"
		}

	case taskTickMsg:
		m.flash = ""
		return m, tea.Batch(m.fetchStatus(), m.fetchTasks(), m.taskTick())

	case analysisTickMsg:
		return m, tea.Batch(m.fetchAnalysis(), m.fetchDepGraph(), m.analysisTick())

	case logTickMsg:
		if m.activeTab == tabLog && m.logTaskID > 0 {
			return m, tea.Batch(m.fetchTaskLog(m.logTaskID), m.logTick())
		}
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit

	case "tab", "right":
		m.activeTab = (m.activeTab + 1) % len(tabNames)
		if m.activeTab == tabLog {
			return m.enterLogTab()
		}

	case "shift+tab", "left":
		m.activeTab = (m.activeTab - 1 + len(tabNames)) % len(tabNames)
		if m.activeTab == tabLog {
			return m.enterLogTab()
		}

	case "1":
		m.activeTab = tabTasks
	case "2":
		m.activeTab = tabDepGraph
	case "3":
		m.activeTab = tabAnalysis
	case "4":
		m.activeTab = tabLog
		return m.enterLogTab()

	// Task navigation.
	case "up", "k":
		if m.activeTab == tabTasks && m.cursor > 0 {
			m.cursor--
		} else if m.activeTab == tabLog {
			m.logAutoTail = false
			if m.logOffset > 0 {
				m.logOffset--
			}
		}
	case "down", "j":
		if m.activeTab == tabTasks && m.cursor < len(m.tasks)-1 {
			m.cursor++
		} else if m.activeTab == tabLog {
			m.logAutoTail = false
			maxOffset := len(m.logLines) - (m.height - 8)
			if maxOffset < 0 {
				maxOffset = 0
			}
			if m.logOffset < maxOffset {
				m.logOffset++
			}
		}
	case "G":
		if m.activeTab == tabLog {
			m.logAutoTail = true
		}

	case "e", "enter":
		if m.activeTab == tabTasks {
			m.expanded = !m.expanded
		}
		if m.activeTab == tabTasks && m.cursor < len(m.tasks) {
			// Pressing enter on a task in Tasks tab also loads its log.
			m.logTaskID = m.tasks[m.cursor].IssueNumber
			m.logOffset = 0
			m.logAutoTail = true
		}

	case "escape":
		m.expanded = false

	case "r":
		return m, tea.Batch(m.fetchStatus(), m.fetchTasks(), m.fetchDepGraph(), m.fetchAnalysis())

	case "p":
		return m, m.triggerPoll()

	case "s":
		if m.activeTab == tabTasks && m.cursor < len(m.tasks) {
			t := m.tasks[m.cursor]
			if t.Status == "running" {
				return m, m.stopTask(t.IssueNumber)
			}
		}

	case "l":
		// Jump to log for selected task.
		if m.activeTab == tabTasks && m.cursor < len(m.tasks) {
			m.logTaskID = m.tasks[m.cursor].IssueNumber
			m.logOffset = 0
			m.logAutoTail = true
			m.activeTab = tabLog
			return m.enterLogTab()
		}
	}

	return m, nil
}

func (m Model) enterLogTab() (Model, tea.Cmd) {
	if m.logTaskID == 0 && len(m.tasks) > 0 {
		for _, t := range m.tasks {
			if t.Status == "running" {
				m.logTaskID = t.IssueNumber
				break
			}
		}
		if m.logTaskID == 0 && m.cursor < len(m.tasks) {
			m.logTaskID = m.tasks[m.cursor].IssueNumber
		}
	}
	if m.logTaskID > 0 {
		return m, tea.Batch(m.fetchTaskLog(m.logTaskID), m.logTick())
	}
	return m, nil
}

// --- View ---

func (m Model) View() tea.View {
	var b strings.Builder

	m.renderHeader(&b)
	m.renderTabs(&b)
	b.WriteString("\n")

	switch m.activeTab {
	case tabTasks:
		m.renderTasksTab(&b)
	case tabDepGraph:
		m.renderDepGraphTab(&b)
	case tabAnalysis:
		m.renderAnalysisTab(&b)
	case tabLog:
		m.renderLogTab(&b)
	}

	m.renderFooter(&b)

	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

func (m Model) renderHeader(b *strings.Builder) {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#89b4fa")).Render("agent-minder")
	fmt.Fprintf(b, " %s", title)

	if m.status != nil {
		id := lipgloss.NewStyle().Foreground(lipgloss.Color("#94e2d5")).Render(m.status.DeployID)
		fmt.Fprintf(b, " · %s", id)
		if m.status.Alive {
			fmt.Fprintf(b, " %s", lipgloss.NewStyle().Foreground(lipgloss.Color("#a6e3a1")).Render("●"))
		} else {
			fmt.Fprintf(b, " %s", lipgloss.NewStyle().Foreground(lipgloss.Color("#f38ba8")).Render("●"))
		}
		uptime := formatDuration(time.Duration(m.status.UptimeSec) * time.Second)
		fmt.Fprintf(b, " %s", lipgloss.NewStyle().Foreground(lipgloss.Color("#9399b2")).Render(uptime))
	}

	remote := lipgloss.NewStyle().Foreground(lipgloss.Color("#9399b2")).Render(m.remote)
	fmt.Fprintf(b, "  %s", remote)

	if m.lastErr != nil {
		errStr := lipgloss.NewStyle().Foreground(lipgloss.Color("#f38ba8")).Render(fmt.Sprintf(" ✗ %v", m.lastErr))
		fmt.Fprintf(b, "  %s", errStr)
	}

	b.WriteString("\n")
}

func (m Model) renderTabs(b *strings.Builder) {
	b.WriteString(" ")
	for i, name := range tabNames {
		if i > 0 {
			b.WriteString(" ")
		}
		label := fmt.Sprintf(" %d:%s ", i+1, name)
		if i == m.activeTab {
			b.WriteString(lipgloss.NewStyle().Bold(true).
				Foreground(lipgloss.Color("#89b4fa")).
				Background(lipgloss.Color("#313244")).
				Render(label))
		} else {
			b.WriteString(lipgloss.NewStyle().
				Foreground(lipgloss.Color("#6c7086")).
				Render(label))
		}
	}
	b.WriteString("\n")
}

func (m Model) renderTasksTab(b *strings.Builder) {
	if len(m.tasks) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#9399b2")).Render(" No tasks.\n"))
		return
	}

	// Column header.
	hdr := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#bac2de"))
	maxTitle := m.titleWidth()
	fmt.Fprintf(b, "   %s  %-*s  %-10s  %-6s  %-8s  %s\n",
		hdr.Render("Issue"), maxTitle, hdr.Render("Title"),
		hdr.Render("Status"), hdr.Render("PR"), hdr.Render("Cost"), hdr.Render("Time"))

	for i, t := range m.tasks {
		cursor := "  "
		if i == m.cursor {
			cursor = lipgloss.NewStyle().Foreground(lipgloss.Color("#89b4fa")).Render("> ")
		}

		icon := statusIcon(t.Status)
		title := t.IssueTitle
		if len(title) > maxTitle {
			title = title[:maxTitle-3] + "..."
		}

		statusStr := colorStatus(t.Status)
		costStr := ""
		if t.CostUSD > 0 {
			costStr = fmt.Sprintf("$%.2f", t.CostUSD)
		}

		elapsed := formatElapsed(t.StartedAt, t.CompletedAt, t.Status == "running")
		prStr := ""
		if t.PRNumber > 0 {
			prStr = fmt.Sprintf("#%d", t.PRNumber)
		}

		// Dependencies.
		deps := parseDeps(t)
		depStr := ""
		if len(deps) > 0 {
			parts := make([]string, len(deps))
			for j, d := range deps {
				parts[j] = fmt.Sprintf("#%v", d)
			}
			depStr = " ← " + strings.Join(parts, ",")
		}

		fmt.Fprintf(b, "%s%s #%-5d %-*s  %-10s  %-6s  %-8s  %s%s\n",
			cursor, icon, t.IssueNumber, maxTitle, title,
			statusStr, prStr, costStr, elapsed, depStr)
	}

	// Expanded detail.
	if m.expanded && m.cursor < len(m.tasks) {
		t := m.tasks[m.cursor]
		sep := lipgloss.NewStyle().Foreground(lipgloss.Color("#585b70")).Render(strings.Repeat("─", min(m.width, 80)))
		b.WriteString("\n")
		b.WriteString(sep)
		b.WriteString("\n")

		muted := lipgloss.NewStyle().Foreground(lipgloss.Color("#9399b2"))
		fmt.Fprintf(b, " %s  #%d %s\n", muted.Render("Issue:"), t.IssueNumber, t.IssueTitle)
		fmt.Fprintf(b, " %s  %s\n", muted.Render("Status:"), colorStatus(t.Status))
		if t.PRNumber > 0 {
			fmt.Fprintf(b, " %s  #%d\n", muted.Render("PR:"), t.PRNumber)
		}
		if t.CostUSD > 0 {
			fmt.Fprintf(b, " %s  $%.2f\n", muted.Render("Cost:"), t.CostUSD)
		}
		if t.Branch != "" {
			fmt.Fprintf(b, " %s  %s\n", muted.Render("Branch:"), t.Branch)
		}
		if t.ReviewRisk != "" {
			fmt.Fprintf(b, " %s  %s\n", muted.Render("Risk:"), t.ReviewRisk)
		}
		if t.FailureReason != "" {
			errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f38ba8"))
			detail := t.FailureReason
			if t.FailureDetail != "" {
				detail += " — " + t.FailureDetail
			}
			fmt.Fprintf(b, " %s  %s\n", muted.Render("Failure:"), errStyle.Render(detail))
		}
	}

	// Summary line.
	b.WriteString("\n")
	counts := map[string]int{}
	var totalCost float64
	for _, t := range m.tasks {
		counts[t.Status]++
		totalCost += t.CostUSD
	}
	parts := fmt.Sprintf("$%.2f", totalCost)
	for _, s := range []string{"running", "queued", "review", "done", "bailed", "failed", "stopped", "blocked"} {
		if c := counts[s]; c > 0 {
			parts += fmt.Sprintf("  %d %s", c, s)
		}
	}
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#bac2de")).Render(" " + parts))
	b.WriteString("\n")
}

func (m Model) renderDepGraphTab(b *strings.Builder) {
	if m.depGraph == nil {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#9399b2")).Render(" No dependency graph available.\n"))
		return
	}

	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("#9399b2"))
	accent := lipgloss.NewStyle().Foreground(lipgloss.Color("#94e2d5"))

	fmt.Fprintf(b, " %s %s\n", muted.Render("Strategy:"), accent.Render(m.depGraph.Strategy))
	if m.depGraph.Confidence > 0 {
		fmt.Fprintf(b, " %s %.0f%%\n", muted.Render("Confidence:"), m.depGraph.Confidence*100)
	}
	if m.depGraph.Reasoning != "" {
		fmt.Fprintf(b, " %s %s\n", muted.Render("Reasoning:"), m.depGraph.Reasoning)
	}
	b.WriteString("\n")

	// Build task title lookup.
	titleMap := make(map[string]string)
	for _, t := range m.tasks {
		titleMap[fmt.Sprintf("%d", t.IssueNumber)] = t.IssueTitle
	}

	// Render each issue and its dependencies.
	for issue, depsRaw := range m.depGraph.Graph {
		title := titleMap[issue]
		if title == "" {
			title = "unknown"
		}
		if len(title) > 50 {
			title = title[:47] + "..."
		}

		issueStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#89b4fa"))
		fmt.Fprintf(b, " %s %s\n", issueStyle.Render(fmt.Sprintf("#%s", issue)), muted.Render(title))

		// Parse deps — could be []any or string.
		switch deps := depsRaw.(type) {
		case []any:
			if len(deps) == 0 {
				fmt.Fprintf(b, "   %s\n", muted.Render("(no dependencies)"))
			} else {
				for _, d := range deps {
					depNum := fmt.Sprintf("%v", d)
					depTitle := titleMap[depNum]
					if depTitle == "" {
						depTitle = ""
					} else if len(depTitle) > 40 {
						depTitle = " " + depTitle[:37] + "..."
					} else {
						depTitle = " " + depTitle
					}
					fmt.Fprintf(b, "   └─ %s%s\n",
						lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af")).Render("#"+depNum),
						muted.Render(depTitle))
				}
			}
		case string:
			fmt.Fprintf(b, "   %s\n", muted.Render(deps))
		default:
			raw, _ := json.Marshal(depsRaw)
			fmt.Fprintf(b, "   %s\n", muted.Render(string(raw)))
		}
	}
}

func (m Model) renderAnalysisTab(b *strings.Builder) {
	if len(m.analyses) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#9399b2")).Render(" No analysis results yet.\n"))
		return
	}

	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("#9399b2"))
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#bac2de"))
	accentStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#94e2d5"))

	for i, a := range m.analyses {
		if i > 0 {
			sep := lipgloss.NewStyle().Foreground(lipgloss.Color("#585b70")).Render(strings.Repeat("─", min(m.width, 80)))
			b.WriteString(sep)
			b.WriteString("\n")
		}

		timeStr := a.PolledAt
		if t, err := time.Parse(time.RFC3339, a.PolledAt); err == nil {
			timeStr = t.Format("15:04:05")
		} else if t, err := time.Parse("2006-01-02 15:04:05", a.PolledAt); err == nil {
			timeStr = t.Format("15:04:05")
		}

		meta := fmt.Sprintf("%s  commits:%d msgs:%d concerns:%d",
			timeStr, a.NewCommits, a.NewMessages, a.ConcernsRaised)
		if a.BusMessageSent {
			meta += " [bus]"
		}

		fmt.Fprintf(b, " %s\n", accentStyle.Render(meta))
		if a.Analysis != "" {
			// Word-wrap analysis to terminal width.
			wrapped := wordWrap(a.Analysis, m.width-4)
			for _, line := range strings.Split(wrapped, "\n") {
				fmt.Fprintf(b, "   %s\n", textStyle.Render(line))
			}
		} else {
			fmt.Fprintf(b, "   %s\n", muted.Render("(no analysis text)"))
		}
	}
}

func (m Model) renderLogTab(b *strings.Builder) {
	if m.logTaskID == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#9399b2")).Render(" Select a task and press 'l' to view its log.\n"))
		return
	}

	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("#9399b2"))
	fmt.Fprintf(b, " %s #%d", muted.Render("Log:"), m.logTaskID)
	if m.logAutoTail {
		fmt.Fprintf(b, "  %s", muted.Render("[auto-tail]"))
	}
	fmt.Fprintf(b, "  %s\n\n", muted.Render(fmt.Sprintf("(%d lines)", len(m.logLines))))

	if len(m.logLines) == 0 {
		b.WriteString(muted.Render(" (empty or no log file)\n"))
		return
	}

	visible := m.height - 10
	if visible < 5 {
		visible = 5
	}

	start := m.logOffset
	if start > len(m.logLines) {
		start = len(m.logLines)
	}
	end := start + visible
	if end > len(m.logLines) {
		end = len(m.logLines)
	}

	logStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#a6adc8"))
	for _, line := range m.logLines[start:end] {
		if len(line) > m.width-2 {
			line = line[:m.width-5] + "..."
		}
		fmt.Fprintf(b, " %s\n", logStyle.Render(line))
	}
}

func (m Model) renderFooter(b *strings.Builder) {
	if m.flash != "" {
		flashStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af"))
		fmt.Fprintf(b, "\n %s\n", flashStyle.Render(m.flash))
	}

	help := lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
	key := lipgloss.NewStyle().Foreground(lipgloss.Color("#94e2d5")).Bold(true)

	var parts []string
	parts = append(parts, key.Render("1-4")+help.Render(" tabs"))
	parts = append(parts, key.Render("↑↓")+help.Render(" nav"))

	switch m.activeTab {
	case tabTasks:
		parts = append(parts, key.Render("e")+help.Render(" expand"))
		parts = append(parts, key.Render("l")+help.Render(" log"))
		parts = append(parts, key.Render("s")+help.Render(" stop"))
	case tabLog:
		parts = append(parts, key.Render("G")+help.Render(" tail"))
	}

	parts = append(parts, key.Render("r")+help.Render(" refresh"))
	parts = append(parts, key.Render("p")+help.Render(" poll"))
	parts = append(parts, key.Render("q")+help.Render(" quit"))

	fmt.Fprintf(b, "\n %s\n", strings.Join(parts, "  "))
}

// --- API commands ---

func (m Model) fetchStatus() tea.Cmd {
	return func() tea.Msg {
		s, err := m.client.GetStatus()
		return statusMsg{status: s, err: err}
	}
}

func (m Model) fetchTasks() tea.Cmd {
	return func() tea.Msg {
		t, err := m.client.GetTasks()
		return tasksMsg{tasks: t, err: err}
	}
}

func (m Model) fetchDepGraph() tea.Cmd {
	return func() tea.Msg {
		g, err := m.client.GetDepGraph()
		return depGraphMsg{graph: g, err: err}
	}
}

func (m Model) fetchAnalysis() tea.Cmd {
	return func() tea.Msg {
		a, err := m.client.GetAnalysis(10)
		return analysisMsg{analyses: a, err: err}
	}
}

func (m Model) fetchTaskLog(issueNumber int) tea.Cmd {
	return func() tea.Msg {
		log, err := m.client.GetTaskLog(fmt.Sprintf("%d", issueNumber))
		return taskLogMsg{log: log, err: err}
	}
}

func (m Model) triggerPoll() tea.Cmd {
	return func() tea.Msg {
		err := m.client.TriggerPoll()
		return triggerPollMsg{err: err}
	}
}

func (m Model) stopTask(issueNumber int) tea.Cmd {
	return func() tea.Msg {
		err := m.client.StopTask(fmt.Sprintf("%d", issueNumber))
		if err != nil {
			return triggerPollMsg{err: fmt.Errorf("stop #%d: %w", issueNumber, err)}
		}
		return triggerPollMsg{err: nil}
	}
}

func (m Model) taskTick() tea.Cmd {
	return tea.Tick(m.taskPollInterval, func(time.Time) tea.Msg {
		return taskTickMsg{}
	})
}

func (m Model) analysisTick() tea.Cmd {
	return tea.Tick(m.analysisPollInterval, func(time.Time) tea.Msg {
		return analysisTickMsg{}
	})
}

func (m Model) logTick() tea.Cmd {
	return tea.Tick(logPollInterval, func(time.Time) tea.Msg {
		return logTickMsg{}
	})
}

// --- Helpers ---

func (m Model) titleWidth() int {
	w := m.width - 60
	if w < 20 {
		w = 20
	}
	if w > 60 {
		w = 60
	}
	return w
}

func statusIcon(status string) string {
	switch status {
	case "running":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#a6e3a1")).Render("●")
	case "queued", "pending":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#9399b2")).Render("○")
	case "done":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#a6e3a1")).Render("✓")
	case "review":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#cba6f7")).Render("◎")
	case "reviewing":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#89b4fa")).Render("◎")
	case "reviewed":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#94e2d5")).Render("◉")
	case "bailed", "failed":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#f38ba8")).Render("✗")
	case "stopped":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af")).Render("■")
	case "blocked":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#fab387")).Render("⊘")
	default:
		return " "
	}
}

func colorStatus(status string) string {
	switch status {
	case "running":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#a6e3a1")).Bold(true).Render(status)
	case "queued", "pending":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#9399b2")).Render(status)
	case "done":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#a6e3a1")).Render(status)
	case "review":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#cba6f7")).Render(status)
	case "reviewing":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#89b4fa")).Render(status)
	case "reviewed":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#94e2d5")).Render(status)
	case "bailed", "failed":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#f38ba8")).Render(status)
	case "stopped":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af")).Render(status)
	case "blocked":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#fab387")).Render(status)
	default:
		return status
	}
}

func formatElapsed(startedAt, completedAt string, live bool) string {
	if startedAt == "" {
		return ""
	}
	var start time.Time
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, startedAt); err == nil {
			start = t
			break
		}
	}
	if start.IsZero() {
		return ""
	}

	var end time.Time
	if completedAt != "" {
		for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05"} {
			if t, err := time.Parse(layout, completedAt); err == nil {
				end = t
				break
			}
		}
	}
	if end.IsZero() {
		if live {
			end = time.Now().UTC()
		} else {
			return ""
		}
	}

	return formatDuration(end.Sub(start))
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func parseDeps(t api.TaskResponse) []any {
	// TaskResponse doesn't directly expose Dependencies, but the raw JSON
	// from GetTasks includes it. We parse it from the available fields.
	// For now, return nil — deps are shown in the dep graph tab.
	return nil
}

func wordWrap(s string, width int) string {
	if width <= 0 {
		return s
	}
	var result strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if len(line) <= width {
			if result.Len() > 0 {
				result.WriteString("\n")
			}
			result.WriteString(line)
			continue
		}
		words := strings.Fields(line)
		lineLen := 0
		for _, word := range words {
			if lineLen > 0 && lineLen+1+len(word) > width {
				result.WriteString("\n")
				lineLen = 0
			}
			if lineLen > 0 {
				result.WriteString(" ")
				lineLen++
			}
			result.WriteString(word)
			lineLen += len(word)
		}
		result.WriteString("\n")
	}
	return result.String()
}
