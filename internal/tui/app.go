// Package tui implements the bubbletea-based dashboard for agent-minder.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
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
	mode            string // "normal" or "broadcast"
	broadcastInput  textinput.Model
	broadcastStatus string
}

// New creates a new TUI model.
func New(project *db.Project, store *db.Store, p *poller.Poller) Model {
	ti := textinput.New()
	ti.Prompt = "broadcast> "
	ti.Placeholder = "Type a message for other agents..."
	ti.CharLimit = 500

	sp := spinner.New(
		spinner.WithSpinner(spinner.MiniDot),
		spinner.WithStyle(spinnerStyle()),
	)

	return Model{
		project:        project,
		store:          store,
		poller:         p,
		events:         make([]poller.Event, 0, 64),
		mode:           "normal",
		broadcastInput: ti,
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
		if m.mode == "broadcast" {
			return m.updateBroadcast(msg)
		}
		return m.updateNormal(msg)

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

	case tickMsg:
		return m, tickEvery()
	}

	return m, nil
}

type clearBroadcastStatusMsg struct{}

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
	case "m":
		m.mode = "broadcast"
		m.broadcastStatus = ""
		m.broadcastInput.Reset()
		cmd := m.broadcastInput.Focus()
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
	case "enter":
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

	// Delegate to textinput.
	var cmd tea.Cmd
	m.broadcastInput, cmd = m.broadcastInput.Update(msg)
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
	b.WriteString(mutedStyle().Render(fmt.Sprintf("  %s — %s", m.project.GoalType, m.project.GoalDescription)))
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
			style := concernInfoStyle()
			prefix := "INFO"
			if c.Severity == "warning" {
				style = concernWarningStyle()
				prefix = "WARN"
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
				// Also limit width.
				maxWidth := m.width - 4
				if maxWidth > 0 {
					var truncated []string
					for _, line := range strings.Split(response, "\n") {
						if len(line) > maxWidth {
							line = line[:maxWidth-3] + "..."
						}
						truncated = append(truncated, line)
					}
					response = strings.Join(truncated, "\n")
				}
			}
			b.WriteString(llmResponseStyle().Render(response))
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

	// Bottom bar: broadcast input or help.
	b.WriteString("\n")
	if m.mode == "broadcast" {
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
			b.WriteString(helpStyle().Render("enter: send • esc: cancel"))
		}
		b.WriteString("\n")
	} else {
		if m.broadcastStatus != "" {
			b.WriteString(broadcastStyle().Render(fmt.Sprintf("  %s", m.broadcastStatus)))
			b.WriteString("\n")
		}
		b.WriteString(helpStyle().Render("p: pause/resume • r: poll now • e: expand/collapse • m: broadcast • t: theme • q: quit"))
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

// tickEvery returns a command that sends a tick every 5 seconds.
func tickEvery() tea.Cmd {
	return func() tea.Msg {
		time.Sleep(5 * time.Second)
		return tickMsg(time.Now())
	}
}
