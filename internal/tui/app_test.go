package tui

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/dustinlange/agent-minder/internal/db"
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
	p := poller.New(store, proj, nil, nil, nil)
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
	p := poller.New(store, proj, nil, nil, nil)
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

// --- Poll confirm ---

func TestPollConfirm_RKey(t *testing.T) {
	m := testModel(t)

	// 'R' (uppercase) should set pollConfirm.
	result, _ := m.Update(keyPress('R'))
	m = result.(Model)
	if !m.pollConfirm {
		t.Error("pollConfirm should be true after 'R'")
	}

	// 'n' should cancel.
	result, _ = m.Update(keyPress('n'))
	m = result.(Model)
	if m.pollConfirm {
		t.Error("pollConfirm should be false after 'n'")
	}
}

func TestPollConfirm_EscCancels(t *testing.T) {
	m := testModel(t)

	result, _ := m.Update(keyPress('R'))
	m = result.(Model)
	if !m.pollConfirm {
		t.Fatal("pollConfirm should be true")
	}

	result, _ = m.Update(specialKey(tea.KeyEscape))
	m = result.(Model)
	if m.pollConfirm {
		t.Error("pollConfirm should be false after esc")
	}
}

func TestPollConfirm_YConfirms(t *testing.T) {
	m := testModel(t)

	result, _ := m.Update(keyPress('R'))
	m = result.(Model)

	result, cmd := m.Update(keyPress('y'))
	m = result.(Model)
	if m.pollConfirm {
		t.Error("pollConfirm should be false after 'y'")
	}
	if m.activeTab != tabAnalysis {
		t.Errorf("activeTab = %d, want %d (should switch to Analysis)", m.activeTab, tabAnalysis)
	}
	if cmd == nil {
		t.Error("'y' confirm should return a non-nil command (poll)")
	}
}

// --- 'r' key (status check) ---

func TestStatusNow_RKey(t *testing.T) {
	m := testModel(t)
	// On non-autopilot tab, 'r' triggers a status check.
	m.activeTab = tabOperations
	_, cmd := m.Update(keyPress('r'))
	if cmd == nil {
		t.Error("'r' should return a non-nil command (StatusNow)")
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

	result, _ := m.Update(keyPress('w'))
	m = result.(Model)
	if !m.showWorktrees {
		t.Error("showWorktrees should be true after 'w' on Ops tab")
	}

	result, _ = m.Update(keyPress('w'))
	m = result.(Model)
	if m.showWorktrees {
		t.Error("showWorktrees should be false after second 'w'")
	}
}

func TestWorktreeToggle_NonOpsTabNoOp(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAnalysis

	result, _ := m.Update(keyPress('w'))
	m = result.(Model)
	if m.showWorktrees {
		t.Error("showWorktrees should remain false on non-Ops tab")
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

// --- Concerns expand toggle ---

func TestConcernsExpand_AnalysisTab(t *testing.T) {
	m := testModel(t)
	m.activeTab = tabAnalysis

	result, _ := m.Update(keyPress('c'))
	m = result.(Model)
	if !m.concernsExpanded {
		t.Error("concernsExpanded should be true after 'c' on Analysis tab")
	}

	result, _ = m.Update(keyPress('c'))
	m = result.(Model)
	if m.concernsExpanded {
		t.Error("concernsExpanded should be false after second 'c'")
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

	result, _ := m.Update(userMsgResultMsg{topic: "test/coord"})
	m = result.(Model)
	if m.userMsgStatus != "Posted to test/coord" {
		t.Errorf("userMsgStatus = %q, want %q", m.userMsgStatus, "Posted to test/coord")
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

// --- Sentinel error for test assertions ---

var errTest = fmt.Errorf("test error")
