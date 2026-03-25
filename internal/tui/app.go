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

// userMsgResultMsg is sent when the analyzer responds to a user query.
type userMsgResultMsg struct {
	response string
	err      error
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
	result *autopilot.PrepareResult
	err    error
}

// reprepareKeepResultMsg is sent when a "keep" reprepare completes.
type reprepareKeepResultMsg struct {
	result *autopilot.PrepareResult
	err    error
}

// reprepareRebuildResultMsg is sent when a "rebuild" reprepare completes.
type reprepareRebuildResultMsg struct {
	result *autopilot.PrepareResult
	err    error
}

// autopilotEventMsg wraps an autopilot supervisor event.
type autopilotEventMsg autopilot.Event

// clearAutopilotStatusMsg clears a temporary autopilot status message.
type clearAutopilotStatusMsg struct{}

// rebuildDepsResultMsg is sent when a dep graph rebuild completes.
type rebuildDepsResultMsg struct {
	options []autopilot.DepOption
	err     error
}

// applyDepOptionResultMsg is sent when a dep option is applied (during running rebuild).
type applyDepOptionResultMsg struct {
	result autopilot.RebuildResult
	err    error
}

// autopilotApplyResultMsg is sent when a dep option is applied during initial prepare flow.
type autopilotApplyResultMsg struct {
	total     int
	unblocked int
	err       error
}

// reviewSessionResultMsg is sent when a review session launch completes.
type reviewSessionResultMsg struct {
	result *autopilot.ReviewSessionResult
	err    error
}

// autopilotAutoloadMsg triggers automatic loading of a saved dep graph on startup.
type autopilotAutoloadMsg struct{}

// autopilotTickMsg triggers periodic refresh of task list and slot status.
type autopilotTickMsg time.Time

// idleCheckMsg triggers periodic idle timeout checking.
type idleCheckMsg time.Time

// Tab constants.
const (
	tabOperations    = 0
	tabAnalysis      = 1
	tabAutopilot     = 2
	tabObservability = 3
	tabCount         = 4
)

