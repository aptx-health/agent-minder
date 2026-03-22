package tui

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/dustinlange/agent-minder/internal/autopilot"
	"github.com/dustinlange/agent-minder/internal/db"
	ghpkg "github.com/dustinlange/agent-minder/internal/github"
	"github.com/dustinlange/agent-minder/internal/poller"
)

// --- Test helpers ---

// testStore creates an in-memory Store with schema applied.
func testStore(t *testing.T) *db.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	conn, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return db.NewStore(conn)
}

// testProject returns a minimal project suitable for TUI tests.
func testProject() *db.Project {
	return &db.Project{
		ID:                 1,
		Name:               "test-project",
		GoalType:           "development",
		GoalDescription:    "unit testing",
		RefreshIntervalSec: 60,
		StatusIntervalSec:  30,
	}
}

// testModel builds a Model wired to a real Store and a no-LLM Poller.
// The poller is created with nil providers (no LLM calls will be made).
func testModel(t *testing.T) Model {
	t.Helper()
	store := testStore(t)
	proj := testProject()
	p := poller.New(store, proj, nil, nil)
	return New(proj, store, p)
}

// keyPress creates a tea.KeyPressMsg for a printable character key.
func keyPress(char rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: char, Text: string(char)}
}

// specialKey creates a tea.KeyPressMsg for a special key (tab, esc, etc.).
func specialKey(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code}
}

// shiftTabKey creates a tea.KeyPressMsg for shift+tab.
func shiftTabKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
}

// ctrlCKey creates a tea.KeyPressMsg for ctrl+c.
func ctrlCKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
}

// testModelWithProject creates a model and persists the project to the DB,
// returning both so tests can insert autopilot tasks against the project ID.
func testModelWithProject(t *testing.T) (Model, *db.Store) {
	t.Helper()
	store := testStore(t)
	proj := testProject()
	if err := store.CreateProject(proj); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	p := poller.New(store, proj, nil, nil)
	return New(proj, store, p), store
}

// --- Tests ---

func TestNew_DefaultState(t *testing.T) {
	m := testModel(t)

	if m.activeTab != tabOperations {
		t.Errorf("activeTab = %d, want %d (tabOperations)", m.activeTab, tabOperations)
	}
	if m.mode != "normal" {
		t.Errorf("mode = %q, want %q", m.mode, "normal")
	}
	if m.polling != true {
		t.Error("polling should be true after New (initial status check)")
	}
	if m.showHelp {
		t.Error("showHelp should be false")
	}
	if m.analysisExpanded {
		t.Error("analysisExpanded should be false")
	}
	if m.broadcastStatus != "" {
		t.Errorf("broadcastStatus = %q, want empty", m.broadcastStatus)
	}
	if m.onboardStatus != "" {
		t.Errorf("onboardStatus = %q, want empty", m.onboardStatus)
	}
	if m.userMsgStatus != "" {
		t.Errorf("userMsgStatus = %q, want empty", m.userMsgStatus)
	}
	if m.autopilotMode != "" {
		t.Errorf("autopilotMode = %q, want empty", m.autopilotMode)
	}
	if m.showLogViewer {
		t.Error("showLogViewer should be false")
	}
	if m.warningBanner != "" {
		t.Errorf("warningBanner = %q, want empty", m.warningBanner)
	}
	if len(m.events) != 0 {
		t.Errorf("events length = %d, want 0", len(m.events))
	}
	if m.lastPoll != nil {
		t.Error("lastPoll should be nil")
	}
}

func TestInit_ReturnsBatchCmd(t *testing.T) {
	m := testModel(t)
	cmd := m.Init()

	// Init should return a non-nil command (a batch of commands).
	if cmd == nil {
		t.Fatal("Init() returned nil, want non-nil batch command")
	}
}

func TestInit_WithIdlePause(t *testing.T) {
	store := testStore(t)
	proj := testProject()
	proj.IdlePauseSec = 300
	p := poller.New(store, proj, nil, nil)
	m := New(proj, store, p)

	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init() with IdlePauseSec > 0 returned nil, want non-nil batch command")
	}
}

// --- Tab switching ---

func TestTabSwitching_NumberKeys(t *testing.T) {
	m := testModel(t)

	// '1' → tabOperations
	result, _ := m.Update(keyPress('1'))
	m = result.(Model)
	if m.activeTab != tabOperations {
		t.Errorf("after '1': activeTab = %d, want %d", m.activeTab, tabOperations)
	}

	// '2' → tabAnalysis
	result, _ = m.Update(keyPress('2'))
	m = result.(Model)
	if m.activeTab != tabAnalysis {
		t.Errorf("after '2': activeTab = %d, want %d", m.activeTab, tabAnalysis)
	}

	// '3' → tabAutopilot
	result, _ = m.Update(keyPress('3'))
	m = result.(Model)
	if m.activeTab != tabAutopilot {
		t.Errorf("after '3': activeTab = %d, want %d", m.activeTab, tabAutopilot)
	}
}

func TestTabSwitching_TabKey(t *testing.T) {
	m := testModel(t)

	// Starts at tabOperations (0).
	if m.activeTab != tabOperations {
		t.Fatalf("initial activeTab = %d, want %d", m.activeTab, tabOperations)
	}

	// tab → tabAnalysis (1)
	result, _ := m.Update(specialKey(tea.KeyTab))
	m = result.(Model)
	if m.activeTab != tabAnalysis {
		t.Errorf("after tab: activeTab = %d, want %d", m.activeTab, tabAnalysis)
	}

	// tab → tabAutopilot (2)
	result, _ = m.Update(specialKey(tea.KeyTab))
	m = result.(Model)
	if m.activeTab != tabAutopilot {
		t.Errorf("after 2nd tab: activeTab = %d, want %d", m.activeTab, tabAutopilot)
	}

	// tab → wraps to tabOperations (0)
	result, _ = m.Update(specialKey(tea.KeyTab))
	m = result.(Model)
	if m.activeTab != tabOperations {
		t.Errorf("after 3rd tab: activeTab = %d, want %d", m.activeTab, tabOperations)
	}
}

func TestTabSwitching_ShiftTab(t *testing.T) {
	m := testModel(t)

	// shift+tab from tabOperations (0) → wraps to tabAutopilot (2)
	result, _ := m.Update(shiftTabKey())
	m = result.(Model)
	if m.activeTab != tabAutopilot {
		t.Errorf("after shift+tab: activeTab = %d, want %d", m.activeTab, tabAutopilot)
	}

	// shift+tab → tabAnalysis (1)
	result, _ = m.Update(shiftTabKey())
	m = result.(Model)
	if m.activeTab != tabAnalysis {
		t.Errorf("after 2nd shift+tab: activeTab = %d, want %d", m.activeTab, tabAnalysis)
	}
}

func TestTabSwitching_ClearsNewIndicator(t *testing.T) {
	m := testModel(t)
	m.analysisHasNew = true

	// Switching to tab 2 (analysis) should clear the indicator.
	result, _ := m.Update(keyPress('2'))
	m = result.(Model)
	if m.analysisHasNew {
		t.Error("analysisHasNew should be false after switching to Analysis tab")
	}
}

func TestTabSwitching_ClearsAutopilotIndicator(t *testing.T) {
	m := testModel(t)
	m.autopilotHasNew = true

	// Switching to tab 3 (autopilot) should clear the indicator.
	result, _ := m.Update(keyPress('3'))
	m = result.(Model)
	if m.autopilotHasNew {
		t.Error("autopilotHasNew should be false after switching to Autopilot tab")
	}
}

// --- Quit ---

func TestQuit_QKey(t *testing.T) {
	m := testModel(t)
	_, cmd := m.Update(keyPress('q'))
	if cmd == nil {
		t.Fatal("'q' should return a non-nil quit command")
	}
	// Execute the command and check for tea.QuitMsg.
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("'q' cmd returned %T, want tea.QuitMsg", msg)
	}
}

func TestQuit_CtrlC(t *testing.T) {
	m := testModel(t)
	_, cmd := m.Update(ctrlCKey())
	if cmd == nil {
		t.Fatal("ctrl+c should return a non-nil quit command")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("ctrl+c cmd returned %T, want tea.QuitMsg", msg)
	}
}

// --- Pause/Resume ---

func TestPauseResume(t *testing.T) {
	m := testModel(t)

	// Initially not paused.
	if m.poller.IsPaused() {
		t.Fatal("poller should not be paused initially")
	}

	// 'p' → pause
	result, _ := m.Update(keyPress('p'))
	m = result.(Model)
	if !m.poller.IsPaused() {
		t.Error("poller should be paused after 'p'")
	}

	// 'p' again → resume
	result, _ = m.Update(keyPress('p'))
	m = result.(Model)
	if m.poller.IsPaused() {
		t.Error("poller should be resumed after second 'p'")
	}
}

// --- Expand toggle ---

func TestExpandToggle_AnalysisTab(t *testing.T) {
	m := testModel(t)
	// Must be on Analysis tab.
	m.activeTab = tabAnalysis

	result, _ := m.Update(keyPress('e'))
	m = result.(Model)
	if !m.analysisExpanded {
		t.Error("analysisExpanded should be true after 'e' on Analysis tab")
	}

	result, _ = m.Update(keyPress('e'))
	m = result.(Model)
	if m.analysisExpanded {
		t.Error("analysisExpanded should be false after second 'e'")
	}
}

func TestExpandToggle_AutopilotTab(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot

	result, _ := m.Update(keyPress('e'))
	m = result.(Model)
	if !m.autopilotTasksExpanded {
		t.Error("autopilotTasksExpanded should be true after 'e' on Autopilot tab")
	}

	result, _ = m.Update(keyPress('e'))
	m = result.(Model)
	if m.autopilotTasksExpanded {
		t.Error("autopilotTasksExpanded should be false after second 'e'")
	}
}

func TestExpandToggle_OpsTabNoOp(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabOperations

	result, _ := m.Update(keyPress('e'))
	m = result.(Model)
	// On operations tab, 'e' should be a no-op (neither expand flag changes).
	if m.analysisExpanded {
		t.Error("analysisExpanded should remain false on Ops tab")
	}
	if m.autopilotTasksExpanded {
		t.Error("autopilotTasksExpanded should remain false on Ops tab")
	}
}

// --- Theme cycle ---

func TestThemeCycle(t *testing.T) {
	m := testModel(t)
	origTheme := currentTheme().Name

	result, _ := m.Update(keyPress('t'))
	m = result.(Model)
	newTheme := currentTheme().Name
	if newTheme == origTheme {
		t.Error("theme should change after 't'")
	}

	// Cycle back by pressing enough times.
	for range len(themes) - 1 {
		result, _ = m.Update(keyPress('t'))
		m = result.(Model)
	}
	if currentTheme().Name != origTheme {
		t.Errorf("after full cycle: theme = %q, want %q", currentTheme().Name, origTheme)
	}
}

// --- Help toggle ---

func TestHelpToggle(t *testing.T) {
	m := testModel(t)

	result, _ := m.Update(keyPress('?'))
	m = result.(Model)
	if !m.showHelp {
		t.Error("showHelp should be true after '?'")
	}

	result, _ = m.Update(keyPress('?'))
	m = result.(Model)
	if m.showHelp {
		t.Error("showHelp should be false after second '?'")
	}
}

// --- Mode transitions ---

func TestModeTransition_Broadcast(t *testing.T) {
	m := testModel(t)

	result, _ := m.Update(keyPress('m'))
	m = result.(Model)
	if m.mode != "broadcast" {
		t.Errorf("mode = %q, want %q after 'm'", m.mode, "broadcast")
	}

	// Escape returns to normal.
	result, _ = m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.mode != "normal" {
		t.Errorf("mode = %q, want %q after escape", m.mode, "normal")
	}
}

func TestModeTransition_UserMsg(t *testing.T) {
	m := testModel(t)

	result, _ := m.Update(keyPress('u'))
	m = result.(Model)
	if m.mode != "usermsg" {
		t.Errorf("mode = %q, want %q after 'u'", m.mode, "usermsg")
	}

	// Escape returns to normal.
	result, _ = m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.mode != "normal" {
		t.Errorf("mode = %q, want %q after escape", m.mode, "normal")
	}
}

func TestModeTransition_Onboard(t *testing.T) {
	m := testModel(t)

	result, _ := m.Update(keyPress('o'))
	m = result.(Model)
	if m.mode != "onboard" {
		t.Errorf("mode = %q, want %q after 'o'", m.mode, "onboard")
	}

	// Escape returns to normal.
	result, _ = m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.mode != "normal" {
		t.Errorf("mode = %q, want %q after escape", m.mode, "normal")
	}
}

func TestModeTransition_Settings(t *testing.T) {
	m := testModel(t)

	result, _ := m.Update(keyPress('s'))
	m = result.(Model)
	if m.mode != "settings" {
		t.Errorf("mode = %q, want %q after 's'", m.mode, "settings")
	}
}

