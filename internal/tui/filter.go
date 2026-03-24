package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	ghpkg "github.com/dustinlange/agent-minder/internal/github"
	"github.com/dustinlange/agent-minder/internal/poller"
)

// filterStep represents the current step in the filter flow.
type filterStep int

const (
	filterStepSelectRepo filterStep = iota
	filterStepSelectType
	filterStepFetchingChoices
	filterStepSelectChoice
	filterStepWatchOrAdd // choose between setting watch filter vs one-shot add
	filterStepInputValue
	filterStepFetching
	filterStepPreview
	filterStepConflict
)

// filterSearchResultMsg is sent when a filter search completes.
type filterSearchResultMsg struct {
	results *ghpkg.SearchResult
	err     error
}

// filterChoicesMsg is sent when available choices for a filter type are fetched.
type filterChoicesMsg struct {
	choices []ghpkg.RepoChoice
	err     error
}

// bulkAddResultMsg is sent when a bulk add completes.
type bulkAddResultMsg struct {
	added int
	err   error
}

// bulkUpdateResultMsg is sent when an update (add new + remove terminal) completes.
type bulkUpdateResultMsg struct {
	added   int
	removed int
	err     error
}

// filterState holds all state for the filter flow.
type filterState struct {
	step         filterStep
	repos        []poller.GitHubRepo
	repoIdx      int
	selectedRepo poller.GitHubRepo
	filterType   ghpkg.FilterType
	filterValue  string
	results      *ghpkg.SearchResult
	choices      []ghpkg.RepoChoice // available choices for the selected filter type
	choiceIdx    int                // currently highlighted choice
	selectedID   int                // ID of the selected choice (for milestone API)
	typeIdx      int                // currently highlighted filter type (0=label, 1=milestone, 2=project, 3=assignee)
	conflictIdx  int                // currently highlighted conflict option (0=update, 1=append, 2=clear)
	watchAddIdx  int                // 0=watch, 1=add issues
	input        textinput.Model
	err          error
	hasExisting  bool // true if project already has tracked items
}

// newFilterState initializes the filter flow state.
func newFilterState(repos []poller.GitHubRepo, hasExisting bool) *filterState {
	ti := textinput.New()
	ti.Placeholder = "filter value..."
	ti.CharLimit = 100
	ti.SetWidth(40)

	fs := &filterState{
		repos:       repos,
		input:       ti,
		hasExisting: hasExisting,
	}

	// Auto-skip repo selection if only one repo.
	if len(repos) == 1 {
		fs.selectedRepo = repos[0]
		fs.step = filterStepSelectType
	} else {
		fs.step = filterStepSelectRepo
	}

	return fs
}

// updateFilter handles keypresses for the filter mode.
func (m Model) updateFilter(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	fs := m.filterState
	if fs == nil {
		m.mode = "normal"
		return m, nil
	}

	switch fs.step {
	case filterStepSelectRepo:
		return m.updateFilterSelectRepo(msg)
	case filterStepSelectType:
		return m.updateFilterSelectType(msg)
	case filterStepSelectChoice:
		return m.updateFilterSelectChoice(msg)
	case filterStepWatchOrAdd:
		return m.updateFilterWatchOrAdd(msg)
	case filterStepInputValue:
		return m.updateFilterInputValue(msg)
	case filterStepPreview:
		return m.updateFilterPreview(msg)
	case filterStepConflict:
		return m.updateFilterConflict(msg)
	}
	return m, nil
}

func (m Model) updateFilterSelectRepo(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	fs := m.filterState
	switch msg.String() {
	case "esc":
		m.mode = "normal"
		m.filterState = nil
		return m, nil
	case "up", "k":
		if fs.repoIdx > 0 {
			fs.repoIdx--
		}
		return m, nil
	case "down", "j":
		if fs.repoIdx < len(fs.repos)-1 {
			fs.repoIdx++
		}
		return m, nil
	case "enter":
		fs.selectedRepo = fs.repos[fs.repoIdx]
		fs.step = filterStepSelectType
		return m, nil
	}
	return m, nil
}

// filterTypeOptions defines the order of filter type choices.
var filterTypeOptions = []ghpkg.FilterType{
	ghpkg.FilterLabel,
	ghpkg.FilterMilestone,
	ghpkg.FilterProject,
	ghpkg.FilterAssignee,
}