// Model is the root bubbletea model for the dashboard.
type Model struct {
	project *db.Project
	store   *db.Store
	poller  *poller.Poller
	width   int
	height  int

	// Tab state.
	activeTab       int  // tabOperations, tabAnalysis, tabAutopilot, or tabObservability
	analysisHasNew  bool // true when new analysis arrived while on Ops tab
	autopilotHasNew bool // true when autopilot state changed while on another tab

	// State.
	events     []poller.Event
	lastPoll   *poller.PollResult
	lastPollAt time.Time

	// Viewports for scrollable sections.
	analysisVP       viewport.Model
	eventLogVP       viewport.Model
	analysisExpanded bool // 'e' toggles proportional vs 3-line (default: expanded)

	// Operations tasks — autopilot_tasks is the single source of truth.
	// Excludes removed/done/skipped for display purposes.
	operationsTasks []db.AutopilotTask
	trackedExpanded bool // 'x' toggles compact strip vs expanded list with titles

	// Settings dialog.
	settingsState  *settingsState
	settingsStatus string

	// Worktree display (refreshed on poll results).
	showWorktrees bool
	worktrees     []db.WorktreeWithRepo

	// Spinner for async operations.
	spinner    spinner.Model
	polling    bool   // true while a manual poll is in progress
	pollNotice string // dismissable notification after poll (e.g., "No new activity")

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

	// Autopilot.
	autopilotSupervisor       *autopilot.Supervisor
	autopilotMode             string // "", "scan-confirm", "dep-select", "confirm", "running", "stop-confirm", "stop-task-confirm", "restart-confirm", "resume-or-restart-confirm", "refresh-confirm", "review-confirm", "manual-confirm", "design-confirm", "add-slot-confirm", "completed", "reprepare-choice"
	autopilotModeBeforeReview string // saved mode to restore on review-confirm cancel
	autopilotPrepareResult    *autopilot.PrepareResult
	autopilotStatus           string
	autopilotTotal            int
	autopilotUnblocked        int
	origPollInterval          time.Duration // saved to restore after autopilot stops
	autopilotTasksExpanded    bool          // 'e' toggles 5-line minimum vs expanded task list
	autopilotTaskVP           viewport.Model

	// Autopilot task list cursor and navigation.
	autopilotTasks         []db.AutopilotTask    // sorted task list for navigation
	autopilotCursor        int                   // index into autopilotTasks
	autopilotSelectedIssue int                   // issue number for pinning cursor across refreshes
	failureDetailExpanded  bool                  // toggle full failure detail in task detail panel
	autopilotPaused        bool                  // tracks pause state for display
	autopilotDepGuidance   string                // user guidance for dep graph analysis
	autopilotDepOptions    []autopilot.DepOption // dep graph options from LLM
	autopilotDepSelection  int                   // 0-2, index into autopilotDepOptions
	autopilotAutoloading   bool                  // true when auto-loading saved dep graph on startup

	// Rebuild deps mode.
	rebuildDepsInput  textarea.Model
	rebuildDepsStatus string

	// Observability tab (cached, refreshed on poll results and tab switch).
	obsDailyCost      *db.CostSummary
	obsWeeklyCost     *db.CostSummary
	obsOverallCost    *db.CostSummary
	obsDailyTaskCosts []db.TaskCostDetail
	obsCostErr        error // non-nil if any cost query failed

	// Auto-pause on idle.
	lastUserInput time.Time
	autoPaused    bool

	// Help overlay.
	showHelp bool

	// Log viewer overlay.
	showLogViewer     bool
	logViewerTask     *db.AutopilotTask
	logViewerVP       viewport.Model
	logViewerContent  string
	logViewerFileSize int64
	logViewerAtBottom bool
	logViewerStatus   string

	// Warning banner (persistent, dismissible with 'd').
	warningBanner   string
	bannerClipboard string // command to copy to clipboard when banner is dismissed
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

	rdi := textarea.New()
	rdi.Placeholder = "e.g., '106 is not a real dep for 88' or 'skip 162, it is not ready'... Leave empty to analyze with no guidance."
	rdi.CharLimit = 500
	rdi.SetHeight(3)
	rdi.SetWidth(80)

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
		project:          project,
		store:            store,
		poller:           p,
		events:           make([]poller.Event, 0, 64),
		mode:             "normal",
		activeTab:        tabOperations,
		broadcastInput:   bi,
		userMsgInput:     ta,
		onboardInput:     oi,
		rebuildDepsInput: rdi,
		spinner:          sp,
		polling:          true, // initial status check starts immediately
		showWorktrees:    true,
		analysisVP:       aVP,
		eventLogVP:       eVP,
		autopilotTaskVP:  apVP,
		analysisExpanded: true,
		lastUserInput:    time.Now(),
	}

	// Restore last analysis from DB so it's visible on restart.
	if polls, err := store.RecentPolls(project.ID, 10); err == nil {
		for _, poll := range polls {
			if poll.Tier2Response != "" {
				m.lastPoll = &poller.PollResult{Tier2Analysis: poll.Tier2Response}
				if t, err := time.Parse("2006-01-02 15:04:05", poll.PolledAt); err == nil {
					m.lastPollAt = t
				}
				break
			}
		}
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
	m.rebuildDepsInput.SetStyles(s)
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
	// Auto-load saved dep graph if one exists.
	if dg, _ := m.store.GetDepGraph(m.project.ID); dg != nil {
		cmds = append(cmds, func() tea.Msg { return autopilotAutoloadMsg{} })
	}
	return tea.Batch(cmds...)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeViewports()
		if m.showLogViewer {
			m.logViewerVP.SetWidth(m.width)
			m.logViewerVP.SetHeight(m.height - 4)
		}
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

		// Log viewer overlay captures all keys when open.
		if m.showLogViewer {
			return m.updateLogViewer(msg)
		}

		switch m.mode {
		case "broadcast":
			return m.updateBroadcast(msg)
		case "usermsg":
			return m.updateUserMsg(msg)
		case "onboard":
			return m.updateOnboard(msg)
		case "rebuild-deps":
			return m.updateRebuildDeps(msg)
		case "dep-guidance":
			return m.updateDepGuidance(msg)
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
			// No new activity: show notice, keep existing analysis, don't update lastPoll analysis.
			if event.PollResult.NoNewActivity {
				m.polling = false
				m.pollNotice = "No new activity since last analysis"
				m.refreshTrackedItems()
				m.worktrees, _ = m.store.GetWorktreesForProject(m.project.ID)
				m.rebuildEventLogContent()
				m.resizeViewports()
				return m, tea.Batch(listenForEvents(m.poller), tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
					return clearPollNoticeMsg{}
				}))
			}
			// Preserve existing analysis when a status-only poll has no new analysis.
			if event.PollResult.Tier2Analysis == "" && m.lastPoll != nil && m.lastPoll.Tier2Analysis != "" {
				event.PollResult.Tier2Analysis = m.lastPoll.Tier2Analysis
			}
			m.lastPoll = event.PollResult
			m.lastPollAt = time.Now()
			m.polling = false
			m.pollNotice = ""
			m.refreshTrackedItems()
			m.refreshObsCosts()
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
			return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
				return clearUserMsgStatusMsg{}
			})
		}
		// Success: update analysis viewport with response and switch to Analysis tab.
		m.userMsgStatus = ""
		m.mode = "normal"
		m.lastPoll = &poller.PollResult{Tier2Analysis: msg.response}
		m.lastPollAt = time.Now()
		m.activeTab = tabAnalysis
		m.analysisHasNew = false
		m.rebuildAnalysisContent()
		m.resizeViewports()
		return m, nil

	case clearUserMsgStatusMsg:
		m.userMsgStatus = ""
		m.mode = "normal"
		m.resizeViewports()
		return m, nil

	case clearPollNoticeMsg:
		m.pollNotice = ""
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

	case applyDepOptionResultMsg:
		if msg.err != nil {
			m.rebuildDepsStatus = fmt.Sprintf("Error: %v", msg.err)
		} else {
			var parts []string
			if msg.result.Unblocked > 0 {
				parts = append(parts, fmt.Sprintf("%d unblocked", msg.result.Unblocked))
			}
			if msg.result.Skipped > 0 {
				parts = append(parts, fmt.Sprintf("%d skipped", msg.result.Skipped))
			}
			if len(parts) == 0 {
				m.rebuildDepsStatus = "Dep graph rebuilt — no changes"
			} else {
				m.rebuildDepsStatus = fmt.Sprintf("Dep graph rebuilt — %s", strings.Join(parts, ", "))
			}
		}
		m.rebuildAutopilotTaskContent()
		return m, nil

	case rebuildDepsResultMsg:
		if msg.err != nil {
			m.rebuildDepsStatus = fmt.Sprintf("Error: %v", msg.err)
			return m, nil
		}
		// Got new options — enter dep-select mode.
		m.autopilotDepOptions = msg.options
		m.autopilotDepSelection = 0
		m.rebuildDepsStatus = ""
		if len(msg.options) > 0 {
			m.autopilotUnblocked = msg.options[0].Unblocked
		}
		// If we were in running/completed mode, go to dep-select for rebuild.
		if m.autopilotMode == "running" || m.autopilotMode == "completed" || m.autopilotMode == "confirm" {
			m.autopilotMode = "dep-select"
		}
		m.rebuildAutopilotTaskContent()
		return m, nil

	case autopilotApplyResultMsg:
		m.polling = false
		if msg.err != nil {
			m.autopilotStatus = fmt.Sprintf("Error applying deps: %v", msg.err)
			m.autopilotMode = ""
			m.autopilotSupervisor = nil
			return m, tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
				return clearAutopilotStatusMsg{}
			})
		}
		m.autopilotTotal = msg.total
		m.autopilotUnblocked = msg.unblocked
		m.autopilotMode = "confirm"
		m.autopilotStatus = ""
		m.autopilotDepOptions = nil
		m.rebuildAutopilotTaskContent()
		return m, nil

	case clearRebuildDepsStatusMsg:
		m.rebuildDepsStatus = ""
		m.mode = "normal"
		m.resizeViewports()
		return m, nil

	case reviewSessionResultMsg:
		if msg.err != nil {
			m.autopilotStatus = fmt.Sprintf("Review session failed: %v", msg.err)
			return m, tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
				return clearAutopilotStatusMsg{}
			})
		} else if msg.result.SessionID != "" {
			clipCmd := fmt.Sprintf("cd %s && claude --resume %s", msg.result.WorktreePath, msg.result.SessionID)
			m.warningBanner = fmt.Sprintf("Session ready for #%d — %s", msg.result.IssueNumber, clipCmd)
			m.bannerClipboard = clipCmd
		} else {
			clipCmd := fmt.Sprintf("cd %s && claude", msg.result.WorktreePath)
			m.warningBanner = fmt.Sprintf("Worktree restored for #%d — %s", msg.result.IssueNumber, clipCmd)
			m.bannerClipboard = clipCmd
		}
		m.autopilotStatus = ""
		return m, nil

	case bulkTrackResultMsg:
		m.refreshTrackedItems()
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

	case autopilotAutoloadMsg:
		// Auto-load saved dep graph on startup — runs the same flow as 'a' then 'k'.
		m.autopilotAutoloading = true
		tm, cmd := m.startAutopilot()
		m2 := tm.(Model)
		if m2.autopilotMode == "scan-confirm" {
			// Prerequisites passed — proceed directly to prepare.
			tm3, cmd2 := m2.prepareAutopilot()
			return tm3, tea.Batch(cmd, cmd2)
		}
		// Prerequisites failed — startAutopilot set status message.
		m2.autopilotAutoloading = false
		return m2, cmd

	case autopilotPrepareResultMsg:
		m.polling = false
		if msg.err != nil {
			m.autopilotStatus = fmt.Sprintf("Error: %v", msg.err)
			m.autopilotMode = ""
			m.autopilotSupervisor = nil
			m.autopilotAutoloading = false
			return m, tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
				return clearAutopilotStatusMsg{}
			})
		}
		r := msg.result
		if r.Total == 0 && r.Existing == 0 {
			m.autopilotStatus = "No issues found matching filter"
			m.autopilotMode = ""
			m.autopilotSupervisor = nil
			m.autopilotAutoloading = false
			return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
				return clearAutopilotStatusMsg{}
			})
		}
		// If existing tasks found, offer keep/rebuild choice (or auto-keep on startup).
		if r.HasGraph || r.Existing > 0 {
			m.autopilotTotal = r.Total
			m.autopilotPrepareResult = r
			m.autopilotStatus = fmt.Sprintf("Agent definition: %s", r.AgentDef.Description())
			m.rebuildAutopilotTaskContent()

			// When autoloading on startup, skip the choice UI and auto-keep.
			if m.autopilotAutoloading {
				m.autopilotAutoloading = false
				sup := m.autopilotSupervisor
				if sup == nil {
					m.autopilotMode = ""
					return m, nil
				}
				m.autopilotStatus = "Loading saved dependency graph..."
				m.polling = true
				m.activeTab = tabAutopilot
				return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
					ctx, cancel := opCtx()
					defer cancel()
					result, err := sup.ReprepareKeep(ctx)
					return reprepareKeepResultMsg{result: result, err: err}
				})
			}

			m.autopilotMode = "reprepare-choice"
			if m.activeTab != tabAutopilot {
				m.autopilotHasNew = true
			}
			return m, tea.Tick(8*time.Second, func(t time.Time) tea.Msg {
				return clearAutopilotStatusMsg{}
			})
		}
		// Fresh prepare — go to dep-select.
		m.autopilotAutoloading = false
		m.autopilotTotal = r.Total
		m.autopilotDepOptions = r.Options
		m.autopilotDepSelection = 0
		if len(r.Options) > 0 {
			m.autopilotUnblocked = r.Options[0].Unblocked
		}
		m.autopilotMode = "dep-select"
		m.autopilotStatus = fmt.Sprintf("Agent definition: %s", r.AgentDef.Description())
		m.rebuildAutopilotTaskContent()
		if m.activeTab != tabAutopilot {
			m.autopilotHasNew = true
		}
		m.events = append(m.events, poller.Event{
			Time:    time.Now(),
			Type:    "autopilot",
			Summary: fmt.Sprintf("[info] Agent definition: %s", r.AgentDef.Description()),
		})
		m.rebuildEventLogContent()
		return m, tea.Tick(8*time.Second, func(t time.Time) tea.Msg {
			return clearAutopilotStatusMsg{}
		})

	case reprepareKeepResultMsg:
		m.polling = false
		if msg.err != nil {
			m.autopilotStatus = fmt.Sprintf("Error: %v", msg.err)
			m.autopilotMode = ""
			return m, tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
				return clearAutopilotStatusMsg{}
			})
		}
		r := msg.result
		if r.Total == 0 {
			m.autopilotStatus = "No tasks remain"
			m.autopilotMode = ""
			return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
				return clearAutopilotStatusMsg{}
			})
		}
		// If incremental options came back, show dep-select for new issues.
		if len(r.Options) > 0 {
			m.autopilotTotal = r.Total
			m.autopilotDepOptions = r.Options
			m.autopilotDepSelection = 0
			m.autopilotUnblocked = r.Options[0].Unblocked
			m.autopilotMode = "dep-select"
			m.rebuildAutopilotTaskContent()
			return m, nil
		}
		// No new issues — go straight to confirm.
		m.autopilotTotal = r.Total
		unblockedTasks, _ := m.store.QueuedUnblockedTasks(m.project.ID)
		m.autopilotUnblocked = len(unblockedTasks)
		m.autopilotMode = "confirm"
		m.rebuildAutopilotTaskContent()
		return m, nil

	case reprepareRebuildResultMsg:
		m.polling = false
		if msg.err != nil {
			m.autopilotStatus = fmt.Sprintf("Error: %v", msg.err)
			m.autopilotMode = ""
			return m, tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
				return clearAutopilotStatusMsg{}
			})
		}
		r := msg.result
		if r.Total == 0 {
			m.autopilotStatus = "No tasks remain"
			m.autopilotMode = ""
			return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
				return clearAutopilotStatusMsg{}
			})
		}
		m.autopilotTotal = r.Total
		if len(r.Options) > 0 {
			m.autopilotDepOptions = r.Options
			m.autopilotDepSelection = 0
			m.autopilotUnblocked = r.Options[0].Unblocked
			m.autopilotMode = "dep-select"
			m.rebuildAutopilotTaskContent()
			return m, nil
		}
		// No workable tasks after rebuild — go straight to confirm.
		unblockedTasks, _ := m.store.QueuedUnblockedTasks(m.project.ID)
		m.autopilotUnblocked = len(unblockedTasks)
		m.autopilotMode = "confirm"
		m.rebuildAutopilotTaskContent()
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
			// If the supervisor was explicitly stopped (via A key), clean up.
			// Otherwise (daemon mode, all work done), stay in running mode
			// with idle slots visible.
			if m.autopilotSupervisor != nil && !m.autopilotSupervisor.IsActive() {
				m.autopilotMode = "completed"
				if m.origPollInterval > 0 {
					m.poller.SetStatusInterval(m.origPollInterval)
					m.origPollInterval = 0
				}
				m.poller.SetAutopilotDepGraphFunc(nil)
				m.autopilotSupervisor = nil
			}
			// If supervisor is still active (daemon mode idle), stay in "running".
		}
		m.resizeViewports()
		cmds := []tea.Cmd{listenForAutopilotEvents(m.autopilotSupervisor)}
		// Sync operations tab on state-changing events.
		switch event.Type {
		case "started", "completed", "failed", "bailed", "stopped", "finished":
			p := m.poller
			cmds = append(cmds, func() tea.Msg {
				time.Sleep(4 * time.Second)
				ctx, cancel := opCtx()
				defer cancel()
				p.StatusNow(ctx)
				return nil
			})
		}
		return m, tea.Batch(cmds...)

	case clearAutopilotStatusMsg:
		m.autopilotStatus = ""
		return m, nil

	case clearBannerMsg:
		m.warningBanner = ""
		return m, nil

	case logRefreshMsg:
		if !m.showLogViewer {
			return m, nil
		}
		m.refreshLogContent()
		// Check if task is still running (refresh task status from our list).
		stillRunning := false
		if m.logViewerTask != nil {
			for _, t := range m.autopilotTasks {
				if t.IssueNumber == m.logViewerTask.IssueNumber {
					m.logViewerTask.Status = t.Status
					stillRunning = t.Status == "running"
					break
				}
			}
		}
		if stillRunning {
			return m, logRefreshTick()
		}
		return m, nil

	case clearLogViewerStatusMsg:
		m.logViewerStatus = ""
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

	case filterWatchSetMsg:
		if msg.err != nil {
			m.filterStatus = fmt.Sprintf("Error setting watch: %v", msg.err)
		} else {
			m.filterStatus = fmt.Sprintf("Watching %s: %s", msg.filterType, msg.filterValue)
		}
		return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return clearFilterStatusMsg{}
		})

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

	case settingsWatchChoicesMsg:
		if m.settingsState != nil {
			if msg.err != nil {
				m.settingsState.err = fmt.Sprintf("Fetch failed: %v", msg.err)
				m.settingsState.step = settingsStepWatchType
			} else if len(msg.choices) == 0 {
				m.settingsState.err = "No choices found"
				m.settingsState.step = settingsStepWatchType
			} else {
				m.settingsState.watchChoices = msg.choices
				m.settingsState.watchIdx = 0
				m.settingsState.step = settingsStepWatchChoice
			}
		}
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

	case autopilotTickMsg:
		if m.autopilotMode == "running" || m.autopilotMode == "stop-task-confirm" || m.autopilotMode == "restart-confirm" || m.autopilotMode == "resume-or-restart-confirm" || m.autopilotMode == "refresh-confirm" || m.autopilotMode == "review-confirm" || m.autopilotMode == "design-confirm" {
			m.rebuildAutopilotTaskContent()
			return m, autopilotTick()
		}
		return m, nil
	}

	return m, nil
}

