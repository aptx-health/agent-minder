// Package tui implements the bubbletea-based dashboard for agent-minder.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	"github.com/dustinlange/agent-minder/internal/db"
	ghpkg "github.com/dustinlange/agent-minder/internal/github"
	"github.com/dustinlange/agent-minder/internal/poller"
)

// pollerEventMsg wraps a poller event for the bubbletea message system.
type pollerEventMsg poller.Event

// tickMsg triggers UI refresh for the elapsed time display.
type tickMsg time.Time

// broadcastResultMsg is sent when a broadcast completes.
type broadcastResultMsg struct {
	topic string
	err   error
}

// userMsgResultMsg is sent when a user message post completes.
type userMsgResultMsg struct {
	topic string
	err   error
}

// onboardResultMsg is sent when an onboarding message generation completes.
type onboardResultMsg struct {
	topic string
	err   error
}

// clearUserMsgStatusMsg clears the user message status after a delay.
type clearUserMsgStatusMsg struct{}

// bulkTrackResultMsg is sent when a bulk track operation completes.
type bulkTrackResultMsg struct {
	added  int
	failed int
	errors []string
}

// bulkUntrackResultMsg is sent when a bulk untrack operation completes.
type bulkUntrackResultMsg struct {
	removed int
	failed  int
	errors  []string
}

// trackFormRow holds one row of the multi-repo track/untrack form.
type trackFormRow struct {
	owner, repo, ownerRepo string
	input                  textinput.Model
	original               string // untrack: original numbers for diffing
}

// clearTrackStatusMsg clears the track status after a delay.
type clearTrackStatusMsg struct{}

// idleCheckMsg triggers periodic idle timeout checking.
type idleCheckMsg time.Time

// Model is the root bubbletea model for the dashboard.
type Model struct {
	project *db.Project
	store   *db.Store
	poller  *poller.Poller
	width   int
	height  int

	// State.
	events   []poller.Event
	lastPoll *poller.PollResult
	err      error

	// Viewports for scrollable sections.
	analysisVP       viewport.Model
	eventLogVP       viewport.Model
	analysisExpanded bool // 'e' toggles 3-line vs proportional
	showInfo         bool // 'i' toggles repos/topics detail

	// Tracked items (refreshed on poll results).
	trackedItems []db.TrackedItem

	// Worktree display (refreshed on poll results).
	showWorktrees bool
	worktrees     []db.WorktreeWithRepo

	// Spinner for async operations.
	spinner spinner.Model
	polling bool // true while a manual poll is in progress

	// Broadcast mode.
	mode            string // "normal", "broadcast", "usermsg", or "onboard"
	broadcastInput  textarea.Model
	broadcastStatus string

	// User message mode.
	userMsgInput  textarea.Model
	userMsgStatus string

	// Onboard mode.
	onboardInput  textarea.Model
	onboardStatus string

	// Track mode (add/remove tracked items).
	trackRows   []trackFormRow
	trackFocus  int
	trackStatus string

	// Auto-pause on idle.
	lastUserInput time.Time
	autoPaused    bool
}

// safeViewportKeyMap returns a viewport KeyMap that only uses arrow keys and
// pgup/pgdn, avoiding conflicts with app-level letter keybindings.
func safeViewportKeyMap() viewport.KeyMap {
	return viewport.KeyMap{
		Up:       key.NewBinding(key.WithKeys("up")),
		Down:     key.NewBinding(key.WithKeys("down")),
		PageUp:   key.NewBinding(key.WithKeys("pgup")),
		PageDown: key.NewBinding(key.WithKeys("pgdown")),
		// HalfPageUp, HalfPageDown, Left, Right left as zero-value (disabled).
	}
}

