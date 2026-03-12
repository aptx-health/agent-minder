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

// addTrackedItemResultMsg is sent when a tracked item add completes.
type addTrackedItemResultMsg struct {
	item *db.TrackedItem
	err  error
}

// removeTrackedItemResultMsg is sent when a tracked item remove completes.
type removeTrackedItemResultMsg struct {
	ref string
	err error
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
	trackInput  textinput.Model
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

	ti := textinput.New()
	ti.Placeholder = "#42 or owner/repo#42"
	ti.CharLimit = 100
	ti.SetWidth(40)

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
		spinner:        sp,
		trackInput:     ti,
		analysisVP:     aVP,
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
		return m, nil

	case addTrackedItemResultMsg:
		if msg.err != nil {
			m.trackStatus = fmt.Sprintf("Error: %v", msg.err)
		} else {
			m.trackStatus = fmt.Sprintf("Now tracking %s: %s [%s]", msg.item.DisplayRef(), msg.item.Title, msg.item.LastStatus)
			m.trackedItems, _ = m.store.GetTrackedItems(m.project.ID)
		}
		return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return clearTrackStatusMsg{}
		})

	case removeTrackedItemResultMsg:
		if msg.err != nil {
			m.trackStatus = fmt.Sprintf("Error: %v", msg.err)
		} else {
			m.trackStatus = fmt.Sprintf("Untracked %s", msg.ref)
			m.trackedItems, _ = m.store.GetTrackedItems(m.project.ID)
		}
		return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return clearTrackStatusMsg{}
		})

	case clearTrackStatusMsg:
		m.trackStatus = ""
		m.mode = "normal"
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
		m.mode = "track"
		m.trackStatus = ""
		m.trackInput.Reset()
		m.trackInput.Placeholder = "#42 or owner/repo#42"
		cmd := m.trackInput.Focus()
		return m, cmd
	case "I":
		m.mode = "untrack"
		m.trackStatus = ""
		m.trackInput.Reset()
		m.trackInput.Placeholder = "#42 or owner/repo#42"
		cmd := m.trackInput.Focus()
		return m, cmd
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
		cmd := m.userMsgInput.Focus()
		return m, cmd
	case "m":
		m.mode = "broadcast"
		m.broadcastStatus = ""
		m.broadcastInput.Reset()
		if m.width > 4 {
			m.broadcastInput.SetWidth(m.width - 4)
		}
		cmd := m.broadcastInput.Focus()
		return m, cmd
	case "o":
		m.mode = "onboard"
		m.onboardStatus = ""
		m.onboardInput.Reset()
		if m.width > 4 {
			m.onboardInput.SetWidth(m.width - 4)
		}
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
		m.trackInput.Blur()
		return m, nil
	case "enter":
		value := m.trackInput.Value()
		if strings.TrimSpace(value) == "" {
			m.mode = "normal"
			m.trackInput.Blur()
			return m, nil
		}

		defaultOwner, defaultRepo := m.poller.DefaultOwnerRepo()
		ref, err := ghpkg.ParseItemRef(value, defaultOwner, defaultRepo)
		if err != nil {
			m.trackStatus = fmt.Sprintf("Error: %v", err)
			return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
				return clearTrackStatusMsg{}
			})
		}

		m.trackInput.Blur()
		mode := m.mode
		p := m.poller

		if mode == "untrack" {
			m.trackStatus = fmt.Sprintf("Removing %s/%s#%d...", ref.Owner, ref.Repo, ref.Number)
			return m, func() tea.Msg {
				err := p.RemoveTrackedItemByRef(ref)
				return removeTrackedItemResultMsg{
					ref: fmt.Sprintf("%s/%s#%d", ref.Owner, ref.Repo, ref.Number),
					err: err,
				}
			}
		}

		// Track mode: resolve via GitHub API.
		m.trackStatus = fmt.Sprintf("Resolving %s/%s#%d...", ref.Owner, ref.Repo, ref.Number)
		return m, func() tea.Msg {
			item, err := p.AddTrackedItemByRef(context.Background(), ref)
			return addTrackedItemResultMsg{item: item, err: err}
		}
	}

	var cmd tea.Cmd
	m.trackInput, cmd = m.trackInput.Update(msg)
	return m, cmd
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
