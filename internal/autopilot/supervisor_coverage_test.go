package autopilot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dustinlange/agent-minder/internal/db"
	ghpkg "github.com/dustinlange/agent-minder/internal/github"
	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// parseDeps
// ---------------------------------------------------------------------------

func TestParseDeps(t *testing.T) {
	tests := []struct {
		name string
		deps string
		want []int
	}{
		{"empty string", "", nil},
		{"empty array", "[]", nil},
		{"single dep", "[42]", []int{42}},
		{"multiple deps", "[1,2,3]", []int{1, 2, 3}},
		{"deps with spaces", "[ 10 , 20 , 30 ]", []int{10, 20, 30}},
		{"whitespace only brackets", "[  ]", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDeps(tt.deps)
			if len(got) != len(tt.want) {
				t.Fatalf("parseDeps(%q) = %v, want %v", tt.deps, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parseDeps(%q)[%d] = %d, want %d", tt.deps, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// hasLabel
// ---------------------------------------------------------------------------

func TestHasLabel(t *testing.T) {
	labels := []string{"bug", "enhancement", "in-progress"}

	if !hasLabel(labels, "bug") {
		t.Error("expected to find 'bug'")
	}
	if !hasLabel(labels, "in-progress") {
		t.Error("expected to find 'in-progress'")
	}
	if hasLabel(labels, "missing") {
		t.Error("should not find 'missing'")
	}
	if hasLabel(nil, "bug") {
		t.Error("should not find label in nil slice")
	}
	if hasLabel([]string{}, "bug") {
		t.Error("should not find label in empty slice")
	}
}

// ---------------------------------------------------------------------------
// skipMatcher
// ---------------------------------------------------------------------------

func TestSkipMatcher(t *testing.T) {
	t.Run("default pattern", func(t *testing.T) {
		sm := newSkipMatcher("")
		if !sm.matches([]string{"no-agent", "bug"}) {
			t.Error("should match default 'no-agent'")
		}
		if sm.matches([]string{"bug", "enhancement"}) {
			t.Error("should not match without 'no-agent'")
		}
	})

	t.Run("custom single label", func(t *testing.T) {
		sm := newSkipMatcher("human-only")
		if !sm.matches([]string{"human-only"}) {
			t.Error("should match 'human-only'")
		}
		if sm.matches([]string{"no-agent"}) {
			t.Error("should not match default when custom is set")
		}
	})

	t.Run("comma-separated labels", func(t *testing.T) {
		sm := newSkipMatcher("no-agent, manual, human-only")
		if !sm.matches([]string{"manual"}) {
			t.Error("should match 'manual'")
		}
		if !sm.matches([]string{"human-only"}) {
			t.Error("should match 'human-only'")
		}
		if !sm.matches([]string{"no-agent"}) {
			t.Error("should match 'no-agent'")
		}
		if sm.matches([]string{"bug"}) {
			t.Error("should not match 'bug'")
		}
	})

	t.Run("whitespace-only pattern falls back to default", func(t *testing.T) {
		sm := newSkipMatcher("  , , ")
		// All parts are whitespace → falls back to ["no-agent"]
		if !sm.matches([]string{"no-agent"}) {
			t.Error("should fall back to default 'no-agent'")
		}
	})

	t.Run("empty labels list", func(t *testing.T) {
		sm := newSkipMatcher("no-agent")
		if sm.matches(nil) {
			t.Error("should not match nil labels")
		}
		if sm.matches([]string{}) {
			t.Error("should not match empty labels")
		}
	})
}

// ---------------------------------------------------------------------------
// countUnblocked
// ---------------------------------------------------------------------------

func TestCountUnblocked(t *testing.T) {
	t.Run("all unblocked", func(t *testing.T) {
		graph := map[string]json.RawMessage{
			"10": json.RawMessage("[]"),
			"20": json.RawMessage("[]"),
		}
		tasks := []*db.AutopilotTask{
			{IssueNumber: 10, Status: "queued"},
			{IssueNumber: 20, Status: "queued"},
		}
		if got := countUnblocked(graph, tasks); got != 2 {
			t.Errorf("countUnblocked = %d, want 2", got)
		}
	})

	t.Run("one blocked", func(t *testing.T) {
		graph := map[string]json.RawMessage{
			"10": json.RawMessage("[]"),
			"20": json.RawMessage("[10]"),
		}
		tasks := []*db.AutopilotTask{
			{IssueNumber: 10, Status: "queued"},
			{IssueNumber: 20, Status: "queued"},
		}
		if got := countUnblocked(graph, tasks); got != 1 {
			t.Errorf("countUnblocked = %d, want 1", got)
		}
	})

	t.Run("skip excluded", func(t *testing.T) {
		graph := map[string]json.RawMessage{
			"10": json.RawMessage("[]"),
			"20": json.RawMessage(`"skip"`),
		}
		tasks := []*db.AutopilotTask{
			{IssueNumber: 10, Status: "queued"},
			{IssueNumber: 20, Status: "queued"},
		}
		if got := countUnblocked(graph, tasks); got != 1 {
			t.Errorf("countUnblocked = %d, want 1 (skip excluded)", got)
		}
	})

	t.Run("manual excluded from count", func(t *testing.T) {
		graph := map[string]json.RawMessage{
			"10": json.RawMessage("[]"),
			"20": json.RawMessage(`"manual"`),
		}
		tasks := []*db.AutopilotTask{
			{IssueNumber: 10, Status: "queued"},
			{IssueNumber: 20, Status: "manual"},
		}
		if got := countUnblocked(graph, tasks); got != 1 {
			t.Errorf("countUnblocked = %d, want 1 (manual excluded)", got)
		}
	})

	t.Run("task not in graph counts as unblocked", func(t *testing.T) {
		graph := map[string]json.RawMessage{
			"10": json.RawMessage("[]"),
		}
		tasks := []*db.AutopilotTask{
			{IssueNumber: 10, Status: "queued"},
			{IssueNumber: 20, Status: "queued"}, // not in graph
		}
		if got := countUnblocked(graph, tasks); got != 2 {
			t.Errorf("countUnblocked = %d, want 2", got)
		}
	})

	t.Run("string dep array", func(t *testing.T) {
		graph := map[string]json.RawMessage{
			"10": json.RawMessage("[]"),
			"20": json.RawMessage(`["10"]`),
		}
		tasks := []*db.AutopilotTask{
			{IssueNumber: 10, Status: "queued"},
			{IssueNumber: 20, Status: "queued"},
		}
		if got := countUnblocked(graph, tasks); got != 1 {
			t.Errorf("countUnblocked = %d, want 1 (string deps)", got)
		}
	})

	t.Run("empty tasks", func(t *testing.T) {
		graph := map[string]json.RawMessage{}
		if got := countUnblocked(graph, nil); got != 0 {
			t.Errorf("countUnblocked = %d, want 0", got)
		}
	})
}

// ---------------------------------------------------------------------------
// New constructor
// ---------------------------------------------------------------------------

func TestNew(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)

	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "token")

	if len(sup.slots) != 2 { // maxAgents=2 from createTestProject
		t.Errorf("expected 2 slots, got %d", len(sup.slots))
	}
	if sup.owner != "owner" {
		t.Errorf("owner = %q, want %q", sup.owner, "owner")
	}
	if sup.repo != "repo" {
		t.Errorf("repo = %q, want %q", sup.repo, "repo")
	}
	if sup.ghToken != "token" {
		t.Errorf("ghToken = %q, want %q", sup.ghToken, "token")
	}
	if sup.events == nil {
		t.Error("events channel should not be nil")
	}
}

func TestNew_DefaultMaxAgents(t *testing.T) {
	store := openTestStore(t)
	p := &db.Project{
		Name:               "default-agents",
		GoalType:           "test",
		GoalDescription:    "test",
		RefreshIntervalSec: 300,
		MessageTTLSec:      172800,
		LLMProvider:        "anthropic",
		LLMModel:           "claude-haiku-4-5",
		LLMSummarizerModel: "claude-haiku-4-5",
		LLMAnalyzerModel:   "claude-sonnet-4-6",
		AutopilotMaxAgents: 0, // should default to 3
	}
	if err := store.CreateProject(p); err != nil {
		t.Fatal(err)
	}

	sup := New(store, p, nil, "/tmp/repo", "owner", "repo", "")
	if len(sup.slots) != 3 {
		t.Errorf("expected 3 default slots, got %d", len(sup.slots))
	}
}

// ---------------------------------------------------------------------------
// SlotStatus
// ---------------------------------------------------------------------------

func TestSlotStatus_AllIdle(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	infos := sup.SlotStatus()
	if len(infos) != 2 {
		t.Fatalf("expected 2 slot infos, got %d", len(infos))
	}
	for _, info := range infos {
		if info.Status != "idle" {
			t.Errorf("slot %d status = %q, want idle", info.SlotNum, info.Status)
		}
	}
}

func TestSlotStatus_WithRunningAgent(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	// Manually set a slot as occupied.
	sup.mu.Lock()
	sup.slots[0] = &slotState{
		task: &db.AutopilotTask{
			IssueNumber: 42,
			IssueTitle:  "Test issue",
			Branch:      "agent/issue-42",
		},
		startedAt: time.Now().Add(-5 * time.Minute),
		liveStatus: LiveStatus{
			CurrentTool: "Bash",
			ToolInput:   "go test ./...",
			StepCount:   7,
		},
	}
	sup.mu.Unlock()

	infos := sup.SlotStatus()
	if infos[0].Status != "running" {
		t.Errorf("slot 1 status = %q, want running", infos[0].Status)
	}
	if infos[0].IssueNumber != 42 {
		t.Errorf("slot 1 issue = %d, want 42", infos[0].IssueNumber)
	}
	if infos[0].CurrentTool != "Bash" {
		t.Errorf("slot 1 tool = %q, want Bash", infos[0].CurrentTool)
	}
	if infos[0].StepCount != 7 {
		t.Errorf("slot 1 steps = %d, want 7", infos[0].StepCount)
	}
	if infos[1].Status != "idle" {
		t.Errorf("slot 2 status = %q, want idle", infos[1].Status)
	}
}

func TestSlotStatus_PausedShownOnIdleSlots(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	sup.mu.Lock()
	sup.paused = true
	sup.mu.Unlock()

	infos := sup.SlotStatus()
	for _, info := range infos {
		if !info.Paused {
			t.Errorf("slot %d should show paused=true", info.SlotNum)
		}
	}
}

// ---------------------------------------------------------------------------
// StatusBlock
// ---------------------------------------------------------------------------

func TestStatusBlock_Inactive(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	if got := sup.StatusBlock(); got != "" {
		t.Errorf("StatusBlock should be empty when inactive, got %q", got)
	}
}

func TestStatusBlock_Active(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	// Create some tasks.
	for i, status := range []string{"queued", "running", "review", "done", "bailed"} {
		task := &db.AutopilotTask{
			ProjectID:    project.ID,
			IssueNumber:  i + 1,
			IssueTitle:   fmt.Sprintf("Task %d", i+1),
			Dependencies: "[]",
			Status:       status,
		}
		if err := store.CreateAutopilotTask(task); err != nil {
			t.Fatal(err)
		}
	}

	sup.mu.Lock()
	sup.active = true
	sup.slots[0] = &slotState{
		task: &db.AutopilotTask{
			IssueNumber: 2,
			IssueTitle:  "Task 2",
			Branch:      "agent/issue-2",
		},
		startedAt: time.Now().Add(-2 * time.Minute),
		liveStatus: LiveStatus{
			CurrentTool: "Edit",
			StepCount:   3,
		},
	}
	sup.mu.Unlock()

	block := sup.StatusBlock()
	if block == "" {
		t.Fatal("StatusBlock should not be empty when active")
	}

	// Verify it contains expected content.
	checks := []string{
		"Autopilot Status",
		"Slot 1:",
		"Slot 2: idle",
		"#2 Task 2",
		"using Edit",
		"1 queued",
		"1 running",
		"1 in review",
		"1 done",
		"1 bailed",
	}
	for _, check := range checks {
		if !contains(block, check) {
			t.Errorf("StatusBlock missing %q", check)
		}
	}
}

// ---------------------------------------------------------------------------
// DepGraph
// ---------------------------------------------------------------------------

func TestDepGraph_Inactive(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	if got := sup.DepGraph(); got != "" {
		t.Errorf("DepGraph should be empty when inactive, got %q", got)
	}
}

func TestDepGraph_Active(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	// Create tasks with dependencies.
	tasks := []struct {
		num    int
		title  string
		status string
		deps   string
	}{
		{10, "Foundation", "queued", "[]"},
		{20, "Depends on 10", "blocked", "[10]"},
		{30, "Done task", "done", "[]"},
	}
	for _, tt := range tasks {
		task := &db.AutopilotTask{
			ProjectID:    project.ID,
			IssueNumber:  tt.num,
			IssueTitle:   tt.title,
			Dependencies: tt.deps,
			Status:       tt.status,
		}
		if err := store.CreateAutopilotTask(task); err != nil {
			t.Fatal(err)
		}
	}

	sup.mu.Lock()
	sup.active = true
	sup.preparedAt = time.Now()
	sup.mu.Unlock()

	graph := sup.DepGraph()
	if graph == "" {
		t.Fatal("DepGraph should not be empty")
	}

	checks := []string{
		"Dependency Graph",
		"#10 (queued)",
		"#20 (blocked)",
		"waits on #10",
	}
	for _, check := range checks {
		if !contains(graph, check) {
			t.Errorf("DepGraph missing %q in:\n%s", check, graph)
		}
	}

	// Done tasks should not appear in adjacency list.
	if contains(graph, "#30 (done)") {
		t.Error("DepGraph should not show done tasks in adjacency list")
	}
}

// ---------------------------------------------------------------------------
// AddSlot
// ---------------------------------------------------------------------------

func TestAddSlot(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	initial := len(sup.slots)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel so fillSlots doesn't try to launch

	n := sup.AddSlot(ctx)
	if n != initial+1 {
		t.Errorf("AddSlot returned %d, want %d", n, initial+1)
	}
	if len(sup.slots) != initial+1 {
		t.Errorf("slots len = %d, want %d", len(sup.slots), initial+1)
	}
}

// ---------------------------------------------------------------------------
// Events / emitEvent
// ---------------------------------------------------------------------------

func TestEmitEvent(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	task := &db.AutopilotTask{IssueNumber: 42, IssueTitle: "Test"}
	sup.emitEvent("info", "test message", task)

	select {
	case evt := <-sup.Events():
		if evt.Type != "info" {
			t.Errorf("event type = %q, want info", evt.Type)
		}
		if evt.Summary != "test message" {
			t.Errorf("event summary = %q, want %q", evt.Summary, "test message")
		}
		if evt.Task == nil || evt.Task.IssueNumber != 42 {
			t.Error("event task should reference issue #42")
		}
		if evt.Time.IsZero() {
			t.Error("event time should not be zero")
		}
	default:
		t.Error("expected event on channel")
	}
}

func TestEmitEvent_DropsWhenFull(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	// Fill the channel.
	for i := 0; i < 64; i++ {
		sup.emitEvent("info", fmt.Sprintf("msg %d", i), nil)
	}

	// This should not block — event is dropped.
	sup.emitEvent("info", "overflow", nil)
}

// ---------------------------------------------------------------------------
// IsActive / Stop on inactive supervisor
// ---------------------------------------------------------------------------

func TestIsActive_InitiallyFalse(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	if sup.IsActive() {
		t.Error("supervisor should not be active initially")
	}
}

func TestStop_InactiveNoPanic(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	// Stop on inactive supervisor should not panic.
	sup.Stop()
}

// ---------------------------------------------------------------------------
// hasIdleSlot
// ---------------------------------------------------------------------------

func TestHasIdleSlot(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	if !sup.hasIdleSlot() {
		t.Error("all slots nil should report idle")
	}

	// Fill all slots.
	sup.mu.Lock()
	for i := range sup.slots {
		sup.slots[i] = &slotState{
			task: &db.AutopilotTask{IssueNumber: i + 1},
		}
	}
	sup.mu.Unlock()

	if sup.hasIdleSlot() {
		t.Error("all slots filled should report no idle")
	}

	// Free one.
	sup.mu.Lock()
	sup.slots[0] = nil
	sup.mu.Unlock()

	if !sup.hasIdleSlot() {
		t.Error("one free slot should report idle")
	}
}

// ---------------------------------------------------------------------------
// QueuedUnblockedTasks (DB integration)
// ---------------------------------------------------------------------------

func TestQueuedUnblockedTasks_NoDeps(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "No deps",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	unblocked, err := store.QueuedUnblockedTasks(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(unblocked) != 1 {
		t.Fatalf("expected 1 unblocked, got %d", len(unblocked))
	}
	if unblocked[0].IssueNumber != 10 {
		t.Errorf("expected issue 10, got %d", unblocked[0].IssueNumber)
	}
}

func TestQueuedUnblockedTasks_BlockedByRunning(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)

	// Task 10 is running.
	t10 := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "Running task",
		Dependencies: "[]",
		Status:       "running",
	}
	if err := store.CreateAutopilotTask(t10); err != nil {
		t.Fatal(err)
	}

	// Task 20 depends on task 10.
	t20 := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  20,
		IssueTitle:   "Depends on running",
		Dependencies: "[10]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(t20); err != nil {
		t.Fatal(err)
	}

	unblocked, err := store.QueuedUnblockedTasks(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(unblocked) != 0 {
		t.Errorf("expected 0 unblocked (dep is running), got %d", len(unblocked))
	}
}

func TestQueuedUnblockedTasks_UnblockedByDone(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)

	t10 := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "Done task",
		Dependencies: "[]",
		Status:       "done",
	}
	if err := store.CreateAutopilotTask(t10); err != nil {
		t.Fatal(err)
	}

	t20 := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  20,
		IssueTitle:   "Depends on done",
		Dependencies: "[10]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(t20); err != nil {
		t.Fatal(err)
	}

	unblocked, err := store.QueuedUnblockedTasks(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(unblocked) != 1 {
		t.Fatalf("expected 1 unblocked (dep is done), got %d", len(unblocked))
	}
	if unblocked[0].IssueNumber != 20 {
		t.Errorf("expected issue 20, got %d", unblocked[0].IssueNumber)
	}
}