// New creates a new TUI model.
func New(project *db.Project, store *db.Store, p *poller.Poller) Model {
	bi := textarea.New()
	bi.Placeholder = "Type a message for other agents..."
	bi.CharLimit = 500
	bi.SetHeight(3)
	bi.SetWidth(80)

	sp := spinner.New(
		spinner.WithSpinner(spinner.MiniDot),
		spinner.WithStyle(spinnerStyle()),
	)

	ta := textarea.New()
	ta.Placeholder = "Type your observation, note, or warning..."
	ta.CharLimit = 1000
	ta.SetHeight(3)
	ta.SetWidth(80)

	oi := textarea.New()
	oi.Placeholder = "Optional: guide the onboarding message (e.g., 'focus on test writing for feature A')... Leave empty for a general onboarding message."
	oi.CharLimit = 500
	oi.SetHeight(3)
	oi.SetWidth(80)

	aVP := viewport.New()
	aVP.KeyMap = safeViewportKeyMap()
	aVP.SoftWrap = true
	aVP.FillHeight = true

	eVP := viewport.New()
	eVP.KeyMap = safeViewportKeyMap()
	eVP.SoftWrap = true
	eVP.FillHeight = true

	return Model{
		project:        project,
		store:          store,
		poller:         p,
		events:         make([]poller.Event, 0, 64),
		mode:           "normal",
		broadcastInput: bi,
		userMsgInput:   ta,
		onboardInput:   oi,
		spinner:    sp,
		analysisVP: aVP,
		eventLogVP:     eVP,
		lastUserInput:  time.Now(),
	}
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		listenForEvents(m.poller),
		tickEvery(),
		m.spinner.Tick,
	}
	if m.project.IdlePauseSec > 0 {
		cmds = append(cmds, idleCheckTick())
	}
	return tea.Batch(cmds...)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeViewports()
		return m, nil

	case tea.KeyPressMsg:
		m.lastUserInput = time.Now()

		// Auto-resume on any keypress if auto-paused.
		if m.autoPaused {
			m.autoPaused = false
			m.poller.Resume()
			m.events = append(m.events, poller.Event{
				Time:    time.Now(),
				Type:    "resumed",
				Summary: "Resumed (user returned)",
			})
			m.rebuildEventLogContent()
			// Fall through to handle the keypress normally.
		}

		switch m.mode {
		case "broadcast":
			return m.updateBroadcast(msg)
		case "usermsg":
			return m.updateUserMsg(msg)
		case "onboard":
			return m.updateOnboard(msg)
		case "track", "untrack":
			return m.updateTrack(msg)
		default:
			return m.updateNormal(msg)
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case pollerEventMsg:
		event := poller.Event(msg)
		m.events = append(m.events, event)
		if len(m.events) > 50 {
			m.events = m.events[len(m.events)-50:]
		}
		if event.PollResult != nil {
			m.lastPoll = event.PollResult
			m.polling = false
			m.trackedItems, _ = m.store.GetTrackedItems(m.project.ID)
			m.worktrees, _ = m.store.GetWorktreesForProject(m.project.ID)
		}
		m.rebuildEventLogContent()
		m.rebuildAnalysisContent()
		return m, listenForEvents(m.poller)

	case broadcastResultMsg:
		if msg.err != nil {
			m.broadcastStatus = fmt.Sprintf("Error: %v", msg.err)
		} else {
			m.broadcastStatus = fmt.Sprintf("Sent to %s", msg.topic)
		}
		return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return clearBroadcastStatusMsg{}
		})

	case clearBroadcastStatusMsg:
		m.broadcastStatus = ""
		m.mode = "normal"
		m.resizeViewports()
		return m, nil

	case userMsgResultMsg:
		if msg.err != nil {
			m.userMsgStatus = fmt.Sprintf("Error: %v", msg.err)
		} else {
			m.userMsgStatus = fmt.Sprintf("Posted to %s", msg.topic)
		}
		return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return clearUserMsgStatusMsg{}
		})

	case clearUserMsgStatusMsg:
		m.userMsgStatus = ""
		m.mode = "normal"
		m.resizeViewports()
		return m, nil

	case onboardResultMsg:
		if msg.err != nil {
			m.onboardStatus = fmt.Sprintf("Error: %v", msg.err)
		} else {
			m.onboardStatus = fmt.Sprintf("Onboarding published to %s", msg.topic)
		}
		return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return clearOnboardStatusMsg{}
		})

	case clearOnboardStatusMsg:
		m.onboardStatus = ""
		m.mode = "normal"
		m.resizeViewports()
		return m, nil

	case bulkTrackResultMsg:
		m.trackedItems, _ = m.store.GetTrackedItems(m.project.ID)
		if msg.failed > 0 {
			m.trackStatus = fmt.Sprintf("Tracked %d, %d failed: %s", msg.added, msg.failed, strings.Join(msg.errors, "; "))
		} else {
			m.trackStatus = fmt.Sprintf("Tracked %d items", msg.added)
		}
		return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return clearTrackStatusMsg{}
		})

	case bulkUntrackResultMsg:
		m.trackedItems, _ = m.store.GetTrackedItems(m.project.ID)
		if msg.failed > 0 {
			m.trackStatus = fmt.Sprintf("Untracked %d, %d failed: %s", msg.removed, msg.failed, strings.Join(msg.errors, "; "))
		} else {
			m.trackStatus = fmt.Sprintf("Untracked %d items", msg.removed)
		}
		return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return clearTrackStatusMsg{}
		})

	case clearTrackStatusMsg:
		m.trackStatus = ""
		m.trackRows = nil
		m.mode = "normal"
		m.resizeViewports()
		return m, nil

	case idleCheckMsg:
		threshold := m.project.IdlePauseDuration()
		if threshold > 0 && !m.autoPaused && !m.poller.IsPaused() && time.Since(m.lastUserInput) >= threshold {
			m.autoPaused = true
			m.poller.Pause()
			idleDur := formatDuration(threshold)
			m.events = append(m.events, poller.Event{
				Time:    time.Now(),
				Type:    "paused",
				Summary: fmt.Sprintf("Auto-paused after %s of inactivity", idleDur),
			})
			m.rebuildEventLogContent()
		}
		return m, idleCheckTick()

	case tickMsg:
		return m, tickEvery()
	}

	return m, nil
}