type clearBannerMsg struct{}
type clearBroadcastStatusMsg struct{}
type clearOnboardStatusMsg struct{}
type clearRebuildDepsStatusMsg struct{}
type clearPollNoticeMsg struct{}
type clearFilterStatusMsg struct{}
type clearSettingsStatusMsg struct{}

func (m Model) updateNormal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
		case "G":
			m.mode = "dep-guidance"
			m.rebuildDepsStatus = ""
			m.rebuildDepsInput.Reset()
			if m.width > 4 {
				m.rebuildDepsInput.SetWidth(m.width - 4)
			}
			m.resizeViewports()
			return m, m.rebuildDepsInput.Focus()
		}
	}
	if m.autopilotMode == "reprepare-choice" && m.activeTab == tabAutopilot {
		switch msg.String() {
		case "k":
			// Keep existing graph, add new issues only.
			sup := m.autopilotSupervisor
			if sup == nil {
				m.autopilotMode = ""
				return m, nil
			}
			m.autopilotStatus = "Keeping existing graph..."
			m.polling = true
			return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
				ctx, cancel := opCtx()
				defer cancel()
				result, err := sup.ReprepareKeep(ctx)
				return reprepareKeepResultMsg{result: result, err: err}
			})
		case "r":
			// Rebuild full graph.
			sup := m.autopilotSupervisor
			if sup == nil {
				m.autopilotMode = ""
				return m, nil
			}
			m.autopilotStatus = "Rebuilding dependency graph..."
			m.polling = true
			guidance := m.autopilotDepGuidance
			return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
				ctx, cancel := opCtx()
				defer cancel()
				result, err := sup.ReprepareRebuild(ctx, guidance)
				return reprepareRebuildResultMsg{result: result, err: err}
			})
		case "esc", "n":
			m.autopilotMode = ""
			m.autopilotSupervisor = nil
			m.autopilotStatus = ""
			m.autopilotPrepareResult = nil
			return m, nil
		case "G":
			m.mode = "dep-guidance"
			m.rebuildDepsStatus = ""
			m.rebuildDepsInput.Reset()
			if m.width > 4 {
				m.rebuildDepsInput.SetWidth(m.width - 4)
			}
			m.resizeViewports()
			return m, m.rebuildDepsInput.Focus()
		}
	}
	if m.autopilotMode == "dep-select" && m.activeTab == tabAutopilot {
		switch msg.String() {
		case "left":
			if n := len(m.autopilotDepOptions); n > 0 {
				m.autopilotDepSelection = (m.autopilotDepSelection - 1 + n) % n
				m.autopilotUnblocked = m.autopilotDepOptions[m.autopilotDepSelection].Unblocked
			}
			return m, nil
		case "right":
			if n := len(m.autopilotDepOptions); n > 0 {
				m.autopilotDepSelection = (m.autopilotDepSelection + 1) % n
				m.autopilotUnblocked = m.autopilotDepOptions[m.autopilotDepSelection].Unblocked
			}
			return m, nil
		case "enter":
			return m.applyDepSelection()
		case "G":
			m.mode = "dep-guidance"
			m.rebuildDepsStatus = ""
			m.rebuildDepsInput.Reset()
			if m.width > 4 {
				m.rebuildDepsInput.SetWidth(m.width - 4)
			}
			m.resizeViewports()
			return m, m.rebuildDepsInput.Focus()
		case "esc":
			m.autopilotMode = ""
			m.autopilotSupervisor = nil
			m.autopilotStatus = ""
			m.autopilotDepOptions = nil
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
	if m.autopilotMode == "stop-task-confirm" && m.activeTab == tabAutopilot {
		switch msg.String() {
		case "enter":
			return m.confirmStopTask()
		case "esc":
			m.autopilotMode = "running"
			return m, nil
		}
	}
	if m.autopilotMode == "restart-confirm" && m.activeTab == tabAutopilot {
		switch msg.String() {
		case "enter":
			return m.confirmRestartTask()
		case "esc":
			m.autopilotMode = "running"
			return m, nil
		}
	}
	if m.autopilotMode == "resume-or-restart-confirm" && m.activeTab == tabAutopilot {
		switch msg.String() {
		case "r":
			return m.confirmResumeTask()
		case "b":
			return m.confirmBumpAndResumeTask()
		case "f":
			return m.confirmRestartTask()
		case "esc":
			m.autopilotMode = "running"
			return m, nil
		}
	}
	if m.autopilotMode == "refresh-confirm" && m.activeTab == tabAutopilot {
		switch msg.String() {
		case "enter":
			return m.confirmRefreshTask()
		case "esc":
			m.autopilotMode = "running"
			return m, nil
		}
	}
	if m.autopilotMode == "review-confirm" && m.activeTab == tabAutopilot {
		switch msg.String() {
		case "enter":
			return m.launchReviewSession()
		case "esc":
			m.autopilotMode = m.autopilotModeBeforeReview
			return m, nil
		}
	}
	if m.autopilotMode == "manual-confirm" && m.activeTab == tabAutopilot {
		switch msg.String() {
		case "enter":
			return m.launchManualSession()
		case "esc":
			m.autopilotMode = m.autopilotModeBeforeReview
			return m, nil
		}
	}
	if m.autopilotMode == "design-confirm" && m.activeTab == tabAutopilot {
		switch msg.String() {
		case "enter":
			return m.launchDesignSession()
		case "esc":
			m.autopilotMode = m.autopilotModeBeforeReview
			return m, nil
		}
	}
	if m.autopilotMode == "add-slot-confirm" && m.activeTab == tabAutopilot {
		switch msg.String() {
		case "enter":
			return m.confirmAddSlot()
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
	case "4":
		m.activeTab = tabObservability
		m.refreshObsCosts()
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
		if m.bannerClipboard != "" {
			if err := copyToClipboard(m.bannerClipboard); err == nil {
				m.warningBanner = "Command copied to clipboard"
			} else {
				m.warningBanner = ""
			}
			m.bannerClipboard = ""
			return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
				return clearBannerMsg{}
			})
		}
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
		// On tab 3 during autopilot, 'r' is task-contextual:
		//   - failed/stopped → resume-or-restart (offer resume in existing worktree)
		//   - bailed → restart (no useful work to resume)
		//   - review/reviewed → launch review session
		//   - manual → spin off worktree with preloaded context
		if m.activeTab == tabAutopilot && (m.autopilotMode == "running" || m.autopilotMode == "completed") {
			task := m.selectedAutopilotTask()
			if task != nil && (task.Status == "failed" || task.Status == "stopped") {
				m.autopilotMode = "resume-or-restart-confirm"
				return m, nil
			}
			if task != nil && task.Status == "bailed" {
				m.autopilotMode = "restart-confirm"
				return m, nil
			}
			if task != nil && (task.Status == "review" || task.Status == "reviewed") {
				m.autopilotModeBeforeReview = m.autopilotMode
				m.autopilotMode = "review-confirm"
				return m, nil
			}
			if task != nil && task.Status == "manual" {
				m.autopilotModeBeforeReview = m.autopilotMode
				m.autopilotMode = "manual-confirm"
				return m, nil
			}
			return m, nil
		}
		m.polling = true
		p := m.poller
		if m.activeTab == tabOperations {
			// Sync only — gather git/bus data without LLM analysis.
			return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
				ctx, cancel := opCtx()
				defer cancel()
				p.StatusNow(ctx)
				return nil
			})
		}
		return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
			ctx, cancel := opCtx()
			defer cancel()
			p.PollNow(ctx)
			return nil
		})
	case "R":
		// Refresh: reset a done/review/reviewed task back to queued.
		if m.activeTab == tabAutopilot && (m.autopilotMode == "running" || m.autopilotMode == "completed") {
			task := m.selectedAutopilotTask()
			if task != nil && (task.Status == "done" || task.Status == "review" || task.Status == "reviewing" || task.Status == "reviewed") {
				m.autopilotMode = "refresh-confirm"
				return m, nil
			}
		}
	case "g":
		// Design interview: available on any task during autopilot running/completed.
		if m.activeTab == tabAutopilot && (m.autopilotMode == "running" || m.autopilotMode == "completed") {
			task := m.selectedAutopilotTask()
			if task != nil {
				m.autopilotModeBeforeReview = m.autopilotMode
				m.autopilotMode = "design-confirm"
				return m, nil
			}
		}
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
		// Toggle failure detail expansion in autopilot task detail.
		if m.activeTab == tabAutopilot && (m.autopilotMode == "running" || m.autopilotMode == "completed") {
			task := m.selectedAutopilotTask()
			if task != nil && task.Status == "failed" && task.FailureDetail != "" {
				m.failureDetailExpanded = !m.failureDetailExpanded
				return m, nil
			}
		}
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
		// On autopilot tab, 'b' bumps limits for queued/failed/stopped tasks.
		if m.activeTab == tabAutopilot && (m.autopilotMode == "running" || m.autopilotMode == "completed") {
			task := m.selectedAutopilotTask()
			if task != nil && (task.Status == "queued" || task.Status == "failed" || task.Status == "stopped" || task.Status == "blocked") {
				return m.bumpTaskLimits()
			}
			return m, nil
		}
		repos := m.poller.GitHubRepos()
		if len(repos) == 0 {
			m.trackStatus = "No GitHub repos enrolled"
			return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
				return clearTrackStatusMsg{}
			})
		}
		hasExisting := len(m.operationsTasks) > 0
		m.filterState = newFilterState(repos, hasExisting)
		m.filterStatus = ""
		m.mode = "filter"
		return m, nil
	case "I":
		if len(m.operationsTasks) == 0 {
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
		// Build tracked items from operations tasks for the untrack form.
		var trackedForUntrack []db.TrackedItem
		for _, t := range m.operationsTasks {
			trackedForUntrack = append(trackedForUntrack, db.TrackedItem{
				Owner:  t.Owner,
				Repo:   t.Repo,
				Number: t.IssueNumber,
				Title:  t.IssueTitle,
			})
		}
		m.trackRows = buildTrackRows(ghRepos, trackedForUntrack)
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
	case "S":
		// On tab 3 during autopilot, 'S' stops the selected agent.
		if m.activeTab == tabAutopilot && m.autopilotMode == "running" {
			task := m.selectedAutopilotTask()
			if task != nil && task.Status == "running" {
				m.autopilotMode = "stop-task-confirm"
			}
			return m, nil
		}
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
			if (m.autopilotMode == "running" || m.autopilotMode == "completed") && len(m.autopilotTasks) > 0 {
				// Navigate cursor through task list.
				switch msg.String() {
				case "up":
					if m.autopilotCursor > 0 {
						m.autopilotCursor--
					} else {
						m.autopilotCursor = len(m.autopilotTasks) - 1 // wrap
					}
				case "down":
					if m.autopilotCursor < len(m.autopilotTasks)-1 {
						m.autopilotCursor++
					} else {
						m.autopilotCursor = 0 // wrap
					}
				case "pgup":
					m.autopilotCursor -= 5
					if m.autopilotCursor < 0 {
						m.autopilotCursor = 0
					}
				case "pgdown":
					m.autopilotCursor += 5
					if m.autopilotCursor >= len(m.autopilotTasks) {
						m.autopilotCursor = len(m.autopilotTasks) - 1
					}
				}
				m.autopilotSelectedIssue = m.autopilotTasks[m.autopilotCursor].IssueNumber
				m.failureDetailExpanded = false // reset on cursor change
				m.rebuildAutopilotTaskContent()
				// Ensure cursor is visible in viewport.
				m.autopilotTaskVP.SetYOffset(m.autopilotCursor)
			} else {
				m.autopilotTaskVP, cmd = m.autopilotTaskVP.Update(msg)
			}
		default:
			m.eventLogVP, cmd = m.eventLogVP.Update(msg)
		}
		return m, cmd

	// Per-agent controls (tab 3 only, when autopilot running).
	case "P":
		if m.activeTab != tabAutopilot || m.autopilotSupervisor == nil || m.autopilotMode != "running" {
			return m, nil
		}
		if m.autopilotPaused {
			ctx, cancel := opCtx()
			defer cancel()
			m.autopilotSupervisor.Resume(ctx)
			m.autopilotPaused = false
			m.autopilotStatus = "Resumed — filling slots"
		} else {
			m.autopilotSupervisor.Pause()
			m.autopilotPaused = true
			// Count running and queued.
			running, queued := 0, 0
			for _, t := range m.autopilotTasks {
				switch t.Status {
				case "running":
					running++
				case "queued":
					queued++
				}
			}
			m.autopilotStatus = fmt.Sprintf("Paused — %d agents still running, %d queued", running, queued)
		}
		return m, tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
			return clearAutopilotStatusMsg{}
		})

	case "+":
		if m.activeTab != tabAutopilot || m.autopilotSupervisor == nil || m.autopilotMode != "running" {
			return m, nil
		}
		currentSlots := len(m.autopilotSupervisor.SlotStatus())
		m.autopilotMode = "add-slot-confirm"
		m.autopilotStatus = fmt.Sprintf("Add slot? Currently %d slots.", currentSlots)
		return m, nil

	case "c":
		if m.activeTab != tabAutopilot || (m.autopilotMode != "running" && m.autopilotMode != "completed") {
			return m, nil
		}
		task := m.selectedAutopilotTask()
		if task == nil || task.WorktreePath == "" {
			return m, nil
		}
		if err := copyToClipboard(task.WorktreePath); err == nil {
			m.autopilotStatus = fmt.Sprintf("Copied: %s", task.WorktreePath)
		} else {
			m.autopilotStatus = fmt.Sprintf("Copy failed: %v", err)
		}
		return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return clearAutopilotStatusMsg{}
		})

	case "l":
		if m.activeTab != tabAutopilot || (m.autopilotMode != "running" && m.autopilotMode != "completed") {
			return m, nil
		}
		task := m.selectedAutopilotTask()
		if task == nil {
			return m, nil
		}
		if task.AgentLog == "" {
			m.autopilotStatus = "No log available for this task"
			return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
				return clearAutopilotStatusMsg{}
			})
		}
		taskCopy := *task
		cmd := m.openLogViewer(&taskCopy)
		return m, cmd

	case "D":
		if m.activeTab != tabAutopilot || (m.autopilotMode != "running" && m.autopilotMode != "completed") {
			return m, nil
		}
		task := m.selectedAutopilotTask()
		if task == nil {
			return m, nil
		}
		deps := m.taskBlockedContext(*task)
		blocks := m.taskBlocksContext(*task)
		if deps == "" && blocks == "" {
			m.autopilotStatus = fmt.Sprintf("#%d has no dependencies", task.IssueNumber)
		} else {
			var parts []string
			if deps != "" {
				parts = append(parts, deps)
			}
			if blocks != "" {
				parts = append(parts, blocks)
			}
			m.autopilotStatus = fmt.Sprintf("#%d: %s", task.IssueNumber, strings.Join(parts, " | "))
		}
		return m, tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
			return clearAutopilotStatusMsg{}
		})

	case "G":
		if m.activeTab != tabAutopilot || (m.autopilotMode != "running" && m.autopilotMode != "completed" && m.autopilotMode != "confirm") {
			return m, nil
		}
		if m.autopilotSupervisor == nil {
			return m, nil
		}
		m.mode = "rebuild-deps"
		m.rebuildDepsStatus = ""
		m.rebuildDepsInput.Reset()
		if m.width > 4 {
			m.rebuildDepsInput.SetWidth(m.width - 4)
		}
		m.resizeViewports()
		cmd := m.rebuildDepsInput.Focus()
		return m, cmd
	}
	return m, nil
}

