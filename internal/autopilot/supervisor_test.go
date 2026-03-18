package autopilot

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dustinlange/agent-minder/internal/db"
	_ "modernc.org/sqlite"
)

// openTestStore creates an in-memory test database with the full schema.
func openTestStore(t *testing.T) *db.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	conn, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return db.NewStore(conn)
}

// createTestProject creates a minimal project for testing.
func createTestProject(t *testing.T, store *db.Store) *db.Project {
	t.Helper()
	p := &db.Project{
		Name:               "test-proj",
		GoalType:           "feature",
		GoalDescription:    "test",
		RefreshIntervalSec: 300,
		MessageTTLSec:      172800,
		LLMProvider:        "anthropic",
		LLMModel:           "claude-haiku-4-5",
		LLMSummarizerModel: "claude-haiku-4-5",
		LLMAnalyzerModel:   "claude-sonnet-4-6",
		AutopilotMaxAgents: 2,
	}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return p
}

func TestFillSlots_CancelledContext(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)

	sup := New(store, project, nil, "/tmp/fake-repo", "owner", "repo", "")

	// Create a queued, unblocked task.
	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  42,
		IssueTitle:   "Test issue",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatalf("CreateAutopilotTask: %v", err)
	}

	// Verify the task is queued and unblocked.
	unblocked, err := store.QueuedUnblockedTasks(project.ID)
	if err != nil {
		t.Fatalf("QueuedUnblockedTasks: %v", err)
	}
	if len(unblocked) != 1 {
		t.Fatalf("expected 1 unblocked task, got %d", len(unblocked))
	}

	// Call fillSlots with a cancelled context — should NOT launch the agent.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	sup.fillSlots(ctx)

	// All slots should still be nil (no agent launched).
	sup.mu.Lock()
	for i, slot := range sup.slots {
		if slot != nil {
			t.Errorf("slot %d should be nil after fillSlots with cancelled context, got task #%d", i, slot.task.IssueNumber)
		}
	}
	sup.mu.Unlock()

	// Task should still be queued (not running).
	tasks, err := store.GetAutopilotTasks(project.ID)
	if err != nil {
		t.Fatalf("GetAutopilotTasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Status != "queued" {
		t.Errorf("task status = %q, want queued", tasks[0].Status)
	}
}

func TestFillSlots_Paused(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)

	sup := New(store, project, nil, "/tmp/fake-repo", "owner", "repo", "")

	// Create a queued, unblocked task.
	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  10,
		IssueTitle:   "Paused test",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatalf("CreateAutopilotTask: %v", err)
	}

	// Pause the supervisor.
	sup.mu.Lock()
	sup.paused = true
	sup.mu.Unlock()

	// fillSlots with a live context should still not launch (paused).
	ctx := context.Background()
	sup.fillSlots(ctx)

	sup.mu.Lock()
	for i, slot := range sup.slots {
		if slot != nil {
			t.Errorf("slot %d should be nil when paused, got task #%d", i, slot.task.IssueNumber)
		}
	}
	sup.mu.Unlock()

	// Task should still be queued.
	tasks, err := store.GetAutopilotTasks(project.ID)
	if err != nil {
		t.Fatalf("GetAutopilotTasks: %v", err)
	}
	if tasks[0].Status != "queued" {
		t.Errorf("task status = %q, want queued", tasks[0].Status)
	}
}

func TestPauseResume(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)

	sup := New(store, project, nil, "/tmp/fake-repo", "owner", "repo", "")

	if sup.IsPaused() {
		t.Error("supervisor should not be paused initially")
	}

	sup.Pause()
	if !sup.IsPaused() {
		t.Error("supervisor should be paused after Pause()")
	}

	// Pause again should be idempotent.
	sup.Pause()
	if !sup.IsPaused() {
		t.Error("supervisor should still be paused after double Pause()")
	}

	sup.Resume(context.Background())
	if sup.IsPaused() {
		t.Error("supervisor should not be paused after Resume()")
	}

	// Resume again should be idempotent.
	sup.Resume(context.Background())
	if sup.IsPaused() {
		t.Error("supervisor should still not be paused after double Resume()")
	}
}

