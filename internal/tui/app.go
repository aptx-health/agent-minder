// Package tui implements the bubbletea-based dashboard for agent-minder.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/dustinlange/agent-minder/internal/autopilot"
	"github.com/dustinlange/agent-minder/internal/config"
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

// cleanupResultMsg is sent when cleanup of terminal items completes.
type cleanupResultMsg struct {
	removed int
	err     error
}

// trackFormRow holds one row of the multi-repo track/untrack form.
type trackFormRow struct {
	owner, repo, ownerRepo string
	input                  textinput.Model
	original               string // untrack: original numbers for diffing
}

// trackStep represents the current step in the track/untrack flow.
type trackStep int

const (
	trackStepInput          trackStep = iota // entering issue numbers
	trackStepFetching                        // fetching item details from GitHub
	trackStepPreview                         // showing items for confirmation
	trackStepCleanupConfirm                  // confirming cleanup of terminal items
)

// trackPreviewItem holds resolved item info for the confirmation preview.
type trackPreviewItem struct {
	ref    *ghpkg.ItemRef
	title  string
	status string // compact status for dot rendering
}

// trackFetchResultMsg is sent when async item detail fetch completes.
type trackFetchResultMsg struct {
	items []trackPreviewItem
	err   error
}

// clearTrackStatusMsg clears the track status after a delay.
type clearTrackStatusMsg struct{}

// autopilotPrepareResultMsg is sent when autopilot issue fetch completes.
type autopilotPrepareResultMsg struct {
	total     int
	unblocked int
	err       error
}

// autopilotEventMsg wraps an autopilot supervisor event.
type autopilotEventMsg autopilot.Event

// clearAutopilotStatusMsg clears a temporary autopilot status message.
type clearAutopilotStatusMsg struct{}

// idleCheckMsg triggers periodic idle timeout checking.
type idleCheckMsg time.Time

// Tab constants.
const (
	tabOperations = 0
	tabAnalysis   = 1
	tabAutopilot  = 2
	tabCount      = 3
)

// Model is the root bubbletea model for the dashboard.
type Model struct {
	project *db.Project
	store   *db.Store
	poller  *poller.Poller
	width   int
	height  int

	// Tab state.
	activeTab       int  // tabOperations, tabAnalysis, or tabAutopilot
	analysisHasNew  bool // true when new analysis arrived while on Ops tab
	autopilotHasNew bool // true when autopilot state changed while on another tab

	// State.
	events   []poller.Event
	lastPoll *poller.PollResult

	// Viewports for scrollable sections.
	analysisVP       viewport.Model
	eventLogVP       viewport.Model
	analysisExpanded bool // 'e' toggles 3-line vs proportional

	// Tracked items (refreshed on poll results).
	trackedItems     []db.TrackedItem
	bailedIssues     map[int]bool // issue numbers with bailed autopilot tasks
	trackedExpanded  bool         // 'x' toggles compact strip vs expanded list with titles
	concernsExpanded bool         // 'c' toggles capped vs full concern display

	// Settings dialog.
	settingsState  *settingsState
	settingsStatus string

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
	trackRows         []trackFormRow
	trackFocus        int
	trackStatus       string
	trackError        bool
	trackStep         trackStep
	trackPreviewItems []trackPreviewItem
	trackCleanupCount int // number of terminal items pending cleanup

	// Filter mode (bulk add tracked items).
	filterState  *filterState
	filterStatus string

	// Poll confirm (R key).
	pollConfirm bool

	// Autopilot.
	autopilotSupervisor    *autopilot.Supervisor
	autopilotMode          string // "", "scan-confirm", "confirm", "running", "stop-confirm"
	autopilotStatus        string
	autopilotTotal         int
	autopilotUnblocked     int
	origPollInterval       time.Duration // saved to restore after autopilot stops
	autopilotTasksExpanded bool          // 'e' toggles 5-line minimum vs expanded task list
	autopilotTaskVP        viewport.Model

	// Auto-pause on idle.
	lastUserInput time.Time
	autoPaused    bool

	// Help overlay.
	showHelp bool

	// Warning banner (persistent, dismissible with 'w').
	warningBanner string
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

	apVP := viewport.New()
	apVP.KeyMap = safeViewportKeyMap()
	apVP.SoftWrap = true
	apVP.FillHeight = true

	m := Model{
		project:         project,
		store:           store,
		poller:          p,
		events:          make([]poller.Event, 0, 64),
		mode:            "normal",
		activeTab:       tabOperations,
		broadcastInput:  bi,
		userMsgInput:    ta,
		onboardInput:    oi,
		spinner:         sp,
		polling:         true, // initial status check starts immediately
		analysisVP:      aVP,
		eventLogVP:      eVP,
		autopilotTaskVP: apVP,
		lastUserInput:   time.Now(),
	}
	m.applyTextareaTheme()
	return m
}