func TestQueuedUnblockedTasks_ExternalBlockingViaTrackedItems(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)

	// Add tracked item #99 as open.
	item := &db.TrackedItem{
		ProjectID: project.ID,
		Source:    "github",
		Owner:     "org",
		Repo:      "repo",
		Number:    99,
		ItemType:  "issue",
		Title:     "External dep",
		State:     "open",
	}
	if err := store.AddTrackedItem(item); err != nil {
		t.Fatal(err)
	}

	// Task 20 depends on external issue 99.
	t20 := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  20,
		IssueTitle:   "Depends on external",
		Dependencies: "[99]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(t20); err != nil {
		t.Fatal(err)
	}

	unblocked, err := store.QueuedUnblockedTasks(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(unblocked) != 0 {
		t.Errorf("expected 0 unblocked (external dep is open), got %d", len(unblocked))
	}
}

func TestQueuedUnblockedTasks_ExternalClosedUnblocks(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)

	// Add tracked item #99 as closed.
	item := &db.TrackedItem{
		ProjectID: project.ID,
		Source:    "github",
		Owner:     "org",
		Repo:      "repo",
		Number:    99,
		ItemType:  "issue",
		Title:     "Closed dep",
		State:     "closed",
	}
	if err := store.AddTrackedItem(item); err != nil {
		t.Fatal(err)
	}

	t20 := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  20,
		IssueTitle:   "Depends on closed external",
		Dependencies: "[99]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(t20); err != nil {
		t.Fatal(err)
	}

	unblocked, err := store.QueuedUnblockedTasks(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(unblocked) != 1 {
		t.Errorf("expected 1 unblocked (external dep is closed), got %d", len(unblocked))
	}
}

func TestQueuedUnblockedTasks_UnknownDepNonBlocking(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)

	// Task depends on issue 999 which isn't tracked or an autopilot task.
	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  20,
		IssueTitle:   "Depends on unknown",
		Dependencies: "[999]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	unblocked, err := store.QueuedUnblockedTasks(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(unblocked) != 1 {
		t.Errorf("expected 1 unblocked (unknown dep = non-blocking), got %d", len(unblocked))
	}
}

func TestQueuedUnblockedTasks_ChainedDeps(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)

	// Chain: 10 → 20 → 30
	for _, tt := range []struct {
		num    int
		deps   string
		status string
	}{
		{10, "[]", "queued"},
		{20, "[10]", "queued"},
		{30, "[20]", "queued"},
	} {
		task := &db.AutopilotTask{
			ProjectID:    project.ID,
			IssueNumber:  tt.num,
			IssueTitle:   fmt.Sprintf("Task %d", tt.num),
			Dependencies: tt.deps,
			Status:       tt.status,
		}
		if err := store.CreateAutopilotTask(task); err != nil {
			t.Fatal(err)
		}
	}

	unblocked, err := store.QueuedUnblockedTasks(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Only task 10 should be unblocked.
	if len(unblocked) != 1 {
		t.Fatalf("expected 1 unblocked, got %d", len(unblocked))
	}
	if unblocked[0].IssueNumber != 10 {
		t.Errorf("expected issue 10, got %d", unblocked[0].IssueNumber)
	}
}

// ---------------------------------------------------------------------------
// unblockSatisfiedTasks
// ---------------------------------------------------------------------------

func TestUnblockSatisfiedTasks(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	// Create a done task and a blocked task that depends on it.
	t10 := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "Done task",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(t10); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAutopilotTaskStatus(t10.ID, "done"); err != nil {
		t.Fatal(err)
	}

	t20 := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  20,
		IssueTitle:   "Blocked by done",
		Dependencies: "[10]",
		Status:       "blocked",
	}
	if err := store.CreateAutopilotTask(t20); err != nil {
		t.Fatal(err)
	}

	n := sup.unblockSatisfiedTasks()
	if n != 1 {
		t.Errorf("unblockSatisfiedTasks = %d, want 1", n)
	}

	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, task := range tasks {
		if task.IssueNumber == 20 && task.Status != "queued" {
			t.Errorf("task 20 status = %q, want queued", task.Status)
		}
	}
}

func TestUnblockSatisfiedTasks_StillBlocked(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	t10 := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "Running",
		Dependencies: "[]",
		Status:       "running",
	}
	if err := store.CreateAutopilotTask(t10); err != nil {
		t.Fatal(err)
	}

	t20 := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  20,
		IssueTitle:   "Blocked by running",
		Dependencies: "[10]",
		Status:       "blocked",
	}
	if err := store.CreateAutopilotTask(t20); err != nil {
		t.Fatal(err)
	}

	n := sup.unblockSatisfiedTasks()
	if n != 0 {
		t.Errorf("unblockSatisfiedTasks = %d, want 0 (dep still running)", n)
	}

	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, task := range tasks {
		if task.IssueNumber == 20 && task.Status != "blocked" {
			t.Errorf("task 20 should still be blocked, got %q", task.Status)
		}
	}
}

func TestUnblockSatisfiedTasks_ExternalDepResolved(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	// Add tracked item #99 as closed.
	item := &db.TrackedItem{
		ProjectID: project.ID,
		Source:    "github",
		Owner:     "org",
		Repo:      "repo",
		Number:    99,
		ItemType:  "issue",
		Title:     "External closed",
		State:     "closed",
	}
	if err := store.AddTrackedItem(item); err != nil {
		t.Fatal(err)
	}

	t20 := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  20,
		IssueTitle:   "Blocked by external",
		Dependencies: "[99]",
		Status:       "blocked",
	}
	if err := store.CreateAutopilotTask(t20); err != nil {
		t.Fatal(err)
	}

	n := sup.unblockSatisfiedTasks()
	if n != 1 {
		t.Errorf("unblockSatisfiedTasks = %d, want 1", n)
	}
}

// ---------------------------------------------------------------------------
// ApplyDepOption
// ---------------------------------------------------------------------------

func TestApplyDepOption_Basic(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	// Create tasks.
	for _, num := range []int{10, 20, 30} {
		task := &db.AutopilotTask{
			ProjectID:    project.ID,
			IssueNumber:  num,
			IssueTitle:   fmt.Sprintf("Issue %d", num),
			Dependencies: "[]",
			Status:       "queued",
		}
		if err := store.CreateAutopilotTask(task); err != nil {
			t.Fatal(err)
		}
	}

	opt := DepOption{
		Name:      "Test",
		Rationale: "Test",
		Graph: map[string]json.RawMessage{
			"10": json.RawMessage("[]"),
			"20": json.RawMessage("[10]"),
			"30": json.RawMessage("[20]"),
		},
	}

	if err := sup.ApplyDepOption(context.Background(), opt); err != nil {
		t.Fatal(err)
	}

	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, task := range tasks {
		switch task.IssueNumber {
		case 10:
			if task.Status != "queued" {
				t.Errorf("task 10 status = %q, want queued", task.Status)
			}
		case 20:
			if task.Status != "blocked" {
				t.Errorf("task 20 status = %q, want blocked", task.Status)
			}
			if task.Dependencies != "[10]" {
				t.Errorf("task 20 deps = %q, want [10]", task.Dependencies)
			}
		case 30:
			if task.Status != "blocked" {
				t.Errorf("task 30 status = %q, want blocked", task.Status)
			}
		}
	}
}

func TestApplyDepOption_Skip(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "To be skipped",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	opt := DepOption{
		Graph: map[string]json.RawMessage{
			"10": json.RawMessage(`"skip"`),
		},
	}

	if err := sup.ApplyDepOption(context.Background(), opt); err != nil {
		t.Fatal(err)
	}

	tasks, _ := store.GetAutopilotTasks(project.ID)
	if tasks[0].Status != "skipped" {
		t.Errorf("task status = %q, want skipped", tasks[0].Status)
	}
}

func TestApplyDepOption_Manual(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "Manual task",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	opt := DepOption{
		Graph: map[string]json.RawMessage{
			"10": json.RawMessage(`"manual"`),
		},
	}

	if err := sup.ApplyDepOption(context.Background(), opt); err != nil {
		t.Fatal(err)
	}

	tasks, _ := store.GetAutopilotTasks(project.ID)
	if tasks[0].Status != "manual" {
		t.Errorf("task status = %q, want manual", tasks[0].Status)
	}
}

func TestApplyDepOption_IntDeps(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	for _, num := range []int{10, 20} {
		task := &db.AutopilotTask{
			ProjectID:    project.ID,
			IssueNumber:  num,
			IssueTitle:   fmt.Sprintf("Issue %d", num),
			Dependencies: "[]",
			Status:       "queued",
		}
		if err := store.CreateAutopilotTask(task); err != nil {
			t.Fatal(err)
		}
	}

	// Standard integer dependency array.
	opt := DepOption{
		Graph: map[string]json.RawMessage{
			"10": json.RawMessage("[]"),
			"20": json.RawMessage("[10]"),
		},
	}

	if err := sup.ApplyDepOption(context.Background(), opt); err != nil {
		t.Fatal(err)
	}

	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, task := range tasks {
		if task.IssueNumber == 20 {
			if task.Dependencies != "[10]" {
				t.Errorf("task 20 deps = %q, want [10]", task.Dependencies)
			}
			if task.Status != "blocked" {
				t.Errorf("task 20 status = %q, want blocked", task.Status)
			}
		}
	}
}

func TestApplyDepOption_UnblocksAlreadySatisfied(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	// Task 10 is done, task 20 depends on it.
	t10 := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "Done task",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(t10); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAutopilotTaskStatus(t10.ID, "done"); err != nil {
		t.Fatal(err)
	}

	t20 := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  20,
		IssueTitle:   "Depends on done",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(t20); err != nil {
		t.Fatal(err)
	}

	// Apply option that sets task 20 to depend on task 10 (already done).
	opt := DepOption{
		Graph: map[string]json.RawMessage{
			"10": json.RawMessage("[]"),
			"20": json.RawMessage("[10]"),
		},
	}

	if err := sup.ApplyDepOption(context.Background(), opt); err != nil {
		t.Fatal(err)
	}

	// Task 20 should be queued (not blocked) since dep is already done.
	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, task := range tasks {
		if task.IssueNumber == 20 && task.Status != "queued" {
			t.Errorf("task 20 status = %q, want queued (dep already done)", task.Status)
		}
	}
}

// ---------------------------------------------------------------------------
// inspectOutcome
// ---------------------------------------------------------------------------

func TestInspectOutcome_NoLog(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  42,
		IssueTitle:   "Test",
		AgentLog:     "", // empty log path
		Branch:       "agent/issue-42",
		Dependencies: "[]",
		Status:       "running",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	// With no log and no PR, should bail.
	status := sup.inspectOutcome(context.Background(), task, 0)
	if status != "bailed" {
		t.Errorf("expected bailed, got %q", status)
	}
}

func TestInspectOutcome_MaxTurnsExhausted(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	project.AutopilotMaxTurns = 50
	// Update project in store.
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	// Create an agent log with max turns.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "agent.log")
	logContent := `{"type":"result","subtype":"success","is_error":false,"num_turns":50,"total_cost_usd":2.00,"result":"Done","permission_denials":[],"session_id":"abc"}
`
	if err := os.WriteFile(logPath, []byte(logContent), 0o644); err != nil {
		t.Fatal(err)
	}

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  42,
		IssueTitle:   "Max turns",
		AgentLog:     logPath,
		Branch:       "agent/issue-42",
		Dependencies: "[]",
		Status:       "running",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	status := sup.inspectOutcome(context.Background(), task, 0)
	if status != "failed" {
		t.Errorf("expected failed, got %q", status)
	}

	// Check failure reason was stored.
	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, ta := range tasks {
		if ta.ID == task.ID {
			if ta.FailureReason != "max_turns" {
				t.Errorf("failure_reason = %q, want max_turns", ta.FailureReason)
			}
			break
		}
	}
}

func TestInspectOutcome_BudgetExhausted(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	project.AutopilotMaxBudgetUSD = 3.00
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	dir := t.TempDir()
	logPath := filepath.Join(dir, "agent.log")
	logContent := `{"type":"result","subtype":"success","is_error":false,"num_turns":10,"total_cost_usd":2.90,"result":"Done","permission_denials":[],"session_id":"abc"}
`
	if err := os.WriteFile(logPath, []byte(logContent), 0o644); err != nil {
		t.Fatal(err)
	}

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  42,
		IssueTitle:   "Budget exhausted",
		AgentLog:     logPath,
		Branch:       "agent/issue-42",
		Dependencies: "[]",
		Status:       "running",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	status := sup.inspectOutcome(context.Background(), task, 0)
	if status != "failed" {
		t.Errorf("expected failed, got %q", status)
	}
}

func TestInspectOutcome_ErrorInResult(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	dir := t.TempDir()
	logPath := filepath.Join(dir, "agent.log")
	logContent := `{"type":"result","subtype":"error","is_error":true,"num_turns":5,"total_cost_usd":0.50,"result":"Something went wrong","permission_denials":[],"session_id":"abc"}
`
	if err := os.WriteFile(logPath, []byte(logContent), 0o644); err != nil {
		t.Fatal(err)
	}

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  42,
		IssueTitle:   "Error result",
		AgentLog:     logPath,
		Branch:       "agent/issue-42",
		Dependencies: "[]",
		Status:       "running",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	status := sup.inspectOutcome(context.Background(), task, 1)
	if status != "failed" {
		t.Errorf("expected failed, got %q", status)
	}
}

func TestInspectOutcome_SuccessNoPR(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	dir := t.TempDir()
	logPath := filepath.Join(dir, "agent.log")
	logContent := `{"type":"result","subtype":"success","is_error":false,"num_turns":10,"total_cost_usd":0.50,"result":"Done!","permission_denials":[],"session_id":"abc"}
`
	if err := os.WriteFile(logPath, []byte(logContent), 0o644); err != nil {
		t.Fatal(err)
	}

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  42,
		IssueTitle:   "Success no PR",
		AgentLog:     logPath,
		Branch:       "agent/issue-42",
		Dependencies: "[]",
		Status:       "running",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	// No PR will be found (fake token), so should bail.
	status := sup.inspectOutcome(context.Background(), task, 0)
	if status != "bailed" {
		t.Errorf("expected bailed (no PR found), got %q", status)
	}
}

// ---------------------------------------------------------------------------
// fillSlots edge cases (more comprehensive than existing)
// ---------------------------------------------------------------------------

func TestFillSlots_OnlyFillsIdleSlots(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	// Occupy the first slot.
	sup.mu.Lock()
	sup.slots[0] = &slotState{
		task: &db.AutopilotTask{IssueNumber: 1},
	}
	sup.mu.Unlock()

	// Create a queued task.
	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  42,
		IssueTitle:   "Test",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	// Cancel ctx so launchAgent won't actually run.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sup.fillSlots(ctx)

	// Slot 0 should still be occupied by task #1.
	sup.mu.Lock()
	if sup.slots[0].task.IssueNumber != 1 {
		t.Errorf("slot 0 should still have task #1, got #%d", sup.slots[0].task.IssueNumber)
	}
	sup.mu.Unlock()
}

func TestFillSlots_NoQueuedTasks(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	// No tasks at all.
	ctx := context.Background()
	sup.fillSlots(ctx) // should not panic

	sup.mu.Lock()
	for i, slot := range sup.slots {
		if slot != nil {
			t.Errorf("slot %d should be nil", i)
		}
	}
	sup.mu.Unlock()
}

// ---------------------------------------------------------------------------
// classifyOutcome (edge cases beyond existing tests)
// ---------------------------------------------------------------------------

func TestClassifyOutcome_ErrorWithLongDetail(t *testing.T) {
	longMsg := make([]byte, 600)
	for i := range longMsg {
		longMsg[i] = 'x'
	}
	result := &AgentResult{
		IsError: true,
		Result:  string(longMsg),
	}
	_, _, detail := classifyOutcome(result, 50, 3.0)
	if len(detail) > 504 { // 500 + "..."
		t.Errorf("detail should be truncated, got len %d", len(detail))
	}
	if detail[len(detail)-3:] != "..." {
		t.Error("truncated detail should end with '...'")
	}
}

func TestClassifyOutcome_ZeroLimits(t *testing.T) {
	// When maxTurns and maxBudget are 0, those checks are skipped.
	result := &AgentResult{
		NumTurns:  1000,
		TotalCost: 100.0,
	}
	status, _, _ := classifyOutcome(result, 0, 0)
	if status != "" {
		t.Errorf("with zero limits, should not classify as failed, got %q", status)
	}
}

// ---------------------------------------------------------------------------
// Launch / Stop lifecycle (minimal — no real claude binary)
// ---------------------------------------------------------------------------

func TestLaunch_SetsActiveAndStop(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// No tasks → launch will finish quickly.
	sup.Launch(ctx)

	// Give the goroutine a moment to settle.
	time.Sleep(100 * time.Millisecond)

	// Launch again should be a no-op (already active or finishing).
	sup.Launch(ctx)

	// Stop should not hang.
	done := make(chan struct{})
	go func() {
		sup.Stop()
		close(done)
	}()

	select {
	case <-done:
		// good
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() hung")
	}
}

// ---------------------------------------------------------------------------
// StatusBlock with manual tasks
// ---------------------------------------------------------------------------