type clearBroadcastStatusMsg struct{}
type clearOnboardStatusMsg struct{}

func (m Model) updateNormal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "p":
		if m.poller.IsPaused() {
			m.poller.Resume()
		} else {
			m.poller.Pause()
		}
		return m, nil
	case "r":
		m.polling = true
		p := m.poller
		return m, func() tea.Msg {
			p.PollNow(context.Background())
			return nil
		}
	case "e":
		m.analysisExpanded = !m.analysisExpanded
		m.resizeViewports()
		return m, nil
	case "i":
		ghRepos := m.poller.GitHubRepos()
		if len(ghRepos) == 0 {
			m.trackStatus = "No GitHub repos enrolled"
			return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
				return clearTrackStatusMsg{}
			})
		}
		m.mode = "track"
		m.trackStatus = ""
		m.trackRows = buildTrackRows(ghRepos, nil)
		m.trackFocus = 0
		cmd := m.trackRows[0].input.Focus()
		m.resizeViewports()
		return m, cmd
	case "I":
		if len(m.trackedItems) == 0 {
			m.trackStatus = "Nothing tracked"
			return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
				return clearTrackStatusMsg{}
			})
		}
		ghRepos := m.poller.GitHubRepos()
		if len(ghRepos) == 0 {
			m.trackStatus = "No GitHub repos enrolled"
			return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
				return clearTrackStatusMsg{}
			})
		}
		m.mode = "untrack"
		m.trackStatus = ""
		m.trackRows = buildTrackRows(ghRepos, m.trackedItems)
		m.trackFocus = 0
		cmd := m.trackRows[0].input.Focus()
		m.resizeViewports()
		return m, cmd
	case "w":
		m.showWorktrees = !m.showWorktrees
		if m.showWorktrees && len(m.worktrees) == 0 {
			m.worktrees, _ = m.store.GetWorktreesForProject(m.project.ID)
		}
		m.resizeViewports()
		return m, nil
	case "d":
		m.showInfo = !m.showInfo
		m.resizeViewports()
		return m, nil
	case "t":
		cycleTheme()
		m.rebuildAnalysisContent()
		m.rebuildEventLogContent()
		return m, nil
	case "u":
		m.mode = "usermsg"
		m.userMsgStatus = ""
		m.userMsgInput.Reset()
		if m.width > 4 {
			m.userMsgInput.SetWidth(m.width - 4)
		}
		m.resizeViewports()
		cmd := m.userMsgInput.Focus()
		return m, cmd
	case "m":
		m.mode = "broadcast"
		m.broadcastStatus = ""
		m.broadcastInput.Reset()
		if m.width > 4 {
			m.broadcastInput.SetWidth(m.width - 4)
		}
		m.resizeViewports()
		cmd := m.broadcastInput.Focus()
		return m, cmd
	case "o":
		m.mode = "onboard"
		m.onboardStatus = ""
		m.onboardInput.Reset()
		if m.width > 4 {
			m.onboardInput.SetWidth(m.width - 4)
		}
		m.resizeViewports()
		cmd := m.onboardInput.Focus()
		return m, cmd
	case "up", "down", "pgup", "pgdown":
		var cmd tea.Cmd
		m.eventLogVP, cmd = m.eventLogVP.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) updateBroadcast(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = "normal"
		m.broadcastStatus = ""
		m.broadcastInput.Blur()
		m.resizeViewports()
		return m, nil
	case "ctrl+d":
		value := m.broadcastInput.Value()
		if strings.TrimSpace(value) == "" {
			m.mode = "normal"
			m.broadcastInput.Blur()
			return m, nil
		}
		m.broadcastStatus = "Sending..."
		m.broadcastInput.Blur()
		p := m.poller
		return m, func() tea.Msg {
			busMsg, err := p.Broadcast(context.Background(), value)
			if err != nil {
				return broadcastResultMsg{err: err}
			}
			return broadcastResultMsg{topic: busMsg.Topic}
		}
	}

	var cmd tea.Cmd
	m.broadcastInput, cmd = m.broadcastInput.Update(msg)
	return m, cmd
}

