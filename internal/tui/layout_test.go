package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/dustinlange/agent-minder/internal/poller"
)

// testLayoutModelWithProject builds a Model with the project inserted in the DB.
// This is needed for tests that insert concerns or other FK-dependent rows.
func testLayoutModelWithProject(t *testing.T) Model {
	m, _ := testModelWithProject(t)
	return m
}

// --- Pure function tests (table-driven) ---

func TestTruncateLine(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxWidth int
		want     string
	}{
		{"empty string", "", 10, ""},
		{"fits exactly", "hello", 5, "hello"},
		{"fits within", "hi", 10, "hi"},
		{"truncated with ellipsis", "hello world test", 10, "hello w..."},
		{"zero width returns as-is", "hello", 0, "hello"},
		{"negative width returns as-is", "hello", -1, "hello"},
		{"maxWidth <= 3 no ellipsis", "hello", 3, "hel"},
		{"maxWidth 1", "hello", 1, "h"},
		{"newlines flattened", "hello\nworld", 20, "hello world"},
		{"newlines flattened then truncated", "hello\nworld\nfoo", 10, "hello w..."},
		{"unicode chars", "héllo wörld", 8, "héllo..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateLine(tt.input, tt.maxWidth)
			if got != tt.want {
				t.Errorf("truncateLine(%q, %d) = %q, want %q", tt.input, tt.maxWidth, got, tt.want)
			}
		})
	}
}

func TestWrapTextHard(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxWidth int
		want     []string
	}{
		{"empty string", "", 10, nil},
		{"fits within", "hello", 10, []string{"hello"}},
		{"exact fit", "hello", 5, []string{"hello"}},                   // maxWidth < 10 → 80, so fits
		{"wraps at maxWidth", "abcdefghij", 5, []string{"abcdefghij"}}, // maxWidth < 10 → 80
		{"wraps at 20", strings.Repeat("a", 40), 20, []string{strings.Repeat("a", 20), strings.Repeat("a", 20)}},
		{"wraps multiple times at 15", strings.Repeat("b", 45), 15, []string{strings.Repeat("b", 15), strings.Repeat("b", 15), strings.Repeat("b", 15)}},
		{"maxWidth < 10 defaults to 80", "short", 3, []string{"short"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapTextHard(tt.input, tt.maxWidth)
			if len(got) != len(tt.want) {
				t.Fatalf("wrapTextHard(%q, %d) returned %d lines, want %d: got %v",
					tt.input, tt.maxWidth, len(got), len(tt.want), got)
			}
			for i, line := range got {
				if line != tt.want[i] {
					t.Errorf("line %d: got %q, want %q", i, line, tt.want[i])
				}
			}
		})
	}
}

func TestSummarizeFailureDetail(t *testing.T) {
	tests := []struct {
		name     string
		reason   string
		detail   string
		maxWidth int
		want     string
	}{
		{
			name:     "generic truncation",
			reason:   "timeout",
			detail:   "The agent timed out after 300 seconds of inactivity",
			maxWidth: 30,
			want:     "The agent timed out after 3...",
		},
		{
			name:     "short detail no truncation",
			reason:   "error",
			detail:   "build failed",
			maxWidth: 40,
			want:     "build failed",
		},
		{
			name:     "permissions with tool names",
			reason:   "permissions",
			detail:   `[{"tool_name":"Bash","denied":true},{"tool_name":"Write","denied":true}]`,
			maxWidth: 80,
			want:     "denied tools: Bash, Write",
		},
		{
			name:     "permissions deduplicates tool names",
			reason:   "permissions",
			detail:   `[{"tool_name":"Bash","denied":true},{"tool_name":"Bash","denied":true}]`,
			maxWidth: 80,
			want:     "denied tools: Bash",
		},
		{
			name:     "permissions invalid json falls back to truncation",
			reason:   "permissions",
			detail:   "not json at all",
			maxWidth: 40,
			want:     "not json at all",
		},
		{
			name:     "small maxWidth defaults to 40",
			reason:   "error",
			detail:   strings.Repeat("x", 50),
			maxWidth: 5,
			want:     strings.Repeat("x", 37) + "...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeFailureDetail(tt.reason, tt.detail, tt.maxWidth)
			if got != tt.want {
				t.Errorf("summarizeFailureDetail(%q, %q, %d) = %q, want %q",
					tt.reason, tt.detail, tt.maxWidth, got, tt.want)
			}
		})
	}
}