func (m Model) updateLogViewer(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		m.closeLogViewer()
		m.resizeViewports()
		return m, nil
	case "c":
		cmd := m.copyLogPath()
		return m, cmd
	case "up", "down", "pgup", "pgdown":
		var cmd tea.Cmd
		m.logViewerVP, cmd = m.logViewerVP.Update(msg)
		// Track whether user is at the bottom for auto-scroll.
		m.logViewerAtBottom = m.logViewerVP.AtBottom()
		return m, cmd
	}
	// Swallow all other keys — modal overlay.
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
			ctx, cancel := opCtx()
			defer cancel()
			busMsg, err := p.Broadcast(ctx, value)
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
		m.userMsgStatus = "Asking..."
		m.userMsgInput.Blur()
		p := m.poller
		return m, func() tea.Msg {
			ctx, cancel := opCtx()
			defer cancel()
			response, err := p.QueryAnalyzer(ctx, value)
			if err != nil {
				return userMsgResultMsg{err: err}
			}
			return userMsgResultMsg{response: response}
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
			ctx, cancel := opCtx()
			defer cancel()
			busMsg, err := p.Onboard(ctx, guidance)
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

func (m Model) updateDepGuidance(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = "normal"
		m.rebuildDepsInput.Blur()
		m.resizeViewports()
		return m, nil
	case "ctrl+d":
		m.autopilotDepGuidance = m.rebuildDepsInput.Value()
		m.mode = "normal"
		m.rebuildDepsInput.Blur()
		m.resizeViewports()
		if strings.TrimSpace(m.autopilotDepGuidance) != "" {
			// If we're in dep-select or scan-confirm with options, regenerate immediately.
			if m.autopilotMode == "dep-select" && m.autopilotSupervisor != nil {
				return m.prepareAutopilot()
			}
			m.autopilotStatus = "Guidance saved — will apply during analysis"
		} else {
			m.autopilotStatus = ""
			m.autopilotDepGuidance = ""
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.rebuildDepsInput, cmd = m.rebuildDepsInput.Update(msg)
	return m, cmd
}

func (m Model) updateRebuildDeps(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = "normal"
		m.rebuildDepsStatus = ""
		m.rebuildDepsInput.Blur()
		m.resizeViewports()
		return m, nil
	case "ctrl+d":
		guidance := m.rebuildDepsInput.Value()
		m.rebuildDepsStatus = "Rebuilding dependency graph..."
		m.rebuildDepsInput.Blur()
		sup := m.autopilotSupervisor
		return m, func() tea.Msg {
			ctx, cancel := opCtx()
			defer cancel()
			options, err := sup.RebuildDependencies(ctx, guidance)
			return rebuildDepsResultMsg{options: options, err: err}
		}
	}

	var cmd tea.Cmd
	m.rebuildDepsInput, cmd = m.rebuildDepsInput.Update(msg)
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

	// Cleanup confirm step: enter to proceed, esc to go back.
	if m.trackStep == trackStepCleanupConfirm {
		switch msg.String() {
		case "enter":
			store := m.store
			projectID := m.project.ID
			m.trackStep = trackStepFetching
			m.trackStatus = fmt.Sprintf("Cleaning up %d items...", m.trackCleanupCount)
			return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
				removed, err := store.ArchiveTerminalTrackedItems(projectID)
				return cleanupResultMsg{removed: removed, err: err}
			})
		case "esc":
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
			ctx, cancel := opCtx()
			defer cancel()
			var items []trackPreviewItem
			for _, ref := range refs {
				status, err := p.FetchItemStatus(ctx, ref)
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

	// For untrack, look up titles from operations tasks (no API call needed).
	type taskInfo struct {
		Title  string
		Status string
	}
	titleMap := make(map[string]taskInfo, len(m.operationsTasks))
	for _, t := range m.operationsTasks {
		key := fmt.Sprintf("%s/%s#%d", t.Owner, t.Repo, t.IssueNumber)
		titleMap[key] = taskInfo{Title: t.IssueTitle, Status: t.Status}
	}
	var items []trackPreviewItem
	for _, ref := range refs {
		key := fmt.Sprintf("%s/%s#%d", ref.Owner, ref.Repo, ref.Number)
		title := fmt.Sprintf("#%d", ref.Number)
		status := "queued"
		if info, ok := titleMap[key]; ok {
			if info.Title != "" {
				title = info.Title
			}
			if info.Status != "" {
				status = info.Status
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
			ctx, cancel := opCtx()
			defer cancel()
			var added, failed int
			var errs []string
			for _, ref := range refs {
				_, err := p.AddTrackedItemByRef(ctx, ref)
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

// opCtx returns a context with a 2-minute timeout for TUI async operations.
// This prevents indefinite hangs when network or database connections are stale
// (e.g., after laptop sleep/wake).
func opCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 2*time.Minute)
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

	// Check prerequisites: need tracked issues, existing tasks, or a watch filter.
	// Watch filters (milestone/label) discover issues via watchPoll() after launch,
	// so skip the tracked-items check when a filter is configured.
	hasWatchFilter := m.project.AutopilotFilterType != "" && m.project.AutopilotFilterValue != ""
	existingTasks, _ := m.store.GetAutopilotTasks(m.project.ID)
	if len(existingTasks) == 0 && !hasWatchFilter {
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
		m.store, m.project, m.poller.Completer(),
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
	guidance := m.autopilotDepGuidance

	return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
		ctx, cancel := opCtx()
		defer cancel()
		result, err := sup.Prepare(ctx, guidance)
		return autopilotPrepareResultMsg{result: result, err: err}
	})
}

// applyDepSelection applies the selected dep option and transitions to confirm.
func (m Model) applyDepSelection() (tea.Model, tea.Cmd) {
	sup := m.autopilotSupervisor
	if sup == nil || len(m.autopilotDepOptions) == 0 {
		m.autopilotMode = ""
		return m, nil
	}

	idx := m.autopilotDepSelection
	if idx >= len(m.autopilotDepOptions) {
		idx = 0
	}
	opt := m.autopilotDepOptions[idx]

	// If we came from a running rebuild, use ApplyRebuildDepOption.
	if m.autopilotSupervisor != nil && m.autopilotSupervisor.IsActive() {
		m.autopilotMode = "running"
		m.autopilotDepOptions = nil
		return m, func() tea.Msg {
			ctx, cancel := opCtx()
			defer cancel()
			result, err := sup.ApplyRebuildDepOption(ctx, opt)
			return applyDepOptionResultMsg{result: result, err: err}
		}
	}

	// Initial prepare flow — apply and go to confirm.
	m.autopilotStatus = "Applying dependencies..."
	m.polling = true
	return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
		ctx, cancel := opCtx()
		defer cancel()
		err := sup.ApplyDepOption(ctx, opt)
		if err != nil {
			return autopilotApplyResultMsg{err: err}
		}
		// Count active and unblocked tasks.
		allTasks, err := m.store.GetAutopilotTasks(m.project.ID)
		if err != nil {
			return autopilotApplyResultMsg{err: err}
		}
		active := 0
		for _, t := range allTasks {
			if t.Status != "skipped" {
				active++
			}
		}
		unblockedTasks, err := m.store.QueuedUnblockedTasks(m.project.ID)
		if err != nil {
			return autopilotApplyResultMsg{err: err}
		}
		return autopilotApplyResultMsg{total: active, unblocked: len(unblockedTasks)}
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

	// Wire autopilot dependency graph into poller for tier 2 analyzer context.
	m.poller.SetAutopilotDepGraphFunc(sup.DepGraph)

	// Halve status check frequency during autopilot for faster review gate checks.
	m.origPollInterval = m.project.StatusInterval()
	newInterval := m.origPollInterval / 2
	if newInterval < 15*time.Second {
		newInterval = 15 * time.Second
	}
	m.poller.SetStatusInterval(newInterval)

	// Supervisor stays alive after all tasks complete — shows idle slots.
	// User presses A to stop explicitly.
	sup.SetDaemonMode(true)
	// Launch uses a long-lived context — autopilot runs until explicitly stopped.
	sup.Launch(context.Background())

	// Show temporary notification with autopilot limits.
	maxAgents := m.project.AutopilotMaxAgents
	if maxAgents < 1 {
		maxAgents = 3
	}
	m.autopilotStatus = fmt.Sprintf(
		"Autopilot running in background — %d agents max — press A to stop",
		maxAgents,
	)

	return m, tea.Batch(
		listenForAutopilotEvents(sup),
		autopilotTick(),
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

// confirmStopTask stops the selected agent after user confirms.
func (m Model) confirmStopTask() (tea.Model, tea.Cmd) {
	task := m.selectedAutopilotTask()
	if task == nil || task.Status != "running" {
		m.autopilotMode = "running"
		return m, nil
	}

	// Find the slot index for this task.
	sup := m.autopilotSupervisor
	if sup == nil {
		m.autopilotMode = "running"
		return m, nil
	}
	slots := sup.SlotStatus()
	slotIdx := -1
	for i, slot := range slots {
		if slot.IssueNumber == task.IssueNumber && slot.Status == "running" {
			slotIdx = i
			break
		}
	}
	if slotIdx < 0 {
		m.autopilotMode = "running"
		m.autopilotStatus = fmt.Sprintf("#%d not found in active slots", task.IssueNumber)
		return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return clearAutopilotStatusMsg{}
		})
	}

	sup.StopAgent(slotIdx)
	m.autopilotMode = "running"
	m.autopilotStatus = fmt.Sprintf("Stopping agent on #%d...", task.IssueNumber)
	return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
		return clearAutopilotStatusMsg{}
	})
}

// confirmRestartTask restarts the selected bailed/stopped task after user confirms.
func (m Model) confirmRestartTask() (tea.Model, tea.Cmd) {
	task := m.selectedAutopilotTask()
	if task == nil || (task.Status != "failed" && task.Status != "bailed" && task.Status != "stopped") {
		m.autopilotMode = "running"
		return m, nil
	}

	sup := m.autopilotSupervisor
	if sup == nil {
		m.autopilotMode = "running"
		return m, nil
	}

	taskID := task.ID
	issueNum := task.IssueNumber
	m.autopilotMode = "running"

	return m, func() tea.Msg {
		ctx, cancel := opCtx()
		defer cancel()
		if err := sup.RestartTask(ctx, taskID); err != nil {
			return autopilotEventMsg(autopilot.Event{
				Time:    time.Now(),
				Type:    "error",
				Summary: fmt.Sprintf("Failed to restart #%d: %v", issueNum, err),
			})
		}
		return autopilotEventMsg(autopilot.Event{
			Time:    time.Now(),
			Type:    "started",
			Summary: fmt.Sprintf("Restarted #%d — re-queued", issueNum),
		})
	}
}

// confirmRefreshTask resets a done/review/reviewed task back to queued.
func (m Model) confirmRefreshTask() (tea.Model, tea.Cmd) {
	task := m.selectedAutopilotTask()
	if task == nil || (task.Status != "done" && task.Status != "review" && task.Status != "reviewing" && task.Status != "reviewed") {
		m.autopilotMode = "running"
		return m, nil
	}

	sup := m.autopilotSupervisor
	if sup == nil {
		m.autopilotMode = "running"
		return m, nil
	}

	taskID := task.ID
	issueNum := task.IssueNumber
	m.autopilotMode = "running"

	return m, func() tea.Msg {
		ctx, cancel := opCtx()
		defer cancel()
		if err := sup.RefreshTask(ctx, taskID); err != nil {
			return autopilotEventMsg(autopilot.Event{
				Time:    time.Now(),
				Type:    "error",
				Summary: fmt.Sprintf("Failed to refresh #%d: %v", issueNum, err),
			})
		}
		return autopilotEventMsg(autopilot.Event{
			Time:    time.Now(),
			Type:    "started",
			Summary: fmt.Sprintf("Refreshed #%d — re-queued", issueNum),
		})
	}
}

// confirmResumeTask resumes the selected failed/stopped task in its existing worktree.
func (m Model) confirmResumeTask() (tea.Model, tea.Cmd) {
	task := m.selectedAutopilotTask()
	if task == nil || (task.Status != "failed" && task.Status != "stopped") {
		m.autopilotMode = "running"
		return m, nil
	}

	sup := m.autopilotSupervisor
	if sup == nil {
		m.autopilotMode = "running"
		return m, nil
	}

	taskID := task.ID
	issueNum := task.IssueNumber
	m.autopilotMode = "running"

	return m, func() tea.Msg {
		ctx, cancel := opCtx()
		defer cancel()
		if err := sup.ResumeTask(ctx, taskID); err != nil {
			return autopilotEventMsg(autopilot.Event{
				Time:    time.Now(),
				Type:    "error",
				Summary: fmt.Sprintf("Failed to resume #%d: %v", issueNum, err),
			})
		}
		return autopilotEventMsg(autopilot.Event{
			Time:    time.Now(),
			Type:    "started",
			Summary: fmt.Sprintf("Resumed #%d in existing worktree", issueNum),
		})
	}
}

// confirmBumpAndResumeTask bumps the task limits by 1.5x, then resumes in existing worktree.
func (m Model) confirmBumpAndResumeTask() (tea.Model, tea.Cmd) {
	task := m.selectedAutopilotTask()
	if task == nil || (task.Status != "failed" && task.Status != "stopped") {
		m.autopilotMode = "running"
		return m, nil
	}

	sup := m.autopilotSupervisor
	if sup == nil {
		m.autopilotMode = "running"
		return m, nil
	}

	taskID := task.ID
	issueNum := task.IssueNumber
	m.autopilotMode = "running"

	return m, func() tea.Msg {
		ctx, cancel := opCtx()
		defer cancel()
		newTurns, newBudget, err := sup.BumpTaskLimits(taskID)
		if err != nil {
			return autopilotEventMsg(autopilot.Event{
				Time:    time.Now(),
				Type:    "error",
				Summary: fmt.Sprintf("Failed to bump limits for #%d: %v", issueNum, err),
			})
		}
		if err := sup.ResumeTask(ctx, taskID); err != nil {
			return autopilotEventMsg(autopilot.Event{
				Time:    time.Now(),
				Type:    "error",
				Summary: fmt.Sprintf("Bumped limits but failed to resume #%d: %v", issueNum, err),
			})
		}
		return autopilotEventMsg(autopilot.Event{
			Time:    time.Now(),
			Type:    "started",
			Summary: fmt.Sprintf("Resumed #%d with bumped limits (%d turns, $%.2f)", issueNum, newTurns, newBudget),
		})
	}
}

// bumpTaskLimits bumps the limits for a queued/failed/stopped/blocked task by 1.5x.
func (m Model) bumpTaskLimits() (tea.Model, tea.Cmd) {
	task := m.selectedAutopilotTask()
	if task == nil {
		return m, nil
	}

	sup := m.autopilotSupervisor
	if sup == nil {
		return m, nil
	}

	taskID := task.ID
	issueNum := task.IssueNumber

	return m, func() tea.Msg {
		newTurns, newBudget, err := sup.BumpTaskLimits(taskID)
		if err != nil {
			return autopilotEventMsg(autopilot.Event{
				Time:    time.Now(),
				Type:    "error",
				Summary: fmt.Sprintf("Failed to bump limits for #%d: %v", issueNum, err),
			})
		}
		return autopilotEventMsg(autopilot.Event{
			Time:    time.Now(),
			Type:    "info",
			Summary: fmt.Sprintf("Bumped #%d limits to %d turns, $%.2f", issueNum, newTurns, newBudget),
		})
	}
}

// confirmAddSlot adds a new slot to the supervisor and persists the setting.
func (m Model) confirmAddSlot() (tea.Model, tea.Cmd) {
	sup := m.autopilotSupervisor
	if sup == nil {
		m.autopilotMode = "running"
		return m, nil
	}

	ctx, cancel := opCtx()
	defer cancel()
	newCount := sup.AddSlot(ctx)

	// Persist to project settings.
	m.project.AutopilotMaxAgents = newCount
	_ = m.store.UpdateProject(m.project)

	m.autopilotMode = "running"
	m.autopilotStatus = fmt.Sprintf("Now %d slots — filling if tasks available", newCount)
	return m, tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
		return clearAutopilotStatusMsg{}
	})
}