func TestStopAgent_InvalidSlot(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)

	sup := New(store, project, nil, "/tmp/fake-repo", "owner", "repo", "")

	// These should not panic.
	sup.StopAgent(-1)
	sup.StopAgent(0)   // nil slot
	sup.StopAgent(100) // out of range
}

func TestStopAgent_SetsStoppedByUser(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)

	sup := New(store, project, nil, "/tmp/fake-repo", "owner", "repo", "")

	// Manually place a fake slot state.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sup.mu.Lock()
	sup.slots[0] = &slotState{
		task: &db.AutopilotTask{
			IssueNumber: 99,
			IssueTitle:  "Test",
		},
		cancelFunc: cancel,
	}
	sup.mu.Unlock()

	sup.StopAgent(0)

	sup.mu.Lock()
	if !sup.slots[0].stoppedByUser {
		t.Error("stoppedByUser should be true after StopAgent")
	}
	sup.mu.Unlock()

	// Context should be cancelled.
	if ctx.Err() == nil {
		t.Error("slot context should be cancelled after StopAgent")
	}
}

func TestRestartTask_WrongStatus(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)

	sup := New(store, project, nil, "/tmp/fake-repo", "owner", "repo", "")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  5,
		IssueTitle:   "Running task",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatalf("CreateAutopilotTask: %v", err)
	}

	err := sup.RestartTask(context.Background(), task.ID)
	if err == nil {
		t.Error("RestartTask should fail for queued task")
	}
}

func TestRestartTask_Requeues(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)

	sup := New(store, project, nil, "/tmp/fake-repo", "owner", "repo", "")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  7,
		IssueTitle:   "Stopped task",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatalf("CreateAutopilotTask: %v", err)
	}

	// Mark it as stopped.
	if err := store.UpdateAutopilotTaskStatus(task.ID, "stopped"); err != nil {
		t.Fatalf("UpdateAutopilotTaskStatus: %v", err)
	}

	// Restart with a cancelled context so fillSlots doesn't try to launch.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := sup.RestartTask(ctx, task.ID); err != nil {
		t.Fatalf("RestartTask: %v", err)
	}

	// Task should be back to queued with cleared fields.
	tasks, err := store.GetAutopilotTasks(project.ID)
	if err != nil {
		t.Fatalf("GetAutopilotTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Status != "queued" {
		t.Errorf("status = %q, want queued", tasks[0].Status)
	}
	if tasks[0].WorktreePath != "" {
		t.Errorf("worktree_path should be cleared, got %q", tasks[0].WorktreePath)
	}
	if tasks[0].Branch != "" {
		t.Errorf("branch should be cleared, got %q", tasks[0].Branch)
	}
	if tasks[0].CompletedAt != "" {
		t.Errorf("completed_at should be cleared, got %q", tasks[0].CompletedAt)
	}
}

func TestRestartTask_FailedRequeues(t *testing.T) {
	store := openTestStore(t)
	project := createTestProject(t, store)

	sup := New(store, project, nil, "/tmp/fake-repo", "owner", "repo", "")

	task := &db.AutopilotTask{
		ProjectID:    project.ID,
		IssueNumber:  8,
		IssueTitle:   "Failed task",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatalf("CreateAutopilotTask: %v", err)
	}

	// Mark it as failed.
	if err := store.UpdateAutopilotTaskStatus(task.ID, "failed"); err != nil {
		t.Fatalf("UpdateAutopilotTaskStatus: %v", err)
	}

	// Restart with a cancelled context so fillSlots doesn't try to launch.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := sup.RestartTask(ctx, task.ID); err != nil {
		t.Fatalf("RestartTask: %v", err)
	}

	tasks, err := store.GetAutopilotTasks(project.ID)
	if err != nil {
		t.Fatalf("GetAutopilotTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Status != "queued" {
		t.Errorf("status = %q, want queued", tasks[0].Status)
	}
}