func TestTaskFailureReasonSuffix(t *testing.T) {
	tests := []struct {
		name string
		task db.AutopilotTask
		want string
	}{
		{
			name: "non-failed task returns empty",
			task: db.AutopilotTask{Status: "running"},
			want: "",
		},
		{
			name: "failed with no reason returns empty",
			task: db.AutopilotTask{Status: "failed", FailureReason: ""},
			want: "",
		},
		{
			name: "failed with reason returns parenthesized",
			task: db.AutopilotTask{Status: "failed", FailureReason: "timeout"},
			want: " (timeout)",
		},
		{
			name: "failed with permissions reason",
			task: db.AutopilotTask{Status: "failed", FailureReason: "permissions"},
			want: " (permissions)",
		},
		{
			name: "queued task returns empty",
			task: db.AutopilotTask{Status: "queued", FailureReason: "should not show"},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := taskFailureReasonSuffix(tt.task)
			if got != tt.want {
				t.Errorf("taskFailureReasonSuffix() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTaskStatusDisplay(t *testing.T) {
	m := testModel(t)

	statuses := []struct {
		status   string
		contains string
	}{
		{"running", "running"},
		{"queued", "queued"},
		{"blocked", "blocked"},
		{"review", "review"},
		{"done", "done"},
		{"failed", "failed"},
		{"bailed", "bailed"},
		{"stopped", "stopped"},
		{"manual", "manual"},
		{"skipped", "skipped"},
		{"unknown", "unknown"},
	}

	for _, tt := range statuses {
		t.Run(tt.status, func(t *testing.T) {
			got := m.taskStatusDisplay(tt.status)
			if got == "" {
				t.Errorf("taskStatusDisplay(%q) returned empty string", tt.status)
			}
			if !strings.Contains(got, tt.contains) {
				t.Errorf("taskStatusDisplay(%q) = %q, want to contain %q", tt.status, got, tt.contains)
			}
		})
	}
}

// --- View rendering tests ---

func TestView_ZeroWidth_Loading(t *testing.T) {
	m := testModel(t)
	// width=0 means we haven't received a WindowSizeMsg yet.
	v := m.View()
	if v.Content == "" {
		t.Error("View() with zero width should return Loading text, got empty")
	}
	if !strings.Contains(v.Content, "Loading") {
		t.Errorf("View() with zero width = %q, want to contain 'Loading'", v.Content)
	}
}

func TestView_NonEmpty(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40

	v := m.View()
	content := v.Content
	if content == "" {
		t.Fatal("View() returned empty content")
	}
	// Should contain header elements.
	if !strings.Contains(content, "agent-minder") {
		t.Error("View() should contain 'agent-minder' in header")
	}
	if !strings.Contains(content, m.project.Name) {
		t.Errorf("View() should contain project name %q", m.project.Name)
	}
}

func TestView_AltScreenEnabled(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40

	v := m.View()
	if !v.AltScreen {
		t.Error("View() should set AltScreen = true")
	}
}

// --- renderHeader tests ---

func TestRenderHeader_ContainsProjectName(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40

	header := m.renderHeader()
	if !strings.Contains(header, "test-project") {
		t.Errorf("renderHeader() should contain project name, got: %q", header)
	}
}

func TestRenderHeader_ContainsGoal(t *testing.T) {
	m := testModel(t)
	m.width = 120

	header := m.renderHeader()
	if !strings.Contains(header, m.project.GoalType) {
		t.Errorf("renderHeader() should contain goal type %q", m.project.GoalType)
	}
	if !strings.Contains(header, m.project.GoalDescription) {
		t.Errorf("renderHeader() should contain goal description %q", m.project.GoalDescription)
	}
}

func TestRenderHeader_SyncingStatus(t *testing.T) {
	m := testModel(t)
	m.width = 120

	header := m.renderHeader()
	if !strings.Contains(header, "SYNCING") {
		t.Error("renderHeader() should contain SYNCING when not paused")
	}
}

func TestRenderHeader_PausedStatus(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.poller.Pause()

	header := m.renderHeader()
	if !strings.Contains(header, "PAUSED") {
		t.Error("renderHeader() should contain PAUSED when poller is paused")
	}
}

func TestRenderHeader_PollingSpinner(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.polling = true

	header := m.renderHeader()
	if !strings.Contains(header, "polling") {
		t.Error("renderHeader() should contain 'polling' when polling is active")
	}
}

func TestRenderHeader_ThemeName(t *testing.T) {
	m := testModel(t)
	m.width = 120

	header := m.renderHeader()
	themeName := currentTheme().Name
	if !strings.Contains(header, themeName) {
		t.Errorf("renderHeader() should contain theme name %q", themeName)
	}
}

func TestRenderHeader_AutopilotIndicator(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.autopilotMode = "running"

	header := m.renderHeader()
	if !strings.Contains(header, "AUTOPILOT") {
		t.Error("renderHeader() should contain AUTOPILOT when autopilot is running")
	}
}

// --- renderTabBar tests ---

func TestRenderTabBar_ContainsTabNames(t *testing.T) {
	m := testModel(t)
	m.width = 120

	tabBar := m.renderTabBar()
	for _, name := range []string{"Operations", "Analysis", "Autopilot"} {
		if !strings.Contains(tabBar, name) {
			t.Errorf("renderTabBar() should contain tab name %q", name)
		}
	}
}

func TestRenderTabBar_ContainsNumbers(t *testing.T) {
	m := testModel(t)
	m.width = 120

	tabBar := m.renderTabBar()
	for _, num := range []string{"1:", "2:", "3:"} {
		if !strings.Contains(tabBar, num) {
			t.Errorf("renderTabBar() should contain tab number %q", num)
		}
	}
}

func TestRenderTabBar_NewIndicator(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.analysisHasNew = true

	tabBar := m.renderTabBar()
	// The bullet indicator (●) should appear when hasNew is set.
	if !strings.Contains(tabBar, "\u25cf") {
		t.Error("renderTabBar() should contain ● indicator when analysisHasNew is true")
	}
}

// --- renderConcerns tests ---

func TestRenderConcerns_EmptyConcerns(t *testing.T) {
	m := testModel(t)
	m.width = 120

	result := m.renderConcerns()
	if result != "" {
		t.Errorf("renderConcerns() with no concerns should return empty string, got: %q", result)
	}
}

func TestRenderConcerns_ShowsConcernCount(t *testing.T) {
	m := testLayoutModelWithProject(t)
	m.width = 120

	// Insert concerns into the store.
	for i := 0; i < 3; i++ {
		_ = m.store.AddConcern(&db.Concern{ProjectID: m.project.ID, Message: fmt.Sprintf("concern-%d", i), Severity: "warning"})
	}

	result := m.renderConcerns()
	if !strings.Contains(result, "Active Concerns (3)") {
		t.Errorf("renderConcerns() should contain count, got: %q", result)
	}
}

func TestRenderConcerns_SeverityPrefixes(t *testing.T) {
	m := testLayoutModelWithProject(t)
	m.width = 120

	_ = m.store.AddConcern(&db.Concern{ProjectID: m.project.ID, Message: "info concern", Severity: "info"})
	_ = m.store.AddConcern(&db.Concern{ProjectID: m.project.ID, Message: "warning concern", Severity: "warning"})
	_ = m.store.AddConcern(&db.Concern{ProjectID: m.project.ID, Message: "danger concern", Severity: "danger"})

	result := m.renderConcerns()
	if !strings.Contains(result, "INFO") {
		t.Error("renderConcerns() should contain INFO prefix")
	}
	if !strings.Contains(result, "WARN") {
		t.Error("renderConcerns() should contain WARN prefix")
	}
	if !strings.Contains(result, "DANGER") {
		t.Error("renderConcerns() should contain DANGER prefix")
	}
}

func TestRenderConcerns_CapsAtFive(t *testing.T) {
	m := testLayoutModelWithProject(t)
	m.width = 120

	for i := 0; i < 8; i++ {
		_ = m.store.AddConcern(&db.Concern{ProjectID: m.project.ID, Message: fmt.Sprintf("concern-%d is a test concern", i), Severity: "info"})
	}

	result := m.renderConcerns()
	if !strings.Contains(result, "+3 more") {
		t.Errorf("renderConcerns() with 8 concerns should show '+3 more', got: %q", result)
	}
}

func TestRenderConcerns_ExpandedShowsAll(t *testing.T) {
	m := testLayoutModelWithProject(t)
	m.width = 120
	m.concernsExpanded = true

	for i := 0; i < 8; i++ {
		_ = m.store.AddConcern(&db.Concern{ProjectID: m.project.ID, Message: fmt.Sprintf("concern-%d", i), Severity: "info"})
	}

	result := m.renderConcerns()
	if strings.Contains(result, "more") {
		t.Error("renderConcerns() with expanded=true should not show '+N more'")
	}
	// All 8 concerns should be present.
	for i := 0; i < 8; i++ {
		if !strings.Contains(result, fmt.Sprintf("concern-%d", i)) {
			t.Errorf("renderConcerns() expanded should contain concern-%d", i)
		}
	}
}

func TestRenderConcerns_ToggleHint(t *testing.T) {
	m := testLayoutModelWithProject(t)
	m.width = 120

	_ = m.store.AddConcern(&db.Concern{ProjectID: m.project.ID, Message: "test concern", Severity: "info"})

	// Not expanded: shows [c: expand]
	result := m.renderConcerns()
	if !strings.Contains(result, "c: expand") {
		t.Error("renderConcerns() should show 'c: expand' when not expanded")
	}

	// Expanded: shows [c: collapse]
	m.concernsExpanded = true
	result = m.renderConcerns()
	if !strings.Contains(result, "c: collapse") {
		t.Error("renderConcerns() should show 'c: collapse' when expanded")
	}
}

// --- renderTrackedStrip tests ---

func TestRenderTrackedStrip_EmptyState(t *testing.T) {
	m := testModel(t)
	m.width = 120

	result := m.renderTrackedStrip()
	if result != "" {
		t.Errorf("renderTrackedStrip() with no items should return empty, got: %q", result)
	}
}

func TestRenderTrackedStrip_ShowsCount(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.trackedItems = []db.TrackedItem{
		{Number: 1, Owner: "org", Repo: "repo", LastStatus: "Open", Title: "Issue 1"},
		{Number: 2, Owner: "org", Repo: "repo", LastStatus: "Open", Title: "Issue 2"},
	}

	result := m.renderTrackedStrip()
	if !strings.Contains(result, "Tracked (2)") {
		t.Errorf("renderTrackedStrip() should show count, got: %q", result)
	}
}

func TestRenderTrackedStrip_ShowsIssueNumbers(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.trackedItems = []db.TrackedItem{
		{Number: 42, Owner: "org", Repo: "repo", LastStatus: "Open"},
		{Number: 99, Owner: "org", Repo: "repo", LastStatus: "InProg"},
	}

	result := m.renderTrackedStrip()
	if !strings.Contains(result, "#42") {
		t.Error("renderTrackedStrip() should contain #42")
	}
	if !strings.Contains(result, "#99") {
		t.Error("renderTrackedStrip() should contain #99")
	}
}

func TestRenderTrackedStrip_ExpandedShowsTitles(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.trackedExpanded = true
	m.trackedItems = []db.TrackedItem{
		{Number: 1, Owner: "org", Repo: "repo", LastStatus: "Open", Title: "First issue title"},
	}

	result := m.renderTrackedStrip()
	if !strings.Contains(result, "First issue title") {
		t.Error("renderTrackedStrip() expanded should show issue titles")
	}
}

func TestRenderTrackedStrip_ToggleHint(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.trackedItems = []db.TrackedItem{
		{Number: 1, Owner: "org", Repo: "repo", LastStatus: "Open"},
	}

	result := m.renderTrackedStrip()
	if !strings.Contains(result, "x: expand") {
		t.Error("renderTrackedStrip() should show 'x: expand'")
	}

	m.trackedExpanded = true
	result = m.renderTrackedStrip()
	if !strings.Contains(result, "x: collapse") {
		t.Error("renderTrackedStrip() expanded should show 'x: collapse'")
	}
}

// --- renderBottomBar tests ---

func TestRenderBottomBar_NormalMode(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40

	result := m.renderBottomBar()
	// Should not be empty.
	if result == "" {
		t.Error("renderBottomBar() in normal mode should not be empty")
	}
}

func TestRenderBottomBar_BroadcastMode(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.mode = "broadcast"

	result := m.renderBottomBar()
	if !strings.Contains(result, "ctrl+d") {
		t.Error("renderBottomBar() in broadcast mode should contain ctrl+d hint")
	}
	if !strings.Contains(result, "esc") {
		t.Error("renderBottomBar() in broadcast mode should contain esc hint")
	}
}

func TestRenderBottomBar_BroadcastSending(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.mode = "broadcast"
	m.broadcastStatus = "Sending..."

	result := m.renderBottomBar()
	if !strings.Contains(result, "Sending") {
		t.Error("renderBottomBar() should show Sending status")
	}
}

func TestRenderBottomBar_BroadcastStatus(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.mode = "broadcast"
	m.broadcastStatus = "Sent to project/coord"

	result := m.renderBottomBar()
	if !strings.Contains(result, "Sent to project/coord") {
		t.Error("renderBottomBar() should show broadcast status")
	}
}

func TestRenderBottomBar_UserMsgMode(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.mode = "usermsg"

	result := m.renderBottomBar()
	if !strings.Contains(result, "ctrl+d") {
		t.Error("renderBottomBar() in usermsg mode should contain ctrl+d hint")
	}
}

func TestRenderBottomBar_OnboardMode(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.mode = "onboard"

	result := m.renderBottomBar()
	if !strings.Contains(result, "Onboard") {
		t.Error("renderBottomBar() in onboard mode should contain 'Onboard'")
	}
}

func TestRenderBottomBar_SettingsMode(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.mode = "settings"
	m.settingsState = newSettingsState(m.project)

	result := m.renderBottomBar()
	// Should contain navigation hints.
	if !strings.Contains(result, "esc") {
		t.Error("renderBottomBar() in settings mode should contain esc hint")
	}
}

func TestRenderBottomBar_PollConfirm(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.pollConfirm = true

	result := m.renderBottomBar()
	if !strings.Contains(result, "analysis") {
		t.Error("renderBottomBar() with pollConfirm should mention analysis")
	}
}

func TestRenderBottomBar_AutopilotStopConfirm(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.activeTab = tabAutopilot
	m.autopilotMode = "stop-confirm"

	result := m.renderBottomBar()
	if !strings.Contains(result, "Stop all running agents") {
		t.Error("renderBottomBar() in stop-confirm should show stop confirmation")
	}
}

// --- renderHelpBar tests ---

func TestRenderHelpBar_OperationsTab(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.activeTab = tabOperations

	result := m.renderHelpBar()
	if !strings.Contains(result, "quit") {
		t.Error("renderHelpBar() on Ops tab should contain 'quit'")
	}
	if !strings.Contains(result, "help") {
		t.Error("renderHelpBar() on Ops tab should contain 'help'")
	}
}

func TestRenderHelpBar_AnalysisTab(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.activeTab = tabAnalysis

	result := m.renderHelpBar()
	if !strings.Contains(result, "analyze") {
		t.Error("renderHelpBar() on Analysis tab should contain 'analyze'")
	}
	if !strings.Contains(result, "broadcast") {
		t.Error("renderHelpBar() on Analysis tab should contain 'broadcast'")
	}
}

func TestRenderHelpBar_AutopilotTab(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.activeTab = tabAutopilot

	result := m.renderHelpBar()
	if !strings.Contains(result, "start") {
		t.Error("renderHelpBar() on Autopilot tab should contain 'start'")
	}
}

func TestRenderHelpBar_AutopilotRunning(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.activeTab = tabAutopilot
	m.autopilotMode = "running"

	result := m.renderHelpBar()
	if !strings.Contains(result, "stop all") {
		t.Error("renderHelpBar() in running mode should contain 'stop all'")
	}
}

// --- renderHelpOverlay tests ---

func TestRenderHelpOverlay_ContainsKeybindings(t *testing.T) {
	result := renderHelpOverlay(120, tabOperations)

	if !strings.Contains(result, "Keybindings") {
		t.Error("renderHelpOverlay() should contain 'Keybindings'")
	}
	if !strings.Contains(result, "Global") {
		t.Error("renderHelpOverlay() should contain 'Global' column")
	}
	if !strings.Contains(result, "Operations") {
		t.Error("renderHelpOverlay() should contain 'Operations' column")
	}
	if !strings.Contains(result, "Analysis") {
		t.Error("renderHelpOverlay() should contain 'Analysis' column")
	}
	if !strings.Contains(result, "Autopilot") {
		t.Error("renderHelpOverlay() should contain 'Autopilot' column")
	}
}

func TestRenderHelpOverlay_ContainsCloseHint(t *testing.T) {
	result := renderHelpOverlay(120, tabOperations)
	if !strings.Contains(result, "press ? to close") {
		t.Error("renderHelpOverlay() should contain close hint")
	}
}

func TestRenderHelpOverlay_DifferentTabs(t *testing.T) {
	// Just verify it doesn't panic for each tab.
	for _, tab := range []int{tabOperations, tabAnalysis, tabAutopilot} {
		result := renderHelpOverlay(120, tab)
		if result == "" {
			t.Errorf("renderHelpOverlay(120, %d) returned empty", tab)
		}
	}
}

func TestRenderHelpOverlay_SmallWidth(t *testing.T) {
	result := renderHelpOverlay(40, tabOperations)
	if result == "" {
		t.Error("renderHelpOverlay() with small width should still return content")
	}
}

// --- computeHeightBudget tests ---

func TestComputeHeightBudget_OperationsTab(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.activeTab = tabOperations

	analysisH, eventLogH, autopilotTaskH := m.computeHeightBudget()

	// Operations tab gives most space to event log.
	if eventLogH < 4 {
		t.Errorf("eventLogH = %d, want >= 4", eventLogH)
	}
	// Other viewports get minimum.
	if analysisH < 2 {
		t.Errorf("analysisH = %d, want >= 2", analysisH)
	}
	if autopilotTaskH < 2 {
		t.Errorf("autopilotTaskH = %d, want >= 2", autopilotTaskH)
	}
}

func TestComputeHeightBudget_AnalysisTab(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.activeTab = tabAnalysis

	analysisH, eventLogH, autopilotTaskH := m.computeHeightBudget()

	// Analysis tab gives space to analysis VP.
	if analysisH < 2 {
		t.Errorf("analysisH = %d, want >= 2", analysisH)
	}
	if eventLogH != 2 {
		t.Errorf("eventLogH = %d, want 2 (minimum)", eventLogH)
	}
	if autopilotTaskH != 2 {
		t.Errorf("autopilotTaskH = %d, want 2 (minimum)", autopilotTaskH)
	}
}

func TestComputeHeightBudget_AnalysisTab_Expanded(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.activeTab = tabAnalysis
	m.analysisExpanded = true

	analysisH, _, _ := m.computeHeightBudget()

	// Expanded analysis should get more space than collapsed.
	m.analysisExpanded = false
	analysisHCollapsed, _, _ := m.computeHeightBudget()

	if analysisH <= analysisHCollapsed {
		t.Errorf("expanded analysisH (%d) should be > collapsed (%d)", analysisH, analysisHCollapsed)
	}
}

func TestComputeHeightBudget_SmallTerminal(t *testing.T) {
	m := testModel(t)
	m.width = 40
	m.height = 10 // Very small terminal
	m.activeTab = tabOperations

	analysisH, eventLogH, autopilotTaskH := m.computeHeightBudget()

	// Minimums should be enforced.
	if analysisH < 2 {
		t.Errorf("analysisH = %d, want >= 2", analysisH)
	}
	if eventLogH < 4 { // ops tab min is 4
		t.Errorf("eventLogH = %d, want >= 4", eventLogH)
	}
	if autopilotTaskH < 2 {
		t.Errorf("autopilotTaskH = %d, want >= 2", autopilotTaskH)
	}
}

func TestComputeHeightBudget_AutopilotTab_NonRunning(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.activeTab = tabAutopilot
	m.autopilotMode = "" // idle

	_, _, autopilotTaskH := m.computeHeightBudget()

	// Non-running states use static content.
	if autopilotTaskH != 2 {
		t.Errorf("autopilotTaskH = %d, want 2 for non-running state", autopilotTaskH)
	}
}

// --- renderAutopilotTab tests ---

func TestRenderAutopilotTab_IdleState(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.autopilotMode = ""
	m.autopilotSupervisor = nil

	result := m.renderAutopilotTab()
	if !strings.Contains(result, "Autopilot") {
		t.Error("renderAutopilotTab() idle should contain 'Autopilot'")
	}
	if !strings.Contains(result, "press a to start") {
		t.Error("renderAutopilotTab() idle should contain 'press a to start'")
	}
}

func TestRenderAutopilotTab_ConfirmState(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.autopilotMode = "confirm"
	m.autopilotTotal = 10
	m.autopilotUnblocked = 7

	result := m.renderAutopilotTab()
	if !strings.Contains(result, "Ready to Launch") {
		t.Error("renderAutopilotTab() confirm should contain 'Ready to Launch'")
	}
	if !strings.Contains(result, "10 issues found") {
		t.Error("renderAutopilotTab() confirm should contain issue count")
	}
	if !strings.Contains(result, "7 unblocked") {
		t.Error("renderAutopilotTab() confirm should contain unblocked count")
	}
}

func TestRenderAutopilotTab_ScanConfirmState(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.autopilotMode = "scan-confirm"

	result := m.renderAutopilotTab()
	if !strings.Contains(result, "Settings Preview") {
		t.Error("renderAutopilotTab() scan-confirm should contain 'Settings Preview'")
	}
	if !strings.Contains(result, "Max agents") {
		t.Error("renderAutopilotTab() scan-confirm should show max agents setting")
	}
}

func TestRenderAutopilotTab_CompletedState(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.autopilotMode = "completed"

	result := m.renderAutopilotTab()
	if !strings.Contains(result, "Completed") {
		t.Error("renderAutopilotTab() completed should contain 'Completed'")
	}
}

// --- renderOperationsTab tests ---

func TestRenderOperationsTab_ContainsEventLog(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40

	result := m.renderOperationsTab()
	if !strings.Contains(result, "Event Log") {
		t.Error("renderOperationsTab() should contain 'Event Log' header")
	}
}

// --- renderAnalysisTab tests ---

func TestRenderAnalysisTab_NoAnalysis(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40

	result := m.renderAnalysisTab()
	if !strings.Contains(result, "Press R to run analysis") {
		t.Error("renderAnalysisTab() with no analysis should show call to action")
	}
}

func TestRenderAnalysisTab_WithAnalysis(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.lastPoll = &poller.PollResult{
		Tier2Analysis: "Test analysis result",
		NewCommits:    5,
		NewMessages:   2,
		Duration:      time.Second * 3,
	}
	m.resizeViewports()
	m.rebuildAnalysisContent()

	result := m.renderAnalysisTab()
	if !strings.Contains(result, "Last Analysis") {
		t.Error("renderAnalysisTab() with analysis should contain 'Last Analysis'")
	}
}

func TestRenderAnalysisTab_ExpandCollapseHint(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.lastPoll = &poller.PollResult{
		Tier2Analysis: "some analysis",
		Duration:      time.Second,
	}
	m.resizeViewports()
	m.rebuildAnalysisContent()

	// Not expanded.
	result := m.renderAnalysisTab()
	if !strings.Contains(result, "e: expand") {
		t.Error("renderAnalysisTab() should show 'e: expand'")
	}

	// Expanded.
	m.analysisExpanded = true
	result = m.renderAnalysisTab()
	if !strings.Contains(result, "e: collapse") {
		t.Error("renderAnalysisTab() expanded should show 'e: collapse'")
	}
}

// --- renderSettingsView tests ---

func TestRenderSettingsView_NilState(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.settingsState = nil

	result := m.renderSettingsView()
	if result != "" {
		t.Errorf("renderSettingsView() with nil state should return empty, got: %q", result)
	}
}

func TestRenderSettingsView_ContainsHeader(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.settingsState = newSettingsState(m.project)

	result := m.renderSettingsView()
	if !strings.Contains(result, "Settings") {
		t.Error("renderSettingsView() should contain 'Settings' header")
	}
}

func TestRenderSettingsView_ContainsFields(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.settingsState = newSettingsState(m.project)

	result := m.renderSettingsView()
	expectedFields := []string{
		"Sync interval",
		"Analyzer focus",
		"Autopilot max agents",
		"Autopilot max turns",
		"Autopilot max budget",
		"Autopilot skip label",
	}
	for _, field := range expectedFields {
		if !strings.Contains(result, field) {
			t.Errorf("renderSettingsView() should contain field %q", field)
		}
	}
}

// --- renderFilterView tests ---

func TestRenderFilterView_NilState(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.filterState = nil

	result := m.renderFilterView()
	if result != "" {
		t.Errorf("renderFilterView() with nil state should return empty, got: %q", result)
	}
}

func TestRenderFilterView_ContainsHeader(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.filterState = newFilterState([]poller.GitHubRepo{
		{Owner: "org", Repo: "repo"},
	}, false)

	result := m.renderFilterView()
	if !strings.Contains(result, "Filter Issues") {
		t.Error("renderFilterView() should contain 'Filter Issues' header")
	}
}

func TestRenderFilterView_SelectType(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.filterState = newFilterState([]poller.GitHubRepo{
		{Owner: "org", Repo: "repo"},
	}, false)
	// Auto-skipped to type selection since only 1 repo.

	result := m.renderFilterView()
	if !strings.Contains(result, "label") {
		t.Error("renderFilterView() at select type should contain 'label'")
	}
	if !strings.Contains(result, "milestone") {
		t.Error("renderFilterView() at select type should contain 'milestone'")
	}
	if !strings.Contains(result, "assignee") {
		t.Error("renderFilterView() at select type should contain 'assignee'")
	}
}

func TestRenderFilterView_RepoSelection(t *testing.T) {
	m := testModel(t)
	m.width = 120
	repos := []poller.GitHubRepo{
		{Owner: "org1", Repo: "repo1"},
		{Owner: "org2", Repo: "repo2"},
	}
	m.filterState = newFilterState(repos, false)
	// With multiple repos, starts at repo selection.

	result := m.renderFilterView()
	if !strings.Contains(result, "Select repository") {
		t.Error("renderFilterView() with multiple repos should show repo selection")
	}
	if !strings.Contains(result, "org1/repo1") {
		t.Error("renderFilterView() should show first repo")
	}
	if !strings.Contains(result, "org2/repo2") {
		t.Error("renderFilterView() should show second repo")
	}
}

// --- View mode routing tests ---

func TestView_SettingsMode(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.mode = "settings"
	m.settingsState = newSettingsState(m.project)

	v := m.View()
	if !strings.Contains(v.Content, "Settings") {
		t.Error("View() in settings mode should contain Settings")
	}
}

func TestView_FilterMode(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.mode = "filter"
	m.filterState = newFilterState([]poller.GitHubRepo{
		{Owner: "org", Repo: "repo"},
	}, false)

	v := m.View()
	if !strings.Contains(v.Content, "Filter Issues") {
		t.Error("View() in filter mode should contain Filter Issues")
	}
}

func TestView_WarningBanner(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.warningBanner = "API rate limit approaching"

	v := m.View()
	if !strings.Contains(v.Content, "API rate limit approaching") {
		t.Error("View() should show warning banner text")
	}
	if !strings.Contains(v.Content, "press d to dismiss") {
		t.Error("View() should show dismiss hint for warning banner")
	}
}

func TestView_HelpOverlay(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.showHelp = true

	v := m.View()
	if !strings.Contains(v.Content, "Keybindings") {
		t.Error("View() with showHelp should contain help overlay")
	}
}

// --- wrapText (from filter.go) tests ---

func TestWrapText(t *testing.T) {
	tests := []struct {
		name  string
		input string
		width int
		want  string
	}{
		{"short string", "hello", 20, "hello"},
		{"exact width", "hello world", 11, "hello world"},
		{"needs wrapping", "hello world foo bar baz", 10, "hello\n  world foo\n  bar baz"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapText(tt.input, tt.width)
			if got != tt.want {
				t.Errorf("wrapText(%q, %d) = %q, want %q", tt.input, tt.width, got, tt.want)
			}
		})
	}
}

// --- parseSkipLabels (from settings.go) tests ---

func TestParseSkipLabels(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty returns default", "", []string{"no-agent"}},
		{"single label", "skip-me", []string{"skip-me"}},
		{"comma separated", "skip-me, also-skip", []string{"skip-me", "also-skip"}},
		{"trims whitespace", " a , b , c ", []string{"a", "b", "c"}},
		{"empty parts dropped", "a,,b", []string{"a", "b"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSkipLabels(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("parseSkipLabels(%q) returned %d items, want %d: %v", tt.input, len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("item %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// --- formatDuration (from app.go) tests ---

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{"5 minutes", 5 * time.Minute, "5m"},
		{"30 minutes", 30 * time.Minute, "30m"},
		{"1 hour", time.Hour, "1h"},
		{"1 hour 30 min", 90 * time.Minute, "1h30m"},
		{"2 hours", 2 * time.Hour, "2h"},
		{"0 minutes", 30 * time.Second, "0m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDuration(tt.duration)
			if got != tt.want {
				t.Errorf("formatDuration(%v) = %q, want %q", tt.duration, got, tt.want)
			}
		})
	}
}

// --- helpOverlayHeight test ---

func TestHelpOverlayHeight(t *testing.T) {
	h := helpOverlayHeight()
	if h < 2 {
		t.Errorf("helpOverlayHeight() = %d, want >= 2", h)
	}
	// Should be 1 (header) + max hint group length.
	maxHints := len(globalHints)
	for _, group := range [][]helpHint{opsHints, analysisHints, autopilotHints} {
		if len(group) > maxHints {
			maxHints = len(group)
		}
	}
	expected := 1 + maxHints
	if h != expected {
		t.Errorf("helpOverlayHeight() = %d, want %d (1 + %d)", h, expected, maxHints)
	}
}

// --- bottomBarHeight tests ---

func TestBottomBarHeight_NormalMode(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40

	h := m.bottomBarHeight()
	if h != 2 {
		t.Errorf("bottomBarHeight() in normal mode = %d, want 2", h)
	}
}

func TestBottomBarHeight_BroadcastMode(t *testing.T) {
	m := testModel(t)
	m.mode = "broadcast"

	h := m.bottomBarHeight()
	if h != 6 {
		t.Errorf("bottomBarHeight() in broadcast mode = %d, want 6", h)
	}
}

func TestBottomBarHeight_BroadcastWithStatus(t *testing.T) {
	m := testModel(t)
	m.mode = "broadcast"
	m.broadcastStatus = "Sent to topic"

	h := m.bottomBarHeight()
	if h != 3 {
		t.Errorf("bottomBarHeight() in broadcast with status = %d, want 3", h)
	}
}

func TestBottomBarHeight_HelpOverlay(t *testing.T) {
	m := testModel(t)
	m.showHelp = true

	h := m.bottomBarHeight()
	expected := 2 + helpOverlayHeight() + 2
	if h != expected {
		t.Errorf("bottomBarHeight() with help = %d, want %d", h, expected)
	}
}

// --- extractLogToolInput tests ---

func TestExtractLogToolInput(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  string
	}{
		{"nil input", nil, ""},
		{"string input", "just a string", ""},
		{"command key", map[string]any{"command": "go test ./..."}, "go test ./..."},
		{"file_path key", map[string]any{"file_path": "/foo/bar.go"}, "/foo/bar.go"},
		{"pattern key", map[string]any{"pattern": "*.go"}, "*.go"},
		{"prompt key", map[string]any{"prompt": "explain this"}, "explain this"},
		{"query key", map[string]any{"query": "search term"}, "search term"},
		{"description key", map[string]any{"description": "does stuff"}, "does stuff"},
		{"prefers command over others", map[string]any{
			"command":   "ls",
			"file_path": "/foo",
		}, "ls"},
		{"fallback to json", map[string]any{
			"unknown": "value",
		}, `{"unknown":"value"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractLogToolInput(tt.input)
			if got != tt.want {
				t.Errorf("extractLogToolInput() = %q, want %q", got, tt.want)
			}
		})
	}
}
