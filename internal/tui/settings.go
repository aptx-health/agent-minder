package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/dustinlange/agent-minder/internal/poller"
)

// settingsStep represents the current step in the settings flow.
type settingsStep int

const (
	settingsStepSelectField settingsStep = iota
	settingsStepEditValue
	settingsStepEditTextarea
)

// settingsField represents a configurable project setting.
type settingsField struct {
	label       string
	description string
	value       string
	unit        string
	multiline   bool // true for textarea fields
}

// settingsState holds all state for the settings dialog.
type settingsState struct {
	step     settingsStep
	fields   []settingsField
	fieldIdx int
	input    textinput.Model
	textarea textarea.Model
	err      string
}

// settingsSavedMsg is sent when settings are persisted.
type settingsSavedMsg struct {
	field string
	err   error
}

// newSettingsState initializes the settings dialog.
func newSettingsState(project *db.Project) *settingsState {
	ti := textinput.New()
	ti.Placeholder = "value..."
	ti.CharLimit = 10
	ti.SetWidth(20)

	ta := textarea.New()
	ta.Placeholder = "Describe how the analyzer should think, what to focus on, and how to communicate..."
	ta.CharLimit = 2000
	ta.SetHeight(5)
	ta.SetWidth(80)

	// Show the effective focus (default if empty) truncated for the field list.
	effectiveFocus := project.AnalyzerFocus
	if effectiveFocus == "" {
		effectiveFocus = poller.DefaultAnalyzerFocus
	}
	displayFocus := effectiveFocus
	if len(displayFocus) > 60 {
		displayFocus = displayFocus[:57] + "..."
	}

	statusSec := project.StatusIntervalSec
	if statusSec <= 0 {
		statusSec = 30
	}
	analysisSec := project.AnalysisIntervalSec
	if analysisSec <= 0 {
		analysisSec = 300
	}

	return &settingsState{
		step:     settingsStepSelectField,
		input:    ti,
		textarea: ta,
		fields: []settingsField{
			{
				label:       "Status interval",
				description: "How often to run mechanical status checks",
				value:       strconv.Itoa(statusSec),
				unit:        "sec",
			},
			{
				label:       "Analysis interval",
				description: "How often to run LLM analysis",
				value:       strconv.Itoa(analysisSec / 60),
				unit:        "min",
			},
			{
				label:       "Analyzer focus",
				description: "Custom instructions for the analyzer's perspective and communication style",
				value:       displayFocus,
				multiline:   true,
			},
			{
				label:       "Autopilot max agents",
				description: "Maximum concurrent agents",
				value:       strconv.Itoa(project.AutopilotMaxAgents),
			},
			{
				label:       "Autopilot max turns",
				description: "Max turns per agent",
				value:       strconv.Itoa(project.AutopilotMaxTurns),
			},
			{
				label:       "Autopilot max budget",
				description: "Max USD budget per agent",
				value:       fmt.Sprintf("%.2f", project.AutopilotMaxBudgetUSD),
				unit:        "USD",
			},
			{
				label:       "Autopilot skip label",
				description: "Issues with this label are excluded",
				value:       project.AutopilotSkipLabel,
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
	case settingsStepEditTextarea:
		return m.updateSettingsEditTextarea(msg)
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
		field := ss.fields[ss.fieldIdx]
		ss.err = ""
		if field.multiline {
			ss.step = settingsStepEditTextarea
			// Load the effective value (default when empty) so users can see and edit it.
			focus := m.project.AnalyzerFocus
			if focus == "" {
				focus = poller.DefaultAnalyzerFocus
			}
			ss.textarea.SetValue(focus)
			if m.width > 4 {
				ss.textarea.SetWidth(m.width - 4)
			}
			cmd := ss.textarea.Focus()
			return m, cmd
		}
		ss.step = settingsStepEditValue
		ss.input.SetValue(field.value)
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
		case "Status interval":
			sec, err := strconv.Atoi(raw)
			if err != nil || sec < 10 {
				ss.err = "Enter a number >= 10"
				return m, nil
			}
			m.project.StatusIntervalSec = sec
			ss.fields[ss.fieldIdx].value = strconv.Itoa(sec)
			ss.input.Blur()
			ss.step = settingsStepSelectField
			ss.err = ""

			return m, func() tea.Msg {
				err := m.store.UpdateProject(m.project)
				if err == nil {
					m.poller.SetStatusInterval(m.project.StatusInterval())
				}
				return settingsSavedMsg{field: "Status interval", err: err}
			}
		case "Analysis interval":
			minutes, err := strconv.Atoi(raw)
			if err != nil || minutes < 1 {
				ss.err = "Enter a number >= 1"
				return m, nil
			}
			m.project.AnalysisIntervalSec = minutes * 60
			ss.fields[ss.fieldIdx].value = strconv.Itoa(minutes)
			ss.input.Blur()
			ss.step = settingsStepSelectField
			ss.err = ""

			return m, func() tea.Msg {
				err := m.store.UpdateProject(m.project)
				if err == nil {
					m.poller.SetAnalysisInterval(m.project.AnalysisInterval())
				}
				return settingsSavedMsg{field: "Analysis interval", err: err}
			}
		case "Autopilot max agents":
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 10 {
				ss.err = "Enter a number 1-10"
				return m, nil
			}
			m.project.AutopilotMaxAgents = n
			return m.saveSettingsField(ss, field.label, raw)
		case "Autopilot max turns":
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 {
				ss.err = "Enter a positive number"
				return m, nil
			}
			m.project.AutopilotMaxTurns = n
			return m.saveSettingsField(ss, field.label, raw)
		case "Autopilot max budget":
			f, err := strconv.ParseFloat(raw, 64)
			if err != nil || f <= 0 {
				ss.err = "Enter a positive number"
				return m, nil
			}
			m.project.AutopilotMaxBudgetUSD = f
			return m.saveSettingsField(ss, field.label, fmt.Sprintf("%.2f", f))
		case "Autopilot skip label":
			m.project.AutopilotSkipLabel = strings.TrimSpace(raw)
			return m.saveSettingsField(ss, field.label, strings.TrimSpace(raw))
		}

		return m, nil
	}

	// Forward to text input.
	var cmd tea.Cmd
	ss.input, cmd = ss.input.Update(msg)
	return m, cmd
}

func (m Model) updateSettingsEditTextarea(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	ss := m.settingsState

	switch msg.String() {
	case "esc":
		ss.step = settingsStepSelectField
		ss.textarea.Blur()
		ss.err = ""
		return m, nil
	case "ctrl+d":
		value := strings.TrimSpace(ss.textarea.Value())
		// If unchanged from default, store empty so the default can evolve with code updates.
		if value == poller.DefaultAnalyzerFocus {
			value = ""
		}
		m.project.AnalyzerFocus = value
		ss.textarea.Blur()
		ss.step = settingsStepSelectField
		ss.err = ""

		// Update display value (show effective, which is default when empty).
		effectiveFocus := value
		if effectiveFocus == "" {
			effectiveFocus = poller.DefaultAnalyzerFocus
		}
		displayFocus := effectiveFocus
		if len(displayFocus) > 60 {
			displayFocus = displayFocus[:57] + "..."
		}
		ss.fields[ss.fieldIdx].value = displayFocus

		return m, func() tea.Msg {
			err := m.store.UpdateProject(m.project)
			return settingsSavedMsg{field: "Analyzer focus", err: err}
		}
	}

	// Forward to textarea.
	var cmd tea.Cmd
	ss.textarea, cmd = ss.textarea.Update(msg)
	return m, cmd
}

// saveSettingsField is a common helper for saving a simple text/numeric setting.
func (m Model) saveSettingsField(ss *settingsState, label, displayValue string) (tea.Model, tea.Cmd) {
	ss.fields[ss.fieldIdx].value = displayValue
	ss.input.Blur()
	ss.step = settingsStepSelectField
	ss.err = ""

	return m, func() tea.Msg {
		err := m.store.UpdateProject(m.project)
		return settingsSavedMsg{field: label, err: err}
	}
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
			var entry string
			if f.unit != "" {
				entry = fmt.Sprintf("  %s%s: %s %s", prefix, f.label, f.value, f.unit)
			} else {
				entry = fmt.Sprintf("  %s%s: %s", prefix, f.label, f.value)
			}
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

	case settingsStepEditTextarea:
		f := ss.fields[ss.fieldIdx]
		b.WriteString(textStyle().Render(fmt.Sprintf("  %s:", f.label)))
		b.WriteString("\n")
		b.WriteString("  ")
		b.WriteString(ss.textarea.View())
		b.WriteString("\n")
		if ss.err != "" {
			b.WriteString("  ")
			b.WriteString(errorStyle().Render(ss.err))
			b.WriteString("\n")
		}
	}

	return b.String()
}
