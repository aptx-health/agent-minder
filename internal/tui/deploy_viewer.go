package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/dustinlange/agent-minder/internal/deploy"
	gitpkg "github.com/dustinlange/agent-minder/internal/git"
)

// DeployViewer is a TUI for monitoring a deploy's progress with restart support.
type DeployViewer struct {
	store    *db.Store
	project  *db.Project
	tasks    []db.AutopilotTask
	cursor   int
	width    int
	height   int
	expanded bool // show detail panel for selected task
	repoRef  string
	repoDir  string // enrolled repo path for worktree cleanup
	flash    string // transient status message
}

type deployTickMsg struct{}
type deployTasksMsg []db.AutopilotTask
type deployRestartMsg struct {
	issueNumber int
	err         error
}

// NewDeployViewer creates a deploy viewer model.
func NewDeployViewer(store *db.Store, project *db.Project) DeployViewer {
	// Resolve enrolled repo dir for worktree cleanup on restart.
	var repoDir string
	repos, _ := store.GetRepos(project.ID)
	if len(repos) > 0 {
		repoDir = repos[0].Path
	}
	return DeployViewer{
		store:   store,
		project: project,
		repoDir: repoDir,
		width:   80,
		height:  24,
	}
}

func (m DeployViewer) Init() tea.Cmd {
	return tea.Batch(m.refreshTasks(), m.tickCmd())
}

func (m DeployViewer) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.tasks)-1 {
				m.cursor++
			}
		case "e", "enter":
			m.expanded = !m.expanded
		case "escape":
			m.expanded = false
		case "c":
			if m.cursor < len(m.tasks) {
				t := m.tasks[m.cursor]
				if t.WorktreePath != "" {
					if err := copyToClipboard(t.WorktreePath); err == nil {
						m.flash = fmt.Sprintf("Copied: %s", t.WorktreePath)
					} else {
						m.flash = fmt.Sprintf("Copy failed: %v", err)
					}
				}
			}
		case "r":
			if m.cursor < len(m.tasks) {
				t := m.tasks[m.cursor]
				if t.Status == "bailed" || t.Status == "failed" || t.Status == "stopped" {
					return m, m.restartTask(t)
				}
			}
		}
	case deployRestartMsg:
		if msg.err != nil {
			m.flash = fmt.Sprintf("Restart failed: %v", msg.err)
		} else {
			m.flash = fmt.Sprintf("Task #%d re-queued — daemon will pick it up shortly", msg.issueNumber)
		}
		return m, m.refreshTasks()
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case deployTickMsg:
		m.flash = "" // clear transient message on next tick
		return m, tea.Batch(m.refreshTasks(), m.tickCmd())
	case deployTasksMsg:
		m.tasks = []db.AutopilotTask(msg)
		if len(m.tasks) > 0 && m.tasks[0].Owner != "" {
			m.repoRef = m.tasks[0].Owner + "/" + m.tasks[0].Repo
		}
		if m.cursor >= len(m.tasks) {
			m.cursor = max(0, len(m.tasks)-1)
		}
	}
	return m, nil
}