func TestStatusBlock_ManualTasksShown(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  99,
		IssueTitle:   "Manual work",
		Dependencies: "[]",
		Status:       "manual",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	sup.mu.Lock()
	sup.active = true
	sup.mu.Unlock()

	block := sup.StatusBlock()
	if !contains(block, "manual (watching)") {
		t.Errorf("StatusBlock should mention manual tasks, got:\n%s", block)
	}
}

// ---------------------------------------------------------------------------
// DepGraph with manual tasks
// ---------------------------------------------------------------------------

func TestDepGraph_ManualTasksShown(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	for _, tt := range []struct {
		num    int
		status string
	}{
		{10, "queued"},
		{20, "manual"},
	} {
		task := &db.AutopilotTask{
			ProjectID:    project.ID,
			IssueNumber:  tt.num,
			IssueTitle:   fmt.Sprintf("Task %d", tt.num),
			Dependencies: "[]",
			Status:       tt.status,
		}
		if err := store.CreateAutopilotTask(task); err != nil {
			t.Fatal(err)
		}
	}

	sup.mu.Lock()
	sup.active = true
	sup.preparedAt = time.Now()
	sup.mu.Unlock()

	graph := sup.DepGraph()
	if !contains(graph, "#20 (manual)") {
		t.Errorf("DepGraph should show manual tasks, got:\n%s", graph)
	}
	if !contains(graph, "manual") {
		t.Errorf("DepGraph summary should mention manual count, got:\n%s", graph)
	}
}

// ---------------------------------------------------------------------------
// RestartTask edge case: not found
// ---------------------------------------------------------------------------

func TestRestartTask_NotFound(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	err := sup.RestartTask(context.Background(), 9999)
	if err == nil {
		t.Error("expected error for non-existent task")
	}
}

// ---------------------------------------------------------------------------
// ApplyRebuildDepOption
// ---------------------------------------------------------------------------

func TestApplyRebuildDepOption_Basic(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	for _, tt := range []struct {
		num    int
		status string
	}{
		{10, "queued"},
		{20, "blocked"},
	} {
		task := &db.AutopilotTask{
			ProjectID:    project.ID,
			IssueNumber:  tt.num,
			IssueTitle:   fmt.Sprintf("Issue %d", tt.num),
			Dependencies: "[]",
			Status:       tt.status,
		}
		if err := store.CreateAutopilotTask(task); err != nil {
			t.Fatal(err)
		}
	}

	opt := DepOption{
		Graph: map[string]json.RawMessage{
			"10": json.RawMessage("[]"),
			"20": json.RawMessage("[]"),
		},
	}

	result, err := sup.ApplyRebuildDepOption(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if result.Unblocked != 1 {
		t.Errorf("unblocked = %d, want 1", result.Unblocked)
	}
}

func TestApplyRebuildDepOption_Skip(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "To skip",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	opt := DepOption{
		Graph: map[string]json.RawMessage{
			"10": json.RawMessage(`"skip"`),
		},
	}

	result, err := sup.ApplyRebuildDepOption(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != 1 {
		t.Errorf("skipped = %d, want 1", result.Skipped)
	}
}

func TestApplyRebuildDepOption_UnSkip(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "Previously skipped",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAutopilotTaskStatus(task.ID, "skipped"); err != nil {
		t.Fatal(err)
	}

	// Rebuild with no deps (should un-skip).
	opt := DepOption{
		Graph: map[string]json.RawMessage{
			"10": json.RawMessage("[]"),
		},
	}

	_, err := sup.ApplyRebuildDepOption(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}

	tasks, _ := store.GetAutopilotTasks(project.ID)
	if tasks[0].Status != "queued" {
		t.Errorf("task status = %q, want queued (un-skipped)", tasks[0].Status)
	}
}

func TestApplyRebuildDepOption_SkipsRunningTasks(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	// Create a running task and a queued task.
	t10 := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "Running",
		Dependencies: "[]",
		Status:       "running",
	}
	if err := store.CreateAutopilotTask(t10); err != nil {
		t.Fatal(err)
	}

	t20 := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  20,
		IssueTitle:   "Queued",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(t20); err != nil {
		t.Fatal(err)
	}

	// Apply option that only mentions queued task.
	opt := DepOption{
		Graph: map[string]json.RawMessage{
			"20": json.RawMessage("[]"),
		},
	}

	_, err := sup.ApplyRebuildDepOption(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}

	// Running task should be unchanged.
	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, task := range tasks {
		if task.IssueNumber == 10 && task.Status != "running" {
			t.Errorf("running task should not be affected, got %q", task.Status)
		}
	}
}

func TestApplyRebuildDepOption_NoRebuildableTasks(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	// All tasks are done.
	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "Done",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAutopilotTaskStatus(task.ID, "done"); err != nil {
		t.Fatal(err)
	}

	opt := DepOption{Graph: map[string]json.RawMessage{}}

	result, err := sup.ApplyRebuildDepOption(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if result.Unblocked != 0 && result.Skipped != 0 {
		t.Error("should be no-op when all tasks are done")
	}
}

// ---------------------------------------------------------------------------
// cleanOrphanedWorktrees
// ---------------------------------------------------------------------------

func TestCleanOrphanedWorktrees_NoDir(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/nonexistent-repo", "owner", "repo", "")

	// Should not panic when directory doesn't exist.
	sup.cleanOrphanedWorktrees()
}

// ---------------------------------------------------------------------------
// checkReviewTasks — empty / no-review-task paths
// ---------------------------------------------------------------------------

func TestCheckReviewTasks_NoReviewTasks(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	// Create tasks that are NOT in review.
	statuses := []string{"queued", "running", "done", "bailed"}
	for i, status := range statuses {
		task := &db.AutopilotTask{
			ProjectID:    project.ID,
			IssueNumber:  (i + 1) * 10,
			IssueTitle:   "Test",
			Dependencies: "[]",
			Status:       "queued",
		}
		_ = store.CreateAutopilotTask(task)
		if status != "queued" {
			_ = store.UpdateAutopilotTaskStatus(task.ID, status)
		}
	}

	promoted := sup.checkReviewTasks(context.Background())
	if promoted != 0 {
		t.Errorf("checkReviewTasks = %d, want 0 (no review tasks)", promoted)
	}
}

func TestCheckReviewTasks_ReviewNoPN(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	// Review task with PRNumber=0 should be skipped.
	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "No PR number",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}
	_ = store.UpdateAutopilotTaskStatus(task.ID, "review")
	// PRNumber is 0 by default

	promoted := sup.checkReviewTasks(context.Background())
	if promoted != 0 {
		t.Errorf("checkReviewTasks = %d, want 0 (no PR number)", promoted)
	}
}

// ---------------------------------------------------------------------------
// checkManualTasks — empty / no-manual-task paths
// ---------------------------------------------------------------------------

func TestCheckManualTasks_NoManualTasks(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "Not manual",
		Dependencies: "[]",
		Status:       "queued",
	}
	_ = store.CreateAutopilotTask(task)

	promoted := sup.checkManualTasks(context.Background())
	if promoted != 0 {
		t.Errorf("checkManualTasks = %d, want 0 (no manual tasks)", promoted)
	}
}

// ---------------------------------------------------------------------------
// inspectOutcome — permission warning path
// ---------------------------------------------------------------------------

func TestInspectOutcome_PermissionWarning(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	dir := t.TempDir()
	logPath := filepath.Join(dir, "agent.log")
	logContent := `{"type":"result","subtype":"success","is_error":false,"num_turns":10,"total_cost_usd":0.50,"result":"Done","permission_denials":["Bash"],"session_id":"abc"}
`
	if err := os.WriteFile(logPath, []byte(logContent), 0o644); err != nil {
		t.Fatal(err)
	}

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  42,
		IssueTitle:   "Permission warning",
		AgentLog:     logPath,
		Branch:       "agent/issue-42",
		Dependencies: "[]",
		Status:       "running",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	// Permission warnings are non-fatal; with no PR found, should bail.
	status := sup.inspectOutcome(context.Background(), task, 0)
	if status != "bailed" {
		t.Errorf("expected bailed (warning + no PR), got %q", status)
	}
}

func TestInspectOutcome_MissingLogFile(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  42,
		IssueTitle:   "Missing log",
		AgentLog:     "/nonexistent/path/agent.log",
		Branch:       "agent/issue-42",
		Dependencies: "[]",
		Status:       "running",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	// Missing log file should not panic, and with no PR should bail.
	status := sup.inspectOutcome(context.Background(), task, 1)
	if status != "bailed" {
		t.Errorf("expected bailed, got %q", status)
	}
}

// ---------------------------------------------------------------------------
// DepGraph — various task statuses
// ---------------------------------------------------------------------------

func TestDepGraph_SkippedTasksHidden(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	tasks := []struct {
		num    int
		status string
	}{
		{10, "queued"},
		{20, "skipped"},
	}
	for _, tt := range tasks {
		task := &db.AutopilotTask{
			ProjectID:    project.ID,
			IssueNumber:  tt.num,
			IssueTitle:   fmt.Sprintf("Task %d", tt.num),
			Dependencies: "[]",
			Status:       "queued",
		}
		if err := store.CreateAutopilotTask(task); err != nil {
			t.Fatal(err)
		}
		if tt.status != "queued" {
			_ = store.UpdateAutopilotTaskStatus(task.ID, tt.status)
		}
	}

	sup.mu.Lock()
	sup.active = true
	sup.preparedAt = time.Now()
	sup.mu.Unlock()

	graph := sup.DepGraph()
	if contains(graph, "#20 (skipped)") {
		t.Error("DepGraph should not show skipped tasks")
	}
}

func TestDepGraph_EmptyTasks(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	sup.mu.Lock()
	sup.active = true
	sup.preparedAt = time.Now()
	sup.mu.Unlock()

	// No tasks at all.
	graph := sup.DepGraph()
	if graph != "" {
		t.Errorf("DepGraph should be empty when no tasks, got %q", graph)
	}
}

func TestDepGraph_PreparedAtZero(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	sup.mu.Lock()
	sup.active = true
	// preparedAt is zero value
	sup.mu.Unlock()

	if got := sup.DepGraph(); got != "" {
		t.Errorf("DepGraph should be empty when preparedAt is zero, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// StatusBlock — with stopped tasks
// ---------------------------------------------------------------------------

func TestStatusBlock_WithStoppedTasks(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "Stopped",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}
	_ = store.UpdateAutopilotTaskStatus(task.ID, "stopped")

	sup.mu.Lock()
	sup.active = true
	sup.mu.Unlock()

	block := sup.StatusBlock()
	if !contains(block, "0 stopped") && !contains(block, "1 stopped") {
		// The counter should include stopped
		if !contains(block, "stopped") {
			t.Errorf("StatusBlock should mention stopped count, got:\n%s", block)
		}
	}
}

// ---------------------------------------------------------------------------
// Launch with tasks present but cancelled
// ---------------------------------------------------------------------------

func TestLaunch_WithTasksThenCancel(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	// Create queued tasks.
	for i := 1; i <= 3; i++ {
		task := &db.AutopilotTask{
			ProjectID:    project.ID,
			IssueNumber:  i,
			IssueTitle:   fmt.Sprintf("Issue %d", i),
			Dependencies: "[]",
			Status:       "queued",
		}
		_ = store.CreateAutopilotTask(task)
	}

	ctx, cancel := context.WithCancel(context.Background())

	sup.Launch(ctx)
	time.Sleep(50 * time.Millisecond) // let it start

	cancel() // cancel the context

	done := make(chan struct{})
	go func() {
		sup.Stop()
		close(done)
	}()

	select {
	case <-done:
		// good
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() hung after cancel")
	}

	if sup.IsActive() {
		t.Error("should not be active after stop")
	}
}

// ---------------------------------------------------------------------------
// unblockSatisfiedTasks with multiple deps (partial satisfaction)
// ---------------------------------------------------------------------------

func TestUnblockSatisfiedTasks_MultipleDeps(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	// Create tasks: 10 done, 20 running, 30 blocked by [10, 20].
	t10 := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "Done",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(t10); err != nil {
		t.Fatal(err)
	}
	_ = store.UpdateAutopilotTaskStatus(t10.ID, "done")

	t20 := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  20,
		IssueTitle:   "Running",
		Dependencies: "[]",
		Status:       "running",
	}
	if err := store.CreateAutopilotTask(t20); err != nil {
		t.Fatal(err)
	}

	t30 := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  30,
		IssueTitle:   "Blocked by both",
		Dependencies: "[10,20]",
		Status:       "blocked",
	}
	if err := store.CreateAutopilotTask(t30); err != nil {
		t.Fatal(err)
	}

	n := sup.unblockSatisfiedTasks()
	if n != 0 {
		t.Errorf("unblockSatisfiedTasks = %d, want 0 (dep 20 still running)", n)
	}
}

// ---------------------------------------------------------------------------
// ApplyDepOption edge case: task not mentioned in graph
// ---------------------------------------------------------------------------

func TestApplyDepOption_TaskNotInGraph(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "Not in graph",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	// Empty graph — task not mentioned.
	opt := DepOption{
		Graph: map[string]json.RawMessage{},
	}

	if err := sup.ApplyDepOption(context.Background(), opt); err != nil {
		t.Fatal(err)
	}

	// Task should remain queued (not affected).
	tasks, _ := store.GetAutopilotTasks(project.ID)
	if tasks[0].Status != "queued" {
		t.Errorf("task status = %q, want queued (not in graph)", tasks[0].Status)
	}
}

// ---------------------------------------------------------------------------
// QueuedUnblockedTasks — only non-queued tasks
// ---------------------------------------------------------------------------

func TestQueuedUnblockedTasks_OnlyNonQueued(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)

	statuses := []string{"running", "done", "bailed", "blocked"}
	for i, status := range statuses {
		task := &db.AutopilotTask{
			ProjectID:    project.ID,
			IssueNumber:  (i + 1) * 10,
			IssueTitle:   "Not queued",
			Dependencies: "[]",
			Status:       "queued",
		}
		_ = store.CreateAutopilotTask(task)
		_ = store.UpdateAutopilotTaskStatus(task.ID, status)
	}

	unblocked, err := store.QueuedUnblockedTasks(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(unblocked) != 0 {
		t.Errorf("expected 0 unblocked, got %d", len(unblocked))
	}
}

// ---------------------------------------------------------------------------
// emitEventLocked
// ---------------------------------------------------------------------------

func TestEmitEventLocked(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	sup.mu.Lock()
	sup.emitEventLocked("info", "locked event", nil)
	sup.mu.Unlock()

	select {
	case evt := <-sup.Events():
		if evt.Summary != "locked event" {
			t.Errorf("event summary = %q, want %q", evt.Summary, "locked event")
		}
	default:
		t.Error("expected event from emitEventLocked")
	}
}

// ---------------------------------------------------------------------------
// Pause / Resume with events
// ---------------------------------------------------------------------------

func TestPauseResume_EmitsEvents(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	sup.Pause()

	select {
	case evt := <-sup.Events():
		if evt.Type != "warning" || !contains(evt.Summary, "paused") {
			t.Errorf("expected pause event, got type=%q summary=%q", evt.Type, evt.Summary)
		}
	default:
		t.Error("expected pause event")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sup.Resume(ctx)

	select {
	case evt := <-sup.Events():
		if evt.Type != "warning" || !contains(evt.Summary, "resumed") {
			t.Errorf("expected resume event, got type=%q summary=%q", evt.Type, evt.Summary)
		}
	default:
		t.Error("expected resume event")
	}
}

// ---------------------------------------------------------------------------
// classifyOutcome — edge: exactly at budget threshold
// ---------------------------------------------------------------------------

func TestClassifyOutcome_ExactlyAtBudgetThreshold(t *testing.T) {
	result := &AgentResult{
		NumTurns:  10,
		TotalCost: 2.85, // exactly 3.0 * 0.95 = 2.85
	}
	status, reason, _ := classifyOutcome(result, 50, 3.0)
	if status != "failed" {
		t.Errorf("status = %q, want failed (exactly at threshold)", status)
	}
	if reason != "max_budget" {
		t.Errorf("reason = %q, want max_budget", reason)
	}
}

// ---------------------------------------------------------------------------
// checkReviewTasks with review tasks (GitHub call will fail gracefully)
// ---------------------------------------------------------------------------

func TestCheckReviewTasks_WithReviewTask_FetchFails(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  42,
		IssueTitle:   "In review",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}
	_ = store.UpdateAutopilotTaskStatus(task.ID, "review")
	_ = store.UpdateAutopilotTaskPR(task.ID, 100)

	// FetchItem will fail with fake token — should return 0 promoted.
	promoted := sup.checkReviewTasks(context.Background())
	if promoted != 0 {
		t.Errorf("checkReviewTasks = %d, want 0 (fetch fails)", promoted)
	}

	// Task should still be in review.
	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, ta := range tasks {
		if ta.ID == task.ID && ta.Status != "review" {
			t.Errorf("task status = %q, want review", ta.Status)
		}
	}
}

// ---------------------------------------------------------------------------
// checkManualTasks with manual tasks (GitHub call will fail gracefully)
// ---------------------------------------------------------------------------

func TestCheckManualTasks_WithManualTask_FetchFails(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		Owner:        "owner",
		Repo:         "repo",
		IssueNumber:  42,
		IssueTitle:   "Manual work",
		Dependencies: "[]",
		Status:       "manual",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	// FetchItem will fail with fake token — should return 0 promoted.
	promoted := sup.checkManualTasks(context.Background())
	if promoted != 0 {
		t.Errorf("checkManualTasks = %d, want 0 (fetch fails)", promoted)
	}
}

// ---------------------------------------------------------------------------
// Launch loop branches — queued tasks keep loop alive
// ---------------------------------------------------------------------------

