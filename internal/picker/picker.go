// Package picker provides interactive job selection using bubbles/list.
package picker

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"

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

// Column widths for formatted job rows.
//   - labelWidth: "#12345" or short job name
//   - agentWidth: fits "autopilot"/"bug-fixer" (9 chars); longer names get
//     ellipsized so a single overlong agent name can't push every row wide
//   - statusWidth: "reviewing" (9 chars; 10 leaves a buffer)
//   - prWidth: "PR#1234"
//   - costWidth: "$1234.45" right-aligned
const (
	labelWidth  = 6
	agentWidth  = 9
	statusWidth = 10
	prWidth     = 7
	costWidth   = 8

	// fixedColumnWidth is the rendered width of everything except the title
	// column. Layout (with 2-space separators):
	//   label  [agent]  TITLE  status  pr  cost
	//   = labelWidth + 2 + 1 + agentWidth + 1 + 2 + 2 + statusWidth + 2 + prWidth + 2 + costWidth
	fixedColumnWidth = labelWidth + 2 + 1 + agentWidth + 1 + 2 + 2 + statusWidth + 2 + prWidth + 2 + costWidth

	// listChromeWidth reserves space for the bubbles list delegate's left
	// indicator/indent so our row never gets right-truncated.
	listChromeWidth = 4

	// minTitleWidth is the smallest title column we'll render at; if the
	// terminal is too narrow to accommodate it, we still render the title at
	// this width (the list will truncate visually).
	minTitleWidth = 20

	// fallbackTermWidth is used when we can't detect terminal size.
	fallbackTermWidth = 100
)

// --- Job items ---

type jobItem struct {
	job  *db.Job
	line string
}

func (i jobItem) Title() string       { return i.line }
func (i jobItem) Description() string { return "" }
func (i jobItem) FilterValue() string { return jobSearchString(i.job) }

type remoteJobItem struct {
	job  *daemon.JobResponse
	line string
}

func (i remoteJobItem) Title() string       { return i.line }
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
	header   string // optional column-header line shown above items
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

// headerStyle styles the column-header row. Indented by 2 spaces so it
// aligns with the list delegate's left padding for item rows.
var headerStyle = lipgloss.NewStyle().Bold(true).Padding(0, 0, 0, 2)

func (m pickerModel) View() string {
	listView := m.list.View()
	if m.header == "" {
		return listView
	}
	// Inject the header line after the first row of the list view (which
	// is the title or, in filter mode, the filter input).
	nl := strings.Index(listView, "\n")
	if nl < 0 {
		return listView
	}
	return listView[:nl+1] + headerStyle.Render(m.header) + "\n" + listView[nl+1:]
}

// newPicker creates a picker model from list items.
func newPicker(items []list.Item, title string, filtering bool, width int) pickerModel {
	// Single-line delegate (no description).
	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = false
	delegate.SetSpacing(0)

	height := len(items) + 6 // items + title + filter + help + padding
	if height > 30 {
		height = 30
	}

	if width < fallbackTermWidth {
		width = fallbackTermWidth
	}

	l := list.New(items, delegate, width, height)
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

	termWidth := detectTermWidth()
	tw := titleWidthFor(termWidth)

	items := make([]list.Item, len(jobs))
	for i, j := range jobs {
		items[i] = jobItem{job: j, line: formatJobLine(j, tw)}
	}

	m := newPicker(items, title, true, termWidth)
	m.header = renderHeader(tw)
	choice, err := runPicker(m)
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

	termWidth := detectTermWidth()
	tw := titleWidthFor(termWidth)

	items := make([]list.Item, len(jobs))
	for i := range jobs {
		items[i] = remoteJobItem{job: &jobs[i], line: formatRemoteJobLine(&jobs[i], tw)}
	}

	m := newPicker(items, title, true, termWidth)
	m.header = renderHeader(tw)
	choice, err := runPicker(m)
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

	choice, err := runPicker(newPicker(items, title, false, detectTermWidth()))
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

	choice, err := runPicker(newPicker(items, title, len(labels) > 5, detectTermWidth()))
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

// padRight returns s padded to width w with spaces on the right, or truncated
// with a "..." suffix if it exceeds w.
func padRight(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if len(s) == w {
		return s
	}
	if len(s) > w {
		if w <= 3 {
			return s[:w]
		}
		return s[:w-3] + "..."
	}
	return s + strings.Repeat(" ", w-len(s))
}

// padLeft returns s padded to width w with spaces on the left
// (right-aligned), or truncated if it exceeds w.
func padLeft(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if len(s) == w {
		return s
	}
	if len(s) > w {
		if w <= 3 {
			return s[:w]
		}
		return s[:w-3] + "..."
	}
	return strings.Repeat(" ", w-len(s)) + s
}

// detectTermWidth returns the current terminal width via the controlling
// terminal, falling back to fallbackTermWidth if detection fails or the
// terminal is unreasonably narrow.
func detectTermWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w < 40 {
		return fallbackTermWidth
	}
	return w
}

