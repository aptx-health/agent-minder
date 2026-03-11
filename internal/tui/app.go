// Package tui implements the bubbletea-based dashboard for agent-minder.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"github.com/dustinlange/agent-minder/internal/db"
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

// Model is the root bubbletea model for the dashboard.
type Model struct {
	project *db.Project
	store   *db.Store
	poller  *poller.Poller
	width   int
	height  int

	// State.
	events       []poller.Event
	lastPoll     *poller.PollResult
	pollExpanded bool
	err          error

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
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		listenForEvents(m.poller),
		tickEvery(),
		m.spinner.Tick,
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyPressMsg:
		switch m.mode {
		case "broadcast":
			return m.updateBroadcast(msg)
		case "usermsg":
			return m.updateUserMsg(msg)
		case "onboard":
			return m.updateOnboard(msg)
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
		}
		return m, listenForEvents(m.poller)

	case broadcastResultMsg:
		if msg.err != nil {
			m.broadcastStatus = fmt.Sprintf("Error: %v", msg.err)
		} else {
			m.broadcastStatus = fmt.Sprintf("Sent to %s", msg.topic)
		}
		// Clear status after a delay by returning to normal mode.
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
		m.pollExpanded = !m.pollExpanded
		return m, nil
	case "t":
		cycleTheme()
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

	// Delegate to textarea (Enter inserts newline by default).
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

	// Delegate to textarea (Enter inserts newline by default).
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

func (m Model) View() tea.View {
	if m.width == 0 {
		return tea.NewView("Loading...")
	}

	var b strings.Builder

	// Header.
	status := statusRunningStyle().Render("RUNNING")
	if m.poller.IsPaused() {
		status = statusPausedStyle().Render("PAUSED")
	}
	b.WriteString(titleStyle().Render(fmt.Sprintf("agent-minder: %s", m.project.Name)))
	b.WriteString("  ")
	b.WriteString(status)
	if m.polling {
		b.WriteString("  ")
		b.WriteString(m.spinner.View())
		b.WriteString(" ")
		b.WriteString(mutedStyle().Render("polling..."))
	}
	b.WriteString("  ")
	b.WriteString(mutedStyle().Render(fmt.Sprintf("[%s]", currentTheme().Name)))
	b.WriteString("\n")

	// Goal.
	goalText := fmt.Sprintf("  %s — %s", m.project.GoalType, m.project.GoalDescription)
	b.WriteString(mutedStyle().Width(m.width).Render(goalText))
	b.WriteString("\n\n")

	// Repos section.
	repos, _ := m.store.GetRepos(m.project.ID)
	b.WriteString(headerStyle().Render("Repos"))
	b.WriteString("\n")
	for _, r := range repos {
		b.WriteString(textStyle().Render(fmt.Sprintf("  %s (%s)", r.ShortName, r.Path)))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// Topics.
	topics, _ := m.store.GetTopics(m.project.ID)
	if len(topics) > 0 {
		b.WriteString(headerStyle().Render("Topics"))
		b.WriteString("\n")
		for _, t := range topics {
			b.WriteString(textStyle().Render(fmt.Sprintf("  %s", t.Name)))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Active concerns (capped to avoid pushing controls off screen).
	concerns, _ := m.store.ActiveConcerns(m.project.ID)
	if len(concerns) > 0 {
		maxConcerns := 5
		b.WriteString(headerStyle().Render(fmt.Sprintf("Active Concerns (%d)", len(concerns))))
		b.WriteString("\n")
		shown := concerns
		if len(shown) > maxConcerns {
			shown = shown[:maxConcerns]
		}
		for _, c := range shown {
			style := concernInfoStyle().Width(m.width - 2)
			prefix := "INFO"
			switch c.Severity {
			case "warning":
				style = concernWarningStyle().Width(m.width - 2)
				prefix = "WARN"
			case "danger":
				style = concernDangerStyle().Width(m.width - 2)
				prefix = "DANGER"
			}
			b.WriteString(style.Render(fmt.Sprintf("  [%s] %s", prefix, c.Message)))
			b.WriteString("\n")
		}
		if len(concerns) > maxConcerns {
			b.WriteString(mutedStyle().Render(fmt.Sprintf("  ... +%d more", len(concerns)-maxConcerns)))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Last poll result.
	expandHint := "e: expand"
	if m.pollExpanded {
		expandHint = "e: collapse"
	}
	b.WriteString(headerStyle().Render("Last Poll"))
	b.WriteString("  ")
	b.WriteString(mutedStyle().Render(fmt.Sprintf("[%s]", expandHint)))
	b.WriteString("\n")
	if m.lastPoll != nil {
		b.WriteString(mutedStyle().Render(fmt.Sprintf("  %d commits, %d messages (took %s)",
			m.lastPoll.NewCommits, m.lastPoll.NewMessages, m.lastPoll.Duration.Round(time.Millisecond))))
		b.WriteString("\n")
		if m.lastPoll.BusMessageSent != "" {
			b.WriteString(broadcastStyle().Render(fmt.Sprintf("  >> Bus: %s", m.lastPoll.BusMessageSent)))
			b.WriteString("\n")
		}
		response := m.lastPoll.LLMResponse()
		if response != "" {
			if !m.pollExpanded {
				// Truncate to ~3 lines.
				lines := strings.Split(response, "\n")
				if len(lines) > 3 {
					response = strings.Join(lines[:3], "\n") + "\n  ..."
				}
			}
			b.WriteString(llmResponseStyle().Width(m.width - 2).Render(response))
			b.WriteString("\n")
		}
	} else {
		b.WriteString(mutedStyle().Render("  Waiting for first poll..."))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// Event log — dynamically sized to fit remaining terminal height.
	// Count lines used so far (approximate: count newlines in buffer).
	linesUsed := strings.Count(b.String(), "\n")
	bottomLines := 4 // help bar + broadcast bar + padding
	maxEvents := m.height - linesUsed - bottomLines
	if maxEvents < 3 {
		maxEvents = 3
	}
	if maxEvents > 10 {
		maxEvents = 10
	}

	b.WriteString(headerStyle().Render("Event Log"))
	b.WriteString("\n")
	start := 0
	if len(m.events) > maxEvents {
		start = len(m.events) - maxEvents
	}
	for i := len(m.events) - 1; i >= start; i-- {
		e := m.events[i]
		ts := e.Time.Format("15:04:05")
		b.WriteString(mutedStyle().Render(fmt.Sprintf("  [%s] %s: %s", ts, e.Type, e.Summary)))
		b.WriteString("\n")
	}
	if len(m.events) == 0 {
		b.WriteString(mutedStyle().Render("  (no events yet)"))
		b.WriteString("\n")
	}

	// Bottom bar: input or help.
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
			b.WriteString(helpStyle().Render("ctrl+d: send • esc: cancel"))
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
			b.WriteString(helpStyle().Render("ctrl+d: send • esc: cancel"))
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
			b.WriteString(headerStyle().Render("  Onboard — optional guidance for the new agent:"))
			b.WriteString("\n")
			b.WriteString("  ")
			b.WriteString(m.onboardInput.View())
		}
		b.WriteString("\n")
		if m.onboardStatus == "" {
			b.WriteString(helpStyle().Render("ctrl+d: generate & publish • esc: cancel (leave empty for generic onboarding)"))
		}
		b.WriteString("\n")
	default:
		if m.broadcastStatus != "" {
			b.WriteString(broadcastStyle().Render(fmt.Sprintf("  %s", m.broadcastStatus)))
			b.WriteString("\n")
		}
		if m.userMsgStatus != "" {
			b.WriteString(userMsgStyle().Render(fmt.Sprintf("  %s", m.userMsgStatus)))
			b.WriteString("\n")
		}
		b.WriteString(renderHelpBar(m.width))
		b.WriteString("\n")
	}

	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
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

// renderHelpBar builds a two-row help bar with styled key hints.
func renderHelpBar(width int) string {
	keyStyle := helpKeyStyle()
	descStyle := helpStyle()
	sep := descStyle.Render(" • ")

	type hint struct {
		key  string
		desc string
	}

	hints := []hint{
		{"p", "pause/resume"},
		{"r", "poll now"},
		{"e", "expand"},
		{"u", "user msg"},
		{"m", "broadcast"},
		{"o", "onboard"},
		{"t", "theme"},
		{"q", "quit"},
	}

	var row1, row2 strings.Builder
	for i, h := range hints {
		entry := keyStyle.Render(h.key) + descStyle.Render(": "+h.desc)
		target := &row1
		if i >= 4 {
			target = &row2
		}
		if target.Len() > 0 {
			target.WriteString(sep)
		}
		target.WriteString(entry)
	}

	return row1.String() + "\n" + row2.String()
}

// tickEvery returns a command that sends a tick every 5 seconds.
func tickEvery() tea.Cmd {
	return func() tea.Msg {
		time.Sleep(5 * time.Second)
		return tickMsg(time.Now())
	}
}