func (m Model) updateUserMsg(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = "normal"
		m.userMsgStatus = ""
		m.userMsgInput.Blur()
		m.resizeViewports()
		return m, nil
	case "ctrl+d":
		value := m.userMsgInput.Value()
		if strings.TrimSpace(value) == "" {
			m.mode = "normal"
			m.userMsgInput.Blur()
			return m, nil
		}
		m.userMsgStatus = "Posting..."
		m.userMsgInput.Blur()
		p := m.poller
		return m, func() tea.Msg {
			err := p.PostUserMessage(context.Background(), value)
			if err != nil {
				return userMsgResultMsg{err: err}
			}
			return userMsgResultMsg{topic: p.Project().Name + "/coord"}
		}
	}

	var cmd tea.Cmd
	m.userMsgInput, cmd = m.userMsgInput.Update(msg)
	return m, cmd
}

func (m Model) updateOnboard(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = "normal"
		m.onboardStatus = ""
		m.onboardInput.Blur()
		m.resizeViewports()
		return m, nil
	case "ctrl+d":
		guidance := m.onboardInput.Value()
		m.onboardStatus = "Generating onboarding message..."
		m.onboardInput.Blur()
		p := m.poller
		return m, func() tea.Msg {
			busMsg, err := p.Onboard(context.Background(), guidance)
			if err != nil {
				return onboardResultMsg{err: err}
			}
			return onboardResultMsg{topic: busMsg.Topic}
		}
	}

	var cmd tea.Cmd
	m.onboardInput, cmd = m.onboardInput.Update(msg)
	return m, cmd
}

func (m Model) updateTrack(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = "normal"
		m.trackStatus = ""
		m.trackRows = nil
		m.resizeViewports()
		return m, nil
	case "up":
		if m.trackFocus > 0 {
			m.trackRows[m.trackFocus].input.Blur()
			m.trackFocus--
			cmd := m.trackRows[m.trackFocus].input.Focus()
			return m, cmd
		}
		return m, nil
	case "down", "tab":
		if m.trackFocus < len(m.trackRows)-1 {
			m.trackRows[m.trackFocus].input.Blur()
			m.trackFocus++
			cmd := m.trackRows[m.trackFocus].input.Focus()
			return m, cmd
		}
		return m, nil
	case "enter":
		return m.submitTrackForm()
	}

	var cmd tea.Cmd
	m.trackRows[m.trackFocus].input, cmd = m.trackRows[m.trackFocus].input.Update(msg)
	return m, cmd
}

// buildTrackRows creates trackFormRow entries from GitHub repos.
// If items is non-nil (untrack mode), pre-populates with tracked numbers.
func buildTrackRows(ghRepos []poller.GitHubRepo, items []db.TrackedItem) []trackFormRow {
	rows := make([]trackFormRow, 0, len(ghRepos))
	for _, gr := range ghRepos {
		ownerRepo := gr.Owner + "/" + gr.Repo
		ti := textinput.New()
		ti.Placeholder = "space-separated numbers, e.g. 42 17 5"
		ti.CharLimit = 200
		ti.SetWidth(50)

		var original string
		if items != nil {
			var nums []string
			for _, item := range items {
				if item.Owner == gr.Owner && item.Repo == gr.Repo {
					nums = append(nums, fmt.Sprintf("%d", item.Number))
				}
			}
			if len(nums) > 0 {
				original = strings.Join(nums, " ")
				ti.SetValue(original)
			}
		}

		rows = append(rows, trackFormRow{
			owner:     gr.Owner,
			repo:      gr.Repo,
			ownerRepo: ownerRepo,
			input:     ti,
			original:  original,
		})
	}
	return rows
}