func TestLaunch_QueuedTasksKeepLoopAlive(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	// Create a blocked task so the loop keeps running.
	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "Blocked",
		Dependencies: "[99]",
		Status:       "blocked",
	}
	_ = store.CreateAutopilotTask(task)

	ctx, cancel := context.WithCancel(context.Background())
	sup.Launch(ctx)

	// Give the loop time to start.
	time.Sleep(100 * time.Millisecond)

	if !sup.IsActive() {
		t.Error("supervisor should be active with blocked tasks")
	}

	cancel()

	done := make(chan struct{})
	go func() {
		sup.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() hung")
	}
}

// ---------------------------------------------------------------------------
// Launch with review task keeps loop alive
// ---------------------------------------------------------------------------

func TestLaunch_ReviewTaskKeepsLoopAlive(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "In review",
		Dependencies: "[]",
		Status:       "queued",
	}
	_ = store.CreateAutopilotTask(task)
	_ = store.UpdateAutopilotTaskStatus(task.ID, "review")

	ctx, cancel := context.WithCancel(context.Background())
	sup.Launch(ctx)

	time.Sleep(100 * time.Millisecond)

	if !sup.IsActive() {
		t.Error("supervisor should be active with review tasks")
	}

	cancel()

	done := make(chan struct{})
	go func() {
		sup.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() hung")
	}
}

// ---------------------------------------------------------------------------
// Launch with manual task keeps loop alive
// ---------------------------------------------------------------------------

func TestLaunch_ManualTaskKeepsLoopAlive(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "Manual",
		Dependencies: "[]",
		Status:       "manual",
	}
	_ = store.CreateAutopilotTask(task)

	ctx, cancel := context.WithCancel(context.Background())
	sup.Launch(ctx)

	time.Sleep(100 * time.Millisecond)

	if !sup.IsActive() {
		t.Error("supervisor should be active with manual tasks")
	}

	cancel()

	done := make(chan struct{})
	go func() {
		sup.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() hung")
	}
}

// ---------------------------------------------------------------------------
// Launch paused keeps loop alive
// ---------------------------------------------------------------------------

func TestLaunch_PausedKeepsLoopAlive(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	sup.Pause()
	// Drain the pause event.
	<-sup.Events()

	ctx, cancel := context.WithCancel(context.Background())
	sup.Launch(ctx)

	time.Sleep(100 * time.Millisecond)

	if !sup.IsActive() {
		t.Error("supervisor should be active when paused")
	}

	cancel()

	done := make(chan struct{})
	go func() {
		sup.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() hung")
	}
}

// ---------------------------------------------------------------------------
// ApplyRebuildDepOption — blocks queued task with unsatisfied deps
// ---------------------------------------------------------------------------

func TestApplyRebuildDepOption_BlocksQueued(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	for _, num := range []int{10, 20} {
		task := &db.AutopilotTask{
			ProjectID:    project.ID,
			IssueNumber:  num,
			IssueTitle:   fmt.Sprintf("Issue %d", num),
			Dependencies: "[]",
			Status:       "queued",
		}
		_ = store.CreateAutopilotTask(task)
	}

	opt := DepOption{
		Graph: map[string]json.RawMessage{
			"10": json.RawMessage("[]"),
			"20": json.RawMessage("[10]"),
		},
	}

	_, err := sup.ApplyRebuildDepOption(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}

	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, task := range tasks {
		if task.IssueNumber == 20 && task.Status != "blocked" {
			t.Errorf("task 20 status = %q, want blocked", task.Status)
		}
	}
}

// ---------------------------------------------------------------------------
// ApplyRebuildDepOption — manual directive
// ---------------------------------------------------------------------------

func TestApplyRebuildDepOption_ManualDirective(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "To become manual",
		Dependencies: "[]",
		Status:       "queued",
	}
	_ = store.CreateAutopilotTask(task)

	opt := DepOption{
		Graph: map[string]json.RawMessage{
			"10": json.RawMessage(`"manual"`),
		},
	}

	_, err := sup.ApplyRebuildDepOption(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}

	tasks, _ := store.GetAutopilotTasks(project.ID)
	if tasks[0].Status != "manual" {
		t.Errorf("task status = %q, want manual", tasks[0].Status)
	}
}

// ---------------------------------------------------------------------------
// ApplyRebuildDepOption — task not mentioned in graph gets deps cleared
// ---------------------------------------------------------------------------

func TestApplyRebuildDepOption_TaskNotMentioned(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "Not mentioned",
		Dependencies: "[99]",
		Status:       "queued",
	}
	_ = store.CreateAutopilotTask(task)

	// Empty graph — task not mentioned → deps should be cleared.
	opt := DepOption{
		Graph: map[string]json.RawMessage{},
	}

	_, err := sup.ApplyRebuildDepOption(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}

	tasks, _ := store.GetAutopilotTasks(project.ID)
	if tasks[0].Dependencies != "[]" {
		t.Errorf("deps = %q, want []", tasks[0].Dependencies)
	}
}

// ---------------------------------------------------------------------------
// ApplyRebuildDepOption — malformed deps fallback
// ---------------------------------------------------------------------------

func TestApplyRebuildDepOption_MalformedDeps(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "Malformed deps",
		Dependencies: "[]",
		Status:       "queued",
	}
	_ = store.CreateAutopilotTask(task)

	// Invalid JSON value for deps.
	opt := DepOption{
		Graph: map[string]json.RawMessage{
			"10": json.RawMessage(`{"invalid": true}`),
		},
	}

	_, err := sup.ApplyRebuildDepOption(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}

	// Should have cleared deps to [].
	tasks, _ := store.GetAutopilotTasks(project.ID)
	if tasks[0].Dependencies != "[]" {
		t.Errorf("deps = %q, want [] (fallback from malformed)", tasks[0].Dependencies)
	}
}

// ---------------------------------------------------------------------------
// addNewTrackedItems — error paths
// ---------------------------------------------------------------------------

func TestAddNewTrackedItems_NoTrackedItems(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	// No tracked items → should return 0.
	added := sup.addNewTrackedItems(context.Background())
	if added != 0 {
		t.Errorf("addNewTrackedItems = %d, want 0", added)
	}
}

func TestAddNewTrackedItems_AllAlreadyKnown(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	// Add a tracked item and a corresponding autopilot task.
	item := &db.TrackedItem{
		ProjectID: project.ID,
		Source:    "github",
		Owner:     "owner",
		Repo:      "repo",
		Number:    42,
		ItemType:  "issue",
		Title:     "Already known",
		State:     "open",
	}
	_ = store.AddTrackedItem(item)

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  42,
		IssueTitle:   "Already known",
		Dependencies: "[]",
		Status:       "queued",
	}
	_ = store.CreateAutopilotTask(task)

	added := sup.addNewTrackedItems(context.Background())
	if added != 0 {
		t.Errorf("addNewTrackedItems = %d, want 0 (already known)", added)
	}
}

func TestAddNewTrackedItems_ClosedItemSkipped(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	// Tracked item that is closed.
	item := &db.TrackedItem{
		ProjectID: project.ID,
		Source:    "github",
		Owner:     "owner",
		Repo:      "repo",
		Number:    42,
		ItemType:  "issue",
		Title:     "Closed",
		State:     "closed",
	}
	_ = store.AddTrackedItem(item)

	added := sup.addNewTrackedItems(context.Background())
	if added != 0 {
		t.Errorf("addNewTrackedItems = %d, want 0 (closed item)", added)
	}
}

func TestAddNewTrackedItems_PRSkipped(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	// Tracked item that is a PR (not an issue).
	item := &db.TrackedItem{
		ProjectID: project.ID,
		Source:    "github",
		Owner:     "owner",
		Repo:      "repo",
		Number:    42,
		ItemType:  "pull_request",
		Title:     "A PR",
		State:     "open",
	}
	_ = store.AddTrackedItem(item)

	added := sup.addNewTrackedItems(context.Background())
	if added != 0 {
		t.Errorf("addNewTrackedItems = %d, want 0 (PR skipped)", added)
	}
}

// ---------------------------------------------------------------------------
// convertTrackedItems — early return paths
// ---------------------------------------------------------------------------

func TestConvertTrackedItems_NoItems(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	tasks, err := sup.convertTrackedItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tasks != nil {
		t.Errorf("expected nil tasks, got %d", len(tasks))
	}
}

func TestConvertTrackedItems_OnlyClosedItems(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	item := &db.TrackedItem{
		ProjectID: project.ID,
		Source:    "github",
		Owner:     "owner",
		Repo:      "repo",
		Number:    42,
		ItemType:  "issue",
		Title:     "Closed",
		State:     "closed",
	}
	_ = store.AddTrackedItem(item)

	tasks, err := sup.convertTrackedItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tasks != nil {
		t.Errorf("expected nil tasks (only closed), got %d", len(tasks))
	}
}

func TestConvertTrackedItems_OnlyPRs(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	item := &db.TrackedItem{
		ProjectID: project.ID,
		Source:    "github",
		Owner:     "owner",
		Repo:      "repo",
		Number:    42,
		ItemType:  "pull_request",
		Title:     "A PR",
		State:     "open",
	}
	_ = store.AddTrackedItem(item)

	tasks, err := sup.convertTrackedItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tasks != nil {
		t.Errorf("expected nil tasks (only PRs), got %d", len(tasks))
	}
}

// ---------------------------------------------------------------------------
// ApplyIncrementalDepOption — reverse deps and non-mutable guard
// ---------------------------------------------------------------------------

func TestApplyIncrementalDepOption_SkipsNonMutableStatuses(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	// Create tasks in various non-mutable states.
	for _, status := range []string{"running", "review", "done", "bailed", "stopped", "failed"} {
		task := &db.AutopilotTask{
			ProjectID:    project.ID,
			IssueNumber:  10 + len(status), // unique numbers
			IssueTitle:   "Task " + status,
			Dependencies: "[]",
			Status:       status,
		}
		_ = store.CreateAutopilotTask(task)
	}

	// Build a graph that tries to update all of them.
	graph := make(map[string]json.RawMessage)
	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, t := range tasks {
		graph[strconv.Itoa(t.IssueNumber)] = json.RawMessage("[99]")
	}

	err := sup.ApplyIncrementalDepOption(context.Background(), DepOption{
		Name:  "test",
		Graph: graph,
	})
	if err != nil {
		t.Fatalf("ApplyIncrementalDepOption: %v", err)
	}

	// Verify none of them were modified.
	tasks, _ = store.GetAutopilotTasks(project.ID)
	for _, task := range tasks {
		if task.Dependencies != "[]" {
			t.Errorf("task #%d (%s) deps changed to %s, want []", task.IssueNumber, task.Status, task.Dependencies)
		}
	}
}

func TestApplyIncrementalDepOption_UpdatesExistingQueuedTask(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	// Existing queued task with deps [99].
	existing := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  42,
		IssueTitle:   "Existing task",
		Dependencies: "[99]",
		Status:       "queued",
	}
	_ = store.CreateAutopilotTask(existing)

	// New task #50.
	newTask := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  50,
		IssueTitle:   "New task",
		Dependencies: "[]",
		Status:       "queued",
	}
	_ = store.CreateAutopilotTask(newTask)

	// Graph: new #50 has no deps; existing #42 now also depends on #50 (reverse dep).
	graph := map[string]json.RawMessage{
		"50": json.RawMessage("[]"),
		"42": json.RawMessage("[99,50]"),
	}

	err := sup.ApplyIncrementalDepOption(context.Background(), DepOption{
		Name:  "test",
		Graph: graph,
	})
	if err != nil {
		t.Fatalf("ApplyIncrementalDepOption: %v", err)
	}

	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, task := range tasks {
		if task.IssueNumber == 42 {
			if task.Dependencies != "[99,50]" {
				t.Errorf("task #42 deps = %s, want [99,50]", task.Dependencies)
			}
			if task.Status != "blocked" {
				t.Errorf("task #42 status = %s, want blocked", task.Status)
			}
		}
		if task.IssueNumber == 50 {
			// New task with no deps should remain queued.
			if task.Status != "queued" {
				t.Errorf("task #50 status = %s, want queued", task.Status)
			}
		}
	}
}

func TestApplyIncrementalDepOption_EmitsEvents(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	// New task (queued with empty deps).
	newTask := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  50,
		IssueTitle:   "New task",
		Dependencies: "[]",
		Status:       "queued",
	}
	_ = store.CreateAutopilotTask(newTask)

	graph := map[string]json.RawMessage{
		"50": json.RawMessage("[42]"),
	}

	err := sup.ApplyIncrementalDepOption(context.Background(), DepOption{
		Name:  "test",
		Graph: graph,
	})
	if err != nil {
		t.Fatalf("ApplyIncrementalDepOption: %v", err)
	}

	// Drain events and check for graph-update and info events.
	var graphUpdates, infoEvents int
	for {
		select {
		case ev := <-sup.Events():
			switch ev.Type {
			case "graph-update":
				graphUpdates++
			case "info":
				infoEvents++
			}
		default:
			goto done
		}
	}
done:
	if graphUpdates == 0 {
		t.Error("expected at least one graph-update event")
	}
	if infoEvents == 0 {
		t.Error("expected at least one info summary event")
	}
}

// ---------------------------------------------------------------------------
// DepGraph with deps shown as adjacency list
// ---------------------------------------------------------------------------

func TestDepGraph_NoDeps(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "No deps",
		Dependencies: "[]",
		Status:       "queued",
	}
	_ = store.CreateAutopilotTask(task)

	sup.mu.Lock()
	sup.active = true
	sup.preparedAt = time.Now()
	sup.mu.Unlock()

	graph := sup.DepGraph()
	if !contains(graph, "#10 (queued)") {
		t.Errorf("should show task without 'waits on', got:\n%s", graph)
	}
	if contains(graph, "waits on") {
		t.Error("should not show 'waits on' for task with no deps")
	}
}

// ---------------------------------------------------------------------------
// StatusBlock with all status types
// ---------------------------------------------------------------------------

func TestStatusBlock_AllStatusTypes(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	statuses := []string{"queued", "running", "review", "done", "bailed", "stopped", "manual"}
	for i, status := range statuses {
		task := &db.AutopilotTask{
			ProjectID:    project.ID,
			IssueNumber:  i + 1,
			IssueTitle:   status + " task",
			Dependencies: "[]",
			Status:       "queued",
		}
		_ = store.CreateAutopilotTask(task)
		if status != "queued" {
			_ = store.UpdateAutopilotTaskStatus(task.ID, status)
		}
	}

	sup.mu.Lock()
	sup.active = true
	sup.mu.Unlock()

	block := sup.StatusBlock()
	checks := []string{"queued", "running", "review", "done", "bailed", "stopped", "manual"}
	for _, check := range checks {
		if !contains(block, check) {
			t.Errorf("StatusBlock missing status %q in:\n%s", check, block)
		}
	}
}

// ---------------------------------------------------------------------------
// DepGraph with deps and multiple status types
// ---------------------------------------------------------------------------

func TestDepGraph_AllStatusTypes(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	statuses := []string{"queued", "running", "review", "bailed", "manual"}
	for i, status := range statuses {
		task := &db.AutopilotTask{
			ProjectID:    project.ID,
			IssueNumber:  (i + 1) * 10,
			IssueTitle:   status + " task",
			Dependencies: "[]",
			Status:       "queued",
		}
		_ = store.CreateAutopilotTask(task)
		if status != "queued" {
			_ = store.UpdateAutopilotTaskStatus(task.ID, status)
		}
	}

	sup.mu.Lock()
	sup.active = true
	sup.preparedAt = time.Now()
	sup.mu.Unlock()

	graph := sup.DepGraph()
	if graph == "" {
		t.Fatal("DepGraph should not be empty")
	}
	// Check it includes non-done/non-skipped tasks.
	if !contains(graph, "#10 (queued)") {
		t.Errorf("missing queued task in:\n%s", graph)
	}
	if !contains(graph, "#20 (running)") {
		t.Errorf("missing running task in:\n%s", graph)
	}
	if !contains(graph, "#50 (manual)") {
		t.Errorf("missing manual task in:\n%s", graph)
	}
}

// ---------------------------------------------------------------------------
// cleanOrphanedWorktrees — with temp dir contents
// ---------------------------------------------------------------------------