func (m DeployViewer) View() tea.View {
	var b strings.Builder

	// Header.
	alive, _ := deploy.IsRunning(m.project.Name)
	status := "completed"
	if alive {
		status = "running"
	}
	fmt.Fprintf(&b, " Deploy %s [%s]", m.project.Name, status)
	if m.repoRef != "" {
		fmt.Fprintf(&b, "  %s", m.repoRef)
	}
	b.WriteString("\n\n")

	// Task list.
	if len(m.tasks) == 0 {
		b.WriteString(" No tasks.\n")
	} else {
		for i, t := range m.tasks {
			cursor := "  "
			if i == m.cursor {
				cursor = "> "
			}

			statusIcon := statusIcon(t.Status)
			costStr := ""
			if t.CostUSD > 0 {
				costStr = fmt.Sprintf("  $%.2f", t.CostUSD)
			}
			elapsed := ""
			if t.StartedAt != "" {
				liveTimer := t.Status == "running"
				elapsed = "  " + formatElapsed(t.StartedAt, t.CompletedAt, liveTimer)
			}
			prStr := ""
			if t.PRNumber > 0 {
				prStr = fmt.Sprintf("  PR #%d", t.PRNumber)
			}

			title := t.IssueTitle
			maxTitle := m.width - 50
			if maxTitle < 20 {
				maxTitle = 20
			}
			if len(title) > maxTitle {
				title = title[:maxTitle-3] + "..."
			}

			fmt.Fprintf(&b, "%s%s #%-5d %-*s %-8s%s%s%s\n",
				cursor, statusIcon, t.IssueNumber, maxTitle, title, t.Status, prStr, costStr, elapsed)
		}
	}

	// Detail panel (expanded view).
	if m.expanded && m.cursor < len(m.tasks) {
		t := m.tasks[m.cursor]
		b.WriteString("\n")
		b.WriteString(strings.Repeat("─", min(m.width, 80)))
		b.WriteString("\n")
		fmt.Fprintf(&b, " Issue:   #%d %s\n", t.IssueNumber, t.IssueTitle)
		fmt.Fprintf(&b, " Status:  %s\n", t.Status)
		if t.PRNumber > 0 {
			fmt.Fprintf(&b, " PR:      #%d\n", t.PRNumber)
		}
		if t.CostUSD > 0 {
			fmt.Fprintf(&b, " Cost:    $%.2f\n", t.CostUSD)
		}
		if t.Branch != "" {
			fmt.Fprintf(&b, " Branch:  %s\n", t.Branch)
		}
		if t.WorktreePath != "" {
			fmt.Fprintf(&b, " Worktree: %s\n", t.WorktreePath)
		}
		if t.AgentLog != "" {
			fmt.Fprintf(&b, " Log:     %s\n", t.AgentLog)
		}
		if t.FailureReason != "" {
			fmt.Fprintf(&b, " Failure: %s", t.FailureReason)
			if t.FailureDetail != "" {
				fmt.Fprintf(&b, " — %s", t.FailureDetail)
			}
			b.WriteString("\n")
		}
	}

	// Footer with summary.
	b.WriteString("\n")
	counts := map[string]int{}
	var totalCost float64
	for _, t := range m.tasks {
		counts[t.Status]++
		totalCost += t.CostUSD
	}
	parts := ""
	for _, s := range []string{"running", "queued", "review", "done", "bailed", "failed", "stopped"} {
		if c := counts[s]; c > 0 {
			if parts != "" {
				parts += "  "
			}
			parts += fmt.Sprintf("%d %s", c, s)
		}
	}
	fmt.Fprintf(&b, " $%.2f  %s", totalCost, parts)
	b.WriteString("\n")
	if m.flash != "" {
		fmt.Fprintf(&b, " %s\n", m.flash)
	}
	b.WriteString(" ↑↓ navigate  e expand  c copy path  r restart  q quit\n")

	return tea.NewView(b.String())
}

func (m DeployViewer) refreshTasks() tea.Cmd {
	return func() tea.Msg {
		tasks, _ := m.store.GetAutopilotTasks(m.project.ID)
		return deployTasksMsg(tasks)
	}
}

func (m DeployViewer) tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		return deployTickMsg{}
	})
}

func statusIcon(status string) string {
	switch status {
	case "running":
		return "●"
	case "queued":
		return "○"
	case "done":
		return "✓"
	case "review":
		return "◎"
	case "bailed", "failed":
		return "✗"
	case "stopped":
		return "■"
	default:
		return " "
	}
}

// restartTask resets a bailed/failed/stopped task back to queued in the DB,
// cleaning up the old worktree and branch. If the daemon has exited (all tasks
// were terminal), it re-spawns the daemon so the queued task gets picked up.
func (m DeployViewer) restartTask(t db.AutopilotTask) tea.Cmd {
	return func() tea.Msg {
		// Clean up old worktree and branch.
		if m.repoDir != "" {
			if t.WorktreePath != "" {
				_ = gitpkg.WorktreeRemove(m.repoDir, t.WorktreePath)
			}
			if t.Branch != "" {
				_ = gitpkg.DeleteBranch(m.repoDir, t.Branch)
			}
		}
		// Reset task in DB — clears runtime fields, sets status to queued.
		if err := m.store.ResetAutopilotTask(t.ID); err != nil {
			return deployRestartMsg{issueNumber: t.IssueNumber, err: err}
		}
		// If daemon is dead, respawn it so the queued task gets picked up.
		alive, _ := deploy.IsRunning(m.project.Name)
		if !alive {
			if err := deploy.RespawnDaemon(m.project.Name); err != nil {
				return deployRestartMsg{issueNumber: t.IssueNumber, err: fmt.Errorf("task re-queued but daemon respawn failed: %w", err)}
			}
		}
		return deployRestartMsg{issueNumber: t.IssueNumber}
	}
}

// formatElapsed computes the duration between startedAt and either completedAt
// (if set) or now (only when liveTimer is true). For terminal statuses without
// a completedAt timestamp (e.g. review), the elapsed time is frozen at the
// startedAt value to avoid a runaway timer.
func formatElapsed(startedAt, completedAt string, liveTimer bool) string {
	start, err := time.Parse("2006-01-02 15:04:05", startedAt)
	if err != nil {
		return ""
	}
	var end time.Time
	if completedAt != "" {
		if e, err := time.Parse("2006-01-02 15:04:05", completedAt); err == nil {
			end = e
		}
	}
	if end.IsZero() {
		if liveTimer {
			end = time.Now().UTC()
		} else {
			// No completedAt and not live — can't compute a meaningful duration.
			return ""
		}
	}
	d := end.Sub(start)
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}