// applyTextareaTheme sets textarea styles to match the current theme.
func (m *Model) applyTextareaTheme() {
	var s textarea.Styles
	if currentTheme().Name == "latte" {
		s = textarea.DefaultLightStyles()
	} else {
		s = textarea.DefaultDarkStyles()
	}
	m.broadcastInput.SetStyles(s)
	m.userMsgInput.SetStyles(s)
	m.onboardInput.SetStyles(s)
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		listenForEvents(m.poller),
		tickEvery(),
		m.spinner.Tick,
		func() tea.Msg { return tea.RequestBackgroundColor() },
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
		case "filter":
			return m.updateFilter(msg)
		case "settings":
			return m.updateSettings(msg)
		default:
			return m.updateNormal(msg)
		}

	case tea.BackgroundColorMsg:
		if msg.IsDark() {
			setThemeByName("mocha")
		} else {
			setThemeByName("latte")
		}
		m.applyTextareaTheme()
		m.rebuildAnalysisContent()
		m.rebuildEventLogContent()
		return m, nil

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
		if event.Type == "warning" {
			m.warningBanner = event.Summary
		}
		if event.Type == "polling" {
			m.polling = true
		}
		if event.PollResult != nil {
			m.lastPoll = event.PollResult
			m.polling = false
			m.refreshTrackedItems()
			m.worktrees, _ = m.store.GetWorktreesForProject(m.project.ID)
			// Flag new analysis if user is on Ops tab and this was an analysis result.
			if m.activeTab == tabOperations && event.PollResult.Tier2Analysis != "" {
				m.analysisHasNew = true
			}
		}
		m.rebuildEventLogContent()
		m.rebuildAnalysisContent()
		m.resizeViewports()
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
		m.refreshTrackedItems()
		if msg.added > 0 && m.autopilotSupervisor != nil {
			m.autopilotSupervisor.TriggerDiscovery()
		}
		if msg.failed > 0 {
			m.trackError = true
			m.trackStatus = fmt.Sprintf("Tracked %d, %d failed: %s", msg.added, msg.failed, strings.Join(msg.errors, "; "))
		} else {
			m.trackStatus = fmt.Sprintf("Tracked %d items", msg.added)
		}
		return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return clearTrackStatusMsg{}
		})

	case bulkUntrackResultMsg:
		m.refreshTrackedItems()
		if msg.failed > 0 {
			m.trackError = true
			m.trackStatus = fmt.Sprintf("Untracked %d, %d failed: %s", msg.removed, msg.failed, strings.Join(msg.errors, "; "))
		} else {
			m.trackStatus = fmt.Sprintf("Untracked %d items", msg.removed)
		}
		return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return clearTrackStatusMsg{}
		})

	case trackFetchResultMsg:
		if msg.err != nil {
			m.trackError = true
			m.trackStatus = fmt.Sprintf("Error fetching items: %v", msg.err)
			m.trackStep = trackStepInput
			return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
				return clearTrackStatusMsg{}
			})
		}
		m.trackPreviewItems = msg.items
		m.trackStep = trackStepPreview
		m.trackStatus = ""
		m.resizeViewports()
		return m, nil

	case cleanupResultMsg:
		m.refreshTrackedItems()
		if msg.err != nil {
			m.trackError = true
			m.trackStatus = fmt.Sprintf("Cleanup failed: %v", msg.err)
		} else {
			m.trackStatus = fmt.Sprintf("Cleaned up %d done items", msg.removed)
		}
		return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return clearTrackStatusMsg{}
		})

	case clearTrackStatusMsg:
		m.trackStatus = ""
		m.trackError = false
		m.trackRows = nil
		m.trackStep = trackStepInput
		m.trackPreviewItems = nil
		m.trackCleanupCount = 0
		m.mode = "normal"
		m.resizeViewports()
		return m, nil

	case autopilotPrepareResultMsg:
		m.polling = false
		if msg.err != nil {
			m.autopilotStatus = fmt.Sprintf("Error: %v", msg.err)
			m.autopilotMode = ""
			m.autopilotSupervisor = nil
			return m, tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
				return clearAutopilotStatusMsg{}
			})
		}
		if msg.total == 0 {
			m.autopilotStatus = "No issues found matching filter"
			m.autopilotMode = ""
			m.autopilotSupervisor = nil
			return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
				return clearAutopilotStatusMsg{}
			})
		}
		m.autopilotTotal = msg.total
		m.autopilotUnblocked = msg.unblocked
		m.autopilotMode = "confirm"
		m.autopilotStatus = ""
		if m.activeTab != tabAutopilot {
			m.autopilotHasNew = true
		}
		return m, nil

	case autopilotEventMsg:
		event := autopilot.Event(msg)
		eventType := "autopilot"
		if event.Type == "warning" {
			eventType = "warning"
			m.warningBanner = event.Summary
		}
		m.events = append(m.events, poller.Event{
			Time:    event.Time,
			Type:    eventType,
			Summary: fmt.Sprintf("[%s] %s", event.Type, event.Summary),
		})
		if len(m.events) > 50 {
			m.events = m.events[len(m.events)-50:]
		}
		m.rebuildEventLogContent()
		// Flag new autopilot activity if user is on another tab.
		if m.activeTab != tabAutopilot {
			m.autopilotHasNew = true
		}
		m.rebuildAutopilotTaskContent()
		if event.Type == "finished" {
			m.autopilotMode = ""
			// Restore status interval.
			if m.origPollInterval > 0 {
				m.poller.SetStatusInterval(m.origPollInterval)
				m.origPollInterval = 0
			}
			m.poller.SetAutopilotStatusFunc(nil)
			m.autopilotSupervisor = nil
		}
		m.resizeViewports()
		return m, listenForAutopilotEvents(m.autopilotSupervisor)

	case clearAutopilotStatusMsg:
		m.autopilotStatus = ""
		return m, nil

	case filterChoicesMsg:
		if m.filterState != nil {
			if msg.err != nil || len(msg.choices) == 0 {
				// No choices available or error — fall back to text input.
				m.filterState.step = filterStepInputValue
				switch m.filterState.filterType {
				case ghpkg.FilterLabel:
					m.filterState.input.Placeholder = "label name..."
				case ghpkg.FilterMilestone:
					m.filterState.input.Placeholder = "milestone title..."
				case ghpkg.FilterAssignee:
					m.filterState.input.Placeholder = "username..."
				}
				return m, m.filterState.input.Focus()
			}
			m.filterState.choices = msg.choices
			m.filterState.choiceIdx = 0
			m.filterState.step = filterStepSelectChoice
		}
		return m, nil

	case filterSearchResultMsg:
		if m.filterState != nil {
			if msg.err != nil {
				m.filterState.err = msg.err
				m.filterState.step = filterStepPreview
			} else {
				m.filterState.results = msg.results
				m.filterState.step = filterStepPreview
			}
		}
		return m, nil

	case bulkAddResultMsg:
		if msg.err != nil {
			m.filterStatus = fmt.Sprintf("Error: %v", msg.err)
		} else {
			m.filterStatus = fmt.Sprintf("Added %d items", msg.added)
			m.refreshTrackedItems()
		}
		m.filterState = nil
		m.mode = "normal"
		return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return clearFilterStatusMsg{}
		})

	case bulkUpdateResultMsg:
		if msg.err != nil {
			m.filterStatus = fmt.Sprintf("Error: %v", msg.err)
		} else {
			m.filterStatus = fmt.Sprintf("Updated: +%d new, -%d closed", msg.added, msg.removed)
			m.refreshTrackedItems()
		}
		m.filterState = nil
		m.mode = "normal"
		return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return clearFilterStatusMsg{}
		})

	case clearFilterStatusMsg:
		m.filterStatus = ""
		return m, nil

	case settingsSavedMsg:
		if msg.err != nil {
			m.settingsStatus = fmt.Sprintf("Error: %v", msg.err)
		} else {
			m.settingsStatus = fmt.Sprintf("%s updated", msg.field)
		}
		return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return clearSettingsStatusMsg{}
		})

	case clearSettingsStatusMsg:
		m.settingsStatus = ""
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
type clearFilterStatusMsg struct{}
type clearSettingsStatusMsg struct{}

