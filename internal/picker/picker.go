// Package picker provides interactive job selection using bubbles/list.
package picker

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/aptx-health/agent-minder/internal/daemon"
	"github.com/aptx-health/agent-minder/internal/db"
)

// Action represents what the user wants to do with a selected job.
type Action string

const (
	ActionCheckout  Action = "checkout"
	ActionResume    Action = "resume"
	ActionLogs      Action = "logs"
	ActionOpenIssue Action = "open_issue"
	ActionOpenPR    Action = "open_pr"
)

// --- Job items ---

type jobItem struct {
	job *db.Job
}

func (i jobItem) Title() string       { return formatJobLine(i.job) }
func (i jobItem) Description() string { return "" }
func (i jobItem) FilterValue() string { return jobSearchString(i.job) }

type remoteJobItem struct {
	job *daemon.JobResponse
}

func (i remoteJobItem) Title() string       { return formatRemoteJobLine(i.job) }
func (i remoteJobItem) Description() string { return "" }
func (i remoteJobItem) FilterValue() string { return remoteJobSearchString(i.job) }

// --- Action items ---

type actionItem struct {
	label  string
	action Action
}

func (i actionItem) Title() string       { return i.label }
func (i actionItem) Description() string { return "" }
func (i actionItem) FilterValue() string { return i.label }

// --- Picker model ---

type pickerModel struct {
	list     list.Model
	choice   list.Item
	quitting bool
}

func (m pickerModel) Init() tea.Cmd {
	return nil
}

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok {
		if key.Matches(msg, m.list.KeyMap.Quit) && m.list.FilterState() == list.Unfiltered {
			m.quitting = true
			return m, tea.Quit
		}
		if msg.String() == "enter" && m.list.FilterState() != list.Filtering {
			item := m.list.SelectedItem()
			if item != nil {
				m.choice = item
			}
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m pickerModel) View() string {
	return m.list.View()
}

// newPicker creates a picker model from list items.
func newPicker(items []list.Item, title string, filtering bool) pickerModel {
	// Single-line delegate (no description).
	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = false
	delegate.SetSpacing(0)

	height := len(items) + 6 // items + title + filter + help + padding
	if height > 30 {
		height = 30
	}

	l := list.New(items, delegate, 100, height)
	l.Title = title
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(filtering)
	l.SetShowHelp(true)
	l.Styles.Title = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))

	// Disable q-to-quit when filtering is on (conflicts with typing 'q').
	if filtering {
		l.KeyMap.Quit = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "quit"))
	}

	return pickerModel{list: l}
}

func runPicker(m pickerModel) (list.Item, error) {
	p := tea.NewProgram(m)
	result, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("picker: %w", err)
	}
	final := result.(pickerModel)
	if final.quitting || final.choice == nil {
		return nil, fmt.Errorf("cancelled")
	}
	return final.choice, nil
}

// --- Public API ---

// PickJob presents an interactive list of jobs and returns the chosen one.
func PickJob(jobs []*db.Job, title string) (*db.Job, error) {
	if len(jobs) == 0 {
		return nil, fmt.Errorf("no jobs to select from")
	}

	items := make([]list.Item, len(jobs))
	for i, j := range jobs {
		items[i] = jobItem{job: j}
	}

	choice, err := runPicker(newPicker(items, title, true))
	if err != nil {
		return nil, err
	}
	return choice.(jobItem).job, nil
}

// PickRemoteJob presents an interactive list of remote API jobs.
func PickRemoteJob(jobs []daemon.JobResponse, title string) (*daemon.JobResponse, error) {
	if len(jobs) == 0 {
		return nil, fmt.Errorf("no jobs to select from")
	}

	items := make([]list.Item, len(jobs))
	for i := range jobs {
		items[i] = remoteJobItem{job: &jobs[i]}
	}

	choice, err := runPicker(newPicker(items, title, true))
	if err != nil {
		return nil, err
	}
	return choice.(remoteJobItem).job, nil
}

// PickAction presents an action menu after job selection.
func PickAction(job *db.Job) (Action, error) {
	title := fmt.Sprintf("#%d [%s] %s", job.IssueNumber, job.Agent, truncate(job.IssueTitle.String, 40))
	if job.IssueNumber == 0 {
		title = fmt.Sprintf("[%s] %s", job.Agent, truncate(job.Name, 40))
	}

	items := []list.Item{
		actionItem{"Checkout worktree", ActionCheckout},
		actionItem{"Resume with Claude", ActionResume},
		actionItem{"View logs", ActionLogs},
	}
	if job.IssueNumber > 0 {
		items = append(items, actionItem{
			fmt.Sprintf("Open issue #%d in browser", job.IssueNumber), ActionOpenIssue,
		})
	}
	if job.PRNumber.Valid && job.PRNumber.Int64 > 0 {
		items = append(items, actionItem{
			fmt.Sprintf("Open PR #%d in browser", job.PRNumber.Int64), ActionOpenPR,
		})
	}

	choice, err := runPicker(newPicker(items, title, false))
	if err != nil {
		return "", err
	}
	return choice.(actionItem).action, nil
}