func (m Model) updateFilterSelectType(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	fs := m.filterState
	switch msg.String() {
	case "esc":
		if len(fs.repos) > 1 {
			fs.step = filterStepSelectRepo
		} else {
			m.mode = "normal"
			m.filterState = nil
		}
		return m, nil
	case "up", "k":
		if fs.typeIdx > 0 {
			fs.typeIdx--
		}
		return m, nil
	case "down", "j":
		if fs.typeIdx < len(filterTypeOptions)-1 {
			fs.typeIdx++
		}
		return m, nil
	case "enter":
		ft := filterTypeOptions[fs.typeIdx]
		fs.filterType = ft
		if ft == ghpkg.FilterProject {
			// Projects are org-level, no list API — go straight to input.
			fs.step = filterStepInputValue
			fs.input.Placeholder = "org/project-number..."
			cmd := fs.input.Focus()
			return m, cmd
		}
		return m.startFetchChoices()
	}
	return m, nil
}

// startFetchChoices kicks off the async fetch of available choices for the selected filter type.
func (m Model) startFetchChoices() (tea.Model, tea.Cmd) {
	fs := m.filterState
	fs.step = filterStepFetchingChoices
	fs.choices = nil
	fs.choiceIdx = 0
	fs.err = nil

	p := m.poller
	owner := fs.selectedRepo.Owner
	repo := fs.selectedRepo.Repo
	ft := fs.filterType

	return m, func() tea.Msg {
		choices, err := p.FetchFilterChoices(context.Background(), owner, repo, ft)
		return filterChoicesMsg{choices: choices, err: err}
	}
}

func (m Model) updateFilterSelectChoice(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	fs := m.filterState
	switch msg.String() {
	case "esc":
		fs.step = filterStepSelectType
		fs.choices = nil
		return m, nil
	case "up", "k":
		if fs.choiceIdx > 0 {
			fs.choiceIdx--
		}
		return m, nil
	case "down", "j":
		// Last entry is the "type custom" option, so total count = len(choices) + 1
		max := len(fs.choices)
		if fs.choiceIdx < max {
			fs.choiceIdx++
		}
		return m, nil
	case "enter":
		if fs.choiceIdx < len(fs.choices) {
			// Selected an existing choice — stash it.
			choice := fs.choices[fs.choiceIdx]
			fs.filterValue = choice.Value
			fs.selectedID = choice.ID
			fs.err = nil

			// For label/milestone, offer watch vs one-shot add.
			if fs.filterType == ghpkg.FilterLabel || fs.filterType == ghpkg.FilterMilestone {
				fs.step = filterStepWatchOrAdd
				fs.watchAddIdx = 0
				return m, nil
			}

			// Other types (project, assignee): go straight to fetch.
			return m.filterDoFetch()
		}
		// "Type custom value" option selected — go to text input.
		fs.step = filterStepInputValue
		switch fs.filterType {
		case ghpkg.FilterLabel:
			fs.input.Placeholder = "label name..."
		case ghpkg.FilterMilestone:
			fs.input.Placeholder = "milestone title..."
		case ghpkg.FilterAssignee:
			fs.input.Placeholder = "username..."
		}
		cmd := fs.input.Focus()
		return m, cmd
	}
	return m, nil
}

// filterWatchSetMsg is sent when a watch filter is saved from the filter flow.
type filterWatchSetMsg struct {
	filterType  string
	filterValue string
	err         error
}

// filterDoFetch starts the async issue search for the selected filter.
func (m Model) filterDoFetch() (tea.Model, tea.Cmd) {
	fs := m.filterState
	fs.step = filterStepFetching
	fs.err = nil

	p := m.poller
	owner := fs.selectedRepo.Owner
	repo := fs.selectedRepo.Repo
	ft := fs.filterType
	fv := fs.filterValue
	choiceID := fs.selectedID

	return m, func() tea.Msg {
		var result *ghpkg.SearchResult
		var err error
		if ft == ghpkg.FilterMilestone && choiceID > 0 {
			result, err = p.SearchIssuesByMilestone(context.Background(), owner, repo, choiceID)
		} else {
			result, err = p.SearchGitHubIssues(context.Background(), owner, repo, ft, fv)
		}
		return filterSearchResultMsg{results: result, err: err}
	}
}