func (m Model) updateNormal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Poll confirm mode: confirm before expensive comprehensive analysis.
	if m.pollConfirm {
		switch msg.String() {
		case "y":
			m.pollConfirm = false
			m.activeTab = tabAnalysis
			m.analysisHasNew = false
			m.resizeViewports()
			p := m.poller
			return m, func() tea.Msg {
				p.PollNow(context.Background())
				return nil
			}
		case "n", "esc":
			m.pollConfirm = false
			return m, nil
		}
		return m, nil
	}
	// Autopilot confirm modes: only intercept y/n/esc when on tab 3.
	// Tab switching keys always pass through so users can browse freely.
	if m.autopilotMode == "scan-confirm" && m.activeTab == tabAutopilot {
		switch msg.String() {
		case "enter":
			return m.prepareAutopilot()
		case "esc":
			m.autopilotMode = ""
			m.autopilotSupervisor = nil
			m.autopilotStatus = ""
			return m, nil
		}
	}
	if m.autopilotMode == "confirm" && m.activeTab == tabAutopilot {
		switch msg.String() {
		case "enter":
			return m.confirmAutopilot()
		case "esc":
			m.autopilotMode = ""
			m.autopilotSupervisor = nil
			m.autopilotStatus = ""
			return m, nil
		}
	}
	if m.autopilotMode == "stop-confirm" && m.activeTab == tabAutopilot {
		switch msg.String() {
		case "enter":
			return m.confirmStopAutopilot()
		case "esc":
			m.autopilotMode = "running"
			return m, nil
		}
	}

	switch msg.String() {
	case "1":
		m.activeTab = tabOperations
		m.resizeViewports()
		return m, nil
	case "2":
		m.activeTab = tabAnalysis
		m.analysisHasNew = false
		m.resizeViewports()
		return m, nil
	case "3":
		m.activeTab = tabAutopilot
		m.autopilotHasNew = false
		m.resizeViewports()
		return m, nil
	case "tab":
		m.activeTab = (m.activeTab + 1) % tabCount
		m.clearTabIndicator()
		m.resizeViewports()
		return m, nil
	case "shift+tab":
		m.activeTab = (m.activeTab + tabCount - 1) % tabCount
		m.clearTabIndicator()
		m.resizeViewports()
		return m, nil
	case "?":
		m.showHelp = !m.showHelp
		m.resizeViewports()
		return m, nil
	case "d":
		m.warningBanner = ""
		return m, nil
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
		p := m.poller
		return m, func() tea.Msg {
			p.StatusNow(context.Background())
			return nil
		}
	case "R":
		m.pollConfirm = true
		return m, nil
	case "e":
		if m.activeTab == tabAnalysis {
			m.analysisExpanded = !m.analysisExpanded
			m.resizeViewports()
			return m, nil
		}
		if m.activeTab == tabAutopilot {
			m.autopilotTasksExpanded = !m.autopilotTasksExpanded
			m.resizeViewports()
			return m, nil
		}
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
		m.trackError = false
		m.trackStep = trackStepInput
		m.trackPreviewItems = nil
		m.trackRows = buildTrackRows(ghRepos, nil)
		m.trackFocus = 0
		cmd := m.trackRows[0].input.Focus()
		m.resizeViewports()
		return m, cmd
	case "b":
		repos := m.poller.GitHubRepos()
		if len(repos) == 0 {
			m.trackStatus = "No GitHub repos enrolled"
			return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
				return clearTrackStatusMsg{}
			})
		}
		hasExisting := len(m.trackedItems) > 0
		m.filterState = newFilterState(repos, hasExisting)
		m.filterStatus = ""
		m.mode = "filter"
		return m, nil
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
		m.trackError = false
		m.trackStep = trackStepInput
		m.trackPreviewItems = nil
		m.trackRows = buildTrackRows(ghRepos, m.trackedItems)
		m.trackFocus = 0
		cmd := m.trackRows[0].input.Focus()
		m.resizeViewports()
		return m, cmd
	case "w":
		if m.activeTab != tabOperations {
			return m, nil
		}
		m.showWorktrees = !m.showWorktrees
		if m.showWorktrees && len(m.worktrees) == 0 {
			m.worktrees, _ = m.store.GetWorktreesForProject(m.project.ID)
		}
		m.resizeViewports()
		return m, nil
	case "x":
		if m.activeTab != tabOperations {
			return m, nil
		}
		m.trackedExpanded = !m.trackedExpanded
		m.resizeViewports()
		return m, nil
	case "c":
		if m.activeTab != tabAnalysis {
			return m, nil
		}
		m.concernsExpanded = !m.concernsExpanded
		m.resizeViewports()
		return m, nil
	case "s":
		m.settingsState = newSettingsState(m.project)
		// Apply textarea theme to match current theme.
		var s textarea.Styles
		if currentTheme().Name == "latte" {
			s = textarea.DefaultLightStyles()
		} else {
			s = textarea.DefaultDarkStyles()
		}
		m.settingsState.textarea.SetStyles(s)
		m.settingsStatus = ""
		m.mode = "settings"
		m.resizeViewports()
		return m, nil
	case "t":
		cycleTheme()
		m.applyTextareaTheme()
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
	case "a":
		if m.activeTab != tabAutopilot {
			return m, nil
		}
		return m.startAutopilot()
	case "A":
		if m.activeTab != tabAutopilot {
			return m, nil
		}
		return m.stopAutopilot()
	case "up", "down", "pgup", "pgdown":
		var cmd tea.Cmd
		switch m.activeTab {
		case tabAnalysis:
			m.analysisVP, cmd = m.analysisVP.Update(msg)
		case tabAutopilot:
			m.autopilotTaskVP, cmd = m.autopilotTaskVP.Update(msg)
		default:
			m.eventLogVP, cmd = m.eventLogVP.Update(msg)
		}
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
	// Preview step: confirm or go back.
	if m.trackStep == trackStepPreview {
		switch msg.String() {
		case "esc":
			m.trackStep = trackStepInput
			m.trackPreviewItems = nil
			// Re-focus the input.
			if len(m.trackRows) > 0 {
				cmd := m.trackRows[m.trackFocus].input.Focus()
				m.resizeViewports()
				return m, cmd
			}
			m.resizeViewports()
			return m, nil
		case "enter":
			return m.executeTrackAction()
		}
		return m, nil
	}

	// Cleanup confirm step: y to proceed, n/esc to go back.
	if m.trackStep == trackStepCleanupConfirm {
		switch msg.String() {
		case "y":
			store := m.store
			projectID := m.project.ID
			m.trackStep = trackStepFetching
			m.trackStatus = fmt.Sprintf("Cleaning up %d items...", m.trackCleanupCount)
			return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
				removed, err := store.ArchiveTerminalTrackedItems(projectID)
				return cleanupResultMsg{removed: removed, err: err}
			})
		case "n", "esc":
			m.trackStep = trackStepInput
			m.trackCleanupCount = 0
			if len(m.trackRows) > 0 {
				cmd := m.trackRows[m.trackFocus].input.Focus()
				m.resizeViewports()
				return m, cmd
			}
			m.resizeViewports()
			return m, nil
		}
		return m, nil
	}

	// Fetching step: only allow esc.
	if m.trackStep == trackStepFetching {
		if msg.String() == "esc" {
			m.mode = "normal"
			m.trackStatus = ""
			m.trackRows = nil
			m.trackStep = trackStepInput
			m.trackPreviewItems = nil
			m.resizeViewports()
		}
		return m, nil
	}

	// Input step.
	switch msg.String() {
	case "esc":
		m.mode = "normal"
		m.trackStatus = ""
		m.trackRows = nil
		m.trackStep = trackStepInput
		m.trackPreviewItems = nil
		m.resizeViewports()
		return m, nil
	case "up", "shift+tab":
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
	case "c":
		if m.mode == "untrack" {
			count, err := m.store.CountTerminalTrackedItems(m.project.ID)
			if err != nil || count == 0 {
				m.trackStatus = "No done items to clean up"
				m.trackError = false
				return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
					return clearTrackStatusMsg{}
				})
			}
			for i := range m.trackRows {
				m.trackRows[i].input.Blur()
			}
			m.trackCleanupCount = count
			m.trackStep = trackStepCleanupConfirm
			m.resizeViewports()
			return m, nil
		}
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