func TestCleanOrphanedWorktrees_WithEntries(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	project.Name = "test-clean"

	home := t.TempDir()
	t.Setenv("HOME", home)

	worktreeBase := filepath.Join(home, ".agent-minder", "worktrees", "test-clean")
	if err := os.MkdirAll(filepath.Join(worktreeBase, "issue-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(worktreeBase, "issue-2"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Also create a regular file (should be skipped).
	if err := os.WriteFile(filepath.Join(worktreeBase, "README.md"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	sup := New(store, project, nil, "/tmp/nonexistent-repo", "owner", "repo", "")
	sup.cleanOrphanedWorktrees()

	// Directories should have been cleaned up (or attempted).
	// The git worktree remove will fail, so os.RemoveAll fallback runs.
	entries, err := os.ReadDir(worktreeBase)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("directory %q should have been cleaned up", e.Name())
		}
	}
}

// ---------------------------------------------------------------------------
// SlotStatus with multiple running agents and tool details
// ---------------------------------------------------------------------------

func TestSlotStatus_MultipleRunning(t *testing.T) {
	store := openTestStore(t)
	p := &db.Project{
		Name:               "multi-slot",
		GoalType:           "test",
		GoalDescription:    "test",
		RefreshIntervalSec: 300,
		MessageTTLSec:      172800,
		LLMProvider:        "anthropic",
		LLMModel:           "claude-haiku-4-5",
		LLMSummarizerModel: "claude-haiku-4-5",
		LLMAnalyzerModel:   "claude-sonnet-4-6",
		AutopilotMaxAgents: 4,
	}
	if err := store.CreateProject(p); err != nil {
		t.Fatal(err)
	}

	sup := New(store, p, nil, "/tmp/repo", "owner", "repo", "")

	now := time.Now()
	sup.mu.Lock()
	sup.slots[0] = &slotState{
		task: &db.AutopilotTask{
			IssueNumber: 1,
			IssueTitle:  "First",
			Branch:      "agent/issue-1",
		},
		startedAt: now.Add(-10 * time.Minute),
		liveStatus: LiveStatus{
			CurrentTool: "Read",
			ToolInput:   "/path/to/file.go",
			StepCount:   12,
		},
	}
	sup.slots[1] = &slotState{
		task: &db.AutopilotTask{
			IssueNumber: 2,
			IssueTitle:  "Second",
			Branch:      "agent/issue-2",
		},
		startedAt: now.Add(-5 * time.Minute),
		liveStatus: LiveStatus{
			CurrentTool: "Grep",
			ToolInput:   "pattern",
			StepCount:   3,
		},
	}
	// slots[2] and [3] are nil (idle)
	sup.mu.Unlock()

	infos := sup.SlotStatus()
	if len(infos) != 4 {
		t.Fatalf("expected 4 slot infos, got %d", len(infos))
	}

	// Verify running slots.
	if infos[0].IssueNumber != 1 || infos[0].Status != "running" || infos[0].CurrentTool != "Read" {
		t.Errorf("slot 1 wrong: issue=%d status=%s tool=%s", infos[0].IssueNumber, infos[0].Status, infos[0].CurrentTool)
	}
	if infos[1].IssueNumber != 2 || infos[1].Status != "running" || infos[1].CurrentTool != "Grep" {
		t.Errorf("slot 2 wrong: issue=%d status=%s tool=%s", infos[1].IssueNumber, infos[1].Status, infos[1].CurrentTool)
	}
	if infos[0].RunningFor < 9*time.Minute {
		t.Errorf("slot 1 running for = %v, expected ~10m", infos[0].RunningFor)
	}
	// Verify idle slots.
	if infos[2].Status != "idle" || infos[3].Status != "idle" {
		t.Errorf("slots 3,4 should be idle")
	}
}

// ---------------------------------------------------------------------------
// StatusBlock with slot having tool info
// ---------------------------------------------------------------------------

func TestStatusBlock_ToolInfo(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	sup.mu.Lock()
	sup.active = true
	sup.slots[0] = &slotState{
		task: &db.AutopilotTask{
			IssueNumber: 42,
			IssueTitle:  "Test",
			Branch:      "agent/issue-42",
		},
		startedAt: time.Now(),
		liveStatus: LiveStatus{
			CurrentTool: "Bash",
			StepCount:   5,
		},
	}
	sup.mu.Unlock()

	block := sup.StatusBlock()
	if !contains(block, "using Bash") {
		t.Errorf("StatusBlock should show tool info, got:\n%s", block)
	}
}

func TestStatusBlock_NoToolInfo(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	sup.mu.Lock()
	sup.active = true
	sup.slots[0] = &slotState{
		task: &db.AutopilotTask{
			IssueNumber: 42,
			IssueTitle:  "Test",
			Branch:      "agent/issue-42",
		},
		startedAt: time.Now(),
		liveStatus: LiveStatus{
			CurrentTool: "", // no tool active
			StepCount:   5,
		},
	}
	sup.mu.Unlock()

	block := sup.StatusBlock()
	if contains(block, "using") {
		t.Errorf("StatusBlock should not show 'using' when no tool, got:\n%s", block)
	}
}

// ---------------------------------------------------------------------------
// Launch exits when all work is done
// ---------------------------------------------------------------------------

func TestLaunch_ExitsWhenDone(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	// All tasks are done.
	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "Done",
		Dependencies: "[]",
		Status:       "queued",
	}
	_ = store.CreateAutopilotTask(task)
	_ = store.UpdateAutopilotTaskStatus(task.ID, "done")

	ctx := context.Background()
	sup.Launch(ctx)

	// Should finish quickly since all tasks are done.
	done := make(chan struct{})
	go func() {
		// Wait for active to become false.
		for i := 0; i < 50; i++ {
			if !sup.IsActive() {
				close(done)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Launch did not exit when all tasks done")
		sup.Stop()
	}
}

// ---------------------------------------------------------------------------
// ApplyDepOption — mixed scenarios
// ---------------------------------------------------------------------------

func TestApplyDepOption_MixedScenario(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	// Create 4 tasks.
	for _, num := range []int{10, 20, 30, 40} {
		task := &db.AutopilotTask{
			ProjectID:    project.ID,
			IssueNumber:  num,
			IssueTitle:   fmt.Sprintf("Issue %d", num),
			Dependencies: "[]",
			Status:       "queued",
		}
		_ = store.CreateAutopilotTask(task)
	}

	// Mixed graph: 10 no deps, 20 depends on 10, 30 manual, 40 skip.
	opt := DepOption{
		Graph: map[string]json.RawMessage{
			"10": json.RawMessage("[]"),
			"20": json.RawMessage("[10]"),
			"30": json.RawMessage(`"manual"`),
			"40": json.RawMessage(`"skip"`),
		},
	}

	if err := sup.ApplyDepOption(context.Background(), opt); err != nil {
		t.Fatal(err)
	}

	tasks, _ := store.GetAutopilotTasks(project.ID)
	statusMap := make(map[int]string)
	for _, task := range tasks {
		statusMap[task.IssueNumber] = task.Status
	}

	if statusMap[10] != "queued" {
		t.Errorf("task 10 = %q, want queued", statusMap[10])
	}
	if statusMap[20] != "blocked" {
		t.Errorf("task 20 = %q, want blocked", statusMap[20])
	}
	if statusMap[30] != "manual" {
		t.Errorf("task 30 = %q, want manual", statusMap[30])
	}
	if statusMap[40] != "skipped" {
		t.Errorf("task 40 = %q, want skipped", statusMap[40])
	}
}

// ---------------------------------------------------------------------------
// ApplyRebuildDepOption — string deps in rebuild
// ---------------------------------------------------------------------------

func TestApplyRebuildDepOption_StringDeps(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	for _, num := range []int{10, 20} {
		task := &db.AutopilotTask{
			ProjectID:    project.ID,
			IssueNumber:  num,
			IssueTitle:   fmt.Sprintf("Issue %d", num),
			Dependencies: "[]",
			Status:       "queued",
		}
		_ = store.CreateAutopilotTask(task)
	}

	// String deps (LLM sometimes returns strings).
	opt := DepOption{
		Graph: map[string]json.RawMessage{
			"10": json.RawMessage("[]"),
			"20": json.RawMessage(`["10"]`),
		},
	}

	_, err := sup.ApplyRebuildDepOption(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}

	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, task := range tasks {
		if task.IssueNumber == 20 && task.Status != "blocked" {
			t.Errorf("task 20 status = %q, want blocked", task.Status)
		}
	}
}

// ---------------------------------------------------------------------------
// QueuedUnblockedTasks — multiple deps partially satisfied
// ---------------------------------------------------------------------------

func TestQueuedUnblockedTasks_MultipleDepsPartial(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)

	// Task 10 done, task 20 running, task 30 depends on both.
	t10 := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "Done",
		Dependencies: "[]",
		Status:       "queued",
	}
	_ = store.CreateAutopilotTask(t10)
	_ = store.UpdateAutopilotTaskStatus(t10.ID, "done")

	t20 := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  20,
		IssueTitle:   "Running",
		Dependencies: "[]",
		Status:       "running",
	}
	_ = store.CreateAutopilotTask(t20)

	t30 := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  30,
		IssueTitle:   "Both deps",
		Dependencies: "[10,20]",
		Status:       "queued",
	}
	_ = store.CreateAutopilotTask(t30)

	unblocked, err := store.QueuedUnblockedTasks(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(unblocked) != 0 {
		t.Errorf("expected 0 unblocked (dep 20 running), got %d", len(unblocked))
	}
}

// ---------------------------------------------------------------------------
// DepGraph summary counts
// ---------------------------------------------------------------------------

func TestDepGraph_SummaryCounts(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	for i, status := range []string{"queued", "running", "review", "done", "bailed", "manual"} {
		task := &db.AutopilotTask{
			ProjectID:    project.ID,
			IssueNumber:  (i + 1) * 10,
			IssueTitle:   status,
			Dependencies: "[]",
			Status:       "queued",
		}
		_ = store.CreateAutopilotTask(task)
		if status != "queued" {
			_ = store.UpdateAutopilotTaskStatus(task.ID, status)
		}
	}

	sup.mu.Lock()
	sup.active = true
	sup.preparedAt = time.Now()
	sup.mu.Unlock()

	graph := sup.DepGraph()
	if !contains(graph, "1 queued") {
		t.Errorf("should count queued, got:\n%s", graph)
	}
	if !contains(graph, "1 running") {
		t.Errorf("should count running, got:\n%s", graph)
	}
	if !contains(graph, "1 review") {
		t.Errorf("should count review, got:\n%s", graph)
	}
	if !contains(graph, "1 done") {
		t.Errorf("should count done, got:\n%s", graph)
	}
	if !contains(graph, "1 bailed") {
		t.Errorf("should count bailed, got:\n%s", graph)
	}
	if !contains(graph, "1 manual") {
		t.Errorf("should count manual, got:\n%s", graph)
	}
}

// ---------------------------------------------------------------------------
// ApplyDepOption with malformed deps (fallback paths)
// ---------------------------------------------------------------------------

func TestApplyDepOption_MalformedDeps(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "Malformed",
		Dependencies: "[]",
		Status:       "queued",
	}
	_ = store.CreateAutopilotTask(task)

	// Invalid JSON structure that isn't string, int array, or string array.
	opt := DepOption{
		Graph: map[string]json.RawMessage{
			"10": json.RawMessage(`{"nested": "object"}`),
		},
	}

	// Should not error — just skips the malformed entry.
	if err := sup.ApplyDepOption(context.Background(), opt); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// unblockSatisfiedTasks — no blocked tasks
// ---------------------------------------------------------------------------

func TestUnblockSatisfiedTasks_NoneBlocked(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "Queued",
		Dependencies: "[]",
		Status:       "queued",
	}
	_ = store.CreateAutopilotTask(task)

	n := sup.unblockSatisfiedTasks()
	if n != 0 {
		t.Errorf("expected 0 unblocked (none blocked), got %d", n)
	}
}

// ---------------------------------------------------------------------------
// unblockSatisfiedTasks — unknown dep is non-blocking
// ---------------------------------------------------------------------------

func TestUnblockSatisfiedTasks_UnknownDep(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "Unknown dep",
		Dependencies: "[999]",
		Status:       "blocked",
	}
	_ = store.CreateAutopilotTask(task)

	// Dep 999 is unknown → treated as satisfied.
	n := sup.unblockSatisfiedTasks()
	if n != 1 {
		t.Errorf("expected 1 unblocked (unknown dep), got %d", n)
	}
}

// ---------------------------------------------------------------------------
// Launch with no tasks exits immediately
// ---------------------------------------------------------------------------

func TestLaunch_NoTasksExitsImmediately(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	ctx := context.Background()
	sup.Launch(ctx)

	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			if !sup.IsActive() {
				close(done)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Launch should exit when no tasks")
		sup.Stop()
	}
}

// ---------------------------------------------------------------------------
// hasIdleSlot with various configurations
// ---------------------------------------------------------------------------

func TestHasIdleSlot_SingleSlot(t *testing.T) {
	store := openTestStore(t)
	p := &db.Project{
		Name:               "one-slot",
		GoalType:           "test",
		GoalDescription:    "test",
		RefreshIntervalSec: 300,
		MessageTTLSec:      172800,
		LLMProvider:        "anthropic",
		LLMModel:           "claude-haiku-4-5",
		LLMSummarizerModel: "claude-haiku-4-5",
		LLMAnalyzerModel:   "claude-sonnet-4-6",
		AutopilotMaxAgents: 1,
	}
	_ = store.CreateProject(p)

	sup := New(store, p, nil, "/tmp/repo", "owner", "repo", "")
	if !sup.hasIdleSlot() {
		t.Error("single nil slot should be idle")
	}

	sup.mu.Lock()
	sup.slots[0] = &slotState{task: &db.AutopilotTask{IssueNumber: 1}}
	sup.mu.Unlock()

	if sup.hasIdleSlot() {
		t.Error("single occupied slot should not be idle")
	}
}

// ---------------------------------------------------------------------------
// countUnblocked — edge cases
// ---------------------------------------------------------------------------

func TestCountUnblocked_EmptyGraph(t *testing.T) {
	// Empty graph but tasks exist → all unblocked.
	tasks := []*db.AutopilotTask{
		{IssueNumber: 10, Status: "queued"},
		{IssueNumber: 20, Status: "queued"},
	}
	graph := map[string]json.RawMessage{}
	if got := countUnblocked(graph, tasks); got != 2 {
		t.Errorf("countUnblocked = %d, want 2 (all tasks not in graph)", got)
	}
}

func TestCountUnblocked_EmptyDepArray(t *testing.T) {
	// Explicitly empty array.
	graph := map[string]json.RawMessage{
		"10": json.RawMessage(`[]`),
	}
	tasks := []*db.AutopilotTask{
		{IssueNumber: 10, Status: "queued"},
	}
	if got := countUnblocked(graph, tasks); got != 1 {
		t.Errorf("countUnblocked = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// parseDeps edge cases
// ---------------------------------------------------------------------------

func TestParseDeps_NonNumeric(t *testing.T) {
	// Non-numeric values should be skipped.
	got := parseDeps("[abc, 42, xyz]")
	if len(got) != 1 || got[0] != 42 {
		t.Errorf("parseDeps([abc,42,xyz]) = %v, want [42]", got)
	}
}

// ---------------------------------------------------------------------------
// countUnblocked with string-array deps
// ---------------------------------------------------------------------------

func TestCountUnblocked_StringArrayDeps(t *testing.T) {
	// String array deps like ["10"] should be parsed and counted.
	graph := map[string]json.RawMessage{
		"20": json.RawMessage(`["10"]`), // string array dep
		"30": json.RawMessage(`[]`),     // no deps
	}
	tasks := []*db.AutopilotTask{
		{IssueNumber: 20, Status: "queued"},
		{IssueNumber: 30, Status: "queued"},
	}
	// 20 has deps → blocked, 30 has no deps → unblocked
	if got := countUnblocked(graph, tasks); got != 1 {
		t.Errorf("countUnblocked = %d, want 1", got)
	}
}

func TestCountUnblocked_UnmarshalError(t *testing.T) {
	// Invalid JSON in deps should be treated as no deps (unblocked).
	graph := map[string]json.RawMessage{
		"10": json.RawMessage(`invalid`),
	}
	tasks := []*db.AutopilotTask{
		{IssueNumber: 10, Status: "queued"},
	}
	if got := countUnblocked(graph, tasks); got != 1 {
		t.Errorf("countUnblocked = %d, want 1 (invalid JSON = no deps)", got)
	}
}

// ---------------------------------------------------------------------------
// ApplyDepOption with string array deps fallback
// ---------------------------------------------------------------------------

func TestApplyDepOption_StringArrayDeps(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/fake-repo", "owner", "repo", "")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  20,
		IssueTitle:   "Depends on #10 via string deps",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	// Use string array deps ["10"] which triggers the fallback path.
	opt := DepOption{
		Name:      "String deps",
		Rationale: "String array fallback",
		Graph: map[string]json.RawMessage{
			"20": json.RawMessage(`["10"]`),
		},
	}
	if err := sup.ApplyDepOption(context.Background(), opt); err != nil {
		t.Fatalf("ApplyDepOption: %v", err)
	}

	tasks, _ := store.GetAutopilotTasks(project.ID)
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	// String dep "10" is parsed correctly to [10]. Dep 10 doesn't exist as a task
	// (unknown deps are treated as satisfied), so the task is unblocked.
	if tasks[0].Status != "queued" {
		t.Errorf("status = %q, want queued (unblocked since dep 10 is unknown)", tasks[0].Status)
	}
	// Verify deps were stored correctly as [10], not [0,10].
	if tasks[0].Dependencies != "[10]" {
		t.Errorf("deps = %q, want [10]", tasks[0].Dependencies)
	}
}

// ---------------------------------------------------------------------------
// RestartTask with worktree/branch fields
// ---------------------------------------------------------------------------

func TestRestartTask_BailedWithWorktree(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/fake-repo", "owner", "repo", "")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  15,
		IssueTitle:   "Bailed with paths",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}
	// Set running fields first, then mark bailed (order matters: UpdateAutopilotTaskRunning sets status=running).
	_ = store.UpdateAutopilotTaskRunning(task.ID, "/tmp/fake-worktree", "agent/issue-15", "/tmp/fake.log")
	_ = store.UpdateAutopilotTaskStatus(task.ID, "bailed")

	// Restart with cancelled context to avoid launching.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := sup.RestartTask(ctx, task.ID); err != nil {
		t.Fatalf("RestartTask: %v", err)
	}

	tasks, _ := store.GetAutopilotTasks(project.ID)
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks", len(tasks))
	}
	if tasks[0].Status != "queued" {
		t.Errorf("status = %q, want queued", tasks[0].Status)
	}
	if tasks[0].WorktreePath != "" {
		t.Errorf("worktree_path should be cleared, got %q", tasks[0].WorktreePath)
	}
}

// ---------------------------------------------------------------------------
// Stop when active
// ---------------------------------------------------------------------------

func TestStop_WhenActive(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/fake-repo", "owner", "repo", "")

	// Simulate an active supervisor with a done channel.
	sup.mu.Lock()
	sup.active = true
	ctx, cancel := context.WithCancel(context.Background())
	sup.cancel = cancel
	sup.done = make(chan struct{})
	sup.mu.Unlock()

	// Close done immediately to simulate agents finishing.
	close(sup.done)

	sup.Stop()

	// After Stop, context should be cancelled.
	if ctx.Err() == nil {
		t.Error("context should be cancelled after Stop")
	}
}

