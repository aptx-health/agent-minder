package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/dustinlange/agent-minder/internal/db"
	gitpkg "github.com/dustinlange/agent-minder/internal/git"
	ghpkg "github.com/dustinlange/agent-minder/internal/github"
	"github.com/dustinlange/agent-minder/internal/poller"
)

// settingsStep represents the current step in the settings flow.
type settingsStep int

const (
	settingsStepSelectField settingsStep = iota
	settingsStepEditValue
	settingsStepEditTextarea
	settingsStepWatchType   // selecting none/label/milestone
	settingsStepWatchFetch  // fetching choices from GitHub
	settingsStepWatchChoice // selecting a specific label/milestone
)

// settingsField represents a configurable project setting.
type settingsField struct {
	label       string
	description string
	value       string
	unit        string
	multiline   bool // true for textarea fields
	toggle      bool // true for boolean toggle fields
}

// optIntStr formats an *int as a string, returning "" for nil.
func optIntStr(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

// optFloatStr formats an *float64 as a string, returning "" for nil.
func optFloatStr(p *float64) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf("%.2f", *p)
}

// boolOnOff returns "on" or "off" for a boolean.
func boolOnOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// settingsState holds all state for the settings dialog.
type settingsState struct {
	step     settingsStep
	fields   []settingsField
	fieldIdx int
	input    textinput.Model
	textarea textarea.Model
	err      string

	// Watch filter sub-flow state.
	watchTypeIdx int                // 0=none, 1=label, 2=milestone
	watchChoices []ghpkg.RepoChoice // fetched choices for selected type
	watchIdx     int                // selected choice index
}

// settingsWatchChoicesMsg is sent when watch filter choices are fetched.
type settingsWatchChoicesMsg struct {
	choices []ghpkg.RepoChoice
	err     error
}

// Watch type options for the settings sub-flow.
var watchTypeLabels = []string{"none", "label", "milestone"}
var watchTypeToFilter = map[string]string{
	"label":     "label",
	"milestone": "milestone",
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
		statusSec = 300
	}

	return &settingsState{
		step:     settingsStepSelectField,
		input:    ti,
		textarea: ta,
		fields: []settingsField{
			{
				label:       "Sync interval",
				description: "How often to run status checks",
				value:       strconv.Itoa(statusSec / 60),
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
				value:       strconv.Itoa(project.EffectiveAutopilotMaxTurns()),
			},
			{
				label:       "Autopilot max budget",
				description: "Max USD budget per agent",
				value:       fmt.Sprintf("%.2f", project.EffectiveAutopilotMaxBudget()),
				unit:        "USD",
			},
			{
				label:       "Autopilot skip label(s)",
				description: "Comma-separated labels to exclude from autopilot",
				value:       project.AutopilotSkipLabel,
			},
			{
				label:       "Autopilot base branch",
				description: "Base branch for worktrees and PRs (empty = auto-detect)",
				value:       project.AutopilotBaseBranch,
			},
			{
				label:       "Review max turns",
				description: "Max turns per review agent (empty = reviews disabled)",
				value:       optIntStr(project.AutopilotReviewMaxTurns),
			},
			{
				label:       "Review max budget",
				description: "Max USD budget per review agent",
				value:       optFloatStr(project.AutopilotReviewMaxBudgetUSD),
				unit:        "USD",
			},
			{
				label:       "Auto-merge",
				description: "Auto-merge low-risk PRs after review (enter to toggle)",
				value:       boolOnOff(project.AutopilotAutoMerge),
				toggle:      true,
			},
			{
				label:       "Watch filter",
				description: "Auto-discover issues by label or milestone",
				value:       watchFilterDisplay(project.AutopilotFilterType, project.AutopilotFilterValue),
			},
		},
	}
}