// dedupeRefs removes duplicate ItemRefs by owner/repo/number.
func dedupeRefs(refs []*ghpkg.ItemRef) []*ghpkg.ItemRef {
	seen := make(map[string]bool, len(refs))
	out := make([]*ghpkg.ItemRef, 0, len(refs))
	for _, r := range refs {
		key := fmt.Sprintf("%s/%s#%d", r.Owner, r.Repo, r.Number)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

// submitTrackForm collects input, resolves item details, and shows a preview for confirmation.
func (m Model) submitTrackForm() (tea.Model, tea.Cmd) {
	for i := range m.trackRows {
		m.trackRows[i].input.Blur()
	}

	mode := m.mode

	if mode == "track" {
		// Collect refs from non-empty rows.
		var refs []*ghpkg.ItemRef
		for _, row := range m.trackRows {
			nums, err := ghpkg.ParseNumbers(row.input.Value())
			if err != nil {
				m.trackError = true
				m.trackStatus = fmt.Sprintf("Error in %s: %v", row.ownerRepo, err)
				return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
					return clearTrackStatusMsg{}
				})
			}
			for _, n := range nums {
				refs = append(refs, &ghpkg.ItemRef{Owner: row.owner, Repo: row.repo, Number: n})
			}
		}
		refs = dedupeRefs(refs)
		if len(refs) == 0 {
			m.mode = "normal"
			m.trackRows = nil
			m.resizeViewports()
			return m, nil
		}

		// Fetch item details from GitHub for preview.
		m.trackStep = trackStepFetching
		m.trackStatus = fmt.Sprintf("Fetching %d items...", len(refs))
		p := m.poller
		return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
			var items []trackPreviewItem
			for _, ref := range refs {
				status, err := p.FetchItemStatus(context.Background(), ref)
				if err != nil {
					return trackFetchResultMsg{err: fmt.Errorf("%s/%s#%d: %w", ref.Owner, ref.Repo, ref.Number, err)}
				}
				items = append(items, trackPreviewItem{
					ref:    ref,
					title:  status.Title,
					status: status.CompactStatus(),
				})
			}
			return trackFetchResultMsg{items: items}
		})
	}

	// Untrack mode: diff original vs current to find removed numbers.
	var refs []*ghpkg.ItemRef
	for _, row := range m.trackRows {
		origNums, _ := ghpkg.ParseNumbers(row.original)
		curNums, err := ghpkg.ParseNumbers(row.input.Value())
		if err != nil {
			m.trackError = true
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
	refs = dedupeRefs(refs)
	if len(refs) == 0 {
		m.mode = "normal"
		m.trackRows = nil
		m.resizeViewports()
		return m, nil
	}

	// For untrack, look up titles from existing tracked items (no API call needed).
	titleMap := make(map[string]db.TrackedItem, len(m.trackedItems))
	for _, ti := range m.trackedItems {
		key := fmt.Sprintf("%s/%s#%d", ti.Owner, ti.Repo, ti.Number)
		titleMap[key] = ti
	}
	var items []trackPreviewItem
	for _, ref := range refs {
		key := fmt.Sprintf("%s/%s#%d", ref.Owner, ref.Repo, ref.Number)
		title := fmt.Sprintf("#%d", ref.Number)
		status := "Open"
		if ti, ok := titleMap[key]; ok {
			if ti.Title != "" {
				title = ti.Title
			}
			if ti.LastStatus != "" {
				status = ti.LastStatus
			}
		}
		items = append(items, trackPreviewItem{
			ref:    ref,
			title:  title,
			status: status,
		})
	}
	m.trackPreviewItems = items
	m.trackStep = trackStepPreview
	m.resizeViewports()
	return m, nil
}

// executeTrackAction runs the actual add/remove after the user confirms the preview.
func (m Model) executeTrackAction() (tea.Model, tea.Cmd) {
	items := m.trackPreviewItems
	if len(items) == 0 {
		m.mode = "normal"
		m.trackRows = nil
		m.trackStep = trackStepInput
		m.trackPreviewItems = nil
		m.resizeViewports()
		return m, nil
	}

	p := m.poller
	mode := m.mode

	if mode == "track" {
		m.trackStep = trackStepFetching
		m.trackStatus = fmt.Sprintf("Tracking %d items...", len(items))
		refs := make([]*ghpkg.ItemRef, len(items))
		for i, item := range items {
			refs[i] = item.ref
		}
		return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
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
		})
	}

	// Untrack mode.
	m.trackStep = trackStepFetching
	m.trackStatus = fmt.Sprintf("Untracking %d items...", len(items))
	refs := make([]*ghpkg.ItemRef, len(items))
	for i, item := range items {
		refs[i] = item.ref
	}
	return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
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
	})
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