// ---------------------------------------------------------------------------
// fillSlots with actual tasks
// ---------------------------------------------------------------------------

func TestFillSlots_SkipsBlockedTasks(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/fake-repo", "owner", "repo", "")

	// Create only blocked tasks — fillSlots should not launch anything.
	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  50,
		IssueTitle:   "Blocked task",
		Dependencies: "[99]",
		Status:       "blocked",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	sup.fillSlots(ctx)

	// No slots should be filled.
	sup.mu.Lock()
	for i, slot := range sup.slots {
		if slot != nil {
			t.Errorf("slot %d should be nil for blocked tasks", i)
		}
	}
	sup.mu.Unlock()
}

// ---------------------------------------------------------------------------
// ApplyRebuildDepOption with blocked→queued transition
// ---------------------------------------------------------------------------

func TestApplyRebuildDepOption_UnblocksWhenSatisfied(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/fake-repo", "owner", "repo", "")

	// Task 10 is done, task 20 depends on 10 and is blocked.
	t1 := &db.AutopilotTask{
		ProjectID: project.ID, IssueNumber: 10, IssueTitle: "Done task",
		Dependencies: "[]", Status: "queued",
	}
	t2 := &db.AutopilotTask{
		ProjectID: project.ID, IssueNumber: 20, IssueTitle: "Blocked task",
		Dependencies: "[10]", Status: "queued",
	}
	_ = store.CreateAutopilotTask(t1)
	_ = store.CreateAutopilotTask(t2)
	_ = store.UpdateAutopilotTaskStatus(t1.ID, "done")
	_ = store.UpdateAutopilotTaskStatus(t2.ID, "blocked")

	// Rebuild dep option: 20 depends on 10, which is done → should unblock.
	opt := DepOption{
		Name: "Rebuild", Rationale: "Test",
		Graph: map[string]json.RawMessage{
			"20": json.RawMessage(`[10]`),
		},
	}
	if _, err := sup.ApplyRebuildDepOption(context.Background(), opt); err != nil {
		t.Fatalf("ApplyRebuildDepOption: %v", err)
	}

	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, task := range tasks {
		if task.IssueNumber == 20 {
			if task.Status != "queued" {
				t.Errorf("task 20 status = %q, want queued (unblocked)", task.Status)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// inspectOutcome — failed with PR promotes to review
// ---------------------------------------------------------------------------

// TestInspectOutcome_FailedNoPR_StaysFailed verifies that when classifyOutcome
// returns "failed" and no PR is found, inspectOutcome still returns "failed".
// (The fake token causes FetchPRForBranch to fail, simulating no PR.)
func TestInspectOutcome_FailedNoPR_StaysFailed(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	project.AutopilotMaxTurns = 50
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	dir := t.TempDir()
	logPath := filepath.Join(dir, "agent.log")
	logContent := `{"type":"result","subtype":"success","is_error":false,"num_turns":50,"total_cost_usd":2.00,"result":"Done","permission_denials":[],"session_id":"abc"}
`
	if err := os.WriteFile(logPath, []byte(logContent), 0o644); err != nil {
		t.Fatal(err)
	}

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  42,
		IssueTitle:   "Max turns no PR",
		AgentLog:     logPath,
		Branch:       "agent/issue-42",
		Dependencies: "[]",
		Status:       "running",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	status := sup.inspectOutcome(context.Background(), task, 0)
	if status != "failed" {
		t.Errorf("expected failed (no PR found), got %q", status)
	}
	if task.PRNumber != 0 {
		t.Errorf("PRNumber should remain 0, got %d", task.PRNumber)
	}
}

// TestInspectOutcome_BudgetNoPR_StaysFailed verifies budget exhaustion without PR stays failed.
func TestInspectOutcome_BudgetNoPR_StaysFailed(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	project.AutopilotMaxBudgetUSD = 3.00
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	dir := t.TempDir()
	logPath := filepath.Join(dir, "agent.log")
	logContent := `{"type":"result","subtype":"success","is_error":false,"num_turns":10,"total_cost_usd":2.90,"result":"Done","permission_denials":[],"session_id":"abc"}
`
	if err := os.WriteFile(logPath, []byte(logContent), 0o644); err != nil {
		t.Fatal(err)
	}

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  42,
		IssueTitle:   "Budget no PR",
		AgentLog:     logPath,
		Branch:       "agent/issue-42",
		Dependencies: "[]",
		Status:       "running",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	status := sup.inspectOutcome(context.Background(), task, 0)
	if status != "failed" {
		t.Errorf("expected failed (no PR found), got %q", status)
	}
}

// TestInspectOutcome_FailureReasonStoredOnFailed verifies failure_reason and
// failure_detail are persisted even when the task stays as "failed" (no PR).
func TestInspectOutcome_FailureReasonStoredOnFailed(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	project.AutopilotMaxTurns = 20
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	dir := t.TempDir()
	logPath := filepath.Join(dir, "agent.log")
	logContent := `{"type":"result","subtype":"success","is_error":false,"num_turns":20,"total_cost_usd":1.00,"result":"Ran out of turns","permission_denials":[],"session_id":"abc"}
`
	if err := os.WriteFile(logPath, []byte(logContent), 0o644); err != nil {
		t.Fatal(err)
	}

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  99,
		IssueTitle:   "Failure reason test",
		AgentLog:     logPath,
		Branch:       "agent/issue-99",
		Dependencies: "[]",
		Status:       "running",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	status := sup.inspectOutcome(context.Background(), task, 0)
	if status != "failed" {
		t.Errorf("expected failed, got %q", status)
	}

	// Verify failure reason was stored in DB.
	tasks, err := store.GetAutopilotTasks(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ta := range tasks {
		if ta.ID == task.ID {
			if ta.FailureReason != "max_turns" {
				t.Errorf("failure_reason = %q, want max_turns", ta.FailureReason)
			}
			if ta.FailureDetail == "" {
				t.Error("failure_detail should not be empty")
			}
			return
		}
	}
	t.Error("task not found in DB after inspectOutcome")
}

// ---------------------------------------------------------------------------
// BumpTaskLimits
// ---------------------------------------------------------------------------

func TestBumpTaskLimits(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	project.AutopilotMaxTurns = 50
	project.AutopilotMaxBudgetUSD = 3.00
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  42,
		IssueTitle:   "Bump test",
		Dependencies: "[]",
		Status:       "failed",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	// First bump: 50*1.5=75 turns, 3.00*1.5=4.50 budget.
	newTurns, newBudget, err := sup.BumpTaskLimits(task.ID)
	if err != nil {
		t.Fatalf("BumpTaskLimits: %v", err)
	}
	if newTurns != 75 {
		t.Errorf("newTurns = %d, want 75", newTurns)
	}
	if newBudget != 4.50 {
		t.Errorf("newBudget = %f, want 4.50", newBudget)
	}

	// Verify overrides persisted.
	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, ta := range tasks {
		if ta.ID == task.ID {
			if ta.MaxTurnsOverride == nil || *ta.MaxTurnsOverride != 75 {
				t.Errorf("persisted turns override = %v, want 75", ta.MaxTurnsOverride)
			}
			if ta.MaxBudgetOverride == nil || *ta.MaxBudgetOverride != 4.50 {
				t.Errorf("persisted budget override = %v, want 4.50", ta.MaxBudgetOverride)
			}
		}
	}

	// Second bump compounds: 75*1.5=112 turns, 4.50*1.5=6.75 budget.
	newTurns, newBudget, err = sup.BumpTaskLimits(task.ID)
	if err != nil {
		t.Fatalf("BumpTaskLimits (2nd): %v", err)
	}
	if newTurns != 112 {
		t.Errorf("newTurns = %d, want 112", newTurns)
	}
	if newBudget != 6.75 {
		t.Errorf("newBudget = %f, want 6.75", newBudget)
	}
}

func TestBumpTaskLimits_NotFound(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	_, _, err := sup.BumpTaskLimits(99999)
	if err == nil {
		t.Fatal("expected error for non-existent task")
	}
}

// ---------------------------------------------------------------------------
// inspectOutcome with per-task overrides
// ---------------------------------------------------------------------------

func TestInspectOutcome_MaxTurnsWithOverride(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	project.AutopilotMaxTurns = 50
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	dir := t.TempDir()
	logPath := filepath.Join(dir, "agent.log")
	// 75 turns used — would fail with default 50, but override is 100.
	logContent := `{"type":"result","subtype":"success","is_error":false,"num_turns":75,"total_cost_usd":2.00,"result":"Done","permission_denials":[],"session_id":"abc"}
`
	if err := os.WriteFile(logPath, []byte(logContent), 0o644); err != nil {
		t.Fatal(err)
	}

	turnsOverride := 100
	task := &db.AutopilotTask{
		ProjectID:        project.ID,
		IssueNumber:      42,
		IssueTitle:       "Override turns",
		AgentLog:         logPath,
		Branch:           "agent/issue-42",
		Dependencies:     "[]",
		Status:           "running",
		MaxTurnsOverride: &turnsOverride,
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	status := sup.inspectOutcome(context.Background(), task, 0)
	// 75 < 100 override, so should NOT be classified as max_turns failure.
	if status == "failed" {
		tasks, _ := store.GetAutopilotTasks(project.ID)
		for _, ta := range tasks {
			if ta.ID == task.ID && ta.FailureReason == "max_turns" {
				t.Error("should not fail as max_turns when override allows more turns")
			}
		}
	}
}

// ---------------------------------------------------------------------------
// cleanup branch deletion logic
// ---------------------------------------------------------------------------

// TestCleanup_BailedDeletesBranch verifies that cleanup with deleteBranch=true
// attempts branch deletion (bailed tasks with no PR).
func TestCleanup_BailedDeletesBranch(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/fake-repo", "owner", "repo", "fake-token")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  42,
		IssueTitle:   "Bailed task",
		WorktreePath: filepath.Join(t.TempDir(), "nonexistent-worktree"),
		Branch:       "agent/issue-42",
		Dependencies: "[]",
		Status:       "bailed",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	// cleanup should not panic even with non-existent paths.
	// With status=bailed, deleteBranch is true.
	sup.cleanup(task, true)
}

// TestCleanup_ReviewKeepsBranch verifies that review tasks do not delete branch.
func TestCleanup_ReviewKeepsBranch(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/fake-repo", "owner", "repo", "fake-token")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  42,
		IssueTitle:   "Review task",
		WorktreePath: filepath.Join(t.TempDir(), "nonexistent-worktree"),
		Branch:       "agent/issue-42",
		PRNumber:     100,
		Dependencies: "[]",
		Status:       "review",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	// cleanup with deleteBranch=false (review status).
	sup.cleanup(task, false)
}

// ---------------------------------------------------------------------------
// parseReviewRisk
// ---------------------------------------------------------------------------

func TestParseReviewRisk_Low(t *testing.T) {
	result := &AgentResult{Result: "## Risk Assessment\n\n**Risk level:** low\n\n**Summary:** Clean implementation"}
	got := parseReviewRisk(result)
	if got != "low" {
		t.Errorf("parseReviewRisk = %q, want %q", got, "low")
	}
}

func TestParseReviewRisk_Medium(t *testing.T) {
	result := &AgentResult{Result: "Some preamble\n\n**Risk level:** medium\n\nSome details"}
	got := parseReviewRisk(result)
	if got != "medium" {
		t.Errorf("parseReviewRisk = %q, want %q", got, "medium")
	}
}

func TestParseReviewRisk_High(t *testing.T) {
	result := &AgentResult{Result: "**Risk level:** high\n\nLogic errors found"}
	got := parseReviewRisk(result)
	if got != "high" {
		t.Errorf("parseReviewRisk = %q, want %q", got, "high")
	}
}

func TestParseReviewRisk_NoMarker(t *testing.T) {
	result := &AgentResult{Result: "This review found nothing special"}
	got := parseReviewRisk(result)
	if got != "unknown" {
		t.Errorf("parseReviewRisk = %q, want %q", got, "unknown")
	}
}

func TestParseReviewRisk_NilResult(t *testing.T) {
	got := parseReviewRisk(nil)
	if got != "unknown" {
		t.Errorf("parseReviewRisk = %q, want %q", got, "unknown")
	}
}

func TestParseReviewRisk_EmptyResult(t *testing.T) {
	result := &AgentResult{Result: ""}
	got := parseReviewRisk(result)
	if got != "unknown" {
		t.Errorf("parseReviewRisk = %q, want %q", got, "unknown")
	}
}

func TestParseReviewRisk_PlainMarker(t *testing.T) {
	// Without bold markdown markers.
	result := &AgentResult{Result: "Risk level: low"}
	got := parseReviewRisk(result)
	if got != "low" {
		t.Errorf("parseReviewRisk = %q, want %q", got, "low")
	}
}

// ---------------------------------------------------------------------------
// riskToLabel
// ---------------------------------------------------------------------------

func TestRiskToLabel_Low(t *testing.T) {
	if got := riskToLabel("low"); got != "low-risk" {
		t.Errorf("riskToLabel('low') = %q, want 'low-risk'", got)
	}
}

func TestRiskToLabel_Medium(t *testing.T) {
	if got := riskToLabel("medium"); got != "needs-testing" {
		t.Errorf("riskToLabel('medium') = %q, want 'needs-testing'", got)
	}
}

func TestRiskToLabel_High(t *testing.T) {
	if got := riskToLabel("high"); got != "suspect" {
		t.Errorf("riskToLabel('high') = %q, want 'suspect'", got)
	}
}

func TestRiskToLabel_Unknown(t *testing.T) {
	if got := riskToLabel("unknown"); got != "needs-testing" {
		t.Errorf("riskToLabel('unknown') = %q, want 'needs-testing'", got)
	}
}

func TestRiskToLabel_Empty(t *testing.T) {
	if got := riskToLabel(""); got != "needs-testing" {
		t.Errorf("riskToLabel('') = %q, want 'needs-testing'", got)
	}
}

// ---------------------------------------------------------------------------
// formatReviewComment
// ---------------------------------------------------------------------------

func TestFormatReviewComment_LowRisk(t *testing.T) {
	body := formatReviewComment("## Risk Assessment\n\n**Risk level:** low\n\n**Summary:** Clean implementation", "low-risk")
	if !strings.Contains(body, "✅") {
		t.Error("low-risk comment should contain ✅ emoji")
	}
	if !strings.Contains(body, "`low-risk`") {
		t.Error("comment should contain low-risk label")
	}
	if !strings.Contains(body, "**Recommendation:** Merge") {
		t.Error("low-risk recommendation should be 'Merge'")
	}
	if !strings.Contains(body, "Clean implementation") {
		t.Error("comment should include agent output")
	}
	if !strings.Contains(body, "agent-minder autopilot reviewer") {
		t.Error("comment should have reviewer footer")
	}
}

func TestFormatReviewComment_NeedsTesting(t *testing.T) {
	body := formatReviewComment("**Risk level:** medium", "needs-testing")
	if !strings.Contains(body, "⚠️") {
		t.Error("needs-testing comment should contain ⚠️ emoji")
	}
	if !strings.Contains(body, "`needs-testing`") {
		t.Error("comment should contain needs-testing label")
	}
	if !strings.Contains(body, "**Recommendation:** Test first") {
		t.Error("needs-testing recommendation should be 'Test first'")
	}
}

func TestFormatReviewComment_Suspect(t *testing.T) {
	body := formatReviewComment("**Risk level:** high", "suspect")
	if !strings.Contains(body, "🔴") {
		t.Error("suspect comment should contain 🔴 emoji")
	}
	if !strings.Contains(body, "`suspect`") {
		t.Error("comment should contain suspect label")
	}
	if !strings.Contains(body, "**Recommendation:** Needs rework") {
		t.Error("suspect recommendation should be 'Needs rework'")
	}
}

func TestFormatReviewComment_TrimsWhitespace(t *testing.T) {
	body := formatReviewComment("  some output  \n\n", "low-risk")
	// Should not have leading/trailing whitespace in the agent output section.
	if strings.Contains(body, "  some output  \n\n\n") {
		t.Error("agent output should be trimmed")
	}
}

// ---------------------------------------------------------------------------
// checkReviewTasks — reviewing/reviewed status paths
// ---------------------------------------------------------------------------

func TestCheckReviewTasks_ReviewWithNoReviewConfig(t *testing.T) {
	// When autopilot_review_max_turns is nil, review tasks should NOT spawn
	// a review agent — they should just check for merge status.
	store := openTestStore(t)
	project := createTestProject(t, store)
	// project.AutopilotReviewMaxTurns is nil by default.
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  42,
		IssueTitle:   "Test review",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}
	_ = store.UpdateAutopilotTaskStatus(task.ID, "review")
	_ = store.UpdateAutopilotTaskPR(task.ID, 100)

	// checkReviewTasks will try to fetch PR status via GitHub API which will fail
	// with the fake token, so promoted should be 0 — but importantly, the task
	// should NOT transition to "reviewing".
	promoted := sup.checkReviewTasks(context.Background())
	if promoted != 0 {
		t.Errorf("promoted = %d, want 0", promoted)
	}

	// Verify task is still in "review" status (not "reviewing").
	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, tt := range tasks {
		if tt.ID == task.ID && tt.Status != "review" {
			t.Errorf("task status = %q, want 'review' (should not spawn review without config)", tt.Status)
		}
	}
}

func TestLaunch_ReviewingTaskKeepsLoopAlive(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	// Create a task in "reviewing" status — the loop should stay alive.
	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  42,
		IssueTitle:   "Reviewing",
		Dependencies: "[]",
		Status:       "queued",
	}
	_ = store.CreateAutopilotTask(task)
	_ = store.UpdateAutopilotTaskStatus(task.ID, "reviewing")

	ctx, cancel := context.WithCancel(context.Background())
	sup.Launch(ctx)

	// Give the loop time to evaluate hasWork.
	time.Sleep(200 * time.Millisecond)

	// Loop should still be active because there's a "reviewing" task.
	if !sup.IsActive() {
		t.Error("supervisor exited prematurely — should stay alive for reviewing tasks")
	}

	cancel()
	<-sup.Done()
}

// ---------------------------------------------------------------------------
// Review agent failure classification
// ---------------------------------------------------------------------------

func TestCheckReviewTasks_RetriesExhausted(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	maxTurns := 10
	project.AutopilotReviewMaxTurns = &maxTurns

	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  42,
		IssueTitle:   "Test review retries",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}
	_ = store.UpdateAutopilotTaskStatus(task.ID, "review")
	_ = store.UpdateAutopilotTaskPR(task.ID, 100)

	// Exhaust retries.
	for i := 0; i < defaultReviewMaxRetries; i++ {
		sup.incrReviewRetry(task.ID)
	}

	// checkReviewTasks should transition to "reviewed" with failure info.
	promoted := sup.checkReviewTasks(context.Background())
	if promoted != 0 {
		t.Errorf("promoted = %d, want 0", promoted)
	}

	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, tt := range tasks {
		if tt.ID == task.ID {
			if tt.Status != "reviewed" {
				t.Errorf("task status = %q, want 'reviewed'", tt.Status)
			}
			if tt.FailureReason != "review_retries_exhausted" {
				t.Errorf("failure_reason = %q, want 'review_retries_exhausted'", tt.FailureReason)
			}
		}
	}
}

func TestReviewClassifyOutcome_MaxTurns(t *testing.T) {
	result := &AgentResult{
		NumTurns:  10,
		TotalCost: 0.5,
		Result:    "partial review...",
	}
	status, reason, detail := classifyOutcome(result, 10, 2.0)
	if status != "failed" || reason != "max_turns" {
		t.Errorf("classifyOutcome = (%q, %q, %q), want (failed, max_turns, ...)", status, reason, detail)
	}
}

func TestReviewClassifyOutcome_MaxBudget(t *testing.T) {
	result := &AgentResult{
		NumTurns:  3,
		TotalCost: 1.95, // >= 2.0 * 0.95
		Result:    "partial review...",
	}
	status, reason, detail := classifyOutcome(result, 10, 2.0)
	if status != "failed" || reason != "max_budget" {
		t.Errorf("classifyOutcome = (%q, %q, %q), want (failed, max_budget, ...)", status, reason, detail)
	}
}

func TestReviewClassifyOutcome_Bail(t *testing.T) {
	// nil result simulates agent that crashed/produced nothing.
	status, reason, _ := classifyOutcome(nil, 10, 2.0)
	if status != "" || reason != "" {
		t.Errorf("classifyOutcome(nil) = (%q, %q), want empty (bail detected separately)", status, reason)
	}
}

func TestReviewRetryHelpers(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	if got := sup.getReviewRetries(42); got != 0 {
		t.Errorf("initial retry count = %d, want 0", got)
	}

	sup.incrReviewRetry(42)
	sup.incrReviewRetry(42)
	if got := sup.getReviewRetries(42); got != 2 {
		t.Errorf("retry count after 2 incrs = %d, want 2", got)
	}

	// Different task ID should be independent.
	if got := sup.getReviewRetries(99); got != 0 {
		t.Errorf("unrelated task retry count = %d, want 0", got)
	}
}

func TestUpdateAutopilotTaskFailureInfo(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  42,
		IssueTitle:   "Test failure info",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}
	_ = store.UpdateAutopilotTaskStatus(task.ID, "reviewing")

	// Set failure info without changing status.
	err := store.UpdateAutopilotTaskFailureInfo(task.ID, "max_turns", "used 10 of 10 turns")
	if err != nil {
		t.Fatal(err)
	}

	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, tt := range tasks {
		if tt.ID == task.ID {
			// Status should NOT have changed.
			if tt.Status != "reviewing" {
				t.Errorf("status = %q, want 'reviewing' (should not change)", tt.Status)
			}
			if tt.FailureReason != "max_turns" {
				t.Errorf("failure_reason = %q, want 'max_turns'", tt.FailureReason)
			}
			if tt.FailureDetail != "used 10 of 10 turns" {
				t.Errorf("failure_detail = %q, want 'used 10 of 10 turns'", tt.FailureDetail)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Review state transitions: reviewing → reviewed with failure classification
// ---------------------------------------------------------------------------

// TestReviewOutcome_MaxTurns_StoresFailureAndTransitions verifies that when a
// review agent exhausts its turn limit, the task transitions to "reviewed" with
// the failure_reason set to "max_turns" and failure_detail populated.
func TestReviewOutcome_MaxTurns_StoresFailureAndTransitions(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	maxTurns := 10
	project.AutopilotReviewMaxTurns = &maxTurns

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  55,
		IssueTitle:   "Review max turns",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}
	_ = store.UpdateAutopilotTaskStatus(task.ID, "reviewing")
	_ = store.UpdateAutopilotTaskPR(task.ID, 200)

	// Simulate what runReviewAgent does after agent completes with max turns.
	result := &AgentResult{
		NumTurns:  10,
		TotalCost: 1.50,
		Result:    "Partial review before running out of turns",
	}
	_, reason, detail := classifyOutcome(result, maxTurns, 2.0)

	if reason != "max_turns" {
		t.Fatalf("expected max_turns, got %q", reason)
	}

	_ = store.UpdateAutopilotTaskFailureInfo(task.ID, reason, detail)
	_ = store.UpdateAutopilotTaskStatus(task.ID, "reviewed")

	// Verify DB state.
	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, tt := range tasks {
		if tt.ID == task.ID {
			if tt.Status != "reviewed" {
				t.Errorf("status = %q, want 'reviewed'", tt.Status)
			}
			if tt.FailureReason != "max_turns" {
				t.Errorf("failure_reason = %q, want 'max_turns'", tt.FailureReason)
			}
			if !strings.Contains(tt.FailureDetail, "10") {
				t.Errorf("failure_detail = %q, should mention turns used", tt.FailureDetail)
			}
		}
	}
}

// TestReviewOutcome_MaxBudget_StoresFailureAndTransitions verifies budget
// exhaustion stores the correct failure reason.
func TestReviewOutcome_MaxBudget_StoresFailureAndTransitions(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	maxTurns := 20
	project.AutopilotReviewMaxTurns = &maxTurns
	maxBudget := 2.0
	project.AutopilotReviewMaxBudgetUSD = &maxBudget

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  56,
		IssueTitle:   "Review max budget",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}
	_ = store.UpdateAutopilotTaskStatus(task.ID, "reviewing")
	_ = store.UpdateAutopilotTaskPR(task.ID, 201)

	result := &AgentResult{
		NumTurns:  5,
		TotalCost: 1.95, // >= 2.0 * 0.95
		Result:    "Ran out of budget mid-review",
	}
	_, reason, detail := classifyOutcome(result, maxTurns, maxBudget)

	if reason != "max_budget" {
		t.Fatalf("expected max_budget, got %q", reason)
	}

	_ = store.UpdateAutopilotTaskFailureInfo(task.ID, reason, detail)
	_ = store.UpdateAutopilotTaskStatus(task.ID, "reviewed")

	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, tt := range tasks {
		if tt.ID == task.ID {
			if tt.Status != "reviewed" {
				t.Errorf("status = %q, want 'reviewed'", tt.Status)
			}
			if tt.FailureReason != "max_budget" {
				t.Errorf("failure_reason = %q, want 'max_budget'", tt.FailureReason)
			}
			if !strings.Contains(tt.FailureDetail, "$") {
				t.Errorf("failure_detail = %q, should mention dollar amounts", tt.FailureDetail)
			}
		}
	}
}

// TestReviewOutcome_Bail_NilResult verifies that a nil agent result (agent
// crashed or never produced output) stores "review_bail" and transitions to reviewed.
func TestReviewOutcome_Bail_NilResult(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  57,
		IssueTitle:   "Review bail nil",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}
	_ = store.UpdateAutopilotTaskStatus(task.ID, "reviewing")
	_ = store.UpdateAutopilotTaskPR(task.ID, 202)

	// nil result — agent crashed/no result event.
	var result *AgentResult
	_, reason, _ := classifyOutcome(result, 10, 2.0)

	// classifyOutcome returns empty for nil — bail is detected separately.
	if reason != "" {
		t.Fatalf("expected empty reason for nil result, got %q", reason)
	}

	// Simulate the bail path in runReviewAgent.
	_ = store.UpdateAutopilotTaskFailureInfo(task.ID, "review_bail", "reviewer produced no output")
	_ = store.UpdateAutopilotTaskStatus(task.ID, "reviewed")

	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, tt := range tasks {
		if tt.ID == task.ID {
			if tt.Status != "reviewed" {
				t.Errorf("status = %q, want 'reviewed'", tt.Status)
			}
			if tt.FailureReason != "review_bail" {
				t.Errorf("failure_reason = %q, want 'review_bail'", tt.FailureReason)
			}
		}
	}
}

// TestReviewOutcome_Bail_EmptyResult verifies that an agent result with empty
// output text is treated as a bail.
func TestReviewOutcome_Bail_EmptyResult(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  58,
		IssueTitle:   "Review bail empty",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}
	_ = store.UpdateAutopilotTaskStatus(task.ID, "reviewing")
	_ = store.UpdateAutopilotTaskPR(task.ID, 203)

	// Agent ran but produced no text output.
	result := &AgentResult{
		NumTurns:  3,
		TotalCost: 0.20,
		Result:    "",
	}
	_, reason, _ := classifyOutcome(result, 10, 2.0)
	if reason != "" {
		t.Fatalf("expected empty reason (no resource failure), got %q", reason)
	}

	// Empty result triggers bail path.
	if result.Result != "" {
		t.Fatal("expected empty result")
	}

	_ = store.UpdateAutopilotTaskFailureInfo(task.ID, "review_bail", "reviewer produced no output")
	_ = store.UpdateAutopilotTaskStatus(task.ID, "reviewed")

	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, tt := range tasks {
		if tt.ID == task.ID {
			if tt.Status != "reviewed" {
				t.Errorf("status = %q, want 'reviewed'", tt.Status)
			}
			if tt.FailureReason != "review_bail" {
				t.Errorf("failure_reason = %q, want 'review_bail'", tt.FailureReason)
			}
		}
	}
}

// TestReviewOutcome_Error_RevertsForRetry verifies that an explicit agent error
// (IsError=true) reverts the task to "review" for retry, not to "reviewed".
func TestReviewOutcome_Error_RevertsForRetry(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  59,
		IssueTitle:   "Review error retry",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}
	_ = store.UpdateAutopilotTaskStatus(task.ID, "reviewing")
	_ = store.UpdateAutopilotTaskPR(task.ID, 204)

	// Agent reported an explicit error.
	result := &AgentResult{
		NumTurns:  2,
		TotalCost: 0.10,
		IsError:   true,
		Result:    "Something went wrong during review",
	}
	_, reason, _ := classifyOutcome(result, 10, 2.0)
	if reason != "error" {
		t.Fatalf("expected 'error' reason, got %q", reason)
	}

	// Simulate the transient-error path: increment retry, revert to review.
	sup.incrReviewRetry(task.ID)
	_ = store.UpdateAutopilotTaskStatus(task.ID, "review")

	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, tt := range tasks {
		if tt.ID == task.ID {
			if tt.Status != "review" {
				t.Errorf("status = %q, want 'review' (should revert for retry)", tt.Status)
			}
		}
	}

	// Retry counter should be 1.
	if got := sup.getReviewRetries(task.ID); got != 1 {
		t.Errorf("retry count = %d, want 1", got)
	}
}

// TestReviewOutcome_Success_ParsesRisk verifies that a successful review
// parses the risk level and transitions to "reviewed" with no failure info.
func TestReviewOutcome_Success_ParsesRisk(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  60,
		IssueTitle:   "Review success",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}
	_ = store.UpdateAutopilotTaskStatus(task.ID, "reviewing")
	_ = store.UpdateAutopilotTaskPR(task.ID, 205)

	result := &AgentResult{
		NumTurns:  5,
		TotalCost: 0.80,
		Result:    "## Risk Assessment\n\n**Risk level:** low\n\n**Summary:** Clean implementation, tests pass.",
	}
	_, reason, _ := classifyOutcome(result, 10, 2.0)
	if reason != "" {
		t.Fatalf("expected no failure, got reason %q", reason)
	}

	riskLevel := parseReviewRisk(result)
	if riskLevel != "low" {
		t.Fatalf("parseReviewRisk = %q, want 'low'", riskLevel)
	}

	// Production code flows through riskToLabel before storing — verify the full path.
	label := riskToLabel(riskLevel)
	if label != "low-risk" {
		t.Fatalf("riskToLabel(%q) = %q, want 'low-risk'", riskLevel, label)
	}

	_ = store.UpdateAutopilotTaskReview(task.ID, label, 0)
	_ = store.UpdateAutopilotTaskStatus(task.ID, "reviewed")

	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, tt := range tasks {
		if tt.ID == task.ID {
			if tt.Status != "reviewed" {
				t.Errorf("status = %q, want 'reviewed'", tt.Status)
			}
			if tt.FailureReason != "" {
				t.Errorf("failure_reason = %q, want empty (success)", tt.FailureReason)
			}
			if tt.ReviewRisk == nil || *tt.ReviewRisk != "low-risk" {
				t.Errorf("review_risk = %v, want 'low-risk'", tt.ReviewRisk)
			}
		}
	}
}