func (m Model) updateFilterWatchOrAdd(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	fs := m.filterState
	switch msg.String() {
	case "esc":
		fs.step = filterStepSelectChoice
		return m, nil
	case "up", "k":
		if fs.watchAddIdx > 0 {
			fs.watchAddIdx--
		}
		return m, nil
	case "down", "j":
		if fs.watchAddIdx < 1 {
			fs.watchAddIdx++
		}
		return m, nil
	case "enter":
		if fs.watchAddIdx == 0 {
			// Watch: save as project watch filter and exit.
			filterType := string(fs.filterType)
			filterValue := fs.filterValue
			m.project.AutopilotFilterType = filterType
			m.project.AutopilotFilterValue = filterValue
			m.mode = "normal"
			m.filterState = nil
			m.resizeViewports()
			return m, func() tea.Msg {
				err := m.store.UpdateProject(m.project)
				return filterWatchSetMsg{filterType: filterType, filterValue: filterValue, err: err}
			}
		}
		// Add issues: proceed with the normal fetch flow.
		return m.filterDoFetch()
	}
	return m, nil
}

func (m Model) updateFilterInputValue(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	fs := m.filterState
	switch msg.String() {
	case "esc":
		if len(fs.choices) > 0 {
			// Go back to the choice list if we came from there.
			fs.step = filterStepSelectChoice
		} else {
			fs.step = filterStepSelectType
		}
		fs.input.Blur()
		fs.input.Reset()
		return m, nil
	case "enter":
		value := strings.TrimSpace(fs.input.Value())
		if value == "" {
			return m, nil
		}
		fs.filterValue = value
		fs.step = filterStepFetching
		fs.input.Blur()
		fs.err = nil

		p := m.poller
		owner := fs.selectedRepo.Owner
		repo := fs.selectedRepo.Repo
		ft := fs.filterType
		fv := fs.filterValue

		return m, func() tea.Msg {
			result, err := p.SearchGitHubIssues(context.Background(), owner, repo, ft, fv)
			return filterSearchResultMsg{results: result, err: err}
		}
	}

	var cmd tea.Cmd
	fs.input, cmd = fs.input.Update(msg)
	return m, cmd
}

func (m Model) updateFilterPreview(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	fs := m.filterState
	switch msg.String() {
	case "esc":
		fs.step = filterStepSelectType
		fs.input.Reset()
		fs.results = nil
		return m, nil
	case "enter":
		if fs.results == nil || len(fs.results.Items) == 0 {
			m.mode = "normal"
			m.filterState = nil
			return m, nil
		}
		// Cap at 20 items.
		items := fs.results.Items
		if len(items) > 20 {
			items = items[:20]
		}
		if fs.hasExisting {
			fs.step = filterStepConflict
			return m, nil
		}
		// No existing items, just add.
		return m.startBulkAdd(items, false)
	}
	return m, nil
}

func (m Model) updateFilterConflict(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	fs := m.filterState
	switch msg.String() {
	case "esc":
		fs.step = filterStepPreview
		return m, nil
	case "up", "k":
		if fs.conflictIdx > 0 {
			fs.conflictIdx--
		}
		return m, nil
	case "down", "j":
		if fs.conflictIdx < 2 {
			fs.conflictIdx++
		}
		return m, nil
	case "enter":
		items := fs.results.Items
		if len(items) > 20 {
			items = items[:20]
		}
		switch fs.conflictIdx {
		case 0: // Update
			return m.startBulkUpdate(items)
		case 1: // Append
			return m.startBulkAdd(items, false)
		case 2: // Clear and replace
			return m.startBulkAdd(items, true)
		}
	}
	return m, nil
}

// startBulkAdd kicks off the async bulk add operation.
func (m Model) startBulkAdd(items []ghpkg.ItemStatus, clearFirst bool) (tea.Model, tea.Cmd) {
	fs := m.filterState
	fs.step = filterStepFetching // reuse fetching step for spinner

	p := m.poller
	owner := fs.selectedRepo.Owner
	repo := fs.selectedRepo.Repo

	return m, func() tea.Msg {
		var added int
		var err error
		if clearFirst {
			added, err = p.ClearAndBulkAddTrackedItems(context.Background(), items, owner, repo)
		} else {
			added, err = p.BulkAddTrackedItems(context.Background(), items, owner, repo)
		}
		return bulkAddResultMsg{added: added, err: err}
	}
}