// watchFilterDisplay returns a human-readable display string for the watch filter.
func watchFilterDisplay(filterType, filterValue string) string {
	if filterType == "" || filterValue == "" {
		return "none"
	}
	return filterType + ": " + filterValue
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
	case settingsStepWatchType:
		return m.updateSettingsWatchType(msg)
	case settingsStepWatchFetch:
		// Waiting for async fetch — ignore keys except esc.
		if msg.String() == "esc" {
			ss.step = settingsStepSelectField
		}
		return m, nil
	case settingsStepWatchChoice:
		return m.updateSettingsWatchChoice(msg)
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
		if field.label == "Watch filter" {
			ss.step = settingsStepWatchType
			ss.watchTypeIdx = 0
			return m, nil
		}
		if field.toggle {
			m.project.AutopilotAutoMerge = !m.project.AutopilotAutoMerge
			ss.fields[ss.fieldIdx].value = boolOnOff(m.project.AutopilotAutoMerge)
			return m, func() tea.Msg {
				err := m.store.UpdateProject(m.project)
				return settingsSavedMsg{field: field.label, err: err}
			}
		}
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
		// Widen char limit for skip labels (comma-separated).
		if field.label == "Autopilot skip label(s)" {
			ss.input.CharLimit = 200
			ss.input.SetWidth(60)
		} else {
			ss.input.CharLimit = 10
			ss.input.SetWidth(20)
		}
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
		case "Sync interval":
			minutes, err := strconv.Atoi(raw)
			if err != nil || minutes < 1 {
				ss.err = "Enter a number >= 1"
				return m, nil
			}
			m.project.StatusIntervalSec = minutes * 60
			ss.fields[ss.fieldIdx].value = strconv.Itoa(minutes)
			ss.input.Blur()
			ss.step = settingsStepSelectField
			ss.err = ""

			return m, func() tea.Msg {
				err := m.store.UpdateProject(m.project)
				if err == nil {
					m.poller.SetStatusInterval(m.project.StatusInterval())
				}
				return settingsSavedMsg{field: "Sync interval", err: err}
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
		case "Autopilot skip label(s)":
			m.project.AutopilotSkipLabel = strings.TrimSpace(raw)
			return m.saveSettingsField(ss, field.label, strings.TrimSpace(raw))
		case "Autopilot base branch":
			branch := strings.TrimSpace(raw)
			if branch != "" {
				// Validate that the branch exists in at least one enrolled repo.
				repos, err := m.store.GetRepos(m.project.ID)
				if err != nil || len(repos) == 0 {
					ss.err = "No enrolled repos to validate against"
					return m, nil
				}
				found := false
				for _, r := range repos {
					if gitpkg.BranchExists(r.Path, branch) {
						found = true
						break
					}
				}
				if !found {
					ss.err = fmt.Sprintf("Branch '%s' not found in enrolled repos", branch)
					return m, nil
				}
			}
			m.project.AutopilotBaseBranch = branch
			return m.saveSettingsField(ss, field.label, branch)
		case "Review max turns":
			raw = strings.TrimSpace(raw)
			if raw == "" {
				m.project.AutopilotReviewMaxTurns = nil
				return m.saveSettingsField(ss, field.label, "")
			}
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 {
				ss.err = "Enter a positive number (or empty to disable)"
				return m, nil
			}
			m.project.AutopilotReviewMaxTurns = &n
			return m.saveSettingsField(ss, field.label, raw)
		case "Review max budget":
			raw = strings.TrimSpace(raw)
			if raw == "" {
				m.project.AutopilotReviewMaxBudgetUSD = nil
				return m.saveSettingsField(ss, field.label, "")
			}
			f, err := strconv.ParseFloat(raw, 64)
			if err != nil || f <= 0 {
				ss.err = "Enter a positive number (or empty to disable)"
				return m, nil
			}
			m.project.AutopilotReviewMaxBudgetUSD = &f
			return m.saveSettingsField(ss, field.label, fmt.Sprintf("%.2f", f))
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

func (m Model) updateSettingsWatchType(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	ss := m.settingsState
	switch msg.String() {
	case "esc":
		ss.step = settingsStepSelectField
		return m, nil
	case "up", "k":
		if ss.watchTypeIdx > 0 {
			ss.watchTypeIdx--
		}
		return m, nil
	case "down", "j":
		if ss.watchTypeIdx < len(watchTypeLabels)-1 {
			ss.watchTypeIdx++
		}
		return m, nil
	case "enter":
		selected := watchTypeLabels[ss.watchTypeIdx]
		if selected == "none" {
			// Clear the watch filter.
			m.project.AutopilotFilterType = ""
			m.project.AutopilotFilterValue = ""
			ss.fields[ss.fieldIdx].value = "none"
			ss.step = settingsStepSelectField
			return m, func() tea.Msg {
				err := m.store.UpdateProject(m.project)
				return settingsSavedMsg{field: "Watch filter", err: err}
			}
		}
		// Fetch choices for label/milestone.
		ss.step = settingsStepWatchFetch
		ss.watchChoices = nil
		ss.watchIdx = 0
		filterType := watchTypeToFilter[selected]

		p := m.poller
		ghRepos := p.GitHubRepos()
		if len(ghRepos) == 0 {
			ss.err = "No GitHub repos enrolled"
			ss.step = settingsStepSelectField
			return m, nil
		}
		owner := ghRepos[0].Owner
		repo := ghRepos[0].Repo
		var ft ghpkg.FilterType
		if filterType == "label" {
			ft = ghpkg.FilterLabel
		} else {
			ft = ghpkg.FilterMilestone
		}

		return m, func() tea.Msg {
			choices, err := p.FetchFilterChoices(context.Background(), owner, repo, ft)
			return settingsWatchChoicesMsg{choices: choices, err: err}
		}
	}
	return m, nil
}

func (m Model) updateSettingsWatchChoice(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	ss := m.settingsState
	switch msg.String() {
	case "esc":
		ss.step = settingsStepWatchType
		return m, nil
	case "up", "k":
		if ss.watchIdx > 0 {
			ss.watchIdx--
		}
		return m, nil
	case "down", "j":
		if ss.watchIdx < len(ss.watchChoices)-1 {
			ss.watchIdx++
		}
		return m, nil
	case "enter":
		if ss.watchIdx >= len(ss.watchChoices) {
			return m, nil
		}
		choice := ss.watchChoices[ss.watchIdx]
		filterType := watchTypeLabels[ss.watchTypeIdx]
		m.project.AutopilotFilterType = filterType
		m.project.AutopilotFilterValue = choice.Value
		ss.fields[ss.fieldIdx].value = watchFilterDisplay(filterType, choice.Value)
		ss.step = settingsStepSelectField
		return m, func() tea.Msg {
			err := m.store.UpdateProject(m.project)
			return settingsSavedMsg{field: "Watch filter", err: err}
		}
	}
	return m, nil
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

// parseSkipLabels splits a comma-separated label string, trims whitespace,
// drops empties, and returns ["no-agent"] when the result is empty.
// This mirrors the runtime skipMatcher logic without importing autopilot.
func parseSkipLabels(raw string) []string {
	var labels []string
	for _, part := range strings.Split(raw, ",") {
		s := strings.TrimSpace(part)
		if s != "" {
			labels = append(labels, s)
		}
	}
	if len(labels) == 0 {
		return []string{"no-agent"}
	}
	return labels
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
		if f.unit != "" {
			b.WriteString(textStyle().Render(fmt.Sprintf("  %s (%s):", f.label, f.unit)))
		} else {
			b.WriteString(textStyle().Render(fmt.Sprintf("  %s:", f.label)))
		}
		b.WriteString("\n")
		b.WriteString("  ")
		b.WriteString(ss.input.View())
		b.WriteString("\n")
		// Show parsed label preview for skip labels.
		if f.label == "Autopilot skip label(s)" {
			labels := parseSkipLabels(ss.input.Value())
			var parts []string
			for _, l := range labels {
				parts = append(parts, fmt.Sprintf("[%s]", l))
			}
			b.WriteString(mutedStyle().Render(fmt.Sprintf("  → Will match: %s", strings.Join(parts, " "))))
			b.WriteString("\n")
		}
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

	case settingsStepWatchType:
		b.WriteString(textStyle().Render("  Select watch filter type:"))
		b.WriteString("\n")
		for i, label := range watchTypeLabels {
			prefix := "    "
			if i == ss.watchTypeIdx {
				prefix = "  > "
			}
			entry := prefix + label
			if i == ss.watchTypeIdx {
				b.WriteString(headerStyle().Render(entry))
			} else {
				b.WriteString(textStyle().Render(entry))
			}
			b.WriteString("\n")
		}
		if ss.err != "" {
			b.WriteString("  ")
			b.WriteString(errorStyle().Render(ss.err))
			b.WriteString("\n")
		}

	case settingsStepWatchFetch:
		b.WriteString(textStyle().Render("  Fetching choices..."))
		b.WriteString("\n")

	case settingsStepWatchChoice:
		filterType := watchTypeLabels[ss.watchTypeIdx]
		b.WriteString(textStyle().Render(fmt.Sprintf("  Select %s:", filterType)))
		b.WriteString("\n")
		for i, choice := range ss.watchChoices {
			prefix := "    "
			if i == ss.watchIdx {
				prefix = "  > "
			}
			entry := prefix + choice.Value
			if i == ss.watchIdx {
				b.WriteString(headerStyle().Render(entry))
			} else {
				b.WriteString(textStyle().Render(entry))
			}
			b.WriteString("\n")
		}
	}

	// Read-only project info.
	b.WriteString("\n")
	b.WriteString(mutedStyle().Render("  ──────────────────────────"))
	b.WriteString("\n\n")

	repos, _ := m.store.GetRepos(m.project.ID)
	b.WriteString(mutedStyle().Render("  Repos"))
	b.WriteString("\n")
	for _, r := range repos {
		b.WriteString(mutedStyle().Render(fmt.Sprintf("    %s (%s)", r.ShortName, r.Path)))
		b.WriteString("\n")
	}

	topics, _ := m.store.GetTopics(m.project.ID)
	if len(topics) > 0 {
		b.WriteString(mutedStyle().Render("  Topics"))
		b.WriteString("\n")
		for _, t := range topics {
			b.WriteString(mutedStyle().Render(fmt.Sprintf("    %s", t.Name)))
			b.WriteString("\n")
		}
	}

	return b.String()
}