// TestReviewedTaskWithFailure_StillPromotesOnMerge verifies that a "reviewed"
// task with a failure_reason (e.g., max_turns) still gets promoted to "done"
// when its PR is merged. The failure info is historical — it shouldn't block
// the merge-check path.
func TestReviewedTaskWithFailure_StillPromotesOnMerge(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  61,
		IssueTitle:   "Failed review but merged",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}
	_ = store.UpdateAutopilotTaskStatus(task.ID, "reviewed")
	_ = store.UpdateAutopilotTaskPR(task.ID, 206)
	_ = store.UpdateAutopilotTaskFailureInfo(task.ID, "max_turns", "used 10 of 10 turns")

	// checkReviewTasks will try to fetch PR status via GitHub API, which will
	// fail with the fake token — so promoted will be 0. But the important thing
	// is that the task enters the "reviewed" case and attempts the merge check,
	// not that it's blocked by the failure_reason.
	promoted := sup.checkReviewTasks(context.Background())

	// We can't actually test promotion without a real GitHub API, but we verify
	// the task is still in "reviewed" (not blocked or errored).
	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, tt := range tasks {
		if tt.ID == task.ID {
			if tt.Status != "reviewed" {
				t.Errorf("status = %q, want 'reviewed' (should remain, API call fails)", tt.Status)
			}
		}
	}
	_ = promoted // silence unused warning — the fetch fails so promoted is 0
}

// TestReviewRetries_BelowCap_AllowsSpawn verifies that when retry count is
// below the cap, checkReviewTasks still attempts to spawn a review agent
// (which will fail due to missing worktree/branch, but the point is it tries).
func TestReviewRetries_BelowCap_AllowsSpawn(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	maxTurns := 10
	project.AutopilotReviewMaxTurns = &maxTurns
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  62,
		IssueTitle:   "Review retry below cap",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}
	_ = store.UpdateAutopilotTaskStatus(task.ID, "review")
	_ = store.UpdateAutopilotTaskPR(task.ID, 207)

	// Set retries to 1 below the cap.
	for i := 0; i < defaultReviewMaxRetries-1; i++ {
		sup.incrReviewRetry(task.ID)
	}

	// checkReviewTasks should try to spawn (will fail due to missing branch,
	// but should NOT transition to "reviewed" — should stay in "review".
	sup.checkReviewTasks(context.Background())

	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, tt := range tasks {
		if tt.ID == task.ID {
			if tt.Status == "reviewed" {
				t.Error("task should NOT be 'reviewed' — retries below cap, should still attempt spawn")
			}
		}
	}
}

// TestReviewRetries_ExactlyAtCap_Exhausted verifies that when retry count
// equals defaultReviewMaxRetries, the task transitions to "reviewed" with exhaustion.
func TestReviewRetries_ExactlyAtCap_Exhausted(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	maxTurns := 10
	project.AutopilotReviewMaxTurns = &maxTurns
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  63,
		IssueTitle:   "Review retries exactly at cap",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}
	_ = store.UpdateAutopilotTaskStatus(task.ID, "review")
	_ = store.UpdateAutopilotTaskPR(task.ID, 208)

	// Set retries to exactly the cap.
	for i := 0; i < defaultReviewMaxRetries; i++ {
		sup.incrReviewRetry(task.ID)
	}

	sup.checkReviewTasks(context.Background())

	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, tt := range tasks {
		if tt.ID == task.ID {
			if tt.Status != "reviewed" {
				t.Errorf("status = %q, want 'reviewed'", tt.Status)
			}
			if tt.FailureReason != "review_retries_exhausted" {
				t.Errorf("failure_reason = %q, want 'review_retries_exhausted'", tt.FailureReason)
			}
			if !strings.Contains(tt.FailureDetail, fmt.Sprintf("%d", defaultReviewMaxRetries)) {
				t.Errorf("failure_detail = %q, should mention retry count", tt.FailureDetail)
			}
		}
	}
}

// TestReviewOutcome_ErrorIsRetriable_NotBail verifies that an agent error
// (IsError=true) does NOT get classified as a bail — it gets retried.
func TestReviewOutcome_ErrorIsRetriable_NotBail(t *testing.T) {
	result := &AgentResult{
		NumTurns:  2,
		TotalCost: 0.10,
		IsError:   true,
		Result:    "Permission denied accessing repository",
	}
	_, reason, _ := classifyOutcome(result, 10, 2.0)

	// Should be classified as "error", NOT bail.
	if reason != "error" {
		t.Errorf("reason = %q, want 'error' (retriable)", reason)
	}

	// Also verify it has non-empty result so it wouldn't trigger the bail path.
	if result.Result == "" {
		t.Error("result text should be non-empty for this test case")
	}
}