func TestModeTransition_AutopilotRequiresTab3(t *testing.T) {
	m := testModel(t)
	// 'a' on non-autopilot tab should be a no-op.
	m.activeTab = tabOperations
	result, _ := m.Update(keyPress('a'))
	m = result.(Model)
	if m.autopilotMode != "" {
		t.Errorf("autopilotMode = %q, want empty (a on non-autopilot tab should no-op)", m.autopilotMode)
	}
}

func TestModeTransition_StopAutopilotRequiresTab3(t *testing.T) {
	m := testModel(t)
	// 'A' on non-autopilot tab should be a no-op.
	m.activeTab = tabOperations
	result, _ := m.Update(keyPress('A'))
	m = result.(Model)
	if m.autopilotMode != "" {
		t.Errorf("autopilotMode = %q, want empty (A on non-autopilot tab should no-op)", m.autopilotMode)
	}
}

// --- 'r' key (full analysis) ---

func TestPollNow_RKey(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabOperations
	result, cmd := m.Update(keyPress('r'))
	m = result.(Model)
	if !m.polling {
		t.Error("polling should be true after 'r'")
	}
	if m.activeTab != tabAnalysis {
		t.Errorf("activeTab = %d, want %d (should switch to Analysis)", m.activeTab, tabAnalysis)
	}
	if cmd == nil {
		t.Error("'r' should return a non-nil command (PollNow)")
	}
}

// --- Warning banner dismiss ---

func TestWarningBannerDismiss(t *testing.T) {
	m := testModel(t)
	m.warningBanner = "some warning"

	result, _ := m.Update(keyPress('d'))
	m = result.(Model)
	if m.warningBanner != "" {
		t.Errorf("warningBanner = %q, want empty after 'd'", m.warningBanner)
	}
}

// --- Worktree toggle ---

func TestWorktreeToggle(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabOperations

	// Default is true; first toggle hides, second toggle shows.
	result, _ := m.Update(keyPress('w'))
	m = result.(Model)
	if m.showWorktrees {
		t.Error("showWorktrees should be false after 'w' on Ops tab (was default true)")
	}

	result, _ = m.Update(keyPress('w'))
	m = result.(Model)
	if !m.showWorktrees {
		t.Error("showWorktrees should be true after second 'w'")
	}
}

func TestWorktreeToggle_NonOpsTabNoOp(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAnalysis

	// Default is true, but 'w' on non-Ops tab should be a no-op.
	result, _ := m.Update(keyPress('w'))
	m = result.(Model)
	if !m.showWorktrees {
		t.Error("showWorktrees should remain true on non-Ops tab (no-op)")
	}
}

// --- Tracked items expand toggle ---

func TestTrackedExpand_OpsTab(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabOperations

	result, _ := m.Update(keyPress('x'))
	m = result.(Model)
	if !m.trackedExpanded {
		t.Error("trackedExpanded should be true after 'x' on Ops tab")
	}

	result, _ = m.Update(keyPress('x'))
	m = result.(Model)
	if m.trackedExpanded {
		t.Error("trackedExpanded should be false after second 'x'")
	}
}

func TestTrackedExpand_NonOpsTabNoOp(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAnalysis

	result, _ := m.Update(keyPress('x'))
	m = result.(Model)
	if m.trackedExpanded {
		t.Error("trackedExpanded should remain false on non-Ops tab")
	}
}

// --- Polled event processing ---

func TestPollerEventMsg_AppendsEvent(t *testing.T) {
	m := testModel(t)
	// Give model dimensions so resizeViewports doesn't bail.
	m.width = 120
	m.height = 40

	evt := pollerEventMsg(poller.Event{
		Time:    time.Now(),
		Type:    "poll",
		Summary: "test event",
	})
	result, _ := m.Update(evt)
	m = result.(Model)

	if len(m.events) != 1 {
		t.Fatalf("events length = %d, want 1", len(m.events))
	}
	if m.events[0].Summary != "test event" {
		t.Errorf("event summary = %q, want %q", m.events[0].Summary, "test event")
	}
}

func TestPollerEventMsg_CapsAt50(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40

	// Push 60 events.
	for range 60 {
		evt := pollerEventMsg(poller.Event{
			Time:    time.Now(),
			Type:    "poll",
			Summary: "event",
		})
		result, _ := m.Update(evt)
		m = result.(Model)
	}

	if len(m.events) != 50 {
		t.Errorf("events length = %d, want 50 (capped)", len(m.events))
	}
}

func TestPollerEventMsg_WarningSetsBanner(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40

	evt := pollerEventMsg(poller.Event{
		Time:    time.Now(),
		Type:    "warning",
		Summary: "API rate limit approaching",
	})
	result, _ := m.Update(evt)
	m = result.(Model)
	if m.warningBanner != "API rate limit approaching" {
		t.Errorf("warningBanner = %q, want %q", m.warningBanner, "API rate limit approaching")
	}
}

func TestPollerEventMsg_PollResultClearsPolling(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.polling = true

	evt := pollerEventMsg(poller.Event{
		Time: time.Now(),
		Type: "poll",
		PollResult: &poller.PollResult{
			NewCommits:    3,
			Tier2Analysis: "Analysis result",
		},
	})
	result, _ := m.Update(evt)
	m = result.(Model)

	if m.polling {
		t.Error("polling should be false after receiving PollResult")
	}
	if m.lastPoll == nil {
		t.Fatal("lastPoll should be set after PollResult event")
	}
	if m.lastPoll.NewCommits != 3 {
		t.Errorf("lastPoll.NewCommits = %d, want 3", m.lastPoll.NewCommits)
	}
}

func TestPollerEventMsg_AnalysisNewIndicator(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.activeTab = tabOperations

	evt := pollerEventMsg(poller.Event{
		Time: time.Now(),
		Type: "poll",
		PollResult: &poller.PollResult{
			Tier2Analysis: "New analysis!",
		},
	})
	result, _ := m.Update(evt)
	m = result.(Model)

	if !m.analysisHasNew {
		t.Error("analysisHasNew should be true when new analysis arrives on Ops tab")
	}
}

// --- Broadcast result messages ---

func TestBroadcastResultMsg_Success(t *testing.T) {
	m := testModel(t)
	m.mode = "broadcast"

	result, _ := m.Update(broadcastResultMsg{topic: "project/coord"})
	m = result.(Model)
	if m.broadcastStatus != "Sent to project/coord" {
		t.Errorf("broadcastStatus = %q, want %q", m.broadcastStatus, "Sent to project/coord")
	}
}

func TestBroadcastResultMsg_Error(t *testing.T) {
	m := testModel(t)
	m.mode = "broadcast"

	result, _ := m.Update(broadcastResultMsg{err: errTest})
	m = result.(Model)
	if m.broadcastStatus != "Error: test error" {
		t.Errorf("broadcastStatus = %q, want %q", m.broadcastStatus, "Error: test error")
	}
}

// --- User message result messages ---

func TestUserMsgResultMsg_Success(t *testing.T) {
	m := testModel(t)
	m.mode = "usermsg"
	m.width = 120
	m.height = 40

	result, _ := m.Update(userMsgResultMsg{response: "Here's the status..."})
	m = result.(Model)
	if m.userMsgStatus != "" {
		t.Errorf("userMsgStatus = %q, want empty on success", m.userMsgStatus)
	}
	if m.activeTab != tabAnalysis {
		t.Errorf("activeTab = %d, want %d (tabAnalysis)", m.activeTab, tabAnalysis)
	}
	if m.lastPoll == nil || m.lastPoll.Tier2Analysis != "Here's the status..." {
		t.Error("expected lastPoll to contain analyzer response")
	}
	if m.mode != "normal" {
		t.Errorf("mode = %q, want normal", m.mode)
	}
}

func TestUserMsgResultMsg_Error(t *testing.T) {
	m := testModel(t)
	m.mode = "usermsg"

	result, _ := m.Update(userMsgResultMsg{err: errTest})
	m = result.(Model)
	if m.userMsgStatus != "Error: test error" {
		t.Errorf("userMsgStatus = %q, want %q", m.userMsgStatus, "Error: test error")
	}
}

// --- Onboard result messages ---

func TestOnboardResultMsg_Success(t *testing.T) {
	m := testModel(t)
	m.mode = "onboard"

	result, _ := m.Update(onboardResultMsg{topic: "test/onboarding"})
	m = result.(Model)
	if m.onboardStatus != "Onboarding published to test/onboarding" {
		t.Errorf("onboardStatus = %q, want %q", m.onboardStatus, "Onboarding published to test/onboarding")
	}
}

func TestOnboardResultMsg_Error(t *testing.T) {
	m := testModel(t)
	m.mode = "onboard"

	result, _ := m.Update(onboardResultMsg{err: errTest})
	m = result.(Model)
	if m.onboardStatus != "Error: test error" {
		t.Errorf("onboardStatus = %q, want %q", m.onboardStatus, "Error: test error")
	}
}

// --- Clear status messages ---

func TestClearBroadcastStatus(t *testing.T) {
	m := testModel(t)
	m.mode = "broadcast"
	m.broadcastStatus = "Sent to something"

	result, _ := m.Update(clearBroadcastStatusMsg{})
	m = result.(Model)
	if m.broadcastStatus != "" {
		t.Errorf("broadcastStatus = %q, want empty", m.broadcastStatus)
	}
	if m.mode != "normal" {
		t.Errorf("mode = %q, want %q after clear", m.mode, "normal")
	}
}

func TestClearUserMsgStatus(t *testing.T) {
	m := testModel(t)
	m.mode = "usermsg"
	m.userMsgStatus = "Posted to something"

	result, _ := m.Update(clearUserMsgStatusMsg{})
	m = result.(Model)
	if m.userMsgStatus != "" {
		t.Errorf("userMsgStatus = %q, want empty", m.userMsgStatus)
	}
	if m.mode != "normal" {
		t.Errorf("mode = %q, want %q after clear", m.mode, "normal")
	}
}

func TestClearOnboardStatus(t *testing.T) {
	m := testModel(t)
	m.mode = "onboard"
	m.onboardStatus = "Published to something"

	result, _ := m.Update(clearOnboardStatusMsg{})
	m = result.(Model)
	if m.onboardStatus != "" {
		t.Errorf("onboardStatus = %q, want empty", m.onboardStatus)
	}
	if m.mode != "normal" {
		t.Errorf("mode = %q, want %q after clear", m.mode, "normal")
	}
}

// --- Window resize ---

func TestWindowResize(t *testing.T) {
	m := testModel(t)

	result, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = result.(Model)
	if m.width != 120 {
		t.Errorf("width = %d, want 120", m.width)
	}
	if m.height != 40 {
		t.Errorf("height = %d, want 40", m.height)
	}
}

func TestWindowResize_UpdatesDimensions(t *testing.T) {
	m := testModel(t)

	// Set initial size.
	result, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = result.(Model)

	// Resize.
	result, _ = m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	m = result.(Model)
	if m.width != 160 || m.height != 50 {
		t.Errorf("after resize: width=%d height=%d, want 160x50", m.width, m.height)
	}
}

// --- Spinner tick ---

func TestSpinnerTick(t *testing.T) {
	m := testModel(t)
	m.polling = true // spinner ticks propagate when polling is active

	tickMsg := m.spinner.Tick()
	result, cmd := m.Update(tickMsg)
	m = result.(Model)
	// Spinner tick should propagate (non-nil cmd for next tick) when polling.
	if cmd == nil {
		t.Error("spinner tick should return a non-nil command when polling is active")
	}
}

// --- Broadcast mode escape clears status ---

func TestBroadcastMode_EscapeClearsStatus(t *testing.T) {
	m := testModel(t)

	// Enter broadcast mode.
	result, _ := m.Update(keyPress('m'))
	m = result.(Model)

	// Escape should clear status and return to normal.
	result, _ = m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.broadcastStatus != "" {
		t.Errorf("broadcastStatus = %q, want empty after escape", m.broadcastStatus)
	}
	if m.mode != "normal" {
		t.Errorf("mode = %q, want normal after escape", m.mode)
	}
}

// --- User message mode escape clears status ---

func TestUserMsgMode_EscapeClearsStatus(t *testing.T) {
	m := testModel(t)

	result, _ := m.Update(keyPress('u'))
	m = result.(Model)

	result, _ = m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.userMsgStatus != "" {
		t.Errorf("userMsgStatus = %q, want empty after escape", m.userMsgStatus)
	}
	if m.mode != "normal" {
		t.Errorf("mode = %q, want normal after escape", m.mode)
	}
}

// --- Onboard mode escape clears status ---

func TestOnboardMode_EscapeClearsStatus(t *testing.T) {
	m := testModel(t)

	result, _ := m.Update(keyPress('o'))
	m = result.(Model)

	result, _ = m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.onboardStatus != "" {
		t.Errorf("onboardStatus = %q, want empty after escape", m.onboardStatus)
	}
	if m.mode != "normal" {
		t.Errorf("mode = %q, want normal after escape", m.mode)
	}
}

// --- Keyboard does not leak between modes ---