// startBulkUpdate kicks off the async update operation: removes terminal items then adds new ones.
func (m Model) startBulkUpdate(items []ghpkg.ItemStatus) (tea.Model, tea.Cmd) {
	fs := m.filterState
	fs.step = filterStepFetching // reuse fetching step for spinner

	p := m.poller
	owner := fs.selectedRepo.Owner
	repo := fs.selectedRepo.Repo

	return m, func() tea.Msg {
		added, removed, err := p.UpdateTrackedItems(context.Background(), items, owner, repo)
		return bulkUpdateResultMsg{added: added, removed: removed, err: err}
	}
}

// renderFilterView renders the filter mode UI content.
func (m Model) renderFilterView() string {
	fs := m.filterState
	if fs == nil {
		return ""
	}

	var b strings.Builder

	b.WriteString(headerStyle().Render("Filter Issues"))
	b.WriteString("\n\n")

	switch fs.step {
	case filterStepSelectRepo:
		b.WriteString(textStyle().Render("  Select repository:"))
		b.WriteString("\n")
		for i, r := range fs.repos {
			prefix := "  "
			if i == fs.repoIdx {
				prefix = "> "
			}
			entry := fmt.Sprintf("  %s%s/%s", prefix, r.Owner, r.Repo)
			if i == fs.repoIdx {
				b.WriteString(headerStyle().Render(entry))
			} else {
				b.WriteString(textStyle().Render(entry))
			}
			b.WriteString("\n")
		}

	case filterStepSelectType:
		b.WriteString(textStyle().Render(fmt.Sprintf("  Repo: %s/%s", fs.selectedRepo.Owner, fs.selectedRepo.Repo)))
		b.WriteString("\n\n")
		b.WriteString(textStyle().Render("  Select filter type:"))
		b.WriteString("\n")
		typeLabels := []string{"label", "milestone", "project", "assignee"}
		for i, label := range typeLabels {
			prefix := "  "
			if i == fs.typeIdx {
				prefix = "> "
			}
			entry := fmt.Sprintf("  %s%s", prefix, label)
			if i == fs.typeIdx {
				b.WriteString(headerStyle().Render(entry))
			} else {
				b.WriteString(textStyle().Render(entry))
			}
			b.WriteString("\n")
		}

	case filterStepFetchingChoices:
		b.WriteString(textStyle().Render(fmt.Sprintf("  Repo: %s/%s  Filter: %s",
			fs.selectedRepo.Owner, fs.selectedRepo.Repo, fs.filterType)))
		b.WriteString("\n\n")
		b.WriteString("  ")
		b.WriteString(m.spinner.View())
		b.WriteString(" ")
		b.WriteString(mutedStyle().Render("Loading available options..."))
		b.WriteString("\n")

	case filterStepSelectChoice:
		b.WriteString(textStyle().Render(fmt.Sprintf("  Repo: %s/%s  Filter: %s",
			fs.selectedRepo.Owner, fs.selectedRepo.Repo, fs.filterType)))
		b.WriteString("\n\n")
		b.WriteString(textStyle().Render("  Select a value:"))
		b.WriteString("\n")
		for i, c := range fs.choices {
			prefix := "  "
			if i == fs.choiceIdx {
				prefix = "> "
			}
			entry := fmt.Sprintf("  %s%s", prefix, c.Value)
			if c.Description != "" {
				entry += mutedStyle().Render(fmt.Sprintf(" (%s)", c.Description))
			}
			if i == fs.choiceIdx {
				b.WriteString(headerStyle().Render(entry))
			} else {
				b.WriteString(textStyle().Render(entry))
			}
			b.WriteString("\n")
		}
		// "Type custom" option at the end.
		customPrefix := "  "
		if fs.choiceIdx == len(fs.choices) {
			customPrefix = "> "
		}
		customEntry := fmt.Sprintf("  %s✎ type custom value...", customPrefix)
		if fs.choiceIdx == len(fs.choices) {
			b.WriteString(headerStyle().Render(customEntry))
		} else {
			b.WriteString(mutedStyle().Render(customEntry))
		}
		b.WriteString("\n")

	case filterStepWatchOrAdd:
		b.WriteString(textStyle().Render(fmt.Sprintf("  %s: %s", fs.filterType, fs.filterValue)))
		b.WriteString("\n\n")
		watchOrAddOptions := []string{
			"Watch — auto-discover new issues matching this filter",
			"Add issues — one-time bulk add of current matches",
		}
		for i, opt := range watchOrAddOptions {
			prefix := "    "
			if i == fs.watchAddIdx {
				prefix = "  > "
			}
			entry := prefix + opt
			if i == fs.watchAddIdx {
				b.WriteString(headerStyle().Render(entry))
			} else {
				b.WriteString(textStyle().Render(entry))
			}
			b.WriteString("\n")
		}

	case filterStepInputValue:
		b.WriteString(textStyle().Render(fmt.Sprintf("  Repo: %s/%s  Filter: %s", fs.selectedRepo.Owner, fs.selectedRepo.Repo, fs.filterType)))
		b.WriteString("\n\n")
		b.WriteString(headerStyle().Render("  Enter value: "))
		b.WriteString(fs.input.View())
		b.WriteString("\n")

	case filterStepFetching:
		b.WriteString(textStyle().Render(fmt.Sprintf("  Repo: %s/%s  Filter: %s=%s",
			fs.selectedRepo.Owner, fs.selectedRepo.Repo, fs.filterType, fs.filterValue)))
		b.WriteString("\n\n")
		b.WriteString("  ")
		b.WriteString(m.spinner.View())
		b.WriteString(" ")
		b.WriteString(mutedStyle().Render("Searching..."))
		b.WriteString("\n")

	case filterStepPreview:
		b.WriteString(textStyle().Render(fmt.Sprintf("  Repo: %s/%s  Filter: %s=%s",
			fs.selectedRepo.Owner, fs.selectedRepo.Repo, fs.filterType, fs.filterValue)))
		b.WriteString("\n\n")

		if fs.err != nil {
			errMsg := fmt.Sprintf("  Error: %v", fs.err)
			maxW := m.width - 4
			if maxW > 20 {
				errMsg = wrapText(errMsg, maxW)
			}
			b.WriteString(errorStyle().Render(errMsg))
			b.WriteString("\n")
		} else if fs.results == nil || len(fs.results.Items) == 0 {
			b.WriteString(mutedStyle().Render("  No matching issues found."))
			b.WriteString("\n")
		} else {
			b.WriteString(textStyle().Render(fmt.Sprintf("  Found %d issues", fs.results.TotalCount)))
			if fs.results.TotalCount > 20 {
				b.WriteString(mutedStyle().Render(" (showing first 20)"))
			}
			b.WriteString("\n\n")

			for i, item := range fs.results.Items {
				if i >= 20 {
					break
				}
				dot := statusDot(item.CompactStatus())
				title := item.Title
				if len(title) > 60 {
					title = title[:57] + "..."
				}
				fmt.Fprintf(&b, "  %s #%d %s", dot, item.Number, mutedStyle().Render(title))
				b.WriteString("\n")
			}
		}

	case filterStepConflict:
		b.WriteString(textStyle().Render(fmt.Sprintf("  %d existing tasks found.", len(m.operationsTasks))))
		b.WriteString("\n\n")
		conflictOptions := []string{
			"update (add new, remove closed/merged)",
			"append to existing",
			"clear and replace",
		}
		for i, label := range conflictOptions {
			prefix := "  "
			if i == fs.conflictIdx {
				prefix = "> "
			}
			entry := fmt.Sprintf("  %s%s", prefix, label)
			if i == fs.conflictIdx {
				b.WriteString(headerStyle().Render(entry))
			} else {
				b.WriteString(textStyle().Render(entry))
			}
			b.WriteString("\n")
		}
	}

	return b.String()
}

// wrapText wraps a string to the given width, breaking on spaces.
func wrapText(s string, width int) string {
	if len(s) <= width {
		return s
	}
	var b strings.Builder
	line := ""
	for _, word := range strings.Fields(s) {
		if line == "" {
			line = word
		} else if len(line)+1+len(word) > width {
			b.WriteString(line)
			b.WriteString("\n  ")
			line = word
		} else {
			line += " " + word
		}
	}
	if line != "" {
		b.WriteString(line)
	}
	return b.String()
}