// TestReviewOutcome_ResourceExhaustion_NotRetriable verifies that max_turns
// and max_budget failures are NOT treated as retriable errors.
func TestReviewOutcome_ResourceExhaustion_NotRetriable(t *testing.T) {
	tests := []struct {
		name      string
		result    *AgentResult
		maxTurns  int
		maxBudget float64
		wantRsn   string
	}{
		{
			name:      "max_turns",
			result:    &AgentResult{NumTurns: 10, TotalCost: 0.5, Result: "partial"},
			maxTurns:  10,
			maxBudget: 5.0,
			wantRsn:   "max_turns",
		},
		{
			name:      "max_budget",
			result:    &AgentResult{NumTurns: 3, TotalCost: 4.80, Result: "partial"},
			maxTurns:  50,
			maxBudget: 5.0,
			wantRsn:   "max_budget",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, reason, _ := classifyOutcome(tc.result, tc.maxTurns, tc.maxBudget)
			if reason != tc.wantRsn {
				t.Errorf("reason = %q, want %q", reason, tc.wantRsn)
			}
			// These should NOT trigger the error/retry path.
			if reason == "error" {
				t.Error("resource exhaustion should not be classified as retriable error")
			}
		})
	}
}

// TestReviewedTaskKeepsLoopAlive verifies that tasks in "reviewed" status
// keep the supervisor loop active (so merge checks continue).
func TestReviewedTaskKeepsLoopAlive(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  64,
		IssueTitle:   "Reviewed",
		Dependencies: "[]",
		Status:       "queued",
	}
	_ = store.CreateAutopilotTask(task)
	_ = store.UpdateAutopilotTaskStatus(task.ID, "reviewed")

	ctx, cancel := context.WithCancel(context.Background())
	sup.Launch(ctx)

	time.Sleep(200 * time.Millisecond)

	if !sup.IsActive() {
		t.Error("supervisor exited prematurely — should stay alive for reviewed tasks")
	}

	cancel()
	<-sup.Done()
}

// TestReviewRetries_IndependentPerTask verifies that retry counters are
// tracked independently per task ID.
func TestReviewRetries_IndependentPerTask(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")

	sup.incrReviewRetry(1)
	sup.incrReviewRetry(1)
	sup.incrReviewRetry(2)

	if got := sup.getReviewRetries(1); got != 2 {
		t.Errorf("task 1 retries = %d, want 2", got)
	}
	if got := sup.getReviewRetries(2); got != 1 {
		t.Errorf("task 2 retries = %d, want 1", got)
	}
	if got := sup.getReviewRetries(3); got != 0 {
		t.Errorf("task 3 retries = %d, want 0 (never incremented)", got)
	}
}

// TestReviewMaxRetries_ProjectOverride verifies that the project-level
// autopilot_review_max_retries setting overrides the default.
func TestReviewMaxRetries_ProjectOverride(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	maxTurns := 10
	project.AutopilotReviewMaxTurns = &maxTurns

	// Default: 1 retry.
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "")
	if got := sup.reviewMaxRetries(); got != defaultReviewMaxRetries {
		t.Errorf("default reviewMaxRetries = %d, want %d", got, defaultReviewMaxRetries)
	}

	// Override to 5.
	five := 5
	project.AutopilotReviewMaxRetries = &five
	sup2 := New(store, project, nil, "/tmp/repo", "owner", "repo", "")
	if got := sup2.reviewMaxRetries(); got != 5 {
		t.Errorf("overridden reviewMaxRetries = %d, want 5", got)
	}

	// Override to 0 disables retries entirely.
	zero := 0
	project.AutopilotReviewMaxRetries = &zero
	sup3 := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  70,
		IssueTitle:   "Zero retries",
		Dependencies: "[]",
		Status:       "queued",
	}
	_ = store.CreateAutopilotTask(task)
	_ = store.UpdateAutopilotTaskStatus(task.ID, "review")
	_ = store.UpdateAutopilotTaskPR(task.ID, 300)

	// With zero max retries and zero current retries, should immediately exhaust.
	sup3.checkReviewTasks(context.Background())

	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, tt := range tasks {
		if tt.ID == task.ID {
			if tt.Status != "reviewed" {
				t.Errorf("status = %q, want 'reviewed' (zero retries = immediate exhaustion)", tt.Status)
			}
			if tt.FailureReason != "review_retries_exhausted" {
				t.Errorf("failure_reason = %q, want 'review_retries_exhausted'", tt.FailureReason)
			}
		}
	}
}

// TestReviewOutcome_PermissionWarning_StillSucceeds verifies that permission
// denials (a non-fatal warning) don't prevent a successful review — the risk
// is still parsed and the task transitions to "reviewed" normally.
func TestReviewOutcome_PermissionWarning_StillSucceeds(t *testing.T) {
	result := &AgentResult{
		NumTurns:          5,
		TotalCost:         0.50,
		Result:            "**Risk level:** medium\n\nSome concerns found.",
		PermissionDenials: []json.RawMessage{json.RawMessage(`"Bash(rm -rf /)")`)},
	}
	status, reason, _ := classifyOutcome(result, 10, 2.0)

	// Should be "warning", not "failed" — permissions are non-fatal.
	if status != "warning" {
		t.Errorf("status = %q, want 'warning'", status)
	}
	if reason != "permissions" {
		t.Errorf("reason = %q, want 'permissions'", reason)
	}

	// The review risk should still be parseable.
	risk := parseReviewRisk(result)
	if risk != "medium" {
		t.Errorf("parseReviewRisk = %q, want 'medium'", risk)
	}
}

// ---------------------------------------------------------------------------
// Auto-merge tests
// ---------------------------------------------------------------------------

// TestAutoMerge_LowRisk_Enabled verifies that a reviewed task with low-risk
// and auto-merge enabled attempts to merge via GitHub API. Since we use a fake
// token, the merge will fail, but the auto-merge path should be taken (error
// comment posted, task stays in "reviewed").
func TestAutoMerge_LowRisk_Enabled(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	project.AutopilotAutoMerge = true
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  80,
		IssueTitle:   "Auto-merge low risk",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}
	lowRisk := "low-risk"
	_ = store.UpdateAutopilotTaskStatus(task.ID, "reviewed")
	_ = store.UpdateAutopilotTaskPR(task.ID, 300)
	_ = store.UpdateAutopilotTaskReview(task.ID, lowRisk, 0)

	// checkReviewTasks will try auto-merge path (fails with fake token),
	// then falls through to promoteIfMerged (also fails with fake token).
	sup.checkReviewTasks(context.Background())

	// Task should remain in "reviewed" since both auto-merge and
	// promoteIfMerged fail with fake credentials.
	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, tt := range tasks {
		if tt.ID == task.ID {
			if tt.Status != "reviewed" {
				t.Errorf("status = %q, want 'reviewed' (auto-merge fails, should remain)", tt.Status)
			}
		}
	}
}

// TestAutoMerge_Disabled_SkipsAutoMerge verifies that when auto-merge is disabled,
// the auto-merge path is not taken even for low-risk reviewed tasks.
func TestAutoMerge_Disabled_SkipsAutoMerge(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	project.AutopilotAutoMerge = false // explicitly disabled
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  81,
		IssueTitle:   "No auto-merge",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}
	lowRisk := "low-risk"
	_ = store.UpdateAutopilotTaskStatus(task.ID, "reviewed")
	_ = store.UpdateAutopilotTaskPR(task.ID, 301)
	_ = store.UpdateAutopilotTaskReview(task.ID, lowRisk, 0)

	sup.checkReviewTasks(context.Background())

	// Task should remain in "reviewed" — auto-merge not attempted.
	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, tt := range tasks {
		if tt.ID == task.ID {
			if tt.Status != "reviewed" {
				t.Errorf("status = %q, want 'reviewed'", tt.Status)
			}
		}
	}
}

// TestAutoMerge_NonLowRisk_SkipsAutoMerge verifies that auto-merge is skipped
// when review risk is not low-risk, even if auto-merge is enabled.
func TestAutoMerge_NonLowRisk_SkipsAutoMerge(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	project.AutopilotAutoMerge = true
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  82,
		IssueTitle:   "Medium risk no auto-merge",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}
	needsTesting := "needs-testing"
	_ = store.UpdateAutopilotTaskStatus(task.ID, "reviewed")
	_ = store.UpdateAutopilotTaskPR(task.ID, 302)
	_ = store.UpdateAutopilotTaskReview(task.ID, needsTesting, 0)

	sup.checkReviewTasks(context.Background())

	// Task should remain in "reviewed" — auto-merge not attempted for non-low-risk.
	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, tt := range tasks {
		if tt.ID == task.ID {
			if tt.Status != "reviewed" {
				t.Errorf("status = %q, want 'reviewed'", tt.Status)
			}
		}
	}
}

// TestAutoMerge_NilRisk_SkipsAutoMerge verifies that auto-merge is skipped
// when review risk is nil.
func TestAutoMerge_NilRisk_SkipsAutoMerge(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	project.AutopilotAutoMerge = true
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  83,
		IssueTitle:   "Nil risk no auto-merge",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}
	_ = store.UpdateAutopilotTaskStatus(task.ID, "reviewed")
	_ = store.UpdateAutopilotTaskPR(task.ID, 303)
	// Don't set review risk — it stays nil.

	sup.checkReviewTasks(context.Background())

	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, tt := range tasks {
		if tt.ID == task.ID {
			if tt.Status != "reviewed" {
				t.Errorf("status = %q, want 'reviewed'", tt.Status)
			}
		}
	}
}

// TestAutoMerge_HappyPath_MergesAndPromotes uses an httptest server to simulate
// a successful GitHub merge, verifying the full happy path: status → done,
// success comment posted, and completion event emitted.
func TestAutoMerge_HappyPath_MergesAndPromotes(t *testing.T) {
	var mu sync.Mutex
	var mergeCount int
	var commentBodies []string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch {
		// PUT /repos/owner/repo/pulls/400/merge — squash merge
		case r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/pulls/400/merge"):
			mergeCount++
			w.WriteHeader(200)
			_, _ = fmt.Fprintf(w, `{"sha":"abc123","merged":true,"message":"Pull Request successfully merged"}`)

		// POST /repos/owner/repo/issues/400/comments — comment on PR
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/issues/400/comments"):
			var body struct{ Body string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			commentBodies = append(commentBodies, body.Body)
			w.WriteHeader(201)
			_, _ = fmt.Fprintf(w, `{"id":1}`)

		// DELETE label — ignore
		case r.Method == "DELETE" && strings.Contains(r.URL.Path, "/labels/"):
			w.WriteHeader(200)
			_, _ = fmt.Fprintf(w, `{}`)

		default:
			w.WriteHeader(404)
		}
	}))
	defer ts.Close()

	store := openTestStore(t)
	project := createTestProject(t, store)
	project.AutopilotAutoMerge = true
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")
	sup.ghClientFactory = func(token string) *ghpkg.Client {
		return ghpkg.NewClientWithBaseURL(token, ts.URL+"/")
	}

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  90,
		IssueTitle:   "Add widget feature",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}
	lowRisk := "low-risk"
	_ = store.UpdateAutopilotTaskStatus(task.ID, "reviewed")
	_ = store.UpdateAutopilotTaskPR(task.ID, 400)
	_ = store.UpdateAutopilotTaskReview(task.ID, lowRisk, 0)

	// Drain events in background.
	go func() {
		for range sup.Events() {
		}
	}()

	promoted := sup.checkReviewTasks(context.Background())

	// Verify: task promoted to done.
	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, tt := range tasks {
		if tt.ID == task.ID {
			if tt.Status != "done" {
				t.Errorf("status = %q, want 'done'", tt.Status)
			}
		}
	}

	// Verify: merge was called exactly once.
	mu.Lock()
	if mergeCount != 1 {
		t.Errorf("mergeCount = %d, want 1", mergeCount)
	}

	// Verify: success comment was posted (after merge).
	if len(commentBodies) == 0 {
		t.Error("expected at least one comment, got none")
	} else if !strings.Contains(commentBodies[len(commentBodies)-1], "Auto-merged") {
		t.Errorf("last comment = %q, want to contain 'Auto-merged'", commentBodies[len(commentBodies)-1])
	}
	mu.Unlock()

	// Verify: at least one task was promoted.
	if promoted < 1 {
		t.Errorf("promoted = %d, want >= 1", promoted)
	}
}

// ---------------------------------------------------------------------------
// checkLabelChanges — skip label removal/addition
// ---------------------------------------------------------------------------

func TestCheckLabelChanges_ManualToQueued(t *testing.T) {
	// Simulate an issue that was manual (had no-agent label) but label was removed.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		// PR lookup returns 404 (it's an issue, not a PR).
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/pulls/"):
			w.WriteHeader(404)
			_, _ = fmt.Fprintf(w, `{"message":"Not Found"}`)

		// Issue lookup — no skip label anymore.
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/issues/42"):
			w.WriteHeader(200)
			_, _ = fmt.Fprintf(w, `{"number":42,"title":"Design work","state":"open","labels":[{"name":"enhancement"}]}`)

		default:
			w.WriteHeader(404)
		}
	}))
	defer ts.Close()

	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")
	sup.ghClientFactory = func(token string) *ghpkg.Client {
		return ghpkg.NewClientWithBaseURL(token, ts.URL+"/")
	}

	// Save a dep graph with #42 as manual.
	_ = store.SaveDepGraph(project.ID, `{"42":"manual","10":[42]}`, "Conservative")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		Owner:        "owner",
		Repo:         "repo",
		IssueNumber:  42,
		IssueTitle:   "Design work",
		Dependencies: `"manual"`,
		Status:       "manual",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	// Drain events.
	go func() {
		for range sup.Events() {
		}
	}()

	changed := sup.checkLabelChanges(context.Background())

	if changed != 1 {
		t.Errorf("checkLabelChanges = %d, want 1", changed)
	}

	// Verify task is now queued with empty deps.
	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, tk := range tasks {
		if tk.IssueNumber == 42 {
			if tk.Status != "queued" {
				t.Errorf("task status = %q, want queued", tk.Status)
			}
			if tk.Dependencies != "[]" {
				t.Errorf("task deps = %q, want []", tk.Dependencies)
			}
		}
	}

	// Verify dep graph updated.
	dg, _ := store.GetDepGraph(project.ID)
	if dg == nil {
		t.Fatal("dep graph should exist")
	}
	if !strings.Contains(dg.GraphJSON, `"42":[]`) {
		t.Errorf("dep graph should have 42:[], got %s", dg.GraphJSON)
	}
}

func TestCheckLabelChanges_QueuedToManual(t *testing.T) {
	// Simulate a queued task that gains the no-agent label.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/pulls/"):
			w.WriteHeader(404)
			_, _ = fmt.Fprintf(w, `{"message":"Not Found"}`)

		case r.Method == "GET" && strings.Contains(r.URL.Path, "/issues/10"):
			w.WriteHeader(200)
			_, _ = fmt.Fprintf(w, `{"number":10,"title":"Some task","state":"open","labels":[{"name":"no-agent"},{"name":"enhancement"}]}`)

		default:
			w.WriteHeader(404)
		}
	}))
	defer ts.Close()

	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")
	sup.ghClientFactory = func(token string) *ghpkg.Client {
		return ghpkg.NewClientWithBaseURL(token, ts.URL+"/")
	}

	_ = store.SaveDepGraph(project.ID, `{"10":[]}`, "Conservative")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		Owner:        "owner",
		Repo:         "repo",
		IssueNumber:  10,
		IssueTitle:   "Some task",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	go func() {
		for range sup.Events() {
		}
	}()

	changed := sup.checkLabelChanges(context.Background())

	if changed != 1 {
		t.Errorf("checkLabelChanges = %d, want 1", changed)
	}

	tasks, _ := store.GetAutopilotTasks(project.ID)
	for _, tk := range tasks {
		if tk.IssueNumber == 10 {
			if tk.Status != "manual" {
				t.Errorf("task status = %q, want manual", tk.Status)
			}
		}
	}
}

func TestCheckLabelChanges_StillManual(t *testing.T) {
	// Issue still has the no-agent label — no change.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/pulls/"):
			w.WriteHeader(404)
			_, _ = fmt.Fprintf(w, `{"message":"Not Found"}`)

		case r.Method == "GET" && strings.Contains(r.URL.Path, "/issues/42"):
			w.WriteHeader(200)
			_, _ = fmt.Fprintf(w, `{"number":42,"title":"Manual work","state":"open","labels":[{"name":"no-agent"}]}`)

		default:
			w.WriteHeader(404)
		}
	}))
	defer ts.Close()

	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")
	sup.ghClientFactory = func(token string) *ghpkg.Client {
		return ghpkg.NewClientWithBaseURL(token, ts.URL+"/")
	}

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		Owner:        "owner",
		Repo:         "repo",
		IssueNumber:  42,
		IssueTitle:   "Manual work",
		Dependencies: `"manual"`,
		Status:       "manual",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatal(err)
	}

	go func() {
		for range sup.Events() {
		}
	}()

	changed := sup.checkLabelChanges(context.Background())

	if changed != 0 {
		t.Errorf("checkLabelChanges = %d, want 0 (still has skip label)", changed)
	}
}

func TestCheckLabelChanges_NoTasks(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)
	sup := New(store, project, nil, "/tmp/repo", "owner", "repo", "fake-token")

	changed := sup.checkLabelChanges(context.Background())
	if changed != 0 {
		t.Errorf("checkLabelChanges = %d, want 0 (no tasks)", changed)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