// PickFromList presents an interactive list of labeled items and returns
// the selected label. Useful for picking from a set of named options.
func PickFromList(labels []string, title string) (string, error) {
	if len(labels) == 0 {
		return "", fmt.Errorf("no items to select from")
	}

	items := make([]list.Item, len(labels))
	for i, l := range labels {
		items[i] = actionItem{label: l, action: Action(l)}
	}

	choice, err := runPicker(newPicker(items, title, len(labels) > 5))
	if err != nil {
		return "", err
	}
	return string(choice.(actionItem).action), nil
}

// FilterJobs returns only the jobs whose fields match the filter string.
func FilterJobs(jobs []*db.Job, filter string) []*db.Job {
	if filter == "" {
		return jobs
	}
	f := strings.ToLower(filter)
	var out []*db.Job
	for _, j := range jobs {
		if strings.Contains(strings.ToLower(jobSearchString(j)), f) {
			out = append(out, j)
		}
	}
	return out
}

// FilterRemoteJobs returns only the remote jobs whose fields match the filter.
func FilterRemoteJobs(jobs []daemon.JobResponse, filter string) []daemon.JobResponse {
	if filter == "" {
		return jobs
	}
	f := strings.ToLower(filter)
	var out []daemon.JobResponse
	for _, j := range jobs {
		if strings.Contains(strings.ToLower(remoteJobSearchString(&j)), f) {
			out = append(out, j)
		}
	}
	return out
}

// --- Formatting helpers ---

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func formatJobLine(j *db.Job) string {
	var label string
	if j.IssueNumber > 0 {
		label = fmt.Sprintf("#%-4d", j.IssueNumber)
	} else {
		name := j.Name
		if len(name) > 20 {
			name = name[:17] + "..."
		}
		label = fmt.Sprintf("%-5s", name)
	}

	agent := j.Agent
	if len(agent) > 12 {
		agent = agent[:12]
	}

	title := j.IssueTitle.String
	if title == "" {
		title = j.Name
	}
	if len(title) > 35 {
		title = title[:32] + "..."
	}

	pr := "      "
	if j.PRNumber.Valid && j.PRNumber.Int64 > 0 {
		pr = fmt.Sprintf("PR#%-3d", j.PRNumber.Int64)
	}

	cost := ""
	if j.CostUSD > 0 {
		cost = fmt.Sprintf("$%.2f", j.CostUSD)
	}

	return fmt.Sprintf("%s  [%-12s]  %-35s  %-10s  %s  %s",
		label, agent, title, j.Status, pr, cost)
}

func formatRemoteJobLine(j *daemon.JobResponse) string {
	var label string
	if j.IssueNumber > 0 {
		label = fmt.Sprintf("#%-4d", j.IssueNumber)
	} else {
		name := j.Name
		if len(name) > 20 {
			name = name[:17] + "..."
		}
		label = fmt.Sprintf("%-5s", name)
	}

	agent := j.Agent
	if len(agent) > 12 {
		agent = agent[:12]
	}

	title := j.Title
	if title == "" {
		title = j.Name
	}
	if len(title) > 35 {
		title = title[:32] + "..."
	}

	pr := "      "
	if j.PRNumber > 0 {
		pr = fmt.Sprintf("PR#%-3d", j.PRNumber)
	}

	cost := ""
	if j.CostUSD > 0 {
		cost = fmt.Sprintf("$%.2f", j.CostUSD)
	}

	return fmt.Sprintf("%s  [%-12s]  %-35s  %-10s  %s  %s",
		label, agent, title, j.Status, pr, cost)
}

func jobSearchString(j *db.Job) string {
	parts := []string{j.Agent, j.Name, j.Status, j.IssueTitle.String}
	if j.IssueNumber > 0 {
		parts = append(parts, strconv.Itoa(j.IssueNumber))
	}
	if j.PRNumber.Valid && j.PRNumber.Int64 > 0 {
		parts = append(parts, strconv.FormatInt(j.PRNumber.Int64, 10))
	}
	if j.CostUSD > 0 {
		parts = append(parts, fmt.Sprintf("%.2f", j.CostUSD))
	}
	return strings.Join(parts, " ")
}

func remoteJobSearchString(j *daemon.JobResponse) string {
	parts := []string{j.Agent, j.Name, j.Status, j.Title}
	if j.IssueNumber > 0 {
		parts = append(parts, strconv.Itoa(j.IssueNumber))
	}
	if j.PRNumber > 0 {
		parts = append(parts, strconv.Itoa(j.PRNumber))
	}
	if j.CostUSD > 0 {
		parts = append(parts, fmt.Sprintf("%.2f", j.CostUSD))
	}
	return strings.Join(parts, " ")
}

// Ensure interfaces are satisfied.
var (
	_ list.DefaultItem = jobItem{}
	_ list.DefaultItem = remoteJobItem{}
	_ list.DefaultItem = actionItem{}
)