// startAutopilot begins the autopilot flow: show scan confirmation before expensive LLM call.
func (m Model) startAutopilot() (tea.Model, tea.Cmd) {
	if m.autopilotMode == "running" {
		m.autopilotStatus = "Autopilot already running — press A to stop"
		return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return clearAutopilotStatusMsg{}
		})
	}

	// Check prerequisites: need tracked issues.
	trackedItems, _ := m.store.GetTrackedItems(m.project.ID)
	hasOpenIssue := false
	for _, item := range trackedItems {
		if item.ItemType == "issue" && item.State == "open" {
			hasOpenIssue = true
			break
		}
	}
	if !hasOpenIssue {
		m.autopilotStatus = "No open issues tracked — use 'i' or 'f' to track issues first"
		return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return clearAutopilotStatusMsg{}
		})
	}

	ghRepos := m.poller.GitHubRepos()
	if len(ghRepos) == 0 {
		m.autopilotStatus = "No GitHub repos enrolled"
		return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return clearAutopilotStatusMsg{}
		})
	}

	// Get repo info for supervisor.
	repos, _ := m.store.GetRepos(m.project.ID)
	if len(repos) == 0 {
		m.autopilotStatus = "No repos enrolled"
		return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return clearAutopilotStatusMsg{}
		})
	}

	ghToken := config.GetIntegrationToken("github")
	if ghToken == "" {
		m.autopilotStatus = "GitHub token required (GITHUB_TOKEN, GH_TOKEN, or keychain)"
		return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return clearAutopilotStatusMsg{}
		})
	}

	// All prerequisites met — ask for confirmation before expensive LLM scan.
	sup := autopilot.New(
		m.store, m.project, m.poller.Provider(),
		repos[0].Path,
		ghRepos[0].Owner, ghRepos[0].Repo,
		ghToken,
	)
	m.autopilotSupervisor = sup
	m.autopilotMode = "scan-confirm"
	return m, nil
}