// submitTrackForm collects input from all rows and fires the async bulk command.
func (m Model) submitTrackForm() (tea.Model, tea.Cmd) {
	for i := range m.trackRows {
		m.trackRows[i].input.Blur()
	}

	mode := m.mode
	p := m.poller

	if mode == "track" {
		// Collect refs from non-empty rows.
		var refs []*ghpkg.ItemRef
		for _, row := range m.trackRows {
			nums, err := ghpkg.ParseNumbers(row.input.Value())
			if err != nil {
				m.trackStatus = fmt.Sprintf("Error in %s: %v", row.ownerRepo, err)
				return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
					return clearTrackStatusMsg{}
				})
			}
			for _, n := range nums {
				refs = append(refs, &ghpkg.ItemRef{Owner: row.owner, Repo: row.repo, Number: n})
			}
		}
		if len(refs) == 0 {
			m.mode = "normal"
			m.trackRows = nil
			m.resizeViewports()
			return m, nil
		}
		m.trackStatus = fmt.Sprintf("Tracking %d items...", len(refs))
		return m, func() tea.Msg {
			var added, failed int
			var errs []string
			for _, ref := range refs {
				_, err := p.AddTrackedItemByRef(context.Background(), ref)
				if err != nil {
					failed++
					errs = append(errs, fmt.Sprintf("%s/%s#%d: %v", ref.Owner, ref.Repo, ref.Number, err))
				} else {
					added++
				}
			}
			return bulkTrackResultMsg{added: added, failed: failed, errors: errs}
		}
	}

	// Untrack mode: diff original vs current to find removed numbers.
	var refs []*ghpkg.ItemRef
	for _, row := range m.trackRows {
		origNums, _ := ghpkg.ParseNumbers(row.original)
		curNums, err := ghpkg.ParseNumbers(row.input.Value())
		if err != nil {
			m.trackStatus = fmt.Sprintf("Error in %s: %v", row.ownerRepo, err)
			return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
				return clearTrackStatusMsg{}
			})
		}
		curSet := make(map[int]bool, len(curNums))
		for _, n := range curNums {
			curSet[n] = true
		}
		for _, n := range origNums {
			if !curSet[n] {
				refs = append(refs, &ghpkg.ItemRef{Owner: row.owner, Repo: row.repo, Number: n})
			}
		}
	}
	if len(refs) == 0 {
		m.mode = "normal"
		m.trackRows = nil
		m.resizeViewports()
		return m, nil
	}
	m.trackStatus = fmt.Sprintf("Untracking %d items...", len(refs))
	return m, func() tea.Msg {
		var removed, failed int
		var errs []string
		for _, ref := range refs {
			err := p.RemoveTrackedItemByRef(ref)
			if err != nil {
				failed++
				errs = append(errs, fmt.Sprintf("%s/%s#%d: %v", ref.Owner, ref.Repo, ref.Number, err))
			} else {
				removed++
			}
		}
		return bulkUntrackResultMsg{removed: removed, failed: failed, errors: errs}
	}
}

// listenForEvents returns a command that waits for the next poller event.
func listenForEvents(p *poller.Poller) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-p.Events()
		if !ok {
			return nil
		}
		return pollerEventMsg(event)
	}
}

// tickEvery returns a command that sends a tick every 5 seconds.
func tickEvery() tea.Cmd {
	return func() tea.Msg {
		time.Sleep(5 * time.Second)
		return tickMsg(time.Now())
	}
}

// idleCheckTick returns a command that checks idle timeout every 60 seconds.
func idleCheckTick() tea.Cmd {
	return func() tea.Msg {
		time.Sleep(60 * time.Second)
		return idleCheckMsg(time.Now())
	}
}

// formatDuration returns a human-readable duration string like "4h" or "30m".
func formatDuration(d time.Duration) string {
	if d >= time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m > 0 {
			return fmt.Sprintf("%dh%dm", h, m)
		}
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}