// titleWidthFor returns the width available for the title column given a
// terminal width. The result is never less than minTitleWidth.
func titleWidthFor(termWidth int) int {
	w := termWidth - fixedColumnWidth - listChromeWidth
	if w < minTitleWidth {
		return minTitleWidth
	}
	return w
}

// jobColumns is the column-level data for a single job row, before
// width-aware padding.
type jobColumns struct {
	label  string // "#1234" or short job name
	agent  string // raw agent name
	title  string // raw title
	status string // raw status
	pr     string // "PR#1234" or ""
	cost   string // "$1.45" or ""
}

// renderRow returns the column-aligned string for a job row, using fixed
// widths for label/agent/status/pr/cost and the supplied titleWidth for the
// flexible title column.
func renderRow(c jobColumns, titleWidth int) string {
	return fmt.Sprintf("%s  [%s]  %s  %s  %s  %s",
		padRight(c.label, labelWidth),
		padRight(c.agent, agentWidth),
		padRight(c.title, titleWidth),
		padRight(c.status, statusWidth),
		padRight(c.pr, prWidth),
		padLeft(c.cost, costWidth),
	)
}

// renderHeader returns the column-header row whose offsets match those
// produced by renderRow. The bracket positions around the agent column are
// rendered as spaces so the header lines up with the agent text below.
func renderHeader(titleWidth int) string {
	return fmt.Sprintf("%s   %s   %s  %s  %s  %s",
		padRight("ISSUE", labelWidth),
		padRight("AGENT", agentWidth),
		padRight("TITLE", titleWidth),
		padRight("STATUS", statusWidth),
		padRight("PR", prWidth),
		padLeft("COST", costWidth),
	)
}

func jobToColumns(j *db.Job) jobColumns {
	c := jobColumns{
		agent:  j.Agent,
		status: j.Status,
	}

	if j.IssueNumber > 0 {
		c.label = fmt.Sprintf("#%d", j.IssueNumber)
	} else {
		c.label = j.Name
	}

	c.title = j.IssueTitle.String
	if c.title == "" {
		c.title = j.Name
	}

	if j.PRNumber.Valid && j.PRNumber.Int64 > 0 {
		c.pr = fmt.Sprintf("PR#%d", j.PRNumber.Int64)
	}

	if j.CostUSD > 0 {
		c.cost = fmt.Sprintf("$%.2f", j.CostUSD)
	}

	return c
}

func remoteJobToColumns(j *daemon.JobResponse) jobColumns {
	c := jobColumns{
		agent:  j.Agent,
		status: j.Status,
	}

	if j.IssueNumber > 0 {
		c.label = fmt.Sprintf("#%d", j.IssueNumber)
	} else {
		c.label = j.Name
	}

	c.title = j.Title
	if c.title == "" {
		c.title = j.Name
	}

	if j.PRNumber > 0 {
		c.pr = fmt.Sprintf("PR#%d", j.PRNumber)
	}

	if j.CostUSD > 0 {
		c.cost = fmt.Sprintf("$%.2f", j.CostUSD)
	}

	return c
}

func formatJobLine(j *db.Job, titleWidth int) string {
	return renderRow(jobToColumns(j), titleWidth)
}

func formatRemoteJobLine(j *daemon.JobResponse, titleWidth int) string {
	return renderRow(remoteJobToColumns(j), titleWidth)
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
