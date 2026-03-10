// Package tui implements the bubbletea-based dashboard for agent-minder.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/dustinlange/agent-minder/internal/poller"
)

// pollerEventMsg wraps a poller event for the bubbletea message system.
type pollerEventMsg poller.Event

// tickMsg triggers UI refresh for the elapsed time display.
type tickMsg time.Time

// Model is the root bubbletea model for the dashboard.
type Model struct {
	project  *db.Project
	store    *db.Store
	poller   *poller.Poller
	width    int
	height   int

	// State.
	events   []poller.Event
	lastPoll *poller.PollResult
	err      error
}

// New creates a new TUI model.
func New(project *db.Project, store *db.Store, p *poller.Poller) Model {
	return Model{
		project: project,
		store:   store,
		poller:  p,
		events:  make([]poller.Event, 0, 64),
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		listenForEvents(m.poller),
		tickEvery(),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyPressMsg:
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
			// Trigger immediate poll.
			go m.poller.PollNow(context.Background())
			return m, nil
		}

	case pollerEventMsg:
		event := poller.Event(msg)
		m.events = append(m.events, event)
		if len(m.events) > 50 {
			m.events = m.events[len(m.events)-50:]
		}
		if event.PollResult != nil {
			m.lastPoll = event.PollResult
		}
		return m, listenForEvents(m.poller)

	case tickMsg:
		return m, tickEvery()
	}

	return m, nil
}

func (m Model) View() tea.View {
	if m.width == 0 {
		return tea.NewView("Loading...")
	}

	var b strings.Builder

	// Header.
	status := statusRunning.Render("RUNNING")
	if m.poller.IsPaused() {
		status = statusPaused.Render("PAUSED")
	}
	b.WriteString(titleStyle.Render(fmt.Sprintf("agent-minder: %s", m.project.Name)))
	b.WriteString("  ")
	b.WriteString(status)
	b.WriteString("\n")

	// Goal.
	b.WriteString(mutedStyle.Render(fmt.Sprintf("  %s — %s", m.project.GoalType, m.project.GoalDescription)))
	b.WriteString("\n\n")

	// Repos section.
	repos, _ := m.store.GetRepos(m.project.ID)
	b.WriteString(headerStyle.Render("Repos"))
	b.WriteString("\n")
	for _, r := range repos {
		b.WriteString(fmt.Sprintf("  %s (%s)\n", r.ShortName, r.Path))
	}
	b.WriteString("\n")

	// Topics.
	topics, _ := m.store.GetTopics(m.project.ID)
	if len(topics) > 0 {
		b.WriteString(headerStyle.Render("Topics"))
		b.WriteString("\n")
		for _, t := range topics {
			b.WriteString(fmt.Sprintf("  %s\n", t.Name))
		}
		b.WriteString("\n")
	}

	// Active concerns.
	concerns, _ := m.store.ActiveConcerns(m.project.ID)
	if len(concerns) > 0 {
		b.WriteString(headerStyle.Render("Active Concerns"))
		b.WriteString("\n")
		for _, c := range concerns {
			style := concernInfo
			prefix := "INFO"
			if c.Severity == "warning" {
				style = concernWarning
				prefix = "WARN"
			}
			b.WriteString(style.Render(fmt.Sprintf("  [%s] %s", prefix, c.Message)))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Last poll result.
	b.WriteString(headerStyle.Render("Last Poll"))
	b.WriteString("\n")
	if m.lastPoll != nil {
		b.WriteString(mutedStyle.Render(fmt.Sprintf("  %d commits, %d messages (took %s)",
			m.lastPoll.NewCommits, m.lastPoll.NewMessages, m.lastPoll.Duration.Round(time.Millisecond))))
		b.WriteString("\n")
		if m.lastPoll.LLMResponse != "" {
			b.WriteString(llmResponseStyle.Render(m.lastPoll.LLMResponse))
			b.WriteString("\n")
		}
	} else {
		b.WriteString(mutedStyle.Render("  Waiting for first poll..."))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// Event log (last N events).
	b.WriteString(headerStyle.Render("Event Log"))
	b.WriteString("\n")
	maxEvents := 10
	start := 0
	if len(m.events) > maxEvents {
		start = len(m.events) - maxEvents
	}
	for i := len(m.events) - 1; i >= start; i-- {
		e := m.events[i]
		ts := e.Time.Format("15:04:05")
		b.WriteString(mutedStyle.Render(fmt.Sprintf("  [%s] %s: %s", ts, e.Type, e.Summary)))
		b.WriteString("\n")
	}
	if len(m.events) == 0 {
		b.WriteString(mutedStyle.Render("  (no events yet)"))
		b.WriteString("\n")
	}

	// Help bar.
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("p: pause/resume • r: poll now • q: quit"))
	b.WriteString("\n")

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