// launchReviewSession restores the worktree and launches a pre-warmed Claude session for review.
func (m Model) launchReviewSession() (tea.Model, tea.Cmd) {
	task := m.selectedAutopilotTask()
	if task == nil || (task.Status != "review" && task.Status != "reviewed") {
		m.autopilotMode = m.autopilotModeBeforeReview
		return m, nil
	}

	sup := m.autopilotSupervisor
	if sup == nil {
		m.autopilotMode = m.autopilotModeBeforeReview
		return m, nil
	}

	taskID := task.ID
	issueNum := task.IssueNumber
	m.autopilotMode = m.autopilotModeBeforeReview
	m.autopilotStatus = fmt.Sprintf("Launching review session for #%d...", issueNum)

	return m, func() tea.Msg {
		ctx, cancel := opCtx()
		defer cancel()
		result, err := sup.ReviewSession(ctx, taskID)
		return reviewSessionResultMsg{result: result, err: err}
	}
}

// launchManualSession creates a worktree and launches a pre-warmed Claude session for a manual task.
func (m Model) launchManualSession() (tea.Model, tea.Cmd) {
	task := m.selectedAutopilotTask()
	if task == nil || task.Status != "manual" {
		m.autopilotMode = m.autopilotModeBeforeReview
		return m, nil
	}

	sup := m.autopilotSupervisor
	if sup == nil {
		m.autopilotMode = m.autopilotModeBeforeReview
		return m, nil
	}

	taskID := task.ID
	issueNum := task.IssueNumber
	m.autopilotMode = m.autopilotModeBeforeReview
	m.autopilotStatus = fmt.Sprintf("Spinning off worktree for #%d...", issueNum)

	return m, func() tea.Msg {
		ctx, cancel := opCtx()
		defer cancel()
		result, err := sup.ManualSession(ctx, taskID)
		return reviewSessionResultMsg{result: result, err: err}
	}
}