// prepareAutopilot runs the expensive LLM-based issue analysis after user confirms.
func (m Model) prepareAutopilot() (tea.Model, tea.Cmd) {
	sup := m.autopilotSupervisor
	if sup == nil {
		m.autopilotMode = ""
		return m, nil
	}

	m.autopilotStatus = "Analyzing tracked items..."
	m.polling = true

	return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
		total, unblocked, err := sup.Prepare(context.Background())
		return autopilotPrepareResultMsg{total: total, unblocked: unblocked, err: err}
	})
}

// confirmAutopilot launches the autopilot after user confirms.
func (m Model) confirmAutopilot() (tea.Model, tea.Cmd) {
	sup := m.autopilotSupervisor
	if sup == nil {
		m.autopilotMode = ""
		return m, nil
	}

	m.autopilotMode = "running"

	// Wire autopilot status into poller.
	m.poller.SetAutopilotStatusFunc(sup.StatusBlock)

	// Halve status check frequency during autopilot for faster review gate checks.
	m.origPollInterval = m.project.StatusInterval()
	newInterval := m.origPollInterval / 2
	if newInterval < 15*time.Second {
		newInterval = 15 * time.Second
	}
	m.poller.SetStatusInterval(newInterval)

	sup.Launch(context.Background())

	// Show temporary notification with autopilot limits.
	maxAgents := m.project.AutopilotMaxAgents
	if maxAgents < 1 {
		maxAgents = 3
	}
	maxTurns := m.project.AutopilotMaxTurns
	if maxTurns < 1 {
		maxTurns = 50
	}
	maxBudget := m.project.AutopilotMaxBudgetUSD
	if maxBudget <= 0 {
		maxBudget = 3.00
	}
	m.autopilotStatus = fmt.Sprintf(
		"Autopilot running in background — %d agents, %d turns, $%.2f/agent max — press A to stop",
		maxAgents, maxTurns, maxBudget,
	)

	return m, tea.Batch(
		listenForAutopilotEvents(sup),
		tea.Tick(8*time.Second, func(t time.Time) tea.Msg {
			return clearAutopilotStatusMsg{}
		}),
	)
}