func TestNormalKeys_IgnoredInBroadcastMode(t *testing.T) {
	m := testModel(t)

	// Enter broadcast mode.
	result, _ := m.Update(keyPress('m'))
	m = result.(Model)
	if m.mode != "broadcast" {
		t.Fatalf("mode = %q, want broadcast", m.mode)
	}

	// 'q' in broadcast mode should NOT quit — it's handled by the textarea.
	_, cmd := m.Update(keyPress('q'))
	if cmd != nil {
		// If cmd is a quit command, that's wrong.
		msg := cmd()
		if _, ok := msg.(tea.QuitMsg); ok {
			t.Error("'q' in broadcast mode should not trigger quit")
		}
	}
}

// ============================================================
// Part 2: Autopilot flow, track mode, settings, filter (#241)
// ============================================================

// --- Autopilot mode transitions ---

func TestAutopilot_AKey_OnTab3_NoTrackedItems(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot

	result, cmd := m.Update(keyPress('a'))
	m = result.(Model)
	// startAutopilot checks prerequisites — with no tracked items it sets status and stays empty.
	if m.autopilotMode != "" {
		t.Errorf("autopilotMode = %q, want empty (no tracked items)", m.autopilotMode)
	}
	if m.autopilotStatus == "" {
		t.Error("autopilotStatus should be set with error when no tracked items")
	}
	if cmd == nil {
		t.Error("expected a non-nil tick command for status clear")
	}
}

func TestAutopilot_StopConfirm_EscCancels(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "stop-confirm"

	result, _ := m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.autopilotMode != "running" {
		t.Errorf("autopilotMode = %q, want %q after esc from stop-confirm", m.autopilotMode, "running")
	}
}

func TestAutopilot_StopConfirm_EnterStops(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "stop-confirm"
	// We need a supervisor to avoid nil panic. Create a minimal one.
	sup := autopilot.New(m.store, m.project, nil, t.TempDir(), "owner", "repo", "fake-token")
	m.autopilotSupervisor = sup

	result, cmd := m.Update(specialKey(tea.KeyEnter))
	m = result.(Model)
	// confirmStopAutopilot sets mode to "running" (waits for finished event).
	if m.autopilotMode != "running" {
		t.Errorf("autopilotMode = %q, want %q after enter on stop-confirm", m.autopilotMode, "running")
	}
	if cmd == nil {
		t.Error("expected non-nil command from confirmStopAutopilot")
	}
}

func TestAutopilot_Confirm_EscCancels(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "confirm"

	result, _ := m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.autopilotMode != "" {
		t.Errorf("autopilotMode = %q, want empty after esc from confirm", m.autopilotMode)
	}
	if m.autopilotSupervisor != nil {
		t.Error("supervisor should be nil after cancelling confirm")
	}
}

func TestAutopilot_StopTaskConfirm_EscCancels2(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "stop-task-confirm"

	result, _ := m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.autopilotMode != "running" {
		t.Errorf("autopilotMode = %q, want %q after esc from stop-task-confirm", m.autopilotMode, "running")
	}
}

func TestAutopilot_StopTaskConfirm_EscCancels(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "stop-task-confirm"

	result, _ := m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.autopilotMode != "running" {
		t.Errorf("autopilotMode = %q, want %q after esc from stop-task-confirm", m.autopilotMode, "running")
	}
}

func TestAutopilot_RestartConfirm_EscCancels(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "restart-confirm"

	result, _ := m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.autopilotMode != "running" {
		t.Errorf("autopilotMode = %q, want %q after esc from restart-confirm", m.autopilotMode, "running")
	}
}

func TestAutopilot_ReviewConfirm_EscCancels_RestoresMode(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "review-confirm"
	m.autopilotModeBeforeReview = "completed"

	result, _ := m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.autopilotMode != "completed" {
		t.Errorf("autopilotMode = %q, want %q (restored from before review)", m.autopilotMode, "completed")
	}
}

func TestAutopilot_AddSlotConfirm_EscCancels(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "add-slot-confirm"

	result, _ := m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.autopilotMode != "running" {
		t.Errorf("autopilotMode = %q, want %q after esc from add-slot-confirm", m.autopilotMode, "running")
	}
}

func TestAutopilot_DepSelect_LeftRight(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "dep-select"
	m.autopilotDepOptions = []autopilot.DepOption{
		{Unblocked: 3},
		{Unblocked: 5},
		{Unblocked: 2},
	}
	m.autopilotDepSelection = 0
	m.autopilotUnblocked = 3

	// Right cycles 0 → 1.
	result, _ := m.Update(specialKey(tea.KeyRight))
	m = result.(Model)
	if m.autopilotDepSelection != 1 {
		t.Errorf("dep selection = %d, want 1 after right", m.autopilotDepSelection)
	}
	if m.autopilotUnblocked != 5 {
		t.Errorf("unblocked = %d, want 5", m.autopilotUnblocked)
	}

	// Right again: 1 → 2.
	result, _ = m.Update(specialKey(tea.KeyRight))
	m = result.(Model)
	if m.autopilotDepSelection != 2 {
		t.Errorf("dep selection = %d, want 2", m.autopilotDepSelection)
	}

	// Right wraps: 2 → 0.
	result, _ = m.Update(specialKey(tea.KeyRight))
	m = result.(Model)
	if m.autopilotDepSelection != 0 {
		t.Errorf("dep selection = %d, want 0 (wrap)", m.autopilotDepSelection)
	}

	// Left wraps: 0 → 2.
	result, _ = m.Update(specialKey(tea.KeyLeft))
	m = result.(Model)
	if m.autopilotDepSelection != 2 {
		t.Errorf("dep selection = %d, want 2 after left wrap", m.autopilotDepSelection)
	}
	if m.autopilotUnblocked != 2 {
		t.Errorf("unblocked = %d, want 2", m.autopilotUnblocked)
	}
}

func TestAutopilot_DepSelect_EscCancels(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "dep-select"
	m.autopilotDepOptions = []autopilot.DepOption{{Unblocked: 3}}
	sup := autopilot.New(m.store, m.project, nil, t.TempDir(), "owner", "repo", "fake-token")
	m.autopilotSupervisor = sup

	result, _ := m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.autopilotMode != "" {
		t.Errorf("autopilotMode = %q, want empty after esc from dep-select", m.autopilotMode)
	}
	if m.autopilotSupervisor != nil {
		t.Error("supervisor should be nil after cancelling dep-select")
	}
	if m.autopilotDepOptions != nil {
		t.Error("dep options should be nil after cancelling dep-select")
	}
}

func TestAutopilot_ScanConfirm_EscCancels(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "scan-confirm"
	sup := autopilot.New(m.store, m.project, nil, t.TempDir(), "owner", "repo", "fake-token")
	m.autopilotSupervisor = sup

	result, _ := m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.autopilotMode != "" {
		t.Errorf("autopilotMode = %q, want empty after esc from scan-confirm", m.autopilotMode)
	}
	if m.autopilotSupervisor != nil {
		t.Error("supervisor should be nil after cancelling scan-confirm")
	}
}

func TestAutopilot_ScanConfirm_GOpensDepGuidance(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "scan-confirm"
	m.width = 120

	result, cmd := m.Update(keyPress('G'))
	m = result.(Model)
	if m.mode != "dep-guidance" {
		t.Errorf("mode = %q, want %q after 'G' from scan-confirm", m.mode, "dep-guidance")
	}
	if cmd == nil {
		t.Error("expected non-nil command (textarea focus)")
	}
}

func TestAutopilot_DepSelect_GOpensDepGuidance(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "dep-select"
	m.autopilotDepOptions = []autopilot.DepOption{{Unblocked: 3}}
	m.width = 120

	result, cmd := m.Update(keyPress('G'))
	m = result.(Model)
	if m.mode != "dep-guidance" {
		t.Errorf("mode = %q, want %q after 'G' from dep-select", m.mode, "dep-guidance")
	}
	if cmd == nil {
		t.Error("expected non-nil command (textarea focus)")
	}
}

// --- Autopilot task cursor navigation ---