// launchDesignSession creates a worktree and launches a pre-warmed Claude session for a design interview.
func (m Model) launchDesignSession() (tea.Model, tea.Cmd) {
	task := m.selectedAutopilotTask()
	if task == nil {
		m.autopilotMode = m.autopilotModeBeforeReview
		return m, nil
	}

	sup := m.autopilotSupervisor
	if sup == nil {
		m.autopilotMode = m.autopilotModeBeforeReview
		return m, nil
	}

	taskID := task.ID
	issueNum := task.IssueNumber
	m.autopilotMode = m.autopilotModeBeforeReview
	m.autopilotStatus = fmt.Sprintf("Launching design interview for #%d...", issueNum)

	return m, func() tea.Msg {
		ctx, cancel := opCtx()
		defer cancel()
		result, err := sup.DesignSession(ctx, taskID)
		return reviewSessionResultMsg{result: result, err: err}
	}
}

// autopilotTick returns a command that fires every 5 seconds for task list refresh.
func autopilotTick() tea.Cmd {
	return tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
		return autopilotTickMsg(t)
	})
}

// selectedAutopilotTask returns the currently selected task from the task list, or nil.
func (m Model) selectedAutopilotTask() *db.AutopilotTask {
	if len(m.autopilotTasks) == 0 || m.autopilotCursor < 0 || m.autopilotCursor >= len(m.autopilotTasks) {
		return nil
	}
	t := m.autopilotTasks[m.autopilotCursor]
	return &t
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

// refreshTrackedItems reloads operations tasks from autopilot_tasks,
// filtering out terminal and removed statuses.
func (m *Model) refreshTrackedItems() {
	allTasks, err := m.store.GetAutopilotTasks(m.project.ID)
	if err != nil {
		m.operationsTasks = nil
		return
	}
	m.operationsTasks = m.operationsTasks[:0]
	for _, t := range allTasks {
		switch t.Status {
		case "removed", "done", "skipped":
			continue
		default:
			m.operationsTasks = append(m.operationsTasks, t)
		}
	}
}

// refreshObsCosts reloads cost data for the observability tab.
func (m *Model) refreshObsCosts() {
	today := time.Now().Format("2006-01-02")
	m.obsDailyCost, m.obsCostErr = m.store.DailyCost(m.project.ID, today)
	if m.obsCostErr != nil {
		return
	}
	m.obsWeeklyCost, m.obsCostErr = m.store.WeeklyCost(m.project.ID, today)
	if m.obsCostErr != nil {
		return
	}
	m.obsOverallCost, m.obsCostErr = m.store.OverallCost(m.project.ID)
	if m.obsCostErr != nil {
		return
	}
	m.obsDailyTaskCosts, m.obsCostErr = m.store.DailyTaskCosts(m.project.ID, today)
}

// taskDisplayStatus returns a short display status for an autopilot task.
func taskDisplayStatus(status string) string {
	switch status {
	case "queued":
		return "Queue"
	case "running":
		return "Run"
	case "review", "reviewing", "reviewed":
		return "Revew"
	case "blocked":
		return "Blckd"
	case "manual":
		return "Manul"
	case "bailed":
		return "Baild"
	case "failed":
		return "Faild"
	case "pending":
		return "Pend"
	default:
		return status
	}
}

// clearTabIndicator clears the new-data indicator for the currently active tab.
func (m *Model) clearTabIndicator() {
	switch m.activeTab {
	case tabAnalysis:
		m.analysisHasNew = false
	case tabAutopilot:
		m.autopilotHasNew = false
	case tabObservability:
		m.refreshObsCosts()
	}
}

// formatTimeAgo returns a human-readable relative time like "3m ago" or "2h ago".
func formatTimeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m > 0 {
			return fmt.Sprintf("%dh%dm ago", h, m)
		}
		return fmt.Sprintf("%dh ago", h)
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