// stopAutopilot stops the running autopilot.
func (m Model) stopAutopilot() (tea.Model, tea.Cmd) {
	if m.autopilotMode != "running" || m.autopilotSupervisor == nil {
		return m, nil
	}
	m.autopilotMode = "stop-confirm"
	return m, nil
}

func (m Model) confirmStopAutopilot() (tea.Model, tea.Cmd) {
	m.autopilotMode = "running"
	sup := m.autopilotSupervisor
	return m, func() tea.Msg {
		sup.Stop()
		return nil
	}
}

// listenForAutopilotEvents returns a command that waits for the next autopilot event.
func listenForAutopilotEvents(sup *autopilot.Supervisor) tea.Cmd {
	if sup == nil {
		return nil
	}
	return func() tea.Msg {
		event, ok := <-sup.Events()
		if !ok {
			return nil
		}
		return autopilotEventMsg(event)
	}
}

// refreshTrackedItems reloads tracked items and rebuilds the bailed issues map
// from autopilot tasks.
func (m *Model) refreshTrackedItems() {
	m.trackedItems, _ = m.store.GetTrackedItems(m.project.ID)
	m.bailedIssues = make(map[int]bool)
	if tasks, err := m.store.GetAutopilotTasks(m.project.ID); err == nil {
		for _, t := range tasks {
			if t.Status == "bailed" {
				m.bailedIssues[t.IssueNumber] = true
			}
		}
	}
}

// effectiveStatus returns the display status for a tracked item, overlaying
// bailed autopilot status when applicable.
func (m Model) effectiveStatus(item db.TrackedItem) string {
	if m.bailedIssues[item.Number] && item.LastStatus != "Mrgd" && item.LastStatus != "Closd" {
		return "Baild"
	}
	return item.LastStatus
}

// clearTabIndicator clears the new-data indicator for the currently active tab.
func (m *Model) clearTabIndicator() {
	switch m.activeTab {
	case tabAnalysis:
		m.analysisHasNew = false
	case tabAutopilot:
		m.autopilotHasNew = false
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
