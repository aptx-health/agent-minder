package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/textinput"
)

// settingsStep represents the current step in the settings flow.
type settingsStep int

const (
	settingsStepSelectField settingsStep = iota
	settingsStepEditValue
)

// settingsField represents a configurable project setting.
type settingsField struct {
	label       string
	description string
	value       string
	unit        string
}

// settingsState holds all state for the settings dialog.
type settingsState struct {
	step     settingsStep
	fields   []settingsField
	fieldIdx int
	input    textinput.Model
	err      string
}

// settingsSavedMsg is sent when settings are persisted.
type settingsSavedMsg struct {
	field string
	err   error
}

// newSettingsState initializes the settings dialog.
func newSettingsState(pollMinutes int) *settingsState {
	ti := textinput.New()
	ti.Placeholder = "value..."
	ti.CharLimit = 10
	ti.SetWidth(20)

	return &settingsState{
		step:  settingsStepSelectField,
		input: ti,
		fields: []settingsField{
			{
				label:       "Poll interval",
				description: "How often to poll for changes",
				value:       strconv.Itoa(pollMinutes),
				unit:        "min",
			},
		},
	}
}

// updateSettings handles keypresses for the settings mode.
func (m Model) updateSettings(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	ss := m.settingsState
	if ss == nil {
		m.mode = "normal"
		return m, nil
	}

	switch ss.step {
	case settingsStepSelectField:
		return m.updateSettingsSelectField(msg)
	case settingsStepEditValue:
		return m.updateSettingsEditValue(msg)
	}

	return m, nil
}

func (m Model) updateSettingsSelectField(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	ss := m.settingsState

	switch msg.String() {
	case "esc":
		m.mode = "normal"
		m.settingsState = nil
		m.resizeViewports()
		return m, nil
	case "up", "k":
		if ss.fieldIdx > 0 {
			ss.fieldIdx--
		}
		return m, nil
	case "down", "j":
		if ss.fieldIdx < len(ss.fields)-1 {
			ss.fieldIdx++
		}
		return m, nil
	case "enter":
		ss.step = settingsStepEditValue
		ss.err = ""
		ss.input.SetValue(ss.fields[ss.fieldIdx].value)
		cmd := ss.input.Focus()
		return m, cmd
	}

	return m, nil
}

func (m Model) updateSettingsEditValue(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	ss := m.settingsState

	switch msg.String() {
	case "esc":
		ss.step = settingsStepSelectField
		ss.input.Blur()
		ss.err = ""
		return m, nil
	case "enter":
		raw := ss.input.Value()
		field := ss.fields[ss.fieldIdx]

		switch field.label {
		case "Poll interval":
			minutes, err := strconv.Atoi(raw)
			if err != nil || minutes < 1 {
				ss.err = "Enter a number >= 1"
				return m, nil
			}
			newSec := minutes * 60
			m.project.RefreshIntervalSec = newSec
			ss.fields[ss.fieldIdx].value = strconv.Itoa(minutes)
			ss.input.Blur()
			ss.step = settingsStepSelectField
			ss.err = ""

			return m, func() tea.Msg {
				err := m.store.UpdateProject(m.project)
				if err == nil {
					m.poller.SetRefreshInterval(m.project.RefreshInterval())
				}
				return settingsSavedMsg{field: "Poll interval", err: err}
			}
		}

		return m, nil
	}

	// Forward to text input.
	var cmd tea.Cmd
	ss.input, cmd = ss.input.Update(msg)
	return m, cmd
}

// renderSettingsView renders the settings dialog.
func (m Model) renderSettingsView() string {
	ss := m.settingsState
	if ss == nil {
		return ""
	}

	var b strings.Builder

	b.WriteString(headerStyle().Render("Settings"))
	b.WriteString("\n\n")

	switch ss.step {
	case settingsStepSelectField:
		for i, f := range ss.fields {
			prefix := "  "
			if i == ss.fieldIdx {
				prefix = "> "
			}
			entry := fmt.Sprintf("  %s%s: %s %s", prefix, f.label, f.value, f.unit)
			if f.description != "" {
				entry += "  " + mutedStyle().Render(fmt.Sprintf("(%s)", f.description))
			}
			if i == ss.fieldIdx {
				b.WriteString(headerStyle().Render(entry))
			} else {
				b.WriteString(textStyle().Render(entry))
			}
			b.WriteString("\n")
		}

	case settingsStepEditValue:
		f := ss.fields[ss.fieldIdx]
		b.WriteString(textStyle().Render(fmt.Sprintf("  %s (%s):", f.label, f.unit)))
		b.WriteString("\n")
		b.WriteString("  ")
		b.WriteString(ss.input.View())
		b.WriteString("\n")
		if ss.err != "" {
			b.WriteString("  ")
			b.WriteString(errorStyle().Render(ss.err))
			b.WriteString("\n")
		}
	}

	return b.String()
}