func TestAutopilot_CursorNavigation_UpDown(t *testing.T) {
	m, store := testModelWithProject(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "running"

	// Insert tasks into the DB so rebuildAutopilotTaskContent picks them up.
	for _, task := range []*db.AutopilotTask{
		{ProjectID: m.project.ID, IssueNumber: 1, IssueTitle: "Task 1", Status: "running"},
		{ProjectID: m.project.ID, IssueNumber: 2, IssueTitle: "Task 2", Status: "queued"},
		{ProjectID: m.project.ID, IssueNumber: 3, IssueTitle: "Task 3", Status: "done"},
	} {
		if err := store.CreateAutopilotTask(task); err != nil {
			t.Fatalf("CreateAutopilotTask: %v", err)
		}
	}
	m.rebuildAutopilotTaskContent()
	m.autopilotCursor = 0

	if len(m.autopilotTasks) != 3 {
		t.Fatalf("tasks = %d, want 3", len(m.autopilotTasks))
	}

	// Down: 0 → 1.
	result, _ := m.Update(specialKey(tea.KeyDown))
	m = result.(Model)
	if m.autopilotCursor != 1 {
		t.Errorf("cursor = %d, want 1 after down", m.autopilotCursor)
	}

	// Down: 1 → 2.
	result, _ = m.Update(specialKey(tea.KeyDown))
	m = result.(Model)
	if m.autopilotCursor != 2 {
		t.Errorf("cursor = %d, want 2", m.autopilotCursor)
	}

	// Down wraps: 2 → 0.
	result, _ = m.Update(specialKey(tea.KeyDown))
	m = result.(Model)
	if m.autopilotCursor != 0 {
		t.Errorf("cursor = %d, want 0 (wrap)", m.autopilotCursor)
	}

	// Up wraps: 0 → 2.
	result, _ = m.Update(specialKey(tea.KeyUp))
	m = result.(Model)
	if m.autopilotCursor != 2 {
		t.Errorf("cursor = %d, want 2 after up wrap", m.autopilotCursor)
	}
}

func TestAutopilot_CursorNavigation_PageUpDown(t *testing.T) {
	m, store := testModelWithProject(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "running"

	for i := range 10 {
		task := &db.AutopilotTask{
			ProjectID:   m.project.ID,
			IssueNumber: i + 1,
			IssueTitle:  fmt.Sprintf("Task %d", i+1),
			Status:      "queued",
		}
		if err := store.CreateAutopilotTask(task); err != nil {
			t.Fatalf("CreateAutopilotTask: %v", err)
		}
	}
	m.rebuildAutopilotTaskContent()
	m.autopilotCursor = 0

	if len(m.autopilotTasks) != 10 {
		t.Fatalf("tasks = %d, want 10", len(m.autopilotTasks))
	}

	// PageDown: jumps 5.
	result, _ := m.Update(specialKey(tea.KeyPgDown))
	m = result.(Model)
	if m.autopilotCursor != 5 {
		t.Errorf("cursor = %d, want 5 after pgdown", m.autopilotCursor)
	}

	// PageDown: 5 + 5 = 10, clamped to 9.
	result, _ = m.Update(specialKey(tea.KeyPgDown))
	m = result.(Model)
	if m.autopilotCursor != 9 {
		t.Errorf("cursor = %d, want 9 (clamped) after pgdown", m.autopilotCursor)
	}

	// PageUp: 9 - 5 = 4.
	result, _ = m.Update(specialKey(tea.KeyPgUp))
	m = result.(Model)
	if m.autopilotCursor != 4 {
		t.Errorf("cursor = %d, want 4 after pgup", m.autopilotCursor)
	}

	// PageUp: 4 - 5 = -1, clamped to 0.
	result, _ = m.Update(specialKey(tea.KeyPgUp))
	m = result.(Model)
	if m.autopilotCursor != 0 {
		t.Errorf("cursor = %d, want 0 (clamped) after pgup", m.autopilotCursor)
	}
}

func TestAutopilot_CursorNavigation_ResetsFailureDetail(t *testing.T) {
	m, store := testModelWithProject(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "running"
	for _, task := range []*db.AutopilotTask{
		{ProjectID: m.project.ID, IssueNumber: 1, IssueTitle: "Task 1", Status: "running"},
		{ProjectID: m.project.ID, IssueNumber: 2, IssueTitle: "Task 2", Status: "failed"},
	} {
		if err := store.CreateAutopilotTask(task); err != nil {
			t.Fatalf("CreateAutopilotTask: %v", err)
		}
	}
	m.rebuildAutopilotTaskContent()
	m.autopilotCursor = 0
	m.failureDetailExpanded = true

	result, _ := m.Update(specialKey(tea.KeyDown))
	m = result.(Model)
	if m.failureDetailExpanded {
		t.Error("failureDetailExpanded should be reset on cursor change")
	}
}

// --- selectedAutopilotTask ---

func TestSelectedAutopilotTask_Empty(t *testing.T) {
	m := testModel(t)
	m.autopilotTasks = nil
	if m.selectedAutopilotTask() != nil {
		t.Error("expected nil when no tasks")
	}
}

func TestSelectedAutopilotTask_ValidCursor(t *testing.T) {
	m := testModel(t)
	m.autopilotTasks = []db.AutopilotTask{
		{IssueNumber: 42, Status: "running"},
		{IssueNumber: 55, Status: "done"},
	}
	m.autopilotCursor = 1
	task := m.selectedAutopilotTask()
	if task == nil {
		t.Fatal("expected non-nil task")
	}
	if task.IssueNumber != 55 {
		t.Errorf("issue = %d, want 55", task.IssueNumber)
	}
}

func TestSelectedAutopilotTask_OutOfBounds(t *testing.T) {
	m := testModel(t)
	m.autopilotTasks = []db.AutopilotTask{
		{IssueNumber: 1, Status: "running"},
	}
	m.autopilotCursor = 5
	if m.selectedAutopilotTask() != nil {
		t.Error("expected nil when cursor out of bounds")
	}
}

// --- Autopilot S key (stop selected agent) ---

func TestAutopilot_SKey_StopRunningTask(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "running"
	m.autopilotTasks = []db.AutopilotTask{
		{IssueNumber: 42, Status: "running"},
	}
	m.autopilotCursor = 0

	result, _ := m.Update(keyPress('S'))
	m = result.(Model)
	if m.autopilotMode != "stop-task-confirm" {
		t.Errorf("autopilotMode = %q, want %q after 'S' on running task", m.autopilotMode, "stop-task-confirm")
	}
}

func TestAutopilot_SKey_NonRunningTask_NoOp(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "running"
	m.autopilotTasks = []db.AutopilotTask{
		{IssueNumber: 42, Status: "done"},
	}
	m.autopilotCursor = 0

	result, _ := m.Update(keyPress('S'))
	m = result.(Model)
	if m.autopilotMode != "running" {
		t.Errorf("autopilotMode = %q, want %q ('S' on non-running task is no-op)", m.autopilotMode, "running")
	}
}

// --- Autopilot P key (pause/resume) ---

func TestAutopilot_PKey_Pause(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "running"
	sup := autopilot.New(m.store, m.project, nil, t.TempDir(), "owner", "repo", "fake-token")
	m.autopilotSupervisor = sup

	result, _ := m.Update(keyPress('P'))
	m = result.(Model)
	if !m.autopilotPaused {
		t.Error("autopilotPaused should be true after 'P'")
	}
	if m.autopilotStatus == "" {
		t.Error("autopilotStatus should be set after pause")
	}
}

func TestAutopilot_PKey_Resume(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "running"
	sup := autopilot.New(m.store, m.project, nil, t.TempDir(), "owner", "repo", "fake-token")
	m.autopilotSupervisor = sup
	m.autopilotPaused = true

	result, _ := m.Update(keyPress('P'))
	m = result.(Model)
	if m.autopilotPaused {
		t.Error("autopilotPaused should be false after resume")
	}
}

// --- Autopilot + key (add slot) ---

func TestAutopilot_PlusKey_AddSlotConfirm(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "running"
	sup := autopilot.New(m.store, m.project, nil, t.TempDir(), "owner", "repo", "fake-token")
	m.autopilotSupervisor = sup

	result, _ := m.Update(keyPress('+'))
	m = result.(Model)
	if m.autopilotMode != "add-slot-confirm" {
		t.Errorf("autopilotMode = %q, want %q after '+'", m.autopilotMode, "add-slot-confirm")
	}
}

func TestAutopilot_PlusKey_NotRunning_NoOp(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "confirm"

	result, _ := m.Update(keyPress('+'))
	m = result.(Model)
	if m.autopilotMode != "confirm" {
		t.Errorf("autopilotMode should not change from '+' in non-running mode")
	}
}

// --- Autopilot r key (task-context: restart/review) ---

func TestAutopilot_RKey_BailedTask_RestartConfirm(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "running"
	m.autopilotTasks = []db.AutopilotTask{
		{IssueNumber: 42, Status: "bailed"},
	}
	m.autopilotCursor = 0

	result, _ := m.Update(keyPress('r'))
	m = result.(Model)
	if m.autopilotMode != "restart-confirm" {
		t.Errorf("autopilotMode = %q, want %q after 'r' on bailed task", m.autopilotMode, "restart-confirm")
	}
}

func TestAutopilot_RKey_ReviewTask_ReviewConfirm(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "running"
	m.autopilotTasks = []db.AutopilotTask{
		{IssueNumber: 42, Status: "review"},
	}
	m.autopilotCursor = 0

	result, _ := m.Update(keyPress('r'))
	m = result.(Model)
	if m.autopilotMode != "review-confirm" {
		t.Errorf("autopilotMode = %q, want %q after 'r' on review task", m.autopilotMode, "review-confirm")
	}
	if m.autopilotModeBeforeReview != "running" {
		t.Errorf("modeBeforeReview = %q, want %q", m.autopilotModeBeforeReview, "running")
	}
}

func TestAutopilot_RKey_CompletedMode(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "completed"
	m.autopilotTasks = []db.AutopilotTask{
		{IssueNumber: 42, Status: "failed"},
	}
	m.autopilotCursor = 0

	result, _ := m.Update(keyPress('r'))
	m = result.(Model)
	if m.autopilotMode != "resume-or-restart-confirm" {
		t.Errorf("autopilotMode = %q, want %q after 'r' on failed task in completed mode", m.autopilotMode, "resume-or-restart-confirm")
	}
}

func TestAutopilot_RKey_FailedTask_ResumeOrRestartConfirm(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "running"
	m.autopilotTasks = []db.AutopilotTask{
		{IssueNumber: 42, Status: "failed", FailureReason: "max_turns"},
	}
	m.autopilotCursor = 0

	result, _ := m.Update(keyPress('r'))
	m = result.(Model)
	if m.autopilotMode != "resume-or-restart-confirm" {
		t.Errorf("autopilotMode = %q, want %q after 'r' on failed task", m.autopilotMode, "resume-or-restart-confirm")
	}
}

func TestAutopilot_RKey_StoppedTask_ResumeOrRestartConfirm(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "running"
	m.autopilotTasks = []db.AutopilotTask{
		{IssueNumber: 42, Status: "stopped"},
	}
	m.autopilotCursor = 0

	result, _ := m.Update(keyPress('r'))
	m = result.(Model)
	if m.autopilotMode != "resume-or-restart-confirm" {
		t.Errorf("autopilotMode = %q, want %q after 'r' on stopped task", m.autopilotMode, "resume-or-restart-confirm")
	}
}

func TestAutopilot_ResumeOrRestart_EscCancels(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "resume-or-restart-confirm"

	result, _ := m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.autopilotMode != "running" {
		t.Errorf("autopilotMode = %q, want %q after esc from resume-or-restart-confirm", m.autopilotMode, "running")
	}
}

func TestAutopilot_ResumeOrRestart_EscKey_Cancels(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "resume-or-restart-confirm"

	result, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = result.(Model)
	if m.autopilotMode != "running" {
		t.Errorf("autopilotMode = %q, want %q after esc from resume-or-restart-confirm", m.autopilotMode, "running")
	}
}

// --- Autopilot i key (failure detail toggle) ---

func TestAutopilot_IKey_ToggleFailureDetail(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "running"
	m.autopilotTasks = []db.AutopilotTask{
		{IssueNumber: 42, Status: "failed", FailureDetail: "some detail"},
	}
	m.autopilotCursor = 0

	result, _ := m.Update(keyPress('i'))
	m = result.(Model)
	if !m.failureDetailExpanded {
		t.Error("failureDetailExpanded should be true after 'i' on failed task")
	}

	result, _ = m.Update(keyPress('i'))
	m = result.(Model)
	if m.failureDetailExpanded {
		t.Error("failureDetailExpanded should be false after second 'i'")
	}
}

// --- Autopilot l key (log viewer) ---

func TestAutopilot_LKey_NoLog_ShowsStatus(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "running"
	m.autopilotTasks = []db.AutopilotTask{
		{IssueNumber: 42, Status: "queued", AgentLog: ""},
	}
	m.autopilotCursor = 0

	result, _ := m.Update(keyPress('l'))
	m = result.(Model)
	if m.showLogViewer {
		t.Error("showLogViewer should be false when task has no log")
	}
	if m.autopilotStatus == "" {
		t.Error("autopilotStatus should indicate no log available")
	}
}

func TestAutopilot_LKey_NotRunningMode_NoOp(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAutopilot
	m.autopilotMode = "confirm"

	result, _ := m.Update(keyPress('l'))
	m = result.(Model)
	if m.showLogViewer {
		t.Error("log viewer should not open in non-running/completed mode")
	}
}

// --- Autopilot event messages ---

func TestAutopilotEventMsg_AppendsEvent(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40

	evt := autopilotEventMsg(autopilot.Event{
		Time:    time.Now(),
		Type:    "started",
		Summary: "Started #42",
	})
	result, _ := m.Update(evt)
	m = result.(Model)

	if len(m.events) < 1 {
		t.Fatal("expected at least 1 event")
	}
	last := m.events[len(m.events)-1]
	if last.Type != "autopilot" {
		t.Errorf("event type = %q, want %q", last.Type, "autopilot")
	}
}

func TestAutopilotEventMsg_Warning_SetsBanner(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40

	evt := autopilotEventMsg(autopilot.Event{
		Time:    time.Now(),
		Type:    "warning",
		Summary: "Agent hit budget limit",
	})
	result, _ := m.Update(evt)
	m = result.(Model)
	if m.warningBanner != "Agent hit budget limit" {
		t.Errorf("warningBanner = %q, want %q", m.warningBanner, "Agent hit budget limit")
	}
}

func TestAutopilotEventMsg_Finished(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.autopilotMode = "running"
	sup := autopilot.New(m.store, m.project, nil, t.TempDir(), "owner", "repo", "fake-token")
	m.autopilotSupervisor = sup
	m.origPollInterval = 120 * time.Second

	evt := autopilotEventMsg(autopilot.Event{
		Time:    time.Now(),
		Type:    "finished",
		Summary: "All tasks complete",
	})
	result, _ := m.Update(evt)
	m = result.(Model)
	if m.autopilotMode != "completed" {
		t.Errorf("autopilotMode = %q, want %q after finished event", m.autopilotMode, "completed")
	}
	if m.autopilotSupervisor != nil {
		t.Error("supervisor should be nil after finished")
	}
}

func TestAutopilotEventMsg_HasNewOnOtherTab(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.activeTab = tabOperations

	evt := autopilotEventMsg(autopilot.Event{
		Time:    time.Now(),
		Type:    "started",
		Summary: "Started #42",
	})
	result, _ := m.Update(evt)
	m = result.(Model)
	if !m.autopilotHasNew {
		t.Error("autopilotHasNew should be true when event arrives on non-autopilot tab")
	}
}

// --- Autopilot prepare result ---

func TestAutopilotPrepareResult_NoIssues(t *testing.T) {
	m := testModel(t)
	m.polling = true
	m.autopilotMode = "scan-confirm"
	sup := autopilot.New(m.store, m.project, nil, t.TempDir(), "owner", "repo", "fake-token")
	m.autopilotSupervisor = sup

	result, _ := m.Update(autopilotPrepareResultMsg{result: &autopilot.PrepareResult{}})
	m = result.(Model)
	if m.autopilotMode != "" {
		t.Errorf("autopilotMode = %q, want empty when no issues found", m.autopilotMode)
	}
	if m.autopilotSupervisor != nil {
		t.Error("supervisor should be nil when no issues found")
	}
}

func TestAutopilotPrepareResult_WithIssues(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.polling = true
	m.activeTab = tabAutopilot

	result, _ := m.Update(autopilotPrepareResultMsg{
		result: &autopilot.PrepareResult{
			Total:    5,
			Options:  []autopilot.DepOption{{Unblocked: 3}},
			AgentDef: autopilot.AgentDefBuiltIn,
		},
	})
	m = result.(Model)
	if m.autopilotMode != "dep-select" {
		t.Errorf("autopilotMode = %q, want %q after prepare with issues", m.autopilotMode, "dep-select")
	}
	if m.autopilotTotal != 5 {
		t.Errorf("total = %d, want 5", m.autopilotTotal)
	}
	if m.autopilotUnblocked != 3 {
		t.Errorf("unblocked = %d, want 3", m.autopilotUnblocked)
	}
}

func TestAutopilotPrepareResult_Error(t *testing.T) {
	m := testModel(t)
	m.polling = true
	m.autopilotMode = "scan-confirm"
	sup := autopilot.New(m.store, m.project, nil, t.TempDir(), "owner", "repo", "fake-token")
	m.autopilotSupervisor = sup

	result, _ := m.Update(autopilotPrepareResultMsg{err: errTest})
	m = result.(Model)
	if m.autopilotMode != "" {
		t.Errorf("autopilotMode = %q, want empty after error", m.autopilotMode)
	}
	if m.autopilotSupervisor != nil {
		t.Error("supervisor should be nil after error")
	}
	if m.autopilotStatus == "" {
		t.Error("status should contain error")
	}
}

// --- clearAutopilotStatusMsg ---

func TestClearAutopilotStatus(t *testing.T) {
	m := testModel(t)
	m.autopilotStatus = "something"

	result, _ := m.Update(clearAutopilotStatusMsg{})
	m = result.(Model)
	if m.autopilotStatus != "" {
		t.Errorf("autopilotStatus = %q, want empty after clear", m.autopilotStatus)
	}
}

// --- Settings mode ---

func TestSettingsMode_Entry(t *testing.T) {
	m := testModel(t)

	result, _ := m.Update(keyPress('s'))
	m = result.(Model)
	if m.mode != "settings" {
		t.Errorf("mode = %q, want %q", m.mode, "settings")
	}
	if m.settingsState == nil {
		t.Fatal("settingsState should not be nil")
	}
	if m.settingsState.step != settingsStepSelectField {
		t.Errorf("step = %d, want %d", m.settingsState.step, settingsStepSelectField)
	}
	if len(m.settingsState.fields) == 0 {
		t.Error("fields should not be empty")
	}
}

func TestSettingsMode_EscExits(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40

	// Enter settings.
	result, _ := m.Update(keyPress('s'))
	m = result.(Model)

	// Esc exits.
	result, _ = m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.mode != "normal" {
		t.Errorf("mode = %q, want %q after esc from settings", m.mode, "normal")
	}
	if m.settingsState != nil {
		t.Error("settingsState should be nil after esc")
	}
}

func TestSettingsMode_FieldNavigation(t *testing.T) {
	m := testModel(t)

	result, _ := m.Update(keyPress('s'))
	m = result.(Model)
	if m.settingsState.fieldIdx != 0 {
		t.Fatalf("initial fieldIdx = %d, want 0", m.settingsState.fieldIdx)
	}

	// Down.
	result, _ = m.Update(specialKey(tea.KeyDown))
	m = result.(Model)
	if m.settingsState.fieldIdx != 1 {
		t.Errorf("fieldIdx = %d, want 1 after down", m.settingsState.fieldIdx)
	}

	// Down again.
	result, _ = m.Update(specialKey(tea.KeyDown))
	m = result.(Model)
	if m.settingsState.fieldIdx != 2 {
		t.Errorf("fieldIdx = %d, want 2 after 2nd down", m.settingsState.fieldIdx)
	}

	// Up.
	result, _ = m.Update(specialKey(tea.KeyUp))
	m = result.(Model)
	if m.settingsState.fieldIdx != 1 {
		t.Errorf("fieldIdx = %d, want 1 after up", m.settingsState.fieldIdx)
	}

	// j (vim down).
	result, _ = m.Update(keyPress('j'))
	m = result.(Model)
	if m.settingsState.fieldIdx != 2 {
		t.Errorf("fieldIdx = %d, want 2 after 'j'", m.settingsState.fieldIdx)
	}

	// k (vim up).
	result, _ = m.Update(keyPress('k'))
	m = result.(Model)
	if m.settingsState.fieldIdx != 1 {
		t.Errorf("fieldIdx = %d, want 1 after 'k'", m.settingsState.fieldIdx)
	}
}

func TestSettingsMode_FieldNavigation_BoundsCheck(t *testing.T) {
	m := testModel(t)

	result, _ := m.Update(keyPress('s'))
	m = result.(Model)

	// Up at 0 is clamped.
	result, _ = m.Update(specialKey(tea.KeyUp))
	m = result.(Model)
	if m.settingsState.fieldIdx != 0 {
		t.Errorf("fieldIdx = %d, want 0 (clamped at top)", m.settingsState.fieldIdx)
	}

	// Navigate to last field.
	lastIdx := len(m.settingsState.fields) - 1
	for range lastIdx {
		result, _ = m.Update(specialKey(tea.KeyDown))
		m = result.(Model)
	}
	if m.settingsState.fieldIdx != lastIdx {
		t.Fatalf("fieldIdx = %d, want %d", m.settingsState.fieldIdx, lastIdx)
	}

	// Down at last is clamped.
	result, _ = m.Update(specialKey(tea.KeyDown))
	m = result.(Model)
	if m.settingsState.fieldIdx != lastIdx {
		t.Errorf("fieldIdx = %d, want %d (clamped at bottom)", m.settingsState.fieldIdx, lastIdx)
	}
}

func TestSettingsMode_EnterOpensEdit(t *testing.T) {
	m := testModel(t)

	result, _ := m.Update(keyPress('s'))
	m = result.(Model)
	// Field 0 is "Sync interval" (not multiline).

	result, cmd := m.Update(specialKey(tea.KeyEnter))
	m = result.(Model)
	if m.settingsState.step != settingsStepEditValue {
		t.Errorf("step = %d, want %d after enter on non-multiline field", m.settingsState.step, settingsStepEditValue)
	}
	if cmd == nil {
		t.Error("expected non-nil command (input focus)")
	}
}

func TestSettingsMode_EnterOpensTextarea_Multiline(t *testing.T) {
	m := testModel(t)
	m.width = 120

	result, _ := m.Update(keyPress('s'))
	m = result.(Model)

	// Navigate to "Analyzer focus" (field 1, multiline).
	result, _ = m.Update(specialKey(tea.KeyDown))
	m = result.(Model)
	if m.settingsState.fields[m.settingsState.fieldIdx].label != "Analyzer focus" {
		t.Fatalf("expected Analyzer focus field, got %q", m.settingsState.fields[m.settingsState.fieldIdx].label)
	}

	result, cmd := m.Update(specialKey(tea.KeyEnter))
	m = result.(Model)
	if m.settingsState.step != settingsStepEditTextarea {
		t.Errorf("step = %d, want %d after enter on multiline field", m.settingsState.step, settingsStepEditTextarea)
	}
	if cmd == nil {
		t.Error("expected non-nil command (textarea focus)")
	}
}

func TestSettingsMode_EditValue_EscGoesBack(t *testing.T) {
	m := testModel(t)

	result, _ := m.Update(keyPress('s'))
	m = result.(Model)

	// Enter edit mode.
	result, _ = m.Update(specialKey(tea.KeyEnter))
	m = result.(Model)
	if m.settingsState.step != settingsStepEditValue {
		t.Fatal("expected editValue step")
	}

	// Esc goes back to selectField.
	result, _ = m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.settingsState.step != settingsStepSelectField {
		t.Errorf("step = %d, want %d after esc from edit", m.settingsState.step, settingsStepSelectField)
	}
}

func TestSettingsMode_EditTextarea_EscGoesBack(t *testing.T) {
	m := testModel(t)
	m.width = 120

	result, _ := m.Update(keyPress('s'))
	m = result.(Model)
	// Navigate to multiline field.
	result, _ = m.Update(specialKey(tea.KeyDown))
	m = result.(Model)
	result, _ = m.Update(specialKey(tea.KeyEnter))
	m = result.(Model)
	if m.settingsState.step != settingsStepEditTextarea {
		t.Fatal("expected editTextarea step")
	}

	result, _ = m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.settingsState.step != settingsStepSelectField {
		t.Errorf("step = %d, want %d after esc from textarea", m.settingsState.step, settingsStepSelectField)
	}
}

func TestSettingsMode_SyncInterval_Validation(t *testing.T) {
	m := testModel(t)

	result, _ := m.Update(keyPress('s'))
	m = result.(Model)
	// Enter edit on Sync interval (field 0).
	result, _ = m.Update(specialKey(tea.KeyEnter))
	m = result.(Model)

	// Type an invalid value.
	m.settingsState.input.SetValue("abc")
	result, _ = m.Update(specialKey(tea.KeyEnter))
	m = result.(Model)
	if m.settingsState.err == "" {
		t.Error("expected validation error for non-numeric sync interval")
	}
	if m.settingsState.step != settingsStepEditValue {
		t.Error("should remain in editValue step on validation error")
	}
}

func TestSettingsMode_SyncInterval_TooSmall(t *testing.T) {
	m := testModel(t)

	result, _ := m.Update(keyPress('s'))
	m = result.(Model)
	result, _ = m.Update(specialKey(tea.KeyEnter))
	m = result.(Model)

	m.settingsState.input.SetValue("0")
	result, _ = m.Update(specialKey(tea.KeyEnter))
	m = result.(Model)
	if m.settingsState.err == "" {
		t.Error("expected validation error for sync interval < 1")
	}
}

func TestSettingsMode_SyncInterval_ValidSave(t *testing.T) {
	m := testModel(t)

	result, _ := m.Update(keyPress('s'))
	m = result.(Model)
	result, _ = m.Update(specialKey(tea.KeyEnter))
	m = result.(Model)

	m.settingsState.input.SetValue("5")
	result, cmd := m.Update(specialKey(tea.KeyEnter))
	m = result.(Model)
	if m.settingsState.err != "" {
		t.Errorf("unexpected error: %q", m.settingsState.err)
	}
	if m.settingsState.step != settingsStepSelectField {
		t.Errorf("step = %d, want %d after valid save", m.settingsState.step, settingsStepSelectField)
	}
	if m.project.StatusIntervalSec != 300 {
		t.Errorf("StatusIntervalSec = %d, want 300 (5 * 60)", m.project.StatusIntervalSec)
	}
	if cmd == nil {
		t.Error("expected non-nil command (save to DB)")
	}
}

func TestSettingsMode_MaxAgents_Validation(t *testing.T) {
	m := testModel(t)

	result, _ := m.Update(keyPress('s'))
	m = result.(Model)
	// Navigate to "Autopilot max agents" (field 2).
	result, _ = m.Update(specialKey(tea.KeyDown))
	m = result.(Model)
	result, _ = m.Update(specialKey(tea.KeyDown))
	m = result.(Model)
	result, _ = m.Update(specialKey(tea.KeyEnter))
	m = result.(Model)

	// Too big.
	m.settingsState.input.SetValue("20")
	result, _ = m.Update(specialKey(tea.KeyEnter))
	m = result.(Model)
	if m.settingsState.err == "" {
		t.Error("expected validation error for agents > 10")
	}
}

// --- settingsSavedMsg ---

func TestSettingsSavedMsg_Success(t *testing.T) {
	m := testModel(t)

	result, _ := m.Update(settingsSavedMsg{field: "Sync interval"})
	m = result.(Model)
	if m.settingsStatus != "Sync interval updated" {
		t.Errorf("settingsStatus = %q, want %q", m.settingsStatus, "Sync interval updated")
	}
}

func TestSettingsSavedMsg_Error(t *testing.T) {
	m := testModel(t)

	result, _ := m.Update(settingsSavedMsg{field: "Sync interval", err: errTest})
	m = result.(Model)
	if m.settingsStatus != "Error: test error" {
		t.Errorf("settingsStatus = %q, want %q", m.settingsStatus, "Error: test error")
	}
}

func TestClearSettingsStatus(t *testing.T) {
	m := testModel(t)
	m.settingsStatus = "something"

	result, _ := m.Update(clearSettingsStatusMsg{})
	m = result.(Model)
	if m.settingsStatus != "" {
		t.Errorf("settingsStatus = %q, want empty", m.settingsStatus)
	}
}

// --- newSettingsState ---

func TestNewSettingsState_DefaultFields(t *testing.T) {
	proj := testProject()
	proj.AutopilotMaxAgents = 3
	proj.AutopilotMaxTurns = 200
	proj.AutopilotMaxBudgetUSD = 15.0
	proj.AutopilotSkipLabel = "no-agent"

	ss := newSettingsState(proj)
	if ss.step != settingsStepSelectField {
		t.Errorf("step = %d, want %d", ss.step, settingsStepSelectField)
	}
	if ss.fieldIdx != 0 {
		t.Errorf("fieldIdx = %d, want 0", ss.fieldIdx)
	}
	if len(ss.fields) != 7 {
		t.Errorf("fields count = %d, want 7", len(ss.fields))
	}
	// Check first field.
	if ss.fields[0].label != "Sync interval" {
		t.Errorf("field 0 label = %q, want %q", ss.fields[0].label, "Sync interval")
	}
	// Check multiline field.
	if !ss.fields[1].multiline {
		t.Error("field 1 (Analyzer focus) should be multiline")
	}
}

// --- parseSkipLabels ---

func TestParseSkipLabels_CommaList(t *testing.T) {
	labels := parseSkipLabels("no-agent, skip-this, wontfix")
	if len(labels) != 3 {
		t.Fatalf("got %d labels, want 3", len(labels))
	}
	if labels[0] != "no-agent" {
		t.Errorf("labels[0] = %q, want %q", labels[0], "no-agent")
	}
	if labels[1] != "skip-this" {
		t.Errorf("labels[1] = %q, want %q", labels[1], "skip-this")
	}
}

func TestParseSkipLabels_Empty(t *testing.T) {
	labels := parseSkipLabels("")
	if len(labels) != 1 || labels[0] != "no-agent" {
		t.Errorf("got %v, want [no-agent] for empty input", labels)
	}
}

func TestParseSkipLabels_OnlyCommas(t *testing.T) {
	labels := parseSkipLabels(",,,")
	if len(labels) != 1 || labels[0] != "no-agent" {
		t.Errorf("got %v, want [no-agent] for only commas", labels)
	}
}

// --- Filter state machine ---

func TestNewFilterState_SingleRepo(t *testing.T) {
	repos := []poller.GitHubRepo{{Owner: "org", Repo: "repo1"}}
	fs := newFilterState(repos, false)

	if fs.step != filterStepSelectType {
		t.Errorf("step = %d, want %d (auto-skip to selectType with single repo)", fs.step, filterStepSelectType)
	}
	if fs.selectedRepo.Owner != "org" || fs.selectedRepo.Repo != "repo1" {
		t.Error("selectedRepo should be auto-set with single repo")
	}
	if fs.hasExisting {
		t.Error("hasExisting should be false")
	}
}

func TestNewFilterState_MultipleRepos(t *testing.T) {
	repos := []poller.GitHubRepo{
		{Owner: "org", Repo: "repo1"},
		{Owner: "org", Repo: "repo2"},
	}
	fs := newFilterState(repos, true)

	if fs.step != filterStepSelectRepo {
		t.Errorf("step = %d, want %d (should start at selectRepo with multiple repos)", fs.step, filterStepSelectRepo)
	}
	if fs.hasExisting != true {
		t.Error("hasExisting should be true")
	}
}

func TestFilter_SelectRepo_Navigation(t *testing.T) {
	repos := []poller.GitHubRepo{
		{Owner: "org", Repo: "repo1"},
		{Owner: "org", Repo: "repo2"},
		{Owner: "org", Repo: "repo3"},
	}
	m := testModel(t)
	m.mode = "filter"
	m.filterState = newFilterState(repos, false)

	// Down: 0 → 1.
	result, _ := m.Update(keyPress('j'))
	m = result.(Model)
	if m.filterState.repoIdx != 1 {
		t.Errorf("repoIdx = %d, want 1 after j", m.filterState.repoIdx)
	}

	// Down: 1 → 2.
	result, _ = m.Update(keyPress('j'))
	m = result.(Model)
	if m.filterState.repoIdx != 2 {
		t.Errorf("repoIdx = %d, want 2", m.filterState.repoIdx)
	}

	// Down at end: clamped.
	result, _ = m.Update(keyPress('j'))
	m = result.(Model)
	if m.filterState.repoIdx != 2 {
		t.Errorf("repoIdx = %d, want 2 (clamped)", m.filterState.repoIdx)
	}

	// Up: 2 → 1.
	result, _ = m.Update(keyPress('k'))
	m = result.(Model)
	if m.filterState.repoIdx != 1 {
		t.Errorf("repoIdx = %d, want 1 after k", m.filterState.repoIdx)
	}

	// Up at start: clamped.
	result, _ = m.Update(keyPress('k'))
	m = result.(Model)
	result, _ = m.Update(keyPress('k'))
	m = result.(Model)
	if m.filterState.repoIdx != 0 {
		t.Errorf("repoIdx = %d, want 0 (clamped)", m.filterState.repoIdx)
	}
}

func TestFilter_SelectRepo_EnterSelects(t *testing.T) {
	repos := []poller.GitHubRepo{
		{Owner: "org", Repo: "repo1"},
		{Owner: "org", Repo: "repo2"},
	}
	m := testModel(t)
	m.mode = "filter"
	m.filterState = newFilterState(repos, false)

	// Navigate to second repo and select.
	result, _ := m.Update(keyPress('j'))
	m = result.(Model)
	result, _ = m.Update(specialKey(tea.KeyEnter))
	m = result.(Model)

	if m.filterState.selectedRepo.Repo != "repo2" {
		t.Errorf("selectedRepo.Repo = %q, want %q", m.filterState.selectedRepo.Repo, "repo2")
	}
	if m.filterState.step != filterStepSelectType {
		t.Errorf("step = %d, want %d after repo selection", m.filterState.step, filterStepSelectType)
	}
}

func TestFilter_SelectRepo_EscExits(t *testing.T) {
	repos := []poller.GitHubRepo{
		{Owner: "org", Repo: "repo1"},
		{Owner: "org", Repo: "repo2"},
	}
	m := testModel(t)
	m.mode = "filter"
	m.filterState = newFilterState(repos, false)

	result, _ := m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.mode != "normal" {
		t.Errorf("mode = %q, want %q after esc from selectRepo", m.mode, "normal")
	}
	if m.filterState != nil {
		t.Error("filterState should be nil after esc")
	}
}

func TestFilter_SelectType_LabelChoice(t *testing.T) {
	repos := []poller.GitHubRepo{{Owner: "org", Repo: "repo1"}}
	m := testModel(t)
	m.mode = "filter"
	m.filterState = newFilterState(repos, false)
	// Auto-skip puts us at selectType, idx 0 = label.

	result, _ := m.Update(specialKey(tea.KeyEnter))
	m = result.(Model)
	if m.filterState.filterType != ghpkg.FilterLabel {
		t.Errorf("filterType = %v, want %v", m.filterState.filterType, ghpkg.FilterLabel)
	}
	if m.filterState.step != filterStepFetchingChoices {
		t.Errorf("step = %d, want %d (fetching choices)", m.filterState.step, filterStepFetchingChoices)
	}
}

func TestFilter_SelectType_MilestoneChoice(t *testing.T) {
	repos := []poller.GitHubRepo{{Owner: "org", Repo: "repo1"}}
	m := testModel(t)
	m.mode = "filter"
	m.filterState = newFilterState(repos, false)

	// Navigate down to milestone (idx 1), then enter.
	result, _ := m.Update(specialKey(tea.KeyDown))
	m = result.(Model)
	result, _ = m.Update(specialKey(tea.KeyEnter))
	m = result.(Model)
	if m.filterState.filterType != ghpkg.FilterMilestone {
		t.Errorf("filterType = %v, want %v", m.filterState.filterType, ghpkg.FilterMilestone)
	}
}

func TestFilter_SelectType_ProjectGoesInput(t *testing.T) {
	repos := []poller.GitHubRepo{{Owner: "org", Repo: "repo1"}}
	m := testModel(t)
	m.mode = "filter"
	m.filterState = newFilterState(repos, false)

	// Navigate down to project (idx 2), then enter.
	result, _ := m.Update(specialKey(tea.KeyDown))
	m = result.(Model)
	result, _ = m.Update(specialKey(tea.KeyDown))
	m = result.(Model)
	result, cmd := m.Update(specialKey(tea.KeyEnter))
	m = result.(Model)
	if m.filterState.filterType != ghpkg.FilterProject {
		t.Errorf("filterType = %v, want %v", m.filterState.filterType, ghpkg.FilterProject)
	}
	if m.filterState.step != filterStepInputValue {
		t.Errorf("step = %d, want %d (project goes straight to input)", m.filterState.step, filterStepInputValue)
	}
	if cmd == nil {
		t.Error("expected non-nil command (input focus)")
	}
}

func TestFilter_SelectType_AssigneeChoice(t *testing.T) {
	repos := []poller.GitHubRepo{{Owner: "org", Repo: "repo1"}}
	m := testModel(t)
	m.mode = "filter"
	m.filterState = newFilterState(repos, false)

	// Navigate down to assignee (idx 3), then enter.
	for i := 0; i < 3; i++ {
		result, _ := m.Update(specialKey(tea.KeyDown))
		m = result.(Model)
	}
	result, _ := m.Update(specialKey(tea.KeyEnter))
	m = result.(Model)
	if m.filterState.filterType != ghpkg.FilterAssignee {
		t.Errorf("filterType = %v, want %v", m.filterState.filterType, ghpkg.FilterAssignee)
	}
}

func TestFilter_SelectType_EscBackToRepo(t *testing.T) {
	repos := []poller.GitHubRepo{
		{Owner: "org", Repo: "repo1"},
		{Owner: "org", Repo: "repo2"},
	}
	m := testModel(t)
	m.mode = "filter"
	m.filterState = newFilterState(repos, false)
	// At selectRepo, select a repo first.
	result, _ := m.Update(specialKey(tea.KeyEnter))
	m = result.(Model)
	if m.filterState.step != filterStepSelectType {
		t.Fatal("expected selectType step")
	}

	// Esc goes back to selectRepo (multiple repos).
	result, _ = m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.filterState.step != filterStepSelectRepo {
		t.Errorf("step = %d, want %d (back to selectRepo)", m.filterState.step, filterStepSelectRepo)
	}
}

func TestFilter_SelectType_EscExits_SingleRepo(t *testing.T) {
	repos := []poller.GitHubRepo{{Owner: "org", Repo: "repo1"}}
	m := testModel(t)
	m.mode = "filter"
	m.filterState = newFilterState(repos, false)

	// Esc on single repo exits filter.
	result, _ := m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.mode != "normal" {
		t.Errorf("mode = %q, want %q after esc from selectType (single repo)", m.mode, "normal")
	}
}

func TestFilter_SelectChoice_Navigation(t *testing.T) {
	repos := []poller.GitHubRepo{{Owner: "org", Repo: "repo1"}}
	m := testModel(t)
	m.mode = "filter"
	m.filterState = newFilterState(repos, false)
	m.filterState.step = filterStepSelectChoice
	m.filterState.choices = []ghpkg.RepoChoice{
		{Value: "bug"},
		{Value: "enhancement"},
		{Value: "docs"},
	}
	m.filterState.choiceIdx = 0

	// Down: 0 → 1.
	result, _ := m.Update(keyPress('j'))
	m = result.(Model)
	if m.filterState.choiceIdx != 1 {
		t.Errorf("choiceIdx = %d, want 1", m.filterState.choiceIdx)
	}

	// Navigate past end to custom option.
	result, _ = m.Update(keyPress('j'))
	m = result.(Model)
	result, _ = m.Update(keyPress('j'))
	m = result.(Model)
	if m.filterState.choiceIdx != 3 {
		t.Errorf("choiceIdx = %d, want 3 (custom option)", m.filterState.choiceIdx)
	}

	// Clamped at max.
	result, _ = m.Update(keyPress('j'))
	m = result.(Model)
	if m.filterState.choiceIdx != 3 {
		t.Errorf("choiceIdx = %d, want 3 (clamped)", m.filterState.choiceIdx)
	}

	// Up.
	result, _ = m.Update(keyPress('k'))
	m = result.(Model)
	if m.filterState.choiceIdx != 2 {
		t.Errorf("choiceIdx = %d, want 2 after up", m.filterState.choiceIdx)
	}
}

func TestFilter_SelectChoice_EscGoesBack(t *testing.T) {
	repos := []poller.GitHubRepo{{Owner: "org", Repo: "repo1"}}
	m := testModel(t)
	m.mode = "filter"
	m.filterState = newFilterState(repos, false)
	m.filterState.step = filterStepSelectChoice
	m.filterState.choices = []ghpkg.RepoChoice{{Value: "bug"}}

	result, _ := m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.filterState.step != filterStepSelectType {
		t.Errorf("step = %d, want %d after esc from selectChoice", m.filterState.step, filterStepSelectType)
	}
}

func TestFilter_SelectChoice_EnterCustom(t *testing.T) {
	repos := []poller.GitHubRepo{{Owner: "org", Repo: "repo1"}}
	m := testModel(t)
	m.mode = "filter"
	m.filterState = newFilterState(repos, false)
	m.filterState.step = filterStepSelectChoice
	m.filterState.filterType = ghpkg.FilterLabel
	m.filterState.choices = []ghpkg.RepoChoice{{Value: "bug"}}
	m.filterState.choiceIdx = 1 // "custom" option index = len(choices)

	result, cmd := m.Update(specialKey(tea.KeyEnter))
	m = result.(Model)
	if m.filterState.step != filterStepInputValue {
		t.Errorf("step = %d, want %d (custom option goes to input)", m.filterState.step, filterStepInputValue)
	}
	if cmd == nil {
		t.Error("expected non-nil command (input focus)")
	}
}

func TestFilter_InputValue_EscGoesBack_HasChoices(t *testing.T) {
	repos := []poller.GitHubRepo{{Owner: "org", Repo: "repo1"}}
	m := testModel(t)
	m.mode = "filter"
	m.filterState = newFilterState(repos, false)
	m.filterState.step = filterStepInputValue
	m.filterState.choices = []ghpkg.RepoChoice{{Value: "bug"}}

	result, _ := m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.filterState.step != filterStepSelectChoice {
		t.Errorf("step = %d, want %d (back to choices)", m.filterState.step, filterStepSelectChoice)
	}
}

func TestFilter_InputValue_EscGoesBack_NoChoices(t *testing.T) {
	repos := []poller.GitHubRepo{{Owner: "org", Repo: "repo1"}}
	m := testModel(t)
	m.mode = "filter"
	m.filterState = newFilterState(repos, false)
	m.filterState.step = filterStepInputValue
	m.filterState.choices = nil

	result, _ := m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.filterState.step != filterStepSelectType {
		t.Errorf("step = %d, want %d (back to selectType)", m.filterState.step, filterStepSelectType)
	}
}

func TestFilter_Preview_EscGoesBack(t *testing.T) {
	repos := []poller.GitHubRepo{{Owner: "org", Repo: "repo1"}}
	m := testModel(t)
	m.mode = "filter"
	m.filterState = newFilterState(repos, false)
	m.filterState.step = filterStepPreview
	m.filterState.results = &ghpkg.SearchResult{
		TotalCount: 1,
		Items:      []ghpkg.ItemStatus{{Number: 1, Title: "test"}},
	}

	result, _ := m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.filterState.step != filterStepSelectType {
		t.Errorf("step = %d, want %d after esc from preview", m.filterState.step, filterStepSelectType)
	}
	if m.filterState.results != nil {
		t.Error("results should be nil after backing out of preview")
	}
}

func TestFilter_Preview_EnterEmpty_Exits(t *testing.T) {
	repos := []poller.GitHubRepo{{Owner: "org", Repo: "repo1"}}
	m := testModel(t)
	m.mode = "filter"
	m.filterState = newFilterState(repos, false)
	m.filterState.step = filterStepPreview
	m.filterState.results = &ghpkg.SearchResult{TotalCount: 0, Items: nil}

	result, _ := m.Update(specialKey(tea.KeyEnter))
	m = result.(Model)
	if m.mode != "normal" {
		t.Errorf("mode = %q, want %q after enter on empty preview", m.mode, "normal")
	}
	if m.filterState != nil {
		t.Error("filterState should be nil after exiting")
	}
}

func TestFilter_Preview_Enter_HasExisting_GoesToConflict(t *testing.T) {
	repos := []poller.GitHubRepo{{Owner: "org", Repo: "repo1"}}
	m := testModel(t)
	m.mode = "filter"
	m.filterState = newFilterState(repos, true)
	m.filterState.step = filterStepPreview
	m.filterState.results = &ghpkg.SearchResult{
		TotalCount: 1,
		Items:      []ghpkg.ItemStatus{{Number: 1, Title: "test"}},
	}

	result, _ := m.Update(specialKey(tea.KeyEnter))
	m = result.(Model)
	if m.filterState.step != filterStepConflict {
		t.Errorf("step = %d, want %d (conflict resolution with existing items)", m.filterState.step, filterStepConflict)
	}
}

func TestFilter_Conflict_EscGoesBack(t *testing.T) {
	repos := []poller.GitHubRepo{{Owner: "org", Repo: "repo1"}}
	m := testModel(t)
	m.mode = "filter"
	m.filterState = newFilterState(repos, true)
	m.filterState.step = filterStepConflict

	result, _ := m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.filterState.step != filterStepPreview {
		t.Errorf("step = %d, want %d after esc from conflict", m.filterState.step, filterStepPreview)
	}
}

// --- Filter async result messages ---

func TestFilterChoicesMsg_Success(t *testing.T) {
	repos := []poller.GitHubRepo{{Owner: "org", Repo: "repo1"}}
	m := testModel(t)
	m.mode = "filter"
	m.filterState = newFilterState(repos, false)
	m.filterState.step = filterStepFetchingChoices

	choices := []ghpkg.RepoChoice{
		{Value: "bug"},
		{Value: "enhancement"},
	}
	result, _ := m.Update(filterChoicesMsg{choices: choices})
	m = result.(Model)
	if m.filterState.step != filterStepSelectChoice {
		t.Errorf("step = %d, want %d after choices", m.filterState.step, filterStepSelectChoice)
	}
	if len(m.filterState.choices) != 2 {
		t.Errorf("choices = %d, want 2", len(m.filterState.choices))
	}
}

func TestFilterChoicesMsg_Error_FallsBackToInput(t *testing.T) {
	repos := []poller.GitHubRepo{{Owner: "org", Repo: "repo1"}}
	m := testModel(t)
	m.mode = "filter"
	m.filterState = newFilterState(repos, false)
	m.filterState.step = filterStepFetchingChoices
	m.filterState.filterType = ghpkg.FilterLabel

	result, _ := m.Update(filterChoicesMsg{err: errTest})
	m = result.(Model)
	if m.filterState.step != filterStepInputValue {
		t.Errorf("step = %d, want %d (fallback to input on error)", m.filterState.step, filterStepInputValue)
	}
}

func TestFilterChoicesMsg_Empty_FallsBackToInput(t *testing.T) {
	repos := []poller.GitHubRepo{{Owner: "org", Repo: "repo1"}}
	m := testModel(t)
	m.mode = "filter"
	m.filterState = newFilterState(repos, false)
	m.filterState.step = filterStepFetchingChoices
	m.filterState.filterType = ghpkg.FilterMilestone

	result, _ := m.Update(filterChoicesMsg{choices: nil})
	m = result.(Model)
	if m.filterState.step != filterStepInputValue {
		t.Errorf("step = %d, want %d (fallback to input on empty choices)", m.filterState.step, filterStepInputValue)
	}
}

func TestFilterSearchResultMsg_Success(t *testing.T) {
	repos := []poller.GitHubRepo{{Owner: "org", Repo: "repo1"}}
	m := testModel(t)
	m.mode = "filter"
	m.filterState = newFilterState(repos, false)
	m.filterState.step = filterStepFetching

	result, _ := m.Update(filterSearchResultMsg{
		results: &ghpkg.SearchResult{
			TotalCount: 5,
			Items:      []ghpkg.ItemStatus{{Number: 1}},
		},
	})
	m = result.(Model)
	if m.filterState.step != filterStepPreview {
		t.Errorf("step = %d, want %d after search results", m.filterState.step, filterStepPreview)
	}
	if m.filterState.results.TotalCount != 5 {
		t.Errorf("totalCount = %d, want 5", m.filterState.results.TotalCount)
	}
}

func TestFilterSearchResultMsg_Error(t *testing.T) {
	repos := []poller.GitHubRepo{{Owner: "org", Repo: "repo1"}}
	m := testModel(t)
	m.mode = "filter"
	m.filterState = newFilterState(repos, false)
	m.filterState.step = filterStepFetching

	result, _ := m.Update(filterSearchResultMsg{err: errTest})
	m = result.(Model)
	if m.filterState.step != filterStepPreview {
		t.Errorf("step = %d, want %d (preview shows error)", m.filterState.step, filterStepPreview)
	}
	if m.filterState.err == nil {
		t.Error("filterState.err should be set")
	}
}

func TestBulkAddResultMsg_Success(t *testing.T) {
	repos := []poller.GitHubRepo{{Owner: "org", Repo: "repo1"}}
	m := testModel(t)
	m.mode = "filter"
	m.filterState = newFilterState(repos, false)
	m.filterState.step = filterStepFetching

	result, _ := m.Update(bulkAddResultMsg{added: 5})
	m = result.(Model)
	if m.filterStatus != "Added 5 items" {
		t.Errorf("filterStatus = %q, want %q", m.filterStatus, "Added 5 items")
	}
	if m.mode != "normal" {
		t.Errorf("mode = %q, want %q after bulk add", m.mode, "normal")
	}
	if m.filterState != nil {
		t.Error("filterState should be nil after bulk add")
	}
}

func TestBulkUpdateResultMsg_Success(t *testing.T) {
	repos := []poller.GitHubRepo{{Owner: "org", Repo: "repo1"}}
	m := testModel(t)
	m.mode = "filter"
	m.filterState = newFilterState(repos, false)
	m.filterState.step = filterStepFetching

	result, _ := m.Update(bulkUpdateResultMsg{added: 3, removed: 2})
	m = result.(Model)
	if m.filterStatus != "Updated: +3 new, -2 closed" {
		t.Errorf("filterStatus = %q, want %q", m.filterStatus, "Updated: +3 new, -2 closed")
	}
	if m.mode != "normal" {
		t.Errorf("mode = %q, want %q", m.mode, "normal")
	}
}

func TestClearFilterStatus(t *testing.T) {
	m := testModel(t)
	m.filterStatus = "something"

	result, _ := m.Update(clearFilterStatusMsg{})
	m = result.(Model)
	if m.filterStatus != "" {
		t.Errorf("filterStatus = %q, want empty", m.filterStatus)
	}
}

// --- Track mode ---

func TestTrackMode_EscExits(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.mode = "track"
	m.trackStep = trackStepInput
	m.trackRows = buildTrackRows([]poller.GitHubRepo{{Owner: "org", Repo: "repo"}}, nil)

	result, _ := m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.mode != "normal" {
		t.Errorf("mode = %q, want %q after esc from track", m.mode, "normal")
	}
	if m.trackRows != nil {
		t.Error("trackRows should be nil after esc")
	}
}

func TestTrackMode_RowNavigation(t *testing.T) {
	m := testModel(t)
	m.mode = "track"
	m.trackStep = trackStepInput
	m.trackRows = buildTrackRows([]poller.GitHubRepo{
		{Owner: "org", Repo: "repo1"},
		{Owner: "org", Repo: "repo2"},
	}, nil)
	m.trackFocus = 0

	// Tab moves down.
	result, _ := m.Update(specialKey(tea.KeyTab))
	m = result.(Model)
	if m.trackFocus != 1 {
		t.Errorf("trackFocus = %d, want 1 after tab", m.trackFocus)
	}

	// Tab at end: clamped.
	result, _ = m.Update(specialKey(tea.KeyTab))
	m = result.(Model)
	if m.trackFocus != 1 {
		t.Errorf("trackFocus = %d, want 1 (clamped)", m.trackFocus)
	}

	// Shift+tab moves up.
	result, _ = m.Update(shiftTabKey())
	m = result.(Model)
	if m.trackFocus != 0 {
		t.Errorf("trackFocus = %d, want 0 after shift+tab", m.trackFocus)
	}

	// Shift+tab at start: clamped.
	result, _ = m.Update(shiftTabKey())
	m = result.(Model)
	if m.trackFocus != 0 {
		t.Errorf("trackFocus = %d, want 0 (clamped)", m.trackFocus)
	}
}

func TestTrackMode_Preview_EscGoesBack(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.mode = "track"
	m.trackStep = trackStepPreview
	m.trackRows = buildTrackRows([]poller.GitHubRepo{{Owner: "org", Repo: "repo"}}, nil)
	m.trackFocus = 0
	m.trackPreviewItems = []trackPreviewItem{
		{ref: &ghpkg.ItemRef{Owner: "org", Repo: "repo", Number: 1}, title: "test", status: "open"},
	}

	result, _ := m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.trackStep != trackStepInput {
		t.Errorf("trackStep = %d, want %d after esc from preview", m.trackStep, trackStepInput)
	}
	if m.trackPreviewItems != nil {
		t.Error("trackPreviewItems should be nil after esc from preview")
	}
}

func TestTrackMode_Fetching_EscExits(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.mode = "track"
	m.trackStep = trackStepFetching

	result, _ := m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.mode != "normal" {
		t.Errorf("mode = %q, want %q after esc from fetching", m.mode, "normal")
	}
}

func TestTrackMode_CleanupConfirm_NCancels(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.mode = "untrack"
	m.trackStep = trackStepCleanupConfirm
	m.trackCleanupCount = 5
	m.trackRows = buildTrackRows([]poller.GitHubRepo{{Owner: "org", Repo: "repo"}}, nil)
	m.trackFocus = 0

	result, _ := m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.trackStep != trackStepInput {
		t.Errorf("trackStep = %d, want %d after esc from cleanup confirm", m.trackStep, trackStepInput)
	}
	if m.trackCleanupCount != 0 {
		t.Errorf("cleanupCount = %d, want 0 after cancel", m.trackCleanupCount)
	}
}

// --- buildTrackRows ---

func TestBuildTrackRows_Track(t *testing.T) {
	ghRepos := []poller.GitHubRepo{
		{Owner: "org1", Repo: "repoA"},
		{Owner: "org2", Repo: "repoB"},
	}
	rows := buildTrackRows(ghRepos, nil)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].ownerRepo != "org1/repoA" {
		t.Errorf("row 0 ownerRepo = %q, want %q", rows[0].ownerRepo, "org1/repoA")
	}
	if rows[1].ownerRepo != "org2/repoB" {
		t.Errorf("row 1 ownerRepo = %q, want %q", rows[1].ownerRepo, "org2/repoB")
	}
	// In track mode (nil items), input should be empty.
	if rows[0].input.Value() != "" {
		t.Errorf("row 0 value = %q, want empty", rows[0].input.Value())
	}
}

func TestBuildTrackRows_Untrack(t *testing.T) {
	ghRepos := []poller.GitHubRepo{
		{Owner: "org", Repo: "repo"},
	}
	items := []db.TrackedItem{
		{Owner: "org", Repo: "repo", Number: 42},
		{Owner: "org", Repo: "repo", Number: 17},
	}
	rows := buildTrackRows(ghRepos, items)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	// Untrack mode pre-populates with existing numbers.
	if rows[0].input.Value() != "42 17" {
		t.Errorf("row value = %q, want %q", rows[0].input.Value(), "42 17")
	}
	if rows[0].original != "42 17" {
		t.Errorf("row original = %q, want %q", rows[0].original, "42 17")
	}
}

// --- dedupeRefs ---

func TestDedupeRefs(t *testing.T) {
	refs := []*ghpkg.ItemRef{
		{Owner: "org", Repo: "repo", Number: 1},
		{Owner: "org", Repo: "repo", Number: 2},
		{Owner: "org", Repo: "repo", Number: 1},  // duplicate
		{Owner: "org", Repo: "repo2", Number: 1}, // different repo, not a dupe
	}
	deduped := dedupeRefs(refs)
	if len(deduped) != 3 {
		t.Errorf("deduped = %d, want 3", len(deduped))
	}
}

// --- Track result messages ---

func TestTrackFetchResult_Error(t *testing.T) {
	m := testModel(t)
	m.mode = "track"
	m.trackStep = trackStepFetching

	result, _ := m.Update(trackFetchResultMsg{err: errTest})
	m = result.(Model)
	if !m.trackError {
		t.Error("trackError should be true")
	}
	if m.trackStep != trackStepInput {
		t.Errorf("trackStep = %d, want %d after error", m.trackStep, trackStepInput)
	}
}

func TestTrackFetchResult_Success(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.mode = "track"
	m.trackStep = trackStepFetching

	items := []trackPreviewItem{
		{ref: &ghpkg.ItemRef{Owner: "org", Repo: "repo", Number: 1}, title: "test", status: "open"},
	}
	result, _ := m.Update(trackFetchResultMsg{items: items})
	m = result.(Model)
	if m.trackStep != trackStepPreview {
		t.Errorf("trackStep = %d, want %d after success", m.trackStep, trackStepPreview)
	}
	if len(m.trackPreviewItems) != 1 {
		t.Errorf("previewItems = %d, want 1", len(m.trackPreviewItems))
	}
}

func TestBulkTrackResultMsg_Success(t *testing.T) {
	m := testModel(t)
	m.mode = "track"

	result, _ := m.Update(bulkTrackResultMsg{added: 3, failed: 0})
	m = result.(Model)
	if m.trackStatus != "Tracked 3 items" {
		t.Errorf("trackStatus = %q, want %q", m.trackStatus, "Tracked 3 items")
	}
}

func TestBulkTrackResultMsg_WithFailures(t *testing.T) {
	m := testModel(t)
	m.mode = "track"

	result, _ := m.Update(bulkTrackResultMsg{added: 2, failed: 1, errors: []string{"err1"}})
	m = result.(Model)
	if !m.trackError {
		t.Error("trackError should be true with failures")
	}
}

func TestBulkUntrackResultMsg_Success(t *testing.T) {
	m := testModel(t)
	m.mode = "untrack"

	result, _ := m.Update(bulkUntrackResultMsg{removed: 2, failed: 0})
	m = result.(Model)
	if m.trackStatus != "Untracked 2 items" {
		t.Errorf("trackStatus = %q, want %q", m.trackStatus, "Untracked 2 items")
	}
}

func TestClearTrackStatus(t *testing.T) {
	m := testModel(t)
	m.mode = "track"
	m.trackStatus = "something"
	m.trackError = true
	m.trackRows = buildTrackRows([]poller.GitHubRepo{{Owner: "org", Repo: "repo"}}, nil)

	result, _ := m.Update(clearTrackStatusMsg{})
	m = result.(Model)
	if m.trackStatus != "" {
		t.Errorf("trackStatus = %q, want empty", m.trackStatus)
	}
	if m.trackError {
		t.Error("trackError should be false after clear")
	}
	if m.mode != "normal" {
		t.Errorf("mode = %q, want %q after clear", m.mode, "normal")
	}
	if m.trackRows != nil {
		t.Error("trackRows should be nil after clear")
	}
}

// --- Dep guidance mode ---

func TestDepGuidance_EscCancels(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.mode = "dep-guidance"

	result, _ := m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.mode != "normal" {
		t.Errorf("mode = %q, want %q after esc from dep-guidance", m.mode, "normal")
	}
}

func TestDepGuidance_CtrlD_SavesGuidance(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.mode = "dep-guidance"
	m.rebuildDepsInput.SetValue("skip issue 106")

	result, _ := m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	m = result.(Model)
	if m.mode != "normal" {
		t.Errorf("mode = %q, want %q after ctrl+d", m.mode, "normal")
	}
	if m.autopilotDepGuidance != "skip issue 106" {
		t.Errorf("guidance = %q, want %q", m.autopilotDepGuidance, "skip issue 106")
	}
}

func TestDepGuidance_CtrlD_EmptyClears(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.mode = "dep-guidance"
	m.autopilotDepGuidance = "old guidance"
	m.rebuildDepsInput.SetValue("   ") // whitespace only

	result, _ := m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	m = result.(Model)
	if m.autopilotDepGuidance != "" {
		t.Errorf("guidance = %q, want empty after blank submission", m.autopilotDepGuidance)
	}
}

// --- Rebuild deps mode ---

func TestRebuildDeps_EscCancels(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.mode = "rebuild-deps"

	result, _ := m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.mode != "normal" {
		t.Errorf("mode = %q, want %q after esc from rebuild-deps", m.mode, "normal")
	}
}

func TestRebuildDeps_CtrlD_Submits(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.mode = "rebuild-deps"
	sup := autopilot.New(m.store, m.project, nil, t.TempDir(), "owner", "repo", "fake-token")
	m.autopilotSupervisor = sup
	m.rebuildDepsInput.SetValue("some guidance")

	result, cmd := m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	m = result.(Model)
	if m.rebuildDepsStatus != "Rebuilding dependency graph..." {
		t.Errorf("status = %q, want %q", m.rebuildDepsStatus, "Rebuilding dependency graph...")
	}
	if cmd == nil {
		t.Error("expected non-nil command for rebuild")
	}
}

// --- Rebuild deps result messages ---

func TestRebuildDepsResult_Success(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.autopilotMode = "running"

	result, _ := m.Update(rebuildDepsResultMsg{
		options: []autopilot.DepOption{{Unblocked: 5}},
	})
	m = result.(Model)
	if m.autopilotMode != "dep-select" {
		t.Errorf("autopilotMode = %q, want %q after rebuild result", m.autopilotMode, "dep-select")
	}
	if m.autopilotUnblocked != 5 {
		t.Errorf("unblocked = %d, want 5", m.autopilotUnblocked)
	}
}

func TestRebuildDepsResult_Error(t *testing.T) {
	m := testModel(t)

	result, _ := m.Update(rebuildDepsResultMsg{err: errTest})
	m = result.(Model)
	if m.rebuildDepsStatus == "" {
		t.Error("status should contain error")
	}
}

// --- ApplyDepOption result ---

func TestApplyDepOptionResult_Success(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40

	result, _ := m.Update(applyDepOptionResultMsg{
		result: autopilot.RebuildResult{Unblocked: 3, Skipped: 1},
	})
	m = result.(Model)
	if m.rebuildDepsStatus == "" {
		t.Error("status should be set after apply dep option")
	}
}

// --- AutopilotApplyResult ---

func TestAutopilotApplyResult_Success(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.polling = true

	result, _ := m.Update(autopilotApplyResultMsg{total: 10, unblocked: 5})
	m = result.(Model)
	if m.autopilotMode != "confirm" {
		t.Errorf("autopilotMode = %q, want %q after apply result", m.autopilotMode, "confirm")
	}
	if m.autopilotTotal != 10 {
		t.Errorf("total = %d, want 10", m.autopilotTotal)
	}
	if m.autopilotUnblocked != 5 {
		t.Errorf("unblocked = %d, want 5", m.autopilotUnblocked)
	}
	if m.polling {
		t.Error("polling should be false after apply")
	}
}

func TestAutopilotApplyResult_Error(t *testing.T) {
	m := testModel(t)
	m.polling = true
	sup := autopilot.New(m.store, m.project, nil, t.TempDir(), "owner", "repo", "fake-token")
	m.autopilotSupervisor = sup

	result, _ := m.Update(autopilotApplyResultMsg{err: errTest})
	m = result.(Model)
	if m.autopilotMode != "" {
		t.Errorf("autopilotMode = %q, want empty after error", m.autopilotMode)
	}
	if m.autopilotSupervisor != nil {
		t.Error("supervisor should be nil after error")
	}
}

// --- Review session result ---

func TestReviewSessionResult_Success(t *testing.T) {
	m := testModel(t)
	m.autopilotStatus = "launching..."

	result, _ := m.Update(reviewSessionResultMsg{
		result: &autopilot.ReviewSessionResult{
			IssueNumber:  42,
			SessionID:    "sess-123",
			WorktreePath: "/path/to/worktree",
		},
	})
	m = result.(Model)
	if m.warningBanner == "" {
		t.Error("warningBanner should contain review session info")
	}
	if m.autopilotStatus != "" {
		t.Errorf("status = %q, want empty after success", m.autopilotStatus)
	}
}

func TestReviewSessionResult_Error(t *testing.T) {
	m := testModel(t)

	result, _ := m.Update(reviewSessionResultMsg{err: errTest})
	m = result.(Model)
	if m.autopilotStatus == "" {
		t.Error("status should contain error")
	}
}

// --- Cleanup result ---

func TestCleanupResult_Success(t *testing.T) {
	m := testModel(t)
	m.mode = "untrack"

	result, _ := m.Update(cleanupResultMsg{removed: 3})
	m = result.(Model)
	if m.trackStatus != "Cleaned up 3 done items" {
		t.Errorf("trackStatus = %q, want %q", m.trackStatus, "Cleaned up 3 done items")
	}
}

func TestCleanupResult_Error(t *testing.T) {
	m := testModel(t)
	m.mode = "untrack"

	result, _ := m.Update(cleanupResultMsg{err: errTest})
	m = result.(Model)
	if !m.trackError {
		t.Error("trackError should be true")
	}
}

// --- b key opens filter mode ---

func TestBKey_OpensFilter_NoReposGuard(t *testing.T) {
	m := testModel(t)
	// b requires GitHub repos — since poller.GitHubRepos() derives from
	// enrolled repos and our test poller has none, the guard path fires.
	result, _ := m.Update(keyPress('b'))
	m = result.(Model)
	// Without GitHub repos, it stays in normal mode.
	if m.mode != "normal" {
		t.Errorf("mode = %q, want %q (no repos guard)", m.mode, "normal")
	}
}

// --- wrapText ---

func TestWrapText_Short(t *testing.T) {
	result := wrapText("short text", 80)
	if result != "short text" {
		t.Errorf("got %q, want %q", result, "short text")
	}
}

func TestWrapText_Long(t *testing.T) {
	text := "This is a very long text that should be wrapped at a certain width boundary"
	result := wrapText(text, 30)
	// Wrapping inserts newlines, so the result should contain "\n".
	if len(result) <= 0 {
		t.Error("wrapText should return non-empty result")
	}
	// Verify the wrapped result contains a newline.
	found := false
	for _, c := range result {
		if c == '\n' {
			found = true
			break
		}
	}
	if !found {
		t.Error("wrapText should insert newline for text longer than width")
	}
}

// --- Idle check ---

func TestIdleCheckMsg_AutoPauses(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.project.IdlePauseSec = 1                         // 1 second
	m.lastUserInput = time.Now().Add(-5 * time.Second) // idle for 5 seconds

	result, _ := m.Update(idleCheckMsg(time.Now()))
	m = result.(Model)
	if !m.autoPaused {
		t.Error("autoPaused should be true after idle threshold exceeded")
	}
	if !m.poller.IsPaused() {
		t.Error("poller should be paused after auto-pause")
	}
}

func TestIdleCheckMsg_AlreadyPaused_NoOp(t *testing.T) {
	m := testModel(t)
	m.project.IdlePauseSec = 1
	m.lastUserInput = time.Now().Add(-5 * time.Second)
	m.autoPaused = true

	result, _ := m.Update(idleCheckMsg(time.Now()))
	m = result.(Model)
	// Should still be paused (no double-pause events).
	if !m.autoPaused {
		t.Error("should remain auto-paused")
	}
}

func TestAutoResume_OnKeypress(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.autoPaused = true
	m.poller.Pause()

	// Any keypress should auto-resume.
	result, _ := m.Update(keyPress('t'))
	m = result.(Model)
	if m.autoPaused {
		t.Error("autoPaused should be false after keypress")
	}
	if m.poller.IsPaused() {
		t.Error("poller should be resumed after keypress")
	}
}

// --- Log viewer overlay key capture ---

func TestLogViewer_CapturesKeys(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.height = 40
	m.showLogViewer = true
	m.logViewerTask = &db.AutopilotTask{IssueNumber: 42, Status: "done"}

	// 'q' should be captured by log viewer (close), not trigger quit.
	_, cmd := m.Update(keyPress('q'))
	if cmd != nil {
		msg := cmd()
		if _, ok := msg.(tea.QuitMsg); ok {
			t.Error("'q' in log viewer should not trigger quit")
		}
	}
}

// --- Sentinel error for test assertions ---

var errTest = fmt.Errorf("test error")
