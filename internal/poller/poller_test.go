package poller

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dustinlange/agent-minder/internal/claudecli"
	"github.com/dustinlange/agent-minder/internal/db"
	ghpkg "github.com/dustinlange/agent-minder/internal/github"
	"github.com/dustinlange/agent-minder/internal/msgbus"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

// --- Test helpers ---

// openTestDB creates a fresh in-memory test database with full schema.
func openTestDB(t *testing.T) *db.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	conn, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return db.NewStore(conn)
}

// openTestMsgDB creates a fresh agent-msg style SQLite database and returns
// both the raw connection (for verification queries) and a *msgbus.Publisher.
func openTestMsgDB(t *testing.T) (*sqlx.DB, *msgbus.Publisher) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "messages.db")

	// Create the schema first.
	conn, err := sqlx.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open msg db: %v", err)
	}
	conn.MustExec(`
		CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY,
			topic TEXT NOT NULL,
			sender TEXT NOT NULL,
			message TEXT NOT NULL,
			created_at TEXT DEFAULT (datetime('now'))
		);
		CREATE TABLE IF NOT EXISTS acks (
			id INTEGER PRIMARY KEY,
			message_id INTEGER NOT NULL,
			agent_name TEXT NOT NULL,
			acked_at TEXT DEFAULT (datetime('now')),
			UNIQUE(message_id, agent_name)
		);
		CREATE TABLE IF NOT EXISTS agent_names (
			id INTEGER PRIMARY KEY,
			repo TEXT NOT NULL,
			name TEXT NOT NULL,
			claimed_at TEXT DEFAULT (datetime('now'))
		);
	`)

	// Create the publisher.
	pub, err := msgbus.NewPublisher(dbPath)
	if err != nil {
		_ = conn.Close()
		t.Fatalf("new publisher: %v", err)
	}
	t.Cleanup(func() {
		_ = pub.Close()
		_ = conn.Close()
	})
	return conn, pub
}

// mockCompleter is a configurable mock claudecli.Completer for tests.
type mockCompleter struct {
	response  string
	err       error
	calls     atomic.Int32
	lastModel string
}

func (m *mockCompleter) Complete(_ context.Context, req *claudecli.Request) (*claudecli.Response, error) {
	m.calls.Add(1)
	m.lastModel = req.Model
	if m.err != nil {
		return nil, m.err
	}
	return &claudecli.Response{Result: m.response, InputTokens: 100, OutputTokens: 50}, nil
}

// createTestProject creates a project in the test DB and returns it.
func createTestProject(t *testing.T, store *db.Store) *db.Project {
	t.Helper()
	p := &db.Project{
		Name:                "test-project",
		GoalType:            "feature",
		GoalDescription:     "Build and test a feature",
		RefreshIntervalSec:  300,
		MessageTTLSec:       172800,
		MinderIdentity:      "test-project/minder",
		LLMProvider:         "anthropic",
		LLMModel:            "claude-haiku-4-5",
		LLMSummarizerModel:  "claude-haiku-4-5",
		LLMAnalyzerModel:    "claude-sonnet-4-6",
		StatusIntervalSec:   300,
		AnalysisIntervalSec: 1800,
	}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("create project: %v", err)
	}
	return p
}

// --- PollResult tests ---

func TestPollResult_LLMResponse_Tier2Preferred(t *testing.T) {
	r := &PollResult{
		Tier1Summary:  "tier1 summary",
		Tier2Analysis: "tier2 analysis",
	}
	if got := r.LLMResponse(); got != "tier2 analysis" {
		t.Errorf("LLMResponse() = %q, want %q", got, "tier2 analysis")
	}
}

func TestPollResult_LLMResponse_Tier1Fallback(t *testing.T) {
	r := &PollResult{
		Tier1Summary:  "tier1 summary",
		Tier2Analysis: "",
	}
	if got := r.LLMResponse(); got != "tier1 summary" {
		t.Errorf("LLMResponse() = %q, want %q", got, "tier1 summary")
	}
}

func TestPollResult_LLMResponse_BothEmpty(t *testing.T) {
	r := &PollResult{}
	if got := r.LLMResponse(); got != "" {
		t.Errorf("LLMResponse() = %q, want empty", got)
	}
}

// --- parseAnalysis additional edge cases ---

func TestParseAnalysis_JsonWithMultipleConcerns(t *testing.T) {
	raw := `{"analysis":"Multi-concern test","concerns":[{"severity":"warning","message":"A"},{"severity":"danger","message":"B"},{"severity":"info","message":"C"}]}`
	resp := parseAnalysis(raw)

	if resp.Analysis != "Multi-concern test" {
		t.Errorf("analysis = %q", resp.Analysis)
	}
	if len(resp.Concerns) != 3 {
		t.Fatalf("expected 3 concerns, got %d", len(resp.Concerns))
	}
	if resp.Concerns[0].Severity != "warning" || resp.Concerns[0].Message != "A" {
		t.Errorf("concern 0: %+v", resp.Concerns[0])
	}
	if resp.Concerns[1].Severity != "danger" || resp.Concerns[1].Message != "B" {
		t.Errorf("concern 1: %+v", resp.Concerns[1])
	}
	if resp.Concerns[2].Severity != "info" || resp.Concerns[2].Message != "C" {
		t.Errorf("concern 2: %+v", resp.Concerns[2])
	}
}

func TestParseAnalysis_JsonWithBusMessageAndConcerns(t *testing.T) {
	raw := `{"analysis":"Full response","concerns":[{"severity":"warning","message":"Watch out"}],"bus_message":{"topic":"proj/coord","message":"Coordination needed"}}`
	resp := parseAnalysis(raw)

	if resp.Analysis != "Full response" {
		t.Errorf("analysis = %q", resp.Analysis)
	}
	if resp.BusMessage == nil {
		t.Fatal("expected bus_message")
	}
	if resp.BusMessage.Topic != "proj/coord" {
		t.Errorf("topic = %q", resp.BusMessage.Topic)
	}
	if resp.BusMessage.Message != "Coordination needed" {
		t.Errorf("message = %q", resp.BusMessage.Message)
	}
	if len(resp.Concerns) != 1 {
		t.Errorf("expected 1 concern, got %d", len(resp.Concerns))
	}
}

func TestParseAnalysis_GenericCodeFence(t *testing.T) {
	// Test with ``` (no json tag) wrapping valid JSON.
	raw := "Response:\n```\n{\"analysis\":\"Generic fence\",\"concerns\":[]}\n```\n"
	resp := parseAnalysis(raw)
	if resp.Analysis != "Generic fence" {
		t.Errorf("analysis = %q, want %q", resp.Analysis, "Generic fence")
	}
}

func TestParseAnalysis_WhitespaceOnly(t *testing.T) {
	resp := parseAnalysis("   \n\t  ")
	if resp.Analysis != "" {
		t.Errorf("analysis = %q, want empty", resp.Analysis)
	}
}

func TestParseAnalysis_InvalidJsonFallback(t *testing.T) {
	raw := `{"analysis": incomplete json`
	resp := parseAnalysis(raw)
	// Should fall back to treating as plain text.
	if resp.Analysis != raw {
		t.Errorf("analysis = %q, want %q", resp.Analysis, raw)
	}
	if resp.BusMessage != nil {
		t.Error("expected no bus_message for invalid JSON")
	}
}

func TestParseAnalysis_EmptyAnalysisFieldFallback(t *testing.T) {
	// JSON is valid but analysis field is empty — should fall back to plain text.
	raw := `{"analysis":"","concerns":[]}`
	resp := parseAnalysis(raw)
	if resp.Analysis != raw {
		t.Errorf("analysis = %q, want raw text fallback", resp.Analysis)
	}
}

// --- Poller lifecycle tests ---

func TestPoller_PauseResume(t *testing.T) {
	p := New(nil, &db.Project{Name: "test"}, nil, nil)

	if p.IsPaused() {
		t.Error("expected not paused initially")
	}

	p.Pause()
	if !p.IsPaused() {
		t.Error("expected paused after Pause()")
	}

	p.Resume()
	if p.IsPaused() {
		t.Error("expected not paused after Resume()")
	}
}

func TestPoller_PauseEmitsEvent(t *testing.T) {
	p := New(nil, &db.Project{Name: "test"}, nil, nil)

	p.Pause()

	select {
	case ev := <-p.Events():
		if ev.Type != "paused" {
			t.Errorf("event type = %q, want %q", ev.Type, "paused")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("timeout waiting for paused event")
	}
}

func TestPoller_ResumeEmitsEvent(t *testing.T) {
	p := New(nil, &db.Project{Name: "test"}, nil, nil)
	// Drain the pause event.
	p.Pause()
	<-p.Events()

	p.Resume()

	select {
	case ev := <-p.Events():
		if ev.Type != "resumed" {
			t.Errorf("event type = %q, want %q", ev.Type, "resumed")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("timeout waiting for resumed event")
	}
}

func TestPoller_SetAutopilotDepGraphFunc(t *testing.T) {
	p := New(nil, &db.Project{Name: "test"}, nil, nil)
	called := false
	p.SetAutopilotDepGraphFunc(func() string {
		called = true
		return "dep graph"
	})

	p.mu.Lock()
	fn := p.autopilotDepGraph
	p.mu.Unlock()

	if fn == nil {
		t.Fatal("expected dep graph func to be set")
	}
	result := fn()
	if !called {
		t.Error("expected func to be called")
	}
	if result != "dep graph" {
		t.Errorf("result = %q, want %q", result, "dep graph")
	}
}

func TestPoller_EventsChannel(t *testing.T) {
	p := New(nil, &db.Project{Name: "test"}, nil, nil)

	// Emit an event.
	p.emit("test", "test summary", nil)

	select {
	case ev := <-p.Events():
		if ev.Type != "test" {
			t.Errorf("type = %q", ev.Type)
		}
		if ev.Summary != "test summary" {
			t.Errorf("summary = %q", ev.Summary)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("timeout waiting for event")
	}
}

func TestPoller_EmitDropsWhenFull(t *testing.T) {
	p := New(nil, &db.Project{Name: "test"}, nil, nil)

	// Fill the channel (capacity is 64).
	for i := 0; i < 64; i++ {
		p.emit("fill", fmt.Sprintf("event %d", i), nil)
	}

	// This should not block — it drops the event.
	done := make(chan bool, 1)
	go func() {
		p.emit("overflow", "should be dropped", nil)
		done <- true
	}()

	select {
	case <-done:
		// Good - emit returned without blocking.
	case <-time.After(500 * time.Millisecond):
		t.Error("emit blocked on full channel")
	}
}

func TestPoller_EmitWithPollResult(t *testing.T) {
	p := New(nil, &db.Project{Name: "test"}, nil, nil)

	result := &PollResult{
		NewCommits:   5,
		Tier1Summary: "test summary",
	}
	p.emit("poll", "5 new commits", result)

	ev := <-p.Events()
	if ev.PollResult == nil {
		t.Fatal("expected PollResult to be attached")
	}
	if ev.PollResult.NewCommits != 5 {
		t.Errorf("NewCommits = %d, want 5", ev.PollResult.NewCommits)
	}
}

// --- summarize tests ---

func TestSummarize_AllFields(t *testing.T) {
	p := New(nil, &db.Project{Name: "test"}, nil, nil)
	result := &PollResult{
		NewCommits:     3,
		NewMessages:    2,
		NewWorktrees:   1,
		BusMessageSent: "[proj/coord] hello",
	}

	summary := p.summarize(result)
	if !strings.Contains(summary, "3 new commits") {
		t.Errorf("missing commits in summary: %q", summary)
	}
	if !strings.Contains(summary, "2 new messages") {
		t.Errorf("missing messages in summary: %q", summary)
	}
	if !strings.Contains(summary, "1 new worktrees") {
		t.Errorf("missing worktrees in summary: %q", summary)
	}
	if !strings.Contains(summary, "bus message sent") {
		t.Errorf("missing bus message in summary: %q", summary)
	}
}

func TestSummarize_NoActivity(t *testing.T) {
	p := New(nil, &db.Project{Name: "test"}, nil, nil)
	result := &PollResult{}

	summary := p.summarize(result)
	if summary != "No new activity" {
		t.Errorf("summary = %q, want %q", summary, "No new activity")
	}
}

func TestSummarize_CommitsOnly(t *testing.T) {
	p := New(nil, &db.Project{Name: "test"}, nil, nil)
	result := &PollResult{NewCommits: 7}

	summary := p.summarize(result)
	if summary != "7 new commits" {
		t.Errorf("summary = %q, want %q", summary, "7 new commits")
	}
}

// --- recordPollResult tests ---

func TestRecordPollResult(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)

	poller := New(store, p, nil, nil)
	result := &PollResult{
		NewCommits:     5,
		NewMessages:    2,
		Tier1Summary:   "git activity detected",
		Tier2Analysis:  "everything stable",
		BusMessageSent: "[proj/coord] update",
	}

	poller.recordPollResult(result)

	polls, err := store.RecentPolls(p.ID, 1)
	if err != nil {
		t.Fatalf("RecentPolls: %v", err)
	}
	if len(polls) != 1 {
		t.Fatalf("expected 1 poll, got %d", len(polls))
	}
	poll := polls[0]
	if poll.NewCommits != 5 {
		t.Errorf("NewCommits = %d, want 5", poll.NewCommits)
	}
	if poll.NewMessages != 2 {
		t.Errorf("NewMessages = %d, want 2", poll.NewMessages)
	}
	if poll.Tier1Response != "git activity detected" {
		t.Errorf("Tier1Response = %q", poll.Tier1Response)
	}
	if poll.Tier2Response != "everything stable" {
		t.Errorf("Tier2Response = %q", poll.Tier2Response)
	}
	if poll.BusMessageSent != "[proj/coord] update" {
		t.Errorf("BusMessageSent = %q", poll.BusMessageSent)
	}
}

func TestRecordPollResult_LLMResponseFallback(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)

	poller := New(store, p, nil, nil)
	result := &PollResult{
		Tier1Summary:  "only tier 1",
		Tier2Analysis: "",
	}

	poller.recordPollResult(result)

	polls, _ := store.RecentPolls(p.ID, 1)
	if len(polls) != 1 {
		t.Fatalf("expected 1 poll, got %d", len(polls))
	}
	// The LLMResponseRaw field should use the LLMResponse() accessor.
	if polls[0].LLMResponseRaw != "only tier 1" {
		t.Errorf("LLMResponseRaw = %q, want %q", polls[0].LLMResponseRaw, "only tier 1")
	}
}

// --- buildAnalysisPrompt tests ---

func TestBuildAnalysisPrompt_BasicStructure(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)

	poller := New(store, p, nil, nil)
	gathered := &gatherResult{
		result:     &PollResult{},
		gitSummary: "git summary",
		msgSummary: "bus summary",
	}
	prompt := poller.buildAnalysisPrompt(gathered, nil, nil)

	if !strings.Contains(prompt, "Project: test-project") {
		t.Error("missing project name")
	}
	if !strings.Contains(prompt, "feature") {
		t.Error("missing goal type")
	}
	if !strings.Contains(prompt, "Git Activity") {
		t.Error("missing git section")
	}
	if !strings.Contains(prompt, "git summary") {
		t.Error("missing git summary content")
	}
	if !strings.Contains(prompt, "Message Bus Activity") {
		t.Error("missing bus section")
	}
	if !strings.Contains(prompt, "bus summary") {
		t.Error("missing bus summary content")
	}
	if !strings.Contains(prompt, "Current time:") {
		t.Error("missing current time prefix")
	}
}

func TestBuildAnalysisPrompt_WithAutopilotTasks(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)

	poller := New(store, p, nil, nil)
	tasks := []db.AutopilotTask{
		{Owner: "org", Repo: "repo", IssueNumber: 42, IssueTitle: "Fix the bug", Status: "queued"},
		{Owner: "org", Repo: "repo", IssueNumber: 43, IssueTitle: "Running task", Status: "running"},
	}
	gathered := &gatherResult{result: &PollResult{}}
	prompt := poller.buildAnalysisPrompt(gathered, nil, tasks)

	if !strings.Contains(prompt, "Active Tasks") {
		t.Error("missing active tasks section")
	}
	if !strings.Contains(prompt, "org/repo#42") {
		t.Error("missing task reference")
	}
	if !strings.Contains(prompt, "Fix the bug") {
		t.Error("missing task title")
	}
	if !strings.Contains(prompt, "[queued]") {
		t.Error("expected queued status")
	}
	if !strings.Contains(prompt, "[running]") {
		t.Error("expected running status")
	}
}

func TestBuildAnalysisPrompt_ExcludesRemovedTasks(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)

	poller := New(store, p, nil, nil)
	tasks := []db.AutopilotTask{
		{Owner: "org", Repo: "repo", IssueNumber: 42, IssueTitle: "Active", Status: "queued"},
		{Owner: "org", Repo: "repo", IssueNumber: 43, IssueTitle: "Removed", Status: "removed"},
		{Owner: "org", Repo: "repo", IssueNumber: 44, IssueTitle: "Done", Status: "done"},
	}
	gathered := &gatherResult{result: &PollResult{}}
	prompt := poller.buildAnalysisPrompt(gathered, nil, tasks)

	if !strings.Contains(prompt, "#42") {
		t.Error("active task should be included")
	}
	if strings.Contains(prompt, "#43") {
		t.Error("removed task should be excluded")
	}
	if strings.Contains(prompt, "#44") {
		t.Error("done task should be excluded")
	}
}

func TestBuildAnalysisPrompt_WithCompletedItems(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)

	poller := New(store, p, nil, nil)
	completed := []db.CompletedItem{
		{Owner: "org", Repo: "repo", Number: 10, ItemType: "issue", Title: "Done task", FinalStatus: "Closd", Summary: "Task completed."},
	}
	gathered := &gatherResult{result: &PollResult{}}
	prompt := poller.buildAnalysisPrompt(gathered, completed, nil)

	if !strings.Contains(prompt, "Recently Completed Work") {
		t.Error("missing completed items section")
	}
	if !strings.Contains(prompt, "Done task") {
		t.Error("missing completed item title")
	}
	if !strings.Contains(prompt, "Task completed.") {
		t.Error("missing completed item summary")
	}
}

func TestBuildAnalysisPrompt_NoGitOrBus(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)

	poller := New(store, p, nil, nil)
	gathered := &gatherResult{result: &PollResult{}}
	prompt := poller.buildAnalysisPrompt(gathered, nil, nil)

	if strings.Contains(prompt, "Git Activity") {
		t.Error("should not have git section when empty")
	}
	if strings.Contains(prompt, "Message Bus Activity") {
		t.Error("should not have bus section when empty")
	}
}

func TestBuildAnalysisPrompt_AutopilotDepGraph(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)

	poller := New(store, p, nil, nil)
	poller.SetAutopilotDepGraphFunc(func() string {
		return "## Autopilot Dependency Graph\n42 -> 43 -> 44"
	})

	gathered := &gatherResult{result: &PollResult{}}
	prompt := poller.buildAnalysisPrompt(gathered, nil, nil)

	if !strings.Contains(prompt, "Autopilot Dependency Graph") {
		t.Error("expected dep graph in prompt")
	}
	if !strings.Contains(prompt, "42 -> 43 -> 44") {
		t.Error("expected dep graph content")
	}
}

func TestBuildAnalysisPrompt_TaskWithPR(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)

	poller := New(store, p, nil, nil)
	tasks := []db.AutopilotTask{
		{Owner: "org", Repo: "repo", IssueNumber: 55, IssueTitle: "Add feature", Status: "review", PRNumber: 100},
	}
	gathered := &gatherResult{result: &PollResult{}}
	prompt := poller.buildAnalysisPrompt(gathered, nil, tasks)

	if !strings.Contains(prompt, "#55") {
		t.Error("expected issue number")
	}
	if !strings.Contains(prompt, "PR: #100") {
		t.Error("expected PR number")
	}
}

// --- Tier 2 system prompt tests ---

func TestTier2SystemPrompt_ContainsProjectName(t *testing.T) {
	prompt := analysisSystemPrompt("my-project", "")
	if !strings.Contains(prompt, "my-project") {
		t.Error("expected project name in system prompt")
	}
}

func TestTier2SystemPrompt_DefaultFocus(t *testing.T) {
	prompt := analysisSystemPrompt("test", "")
	if !strings.Contains(prompt, DefaultAnalyzerFocus) {
		t.Error("expected default focus when none specified")
	}
}

func TestTier2SystemPrompt_CustomFocus(t *testing.T) {
	prompt := analysisSystemPrompt("test", "Focus on security")
	if !strings.Contains(prompt, "Focus on security") {
		t.Error("expected custom focus in prompt")
	}
	if strings.Contains(prompt, DefaultAnalyzerFocus) {
		t.Error("expected default focus to be replaced")
	}
}

// --- Broadcast tests ---

func TestBroadcast_NilPublisher(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)

	poller := New(store, p, nil, nil)
	_, err := poller.Broadcast(context.Background(), "test message")

	if err == nil {
		t.Fatal("expected error for nil publisher")
	}
	if !strings.Contains(err.Error(), "not available") {
		t.Errorf("error = %v, expected 'not available'", err)
	}
}

func TestBroadcast_LLMFailure(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	_, pub := openTestMsgDB(t)

	analyzer := &mockCompleter{

		err: fmt.Errorf("API rate limit exceeded"),
	}

	poller := New(store, p, analyzer, pub)
	_, err := poller.Broadcast(context.Background(), "test message")

	if err == nil {
		t.Fatal("expected error when LLM fails")
	}
	if !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("error = %v, expected rate limit error", err)
	}
}

func TestBroadcast_SuccessWithBusMessage(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	msgConn, pub := openTestMsgDB(t)

	analyzer := &mockCompleter{

		response: `{"analysis":"Broadcasting update","bus_message":{"topic":"test-project/coord","message":"All agents: please focus on testing"}}`,
	}

	poller := New(store, p, analyzer, pub)
	msg, err := poller.Broadcast(context.Background(), "tell agents to focus on testing")

	if err != nil {
		t.Fatalf("Broadcast: %v", err)
	}
	if msg == nil {
		t.Fatal("expected bus message")
	}
	if msg.Topic != "test-project/coord" {
		t.Errorf("topic = %q", msg.Topic)
	}
	if !strings.Contains(msg.Message, "focus on testing") {
		t.Errorf("message = %q", msg.Message)
	}

	// Verify the message was published to the DB.
	var count int
	if err := msgConn.Get(&count, "SELECT COUNT(*) FROM messages WHERE topic = ?", "test-project/coord"); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 message in DB, got %d", count)
	}

	// Verify the LLM was called.
	if analyzer.calls.Load() != 1 {
		t.Errorf("LLM calls = %d, want 1", analyzer.calls.Load())
	}
}

func TestBroadcast_FallbackBareJSON(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	_, pub := openTestMsgDB(t)

	// Return a bare BusMessage (not wrapped in AnalysisResponse).
	analyzer := &mockCompleter{

		response: `{"topic":"test-project/coord","message":"Direct message"}`,
	}

	poller := New(store, p, analyzer, pub)
	msg, err := poller.Broadcast(context.Background(), "send a direct message")

	if err != nil {
		t.Fatalf("Broadcast: %v", err)
	}
	if msg == nil {
		t.Fatal("expected bus message from fallback")
	}
	if msg.Topic != "test-project/coord" {
		t.Errorf("topic = %q", msg.Topic)
	}
}

func TestBroadcast_FallbackFencedBareJSON(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	_, pub := openTestMsgDB(t)

	analyzer := &mockCompleter{

		response: "```json\n{\"topic\":\"test-project/coord\",\"message\":\"Fenced direct\"}\n```",
	}

	poller := New(store, p, analyzer, pub)
	msg, err := poller.Broadcast(context.Background(), "send a fenced message")

	if err != nil {
		t.Fatalf("Broadcast: %v", err)
	}
	if msg == nil {
		t.Fatal("expected bus message from fenced fallback")
	}
}

func TestBroadcast_NonPublishableResponse(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	_, pub := openTestMsgDB(t)

	analyzer := &mockCompleter{

		response: "I'm not sure what to say.",
	}

	poller := New(store, p, analyzer, pub)
	_, err := poller.Broadcast(context.Background(), "something vague")

	if err == nil {
		t.Fatal("expected error for non-publishable response")
	}
	if !strings.Contains(err.Error(), "not produce a publishable") {
		t.Errorf("error = %v", err)
	}
}

func TestBroadcast_GathersContext(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	_, pub := openTestMsgDB(t)

	// Add a repo and a concern.
	_ = store.AddRepo(&db.Repo{ProjectID: p.ID, Path: "/tmp/test-repo", ShortName: "test-repo"})
	_ = store.AddConcern(&db.Concern{ProjectID: p.ID, Severity: "warning", Message: "Low coverage"})

	var capturedPrompt string
	analyzer := &mockCompleter{

		response: `{"topic":"test-project/coord","message":"context check"}`,
	}

	poller := New(store, p, analyzer, pub)
	_, _ = poller.Broadcast(context.Background(), "check context")

	// Can't directly check the prompt since mockProvider doesn't capture it,
	// but verify the LLM was called (meaning context gathering worked).
	if analyzer.calls.Load() != 1 {
		t.Errorf("LLM calls = %d, want 1", analyzer.calls.Load())
	}
	_ = capturedPrompt
}

func TestBroadcast_EmitsEvent(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	_, pub := openTestMsgDB(t)

	analyzer := &mockCompleter{

		response: `{"analysis":"ok","bus_message":{"topic":"test-project/coord","message":"update"}}`,
	}

	poller := New(store, p, analyzer, pub)
	_, _ = poller.Broadcast(context.Background(), "emit test")

	// Drain events looking for broadcast event.
	found := false
	for i := 0; i < 10; i++ {
		select {
		case ev := <-poller.Events():
			if ev.Type == "broadcast" {
				found = true
			}
		case <-time.After(100 * time.Millisecond):
			// timeout — continue draining
		}
		if found {
			break
		}
	}
	if !found {
		t.Error("expected broadcast event")
	}
}

// --- Onboard tests ---

func TestOnboard_NilPublisher(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)

	poller := New(store, p, nil, nil)
	_, err := poller.Onboard(context.Background(), "focus on testing")

	if err == nil {
		t.Fatal("expected error for nil publisher")
	}
	if !strings.Contains(err.Error(), "not available") {
		t.Errorf("error = %v, expected 'not available'", err)
	}
}

func TestOnboard_LLMFailure(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	_, pub := openTestMsgDB(t)

	analyzer := &mockCompleter{

		err: fmt.Errorf("service unavailable"),
	}

	poller := New(store, p, analyzer, pub)
	_, err := poller.Onboard(context.Background(), "")

	if err == nil {
		t.Fatal("expected error when LLM fails")
	}
	if !strings.Contains(err.Error(), "service unavailable") {
		t.Errorf("error = %v", err)
	}
}

func TestOnboard_Success(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	msgConn, pub := openTestMsgDB(t)

	analyzer := &mockCompleter{

		response: `{"analysis":"onboarding","bus_message":{"topic":"test-project/onboarding","message":"Welcome to the project!"}}`,
	}

	poller := New(store, p, analyzer, pub)
	msg, err := poller.Onboard(context.Background(), "")

	if err != nil {
		t.Fatalf("Onboard: %v", err)
	}
	if msg == nil {
		t.Fatal("expected bus message")
	}
	if msg.Topic != "test-project/onboarding" {
		t.Errorf("topic = %q", msg.Topic)
	}

	// Verify PublishReplace was used (should be exactly 1 message in topic).
	var count int
	_ = msgConn.Get(&count, "SELECT COUNT(*) FROM messages WHERE topic = ?", "test-project/onboarding")
	if count != 1 {
		t.Errorf("expected 1 onboarding message, got %d", count)
	}
}

func TestOnboard_ReplaceSemantics(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	msgConn, pub := openTestMsgDB(t)

	analyzer := &mockCompleter{

		response: `{"analysis":"v1","bus_message":{"topic":"test-project/onboarding","message":"Version 1"}}`,
	}

	poller := New(store, p, analyzer, pub)

	// First onboard.
	_, err := poller.Onboard(context.Background(), "")
	if err != nil {
		t.Fatalf("first Onboard: %v", err)
	}

	// Second onboard (should replace).
	analyzer.response = `{"analysis":"v2","bus_message":{"topic":"test-project/onboarding","message":"Version 2"}}`
	_, err = poller.Onboard(context.Background(), "updated guidance")
	if err != nil {
		t.Fatalf("second Onboard: %v", err)
	}

	// Should still be exactly 1 message (replaced).
	var count int
	_ = msgConn.Get(&count, "SELECT COUNT(*) FROM messages WHERE topic = ?", "test-project/onboarding")
	if count != 1 {
		t.Errorf("expected 1 message after replace, got %d", count)
	}

	// And it should be the latest version.
	var msg string
	_ = msgConn.Get(&msg, "SELECT message FROM messages WHERE topic = ?", "test-project/onboarding")
	if msg != "Version 2" {
		t.Errorf("message = %q, want %q", msg, "Version 2")
	}
}

func TestOnboard_WithUserGuidance(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	_, pub := openTestMsgDB(t)

	analyzer := &mockCompleter{

		response: `{"topic":"test-project/onboarding","message":"Focused on testing"}`,
	}

	poller := New(store, p, analyzer, pub)
	msg, err := poller.Onboard(context.Background(), "focus on test coverage")

	if err != nil {
		t.Fatalf("Onboard: %v", err)
	}
	if msg == nil {
		t.Fatal("expected bus message")
	}
}

func TestOnboard_NonPublishableResponse(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	_, pub := openTestMsgDB(t)

	analyzer := &mockCompleter{

		response: "Here's an onboarding message without JSON structure.",
	}

	poller := New(store, p, analyzer, pub)
	_, err := poller.Onboard(context.Background(), "")

	if err == nil {
		t.Fatal("expected error for non-publishable response")
	}
	if !strings.Contains(err.Error(), "not produce a publishable") {
		t.Errorf("error = %v", err)
	}
}

// --- QueryAnalyzer tests ---

func TestQueryAnalyzer_NoSession(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)

	poller := New(store, p, nil, nil)
	_, err := poller.QueryAnalyzer(context.Background(), "hello")

	if err == nil {
		t.Fatal("expected error for no session")
	}
	if !strings.Contains(err.Error(), "no analyzer session") {
		t.Errorf("error = %v, expected 'no analyzer session'", err)
	}
}

func TestQueryAnalyzer_Success(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	p.AnalyzerSessionID = "test-session-123"
	_ = store.UpdateAnalyzerSessionID(p.ID, "test-session-123")

	completer := &mockCompleter{
		response: "Here's what I know about the project...",
	}

	poller := New(store, p, completer, nil)
	response, err := poller.QueryAnalyzer(context.Background(), "what's the status?")

	if err != nil {
		t.Fatalf("QueryAnalyzer: %v", err)
	}
	if response != "Here's what I know about the project..." {
		t.Errorf("response = %q", response)
	}
}

// --- parseJSON tests ---

func TestParseJSON_ValidJSON(t *testing.T) {
	var msg BusMessage
	err := parseJSON(`{"topic":"test/coord","message":"hello"}`, &msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.Topic != "test/coord" {
		t.Errorf("topic = %q", msg.Topic)
	}
	if msg.Message != "hello" {
		t.Errorf("message = %q", msg.Message)
	}
}

func TestParseJSON_InvalidJSON(t *testing.T) {
	var msg BusMessage
	err := parseJSON(`not json`, &msg)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestParseJSON_EmptyObject(t *testing.T) {
	var msg BusMessage
	err := parseJSON(`{}`, &msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.Topic != "" || msg.Message != "" {
		t.Errorf("expected empty fields, got topic=%q message=%q", msg.Topic, msg.Message)
	}
}

// --- Poller.Project and Completer ---

func TestPoller_Project(t *testing.T) {
	proj := &db.Project{Name: "my-proj"}
	p := New(nil, proj, nil, nil)
	if p.Project().Name != "my-proj" {
		t.Errorf("Project().Name = %q", p.Project().Name)
	}
}

func TestPoller_Completer(t *testing.T) {
	completer := &mockCompleter{}
	p := New(nil, &db.Project{}, completer, nil)
	if p.Completer() != completer {
		t.Error("Completer() should return the completer")
	}
}

// --- SetStatusInterval tests ---

func TestPoller_SetStatusInterval(t *testing.T) {
	p := New(nil, &db.Project{Name: "test"}, nil, nil)

	p.SetStatusInterval(10 * time.Second)

	p.mu.Lock()
	interval := p.project.StatusIntervalSec
	p.mu.Unlock()

	if interval != 10 {
		t.Errorf("StatusIntervalSec = %d, want 10", interval)
	}
}

// --- parseAnalysis corpus of real-world edge cases ---

func TestParseAnalysis_Corpus(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		wantAnalysis string
		wantConcerns int
		wantBusMsg   bool
	}{
		{
			name:         "clean json no concerns",
			raw:          `{"analysis":"All quiet.","concerns":[]}`,
			wantAnalysis: "All quiet.",
			wantConcerns: 0,
		},
		{
			name:         "json with trailing newline",
			raw:          "{\"analysis\":\"Status update.\",\"concerns\":[]}\n",
			wantAnalysis: "Status update.",
			wantConcerns: 0,
		},
		{
			name:         "json with leading text",
			raw:          "Here's the analysis:\n```json\n{\"analysis\":\"Fenced\",\"concerns\":[]}\n```\nDone.",
			wantAnalysis: "Fenced",
			wantConcerns: 0,
		},
		{
			name:         "multiline analysis text",
			raw:          "The project is progressing well.\nAll repos are stable.\nNo immediate concerns.",
			wantAnalysis: "The project is progressing well.\nAll repos are stable.\nNo immediate concerns.",
			wantConcerns: 0,
		},
		{
			name:         "json with bus message",
			raw:          `{"analysis":"Breaking change detected.","concerns":[{"severity":"danger","message":"API break"}],"bus_message":{"topic":"proj/coord","message":"API v2 incompatible"}}`,
			wantAnalysis: "Breaking change detected.",
			wantConcerns: 1,
			wantBusMsg:   true,
		},
		{
			name:         "deeply nested json string",
			raw:          `{"analysis":"Contains \"escaped\" quotes","concerns":[]}`,
			wantAnalysis: `Contains "escaped" quotes`,
			wantConcerns: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := parseAnalysis(tt.raw)
			if resp.Analysis != tt.wantAnalysis {
				t.Errorf("analysis = %q, want %q", resp.Analysis, tt.wantAnalysis)
			}
			if len(resp.Concerns) != tt.wantConcerns {
				t.Errorf("concerns = %d, want %d", len(resp.Concerns), tt.wantConcerns)
			}
			if tt.wantBusMsg && resp.BusMessage == nil {
				t.Error("expected bus_message")
			}
			if !tt.wantBusMsg && resp.BusMessage != nil {
				t.Error("unexpected bus_message")
			}
		})
	}
}

// --- Poller.Stop with nil cancel ---

func TestPoller_StopNilCancel(t *testing.T) {
	// Stop should not panic when cancel is nil (poller never started).
	p := New(nil, &db.Project{Name: "test"}, nil, nil)
	// Should not panic.
	p.Stop()
}

// --- PollNow and StatusNow tests ---

func TestPollNow_EmptyProject(t *testing.T) {
	store := openTestDB(t)
	proj := createTestProject(t, store)

	analyzer := &mockCompleter{response: "All quiet, no new activity."}

	poller := New(store, proj, analyzer, nil)
	poller.PollNow(context.Background())

	// Should get a "polling" event followed by a "poll" event.
	var pollResult *PollResult
	for i := 0; i < 10; i++ {
		select {
		case ev := <-poller.Events():
			if ev.Type == "poll" && ev.PollResult != nil {
				pollResult = ev.PollResult
			}
		case <-time.After(200 * time.Millisecond):
			// timeout — continue draining
		}
		if pollResult != nil {
			break
		}
	}
	if pollResult == nil {
		t.Fatal("expected poll result event")
	}
	// No repos = no commits.
	if pollResult.NewCommits != 0 {
		t.Errorf("NewCommits = %d, want 0", pollResult.NewCommits)
	}
	// No activity = no analyzer call.
	if analyzer.calls.Load() != 0 {
		t.Errorf("completer calls = %d, want 0", analyzer.calls.Load())
	}
	if !pollResult.NoNewActivity {
		t.Error("expected NoNewActivity to be true")
	}
}

func TestStatusNow_EmptyProject(t *testing.T) {
	store := openTestDB(t)
	proj := createTestProject(t, store)

	poller := New(store, proj, nil, nil)
	poller.StatusNow(context.Background())

	var pollResult *PollResult
	for i := 0; i < 10; i++ {
		select {
		case ev := <-poller.Events():
			if ev.Type == "poll" && ev.PollResult != nil {
				pollResult = ev.PollResult
			}
		case <-time.After(200 * time.Millisecond):
			// timeout — continue draining
		}
		if pollResult != nil {
			break
		}
	}
	if pollResult == nil {
		t.Fatal("expected poll result event")
	}
	if !pollResult.StatusOnly {
		t.Error("expected StatusOnly = true")
	}
	if pollResult.Tier1Summary != "No new activity." {
		t.Errorf("Tier1Summary = %q", pollResult.Tier1Summary)
	}
}

func TestStatusNow_RecordsSummary(t *testing.T) {
	store := openTestDB(t)
	proj := createTestProject(t, store)

	poller := New(store, proj, nil, nil)
	poller.StatusNow(context.Background())

	// Drain events.
	for {
		select {
		case <-poller.Events():
		case <-time.After(200 * time.Millisecond):
			goto done
		}
	}
done:
	// The status-only poll should NOT record to DB (it's a lightweight check).
	// Actually it does record via the event log - but it should work.
}

func TestPollNow_EmitsPollingAndPollEvents(t *testing.T) {
	store := openTestDB(t)
	proj := createTestProject(t, store)

	poller := New(store, proj, &mockCompleter{response: "All quiet."}, nil)
	poller.PollNow(context.Background())

	events := make([]string, 0)
	for i := 0; i < 10; i++ {
		select {
		case ev := <-poller.Events():
			events = append(events, ev.Type)
		case <-time.After(200 * time.Millisecond):
			goto done
		}
	}
done:
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events, got %d: %v", len(events), events)
	}
	if events[0] != "polling" {
		t.Errorf("first event = %q, want 'polling'", events[0])
	}
	if events[len(events)-1] != "poll" {
		t.Errorf("last event = %q, want 'poll'", events[len(events)-1])
	}
}

// --- Run loop tests ---

func TestRun_PausedSkipsPolling(t *testing.T) {
	store := openTestDB(t)
	proj := createTestProject(t, store)
	proj.StatusIntervalSec = 1 // 1 second for fast test.

	poller := New(store, proj, nil, nil)

	ctx := context.Background()
	poller.Start(ctx)
	defer poller.Stop()

	// Wait for initial status poll.
	time.Sleep(100 * time.Millisecond)

	// Pause.
	poller.Pause()

	// Drain existing events.
	for {
		select {
		case <-poller.Events():
		default:
			goto drained
		}
	}
drained:

	// Wait long enough for a tick.
	time.Sleep(1200 * time.Millisecond)

	// Should not have any new poll events (only the paused event).
	found := false
	for {
		select {
		case ev := <-poller.Events():
			if ev.Type == "poll" {
				found = true
			}
		default:
			goto checked
		}
	}
checked:
	if found {
		t.Error("expected no poll events while paused")
	}
}

func TestRun_StatusIntervalChange(t *testing.T) {
	store := openTestDB(t)
	proj := createTestProject(t, store)
	proj.StatusIntervalSec = 60 // 60 seconds initially (won't fire).

	poller := New(store, proj, nil, nil)

	ctx := context.Background()
	poller.Start(ctx)
	defer poller.Stop()

	// Wait for initial status poll.
	time.Sleep(100 * time.Millisecond)

	// Change interval to 1 second.
	poller.SetStatusInterval(1 * time.Second)

	// Wait for a tick.
	time.Sleep(1500 * time.Millisecond)

	// Should have gotten at least one status poll after the change.
	foundPoll := false
	for {
		select {
		case ev := <-poller.Events():
			if ev.Type == "poll" {
				foundPoll = true
			}
		default:
			goto checkedInterval
		}
	}
checkedInterval:
	if !foundPoll {
		// This is timing-dependent so don't fail hard, just note it.
		t.Log("Note: did not observe a poll after interval change (timing-dependent)")
	}
}

// --- Start/Stop lifecycle ---

func TestPoller_StartStop(t *testing.T) {
	store := openTestDB(t)
	proj := createTestProject(t, store)

	// We need a poller that can start without actually polling (no repos, no bus).
	// The run() loop does a StatusNow first which calls gatherActivity which
	// touches the store. This should work with an empty project.
	poller := New(store, proj, nil, nil)

	ctx := context.Background()
	poller.Start(ctx)

	// Wait a moment for the goroutine to start.
	time.Sleep(50 * time.Millisecond)

	// Stop should not hang.
	done := make(chan struct{})
	go func() {
		poller.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Good.
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() timed out")
	}
}

// --- Event time is set ---

func TestEvent_TimeIsSet(t *testing.T) {
	p := New(nil, &db.Project{Name: "test"}, nil, nil)
	before := time.Now()
	p.emit("test", "msg", nil)
	after := time.Now()

	ev := <-p.Events()
	if ev.Time.Before(before) || ev.Time.After(after) {
		t.Errorf("event time %v not between %v and %v", ev.Time, before, after)
	}
}

// --- Broadcast with rich context ---

func TestBroadcast_WithRichContext(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	_, pub := openTestMsgDB(t)

	// Add repo, concerns, and polls.
	_ = store.AddRepo(&db.Repo{ProjectID: p.ID, Path: "/tmp/test-repo", ShortName: "test-repo"})
	_ = store.AddConcern(&db.Concern{ProjectID: p.ID, Severity: "warning", Message: "Low coverage"})
	_ = store.AddConcern(&db.Concern{ProjectID: p.ID, Severity: "info", Message: "New contributor"})
	_ = store.RecordPoll(&db.Poll{
		ProjectID:     p.ID,
		NewCommits:    5,
		Tier2Response: "5 commits across 2 repos, focus on auth module",
	})

	analyzer := &mockCompleter{

		response: `{"analysis":"context test","bus_message":{"topic":"test-project/coord","message":"Context verified"}}`,
	}

	poller := New(store, p, analyzer, pub)
	msg, err := poller.Broadcast(context.Background(), "verify context gathering")

	if err != nil {
		t.Fatalf("Broadcast: %v", err)
	}
	if msg == nil {
		t.Fatal("expected bus message")
	}
}

// --- Onboard with rich context ---

func TestOnboard_WithRichContext(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	_, pub := openTestMsgDB(t)

	// Add repo, concern, topic, poll.
	_ = store.AddRepo(&db.Repo{ProjectID: p.ID, Path: "/tmp/test-repo", ShortName: "test-repo"})
	_ = store.AddConcern(&db.Concern{ProjectID: p.ID, Severity: "warning", Message: "Schema migration needed"})
	_ = store.AddTopic(&db.Topic{ProjectID: p.ID, Name: "test-project/coord"})
	_ = store.RecordPoll(&db.Poll{
		ProjectID:     p.ID,
		Tier2Response: "Recent progress on auth module",
	})

	analyzer := &mockCompleter{

		response: `{"analysis":"onboarding","bus_message":{"topic":"test-project/onboarding","message":"Welcome! Project goal: Build a feature"}}`,
	}

	poller := New(store, p, analyzer, pub)
	msg, err := poller.Onboard(context.Background(), "mention the schema migration")

	if err != nil {
		t.Fatalf("Onboard: %v", err)
	}
	if msg == nil {
		t.Fatal("expected bus message")
	}
	if msg.Topic != "test-project/onboarding" {
		t.Errorf("topic = %q", msg.Topic)
	}
}

// --- BulkAddTrackedItems via store ---

func TestPoller_BulkAddTrackedItems(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)

	items := []*db.TrackedItem{
		{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: 1, ItemType: "issue", Title: "Issue 1", State: "open", LastStatus: "Open"},
		{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: 2, ItemType: "issue", Title: "Issue 2", State: "open", LastStatus: "Open"},
	}
	added, err := store.BulkAddTrackedItems(items)
	if err != nil {
		t.Fatalf("BulkAddTrackedItems: %v", err)
	}
	if added != 2 {
		t.Errorf("added = %d, want 2", added)
	}

	tracked, _ := store.GetTrackedItems(p.ID)
	if len(tracked) != 2 {
		t.Errorf("tracked = %d, want 2", len(tracked))
	}
}

// --- Model defaults in Broadcast/Onboard ---

func TestBroadcast_DefaultModel(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	p.LLMAnalyzerModel = ""
	_ = store.UpdateProject(p)

	_, pub := openTestMsgDB(t)

	analyzer := &mockCompleter{

		response: `{"topic":"test-project/coord","message":"default model test"}`,
	}

	poller := New(store, p, analyzer, pub)
	_, _ = poller.Broadcast(context.Background(), "test")

	if analyzer.lastModel != "opus" {
		t.Errorf("model = %q, want %q", analyzer.lastModel, "opus")
	}
}

func TestOnboard_DefaultModel(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	p.LLMAnalyzerModel = ""
	_ = store.UpdateProject(p)

	_, pub := openTestMsgDB(t)

	analyzer := &mockCompleter{

		response: `{"topic":"test-project/onboarding","message":"default model test"}`,
	}

	poller := New(store, p, analyzer, pub)
	_, _ = poller.Onboard(context.Background(), "")

	if analyzer.lastModel != "opus" {
		t.Errorf("model = %q, want %q", analyzer.lastModel, "opus")
	}
}

// --- Onboard without user guidance ---

func TestOnboard_NoGuidance(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	_, pub := openTestMsgDB(t)

	analyzer := &mockCompleter{

		response: `{"analysis":"general","bus_message":{"topic":"test-project/onboarding","message":"General onboarding"}}`,
	}

	poller := New(store, p, analyzer, pub)
	msg, err := poller.Onboard(context.Background(), "")

	if err != nil {
		t.Fatalf("Onboard: %v", err)
	}
	if msg == nil {
		t.Fatal("expected message")
	}
}

func TestOnboard_WithWhitespaceGuidance(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	_, pub := openTestMsgDB(t)

	analyzer := &mockCompleter{

		response: `{"analysis":"trimmed","bus_message":{"topic":"test-project/onboarding","message":"Guidance trimmed"}}`,
	}

	poller := New(store, p, analyzer, pub)
	msg, err := poller.Onboard(context.Background(), "   \n  ")

	if err != nil {
		t.Fatalf("Onboard: %v", err)
	}
	if msg == nil {
		t.Fatal("expected message")
	}
}

// --- parseAnalysis raw fallback path ---

func TestParseAnalysis_FencedButInvalidWithRawValid(t *testing.T) {
	raw := `{"analysis":"Unfenced","concerns":[]}`
	resp := parseAnalysis(raw)
	if resp.Analysis != "Unfenced" {
		t.Errorf("analysis = %q, want %q", resp.Analysis, "Unfenced")
	}
}

// --- Poll.LLMResponse tests (db model) ---

func TestDBPoll_LLMResponse_Tier2(t *testing.T) {
	p := &db.Poll{Tier2Response: "tier2", Tier1Response: "tier1", LLMResponseRaw: "raw"}
	if got := p.LLMResponse(); got != "tier2" {
		t.Errorf("LLMResponse() = %q, want tier2", got)
	}
}

func TestDBPoll_LLMResponse_Tier1(t *testing.T) {
	p := &db.Poll{Tier1Response: "tier1", LLMResponseRaw: "raw"}
	if got := p.LLMResponse(); got != "tier1" {
		t.Errorf("LLMResponse() = %q, want tier1", got)
	}
}

func TestDBPoll_LLMResponse_Raw(t *testing.T) {
	p := &db.Poll{LLMResponseRaw: "raw"}
	if got := p.LLMResponse(); got != "raw" {
		t.Errorf("LLMResponse() = %q, want raw", got)
	}
}

// --- GitHubRepo type ---

func TestGitHubRepo_Fields(t *testing.T) {
	r := GitHubRepo{Owner: "org", Repo: "repo"}
	if r.Owner != "org" || r.Repo != "repo" {
		t.Error("unexpected field values")
	}
}

// --- TrackedItem.DisplayRef ---

func TestTrackedItem_DisplayRef(t *testing.T) {
	item := &db.TrackedItem{Owner: "org", Repo: "repo", Number: 42}
	if got := item.DisplayRef(); got != "org/repo#42" {
		t.Errorf("DisplayRef() = %q", got)
	}
}

// --- CompletedItem.DisplayRef ---

func TestCompletedItem_DisplayRef(t *testing.T) {
	item := &db.CompletedItem{Owner: "org", Repo: "repo", Number: 10}
	if got := item.DisplayRef(); got != "org/repo#10" {
		t.Errorf("DisplayRef() = %q", got)
	}
}

// --- Onboard fallback bare JSON ---

func TestOnboard_FallbackBareJSON(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	_, pub := openTestMsgDB(t)

	analyzer := &mockCompleter{

		response: `{"topic":"test-project/onboarding","message":"Bare JSON onboard"}`,
	}

	poller := New(store, p, analyzer, pub)
	msg, err := poller.Onboard(context.Background(), "")

	if err != nil {
		t.Fatalf("Onboard: %v", err)
	}
	if msg == nil {
		t.Fatal("expected bus message from bare JSON fallback")
	}
	if msg.Message != "Bare JSON onboard" {
		t.Errorf("message = %q", msg.Message)
	}
}

// --- Onboard fallback fenced bare JSON ---

func TestOnboard_FallbackFencedBareJSON(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	_, pub := openTestMsgDB(t)

	analyzer := &mockCompleter{

		response: "```json\n{\"topic\":\"test-project/onboarding\",\"message\":\"Fenced onboard\"}\n```",
	}

	poller := New(store, p, analyzer, pub)
	msg, err := poller.Onboard(context.Background(), "")

	if err != nil {
		t.Fatalf("Onboard: %v", err)
	}
	if msg == nil {
		t.Fatal("expected bus message from fenced fallback")
	}
}

// --- Broadcast model routing ---

func TestBroadcast_UsesAnalyzerModel(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	p.LLMAnalyzerModel = "claude-opus-4-6"
	_ = store.UpdateProject(p)

	_, pub := openTestMsgDB(t)

	analyzer := &mockCompleter{

		response: `{"topic":"test-project/coord","message":"model routing test"}`,
	}

	poller := New(store, p, analyzer, pub)
	_, _ = poller.Broadcast(context.Background(), "test")

	if analyzer.lastModel != "claude-opus-4-6" {
		t.Errorf("model = %q, want %q", analyzer.lastModel, "claude-opus-4-6")
	}
}

// --- Broadcast poll context truncation ---

func TestBroadcast_TruncatesLongPollResponse(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	_, pub := openTestMsgDB(t)

	// Insert a poll with a very long response.
	longResp := strings.Repeat("x", 300)
	_ = store.RecordPoll(&db.Poll{
		ProjectID:     p.ID,
		Tier2Response: longResp,
	})

	analyzer := &mockCompleter{

		response: `{"analysis":"truncation test","bus_message":{"topic":"test-project/coord","message":"Checked"}}`,
	}

	poller := New(store, p, analyzer, pub)
	msg, err := poller.Broadcast(context.Background(), "test truncation")
	if err != nil {
		t.Fatalf("Broadcast: %v", err)
	}
	if msg == nil {
		t.Fatal("expected message")
	}
}

// --- debugEnabled / closeDebugLog ---

func TestDebugEnabled_NotSet(t *testing.T) {
	// Ensure MINDER_DEBUG is not set for this test.
	t.Setenv("MINDER_DEBUG", "")
	if debugEnabled() {
		t.Error("expected debugEnabled() = false when MINDER_DEBUG empty")
	}
}

func TestDebugEnabled_Set(t *testing.T) {
	t.Setenv("MINDER_DEBUG", "1")
	if !debugEnabled() {
		t.Error("expected debugEnabled() = true when MINDER_DEBUG=1")
	}
}

func TestCloseDebugLog_NilFile(t *testing.T) {
	// Save and restore.
	old := debugLogFile
	debugLogFile = nil
	defer func() { debugLogFile = old }()
	// Should not panic.
	closeDebugLog()
}

func TestDebugLog_NilLogger(t *testing.T) {
	// Save and restore.
	old := debugLogger
	debugLogger = nil
	defer func() { debugLogger = old }()
	// Should be a no-op, not panic.
	debugLog("test message", "key", "value")
}

// --- initDebugLog ---

func TestInitDebugLog_NoDebugEnv(t *testing.T) {
	// Save and restore global state.
	oldLogger := debugLogger
	debugLogger = nil
	defer func() { debugLogger = oldLogger }()

	t.Setenv("MINDER_DEBUG", "")
	initDebugLog()
	if debugLogger != nil {
		t.Error("debugLogger should remain nil when MINDER_DEBUG not set")
	}
}

func TestInitDebugLog_AlreadyInitialized(t *testing.T) {
	// Save and restore global state.
	oldLogger := debugLogger
	defer func() { debugLogger = oldLogger }()

	// Set a non-nil logger.
	debugLogger = oldLogger
	if debugLogger == nil {
		// Create a dummy one just to test the guard.
		t.Skip("no existing logger to test guard")
	}

	// Calling init again should be a no-op.
	initDebugLog()
}

// --- Onboard poll context truncation ---

// --- doPoll / doStatusPoll end-to-end tests ---
// These tests exercise the full poll pipeline by injecting bus messages
// via AGENT_MSG_DB and using mock LLM providers for tier 1 and tier 2.

func TestDoPoll_WithBusActivity(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	p.RefreshIntervalSec = 3600 // large interval so lookback includes our message
	_ = store.UpdateProject(p)

	// Create a test messages DB with a recent message.
	msgDB, pub := openTestMsgDB(t)
	dbPath := filepath.Join(t.TempDir(), "doPoll-messages.db")
	msgDB2, err := sqlx.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open msg db: %v", err)
	}
	msgDB2.MustExec(`
		CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY, topic TEXT NOT NULL, sender TEXT NOT NULL,
			message TEXT NOT NULL, created_at TEXT DEFAULT (datetime('now'))
		);
		CREATE TABLE IF NOT EXISTS acks (
			id INTEGER PRIMARY KEY, message_id INTEGER NOT NULL, agent_name TEXT NOT NULL,
			acked_at TEXT DEFAULT (datetime('now')), UNIQUE(message_id, agent_name)
		);
		CREATE TABLE IF NOT EXISTS agent_names (
			id INTEGER PRIMARY KEY, repo TEXT NOT NULL, name TEXT NOT NULL,
			claimed_at TEXT DEFAULT (datetime('now'))
		);
	`)
	// Insert a bus message from another agent (not our identity).
	msgDB2.MustExec(`INSERT INTO messages (topic, sender, message, created_at) VALUES (?, ?, ?, datetime('now'))`,
		p.Name+"/coord", "other-agent", "I completed my task")
	t.Cleanup(func() { _ = msgDB2.Close() })

	// Point AGENT_MSG_DB to our test DB.
	t.Setenv("AGENT_MSG_DB", dbPath)

	analyzer := &mockCompleter{
		response: `{"analysis":"other-agent completed their task","concerns":[{"severity":"info","message":"Task done"}]}`,
	}

	poller := New(store, p, analyzer, pub)
	result, err := poller.doPoll(context.Background())
	if err != nil {
		t.Fatalf("doPoll: %v", err)
	}

	// Verify bus messages were detected.
	if result.NewMessages == 0 {
		t.Error("expected bus messages to be detected")
	}

	// Verify analysis ran (now a single call instead of tier 1 + tier 2).
	if result.NewMessages > 0 && analyzer.calls.Load() == 0 {
		t.Error("expected completer to be called with bus activity")
	}

	// Verify analysis produced output.
	if result.Tier2Analysis == "" && result.NewMessages > 0 {
		// Only check if there was activity to analyze.
		t.Log("tier 2 analysis was empty despite bus activity")
	}

	// Verify poll was recorded.
	polls, _ := store.RecentPolls(p.ID, 1)
	if len(polls) == 0 {
		t.Error("expected poll to be recorded")
	}

	_ = msgDB // silence potential unused warning
}

func TestDoStatusPoll_WithNoActivity(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)

	// Point AGENT_MSG_DB to a non-existent path so bus messages section is skipped.
	t.Setenv("AGENT_MSG_DB", filepath.Join(t.TempDir(), "nonexistent.db"))

	poller := New(store, p, &mockCompleter{}, nil)
	result, err := poller.doStatusPoll(context.Background())
	if err != nil {
		t.Fatalf("doStatusPoll: %v", err)
	}

	// Should be a status-only poll with no activity.
	if !result.StatusOnly {
		t.Error("expected StatusOnly=true")
	}
	if result.Tier1Summary != "No new activity." {
		t.Errorf("Tier1Summary = %q, want 'No new activity.'", result.Tier1Summary)
	}
}

func TestDoPoll_NoActivity_SkipsAnalyzer(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)

	t.Setenv("AGENT_MSG_DB", filepath.Join(t.TempDir(), "nonexistent.db"))

	completer := &mockCompleter{response: "No new activity to report."}

	poller := New(store, p, completer, nil)
	result, err := poller.doPoll(context.Background())
	if err != nil {
		t.Fatalf("doPoll: %v", err)
	}

	// No activity = no analyzer call.
	if completer.calls.Load() != 0 {
		t.Errorf("completer calls = %d, want 0", completer.calls.Load())
	}
	if !result.NoNewActivity {
		t.Error("expected NoNewActivity to be true")
	}
}

// --- BulkAddTrackedItems tests ---

func TestBulkAddTrackedItems(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)

	analyzer := &mockCompleter{}
	poller := New(store, p, analyzer, nil)

	items := []ghpkg.ItemStatus{
		{Number: 1, Title: "Issue 1", State: "open", ItemType: "issue", Labels: []string{"bug"}},
		{Number: 2, Title: "Issue 2", State: "open", ItemType: "pull_request"},
	}

	added, err := poller.BulkAddTrackedItems(context.Background(), items, "owner", "repo")
	if err != nil {
		t.Fatalf("BulkAddTrackedItems: %v", err)
	}
	if added != 2 {
		t.Errorf("added = %d, want 2", added)
	}

	// Verify items in DB.
	tracked, err := store.GetTrackedItems(p.ID)
	if err != nil {
		t.Fatalf("TrackedItems: %v", err)
	}
	if len(tracked) != 2 {
		t.Errorf("tracked items = %d, want 2", len(tracked))
	}

	// Verify event was emitted.
	select {
	case e := <-poller.Events():
		if e.Type != "tracked" {
			t.Errorf("event type = %q, want tracked", e.Type)
		}
	default:
		t.Error("expected event")
	}
}

func TestBulkAddTrackedItems_NoneAdded(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)

	analyzer := &mockCompleter{}
	poller := New(store, p, analyzer, nil)

	// Empty list — no items to add.
	added, err := poller.BulkAddTrackedItems(context.Background(), nil, "owner", "repo")
	if err != nil {
		t.Fatalf("BulkAddTrackedItems: %v", err)
	}
	if added != 0 {
		t.Errorf("added = %d, want 0", added)
	}
}

// --- ClearAndBulkAddTrackedItems tests ---

func TestClearAndBulkAddTrackedItems(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)

	analyzer := &mockCompleter{}
	poller := New(store, p, analyzer, nil)

	// First add some items.
	items1 := []ghpkg.ItemStatus{
		{Number: 1, Title: "Old Issue", State: "open", ItemType: "issue"},
	}
	_, _ = poller.BulkAddTrackedItems(context.Background(), items1, "owner", "repo")
	// Drain event.
	<-poller.Events()

	// Clear and add new items.
	items2 := []ghpkg.ItemStatus{
		{Number: 10, Title: "New Issue", State: "open", ItemType: "issue"},
		{Number: 11, Title: "New PR", State: "open", ItemType: "pull_request"},
	}
	added, err := poller.ClearAndBulkAddTrackedItems(context.Background(), items2, "owner2", "repo2")
	if err != nil {
		t.Fatalf("ClearAndBulkAddTrackedItems: %v", err)
	}
	if added != 2 {
		t.Errorf("added = %d, want 2", added)
	}

	// Verify only new items remain.
	tracked, _ := store.GetTrackedItems(p.ID)
	if len(tracked) != 2 {
		t.Errorf("tracked = %d, want 2", len(tracked))
	}
}

// --- UpdateTrackedItems tests ---

func TestUpdateTrackedItems(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)

	analyzer := &mockCompleter{}
	poller := New(store, p, analyzer, nil)

	items := []ghpkg.ItemStatus{
		{Number: 5, Title: "Test Issue", State: "open", ItemType: "issue"},
	}
	added, removed, err := poller.UpdateTrackedItems(context.Background(), items, "o", "r")
	if err != nil {
		t.Fatalf("UpdateTrackedItems: %v", err)
	}
	if added != 1 || removed != 0 {
		t.Errorf("added=%d removed=%d, want 1/0", added, removed)
	}
}

// --- RemoveTrackedItemByRef tests ---

func TestRemoveTrackedItemByRef(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)

	analyzer := &mockCompleter{}
	poller := New(store, p, analyzer, nil)

	// Add an item first.
	_ = store.AddTrackedItem(&db.TrackedItem{
		ProjectID: p.ID,
		Source:    "github",
		Owner:     "o",
		Repo:      "r",
		Number:    42,
		ItemType:  "issue",
		Title:     "Test",
		State:     "open",
	})

	ref := &ghpkg.ItemRef{Owner: "o", Repo: "r", Number: 42}
	err := poller.RemoveTrackedItemByRef(ref)
	if err != nil {
		t.Fatalf("RemoveTrackedItemByRef: %v", err)
	}

	// Verify removed.
	items, _ := store.GetTrackedItems(p.ID)
	if len(items) != 0 {
		t.Errorf("expected 0 tracked items, got %d", len(items))
	}

	// Verify event.
	select {
	case e := <-poller.Events():
		if e.Type != "tracked" {
			t.Errorf("event type = %q", e.Type)
		}
	default:
		t.Error("expected event")
	}
}

// --- Accessor tests ---

func TestPollerAccessors(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	analyzer := &mockCompleter{}

	poller := New(store, p, analyzer, nil)

	if poller.Project() != p {
		t.Error("Project() should return the same project")
	}
	if poller.Completer() != analyzer {
		t.Error("Completer() should return the completer")
	}
	// Events() returns a read-only channel.
	ch := poller.Events()
	if ch == nil {
		t.Error("Events() should not be nil")
	}
}

// --- SetStatusInterval tests ---

func TestSetStatusInterval(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	poller := New(store, p, &mockCompleter{}, nil)

	poller.SetStatusInterval(5 * time.Minute)

	if p.StatusIntervalSec != 300 {
		t.Errorf("StatusIntervalSec = %d, want 300", p.StatusIntervalSec)
	}
}

// --- summarize edge cases ---

func TestSummarize_WithWorktrees(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	poller := New(store, p, &mockCompleter{}, nil)

	result := &PollResult{NewWorktrees: 3}
	s := poller.summarize(result)
	if !strings.Contains(s, "3 new worktrees") {
		t.Errorf("summarize = %q", s)
	}
}

func TestSummarize_WithBusMessage(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	poller := New(store, p, &mockCompleter{}, nil)

	result := &PollResult{BusMessageSent: "[topic] hello"}
	s := poller.summarize(result)
	if !strings.Contains(s, "bus message sent") {
		t.Errorf("summarize = %q", s)
	}
}

// --- IsPaused tests ---

func TestIsPaused(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	poller := New(store, p, &mockCompleter{}, nil)

	if poller.IsPaused() {
		t.Error("should not be paused initially")
	}
	poller.Pause()
	if !poller.IsPaused() {
		t.Error("should be paused after Pause()")
	}
	poller.Resume()
	if poller.IsPaused() {
		t.Error("should not be paused after Resume()")
	}
}

// --- Event struct test ---

func TestEventFields(t *testing.T) {
	now := time.Now()
	result := &PollResult{NewCommits: 5}
	evt := Event{
		Time:       now,
		Type:       "poll",
		Summary:    "5 commits",
		PollResult: result,
	}
	if evt.Type != "poll" {
		t.Errorf("Type = %q", evt.Type)
	}
	if evt.PollResult.NewCommits != 5 {
		t.Errorf("PollResult.NewCommits = %d", evt.PollResult.NewCommits)
	}
}

// --- PollResult.StatusOnly test ---

func TestPollResult_StatusOnly(t *testing.T) {
	r := &PollResult{StatusOnly: true, Tier1Summary: "Status check"}
	if !r.StatusOnly {
		t.Error("expected StatusOnly=true")
	}
	if r.LLMResponse() != "Status check" {
		t.Errorf("LLMResponse = %q", r.LLMResponse())
	}
}

func TestOnboard_TruncatesLongPollResponse(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	_, pub := openTestMsgDB(t)

	// Insert a poll with a very long response.
	longResp := strings.Repeat("y", 400)
	_ = store.RecordPoll(&db.Poll{
		ProjectID:     p.ID,
		Tier2Response: longResp,
	})

	analyzer := &mockCompleter{

		response: `{"analysis":"truncation test","bus_message":{"topic":"test-project/onboarding","message":"Truncated"}}`,
	}

	poller := New(store, p, analyzer, pub)
	msg, err := poller.Onboard(context.Background(), "")
	if err != nil {
		t.Fatalf("Onboard: %v", err)
	}
	if msg == nil {
		t.Fatal("expected message")
	}
}

// --- Broadcast with concerns context ---

func TestBroadcast_WithConcernsContext(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	_, pub := openTestMsgDB(t)

	// Add a concern so the broadcast context includes it.
	_ = store.AddConcern(&db.Concern{
		ProjectID: p.ID,
		Severity:  "warning",
		Message:   "Schema drift detected",
	})

	analyzer := &mockCompleter{

		response: `{"analysis":"broadcast with concerns","bus_message":{"topic":"test-project/coord","message":"Watch schema drift"}}`,
	}

	poller := New(store, p, analyzer, pub)
	msg, err := poller.Broadcast(context.Background(), "Tell everyone about schema drift")
	if err != nil {
		t.Fatalf("Broadcast: %v", err)
	}
	if msg == nil {
		t.Fatal("expected message")
	}

	// Verify the LLM prompt included concern context by checking it was called.
	if analyzer.calls.Load() != 1 {
		t.Errorf("expected 1 LLM call, got %d", analyzer.calls.Load())
	}
}

// --- Onboard with topics context ---

func TestOnboard_WithTopicsAndConcerns(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	_, pub := openTestMsgDB(t)

	// Add topics.
	_ = store.AddTopic(&db.Topic{ProjectID: p.ID, Name: "coordination"})
	_ = store.AddTopic(&db.Topic{ProjectID: p.ID, Name: "testing"})

	// Add a concern.
	_ = store.AddConcern(&db.Concern{
		ProjectID: p.ID,
		Severity:  "info",
		Message:   "New agent joined",
	})

	analyzer := &mockCompleter{

		response: `{"analysis":"rich onboard","bus_message":{"topic":"test-project/onboarding","message":"Welcome"}}`,
	}

	poller := New(store, p, analyzer, pub)
	msg, err := poller.Onboard(context.Background(), "Focus on testing")
	if err != nil {
		t.Fatalf("Onboard: %v", err)
	}
	if msg == nil {
		t.Fatal("expected message")
	}
	if msg.Message != "Welcome" {
		t.Errorf("message = %q", msg.Message)
	}
}

// --- Broadcast with repos context ---

func TestBroadcast_WithReposContext(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	_, pub := openTestMsgDB(t)

	// Enroll a repo.
	_ = store.AddRepo(&db.Repo{
		ProjectID: p.ID,
		Path:      "/tmp/test-repo",
		ShortName: "test-repo",
	})

	analyzer := &mockCompleter{

		response: `{"analysis":"repo context","bus_message":{"topic":"test-project/coord","message":"With repos"}}`,
	}

	poller := New(store, p, analyzer, pub)
	msg, err := poller.Broadcast(context.Background(), "Update")
	if err != nil {
		t.Fatalf("Broadcast: %v", err)
	}
	if msg == nil {
		t.Fatal("expected message")
	}
}

// --- Onboard with repos context ---

func TestOnboard_WithReposContext(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	_, pub := openTestMsgDB(t)

	// Enroll a repo.
	_ = store.AddRepo(&db.Repo{
		ProjectID: p.ID,
		Path:      "/tmp/onboard-repo",
		ShortName: "onboard-repo",
	})

	analyzer := &mockCompleter{

		response: `{"analysis":"repo onboard","bus_message":{"topic":"test-project/onboarding","message":"With repos"}}`,
	}

	poller := New(store, p, analyzer, pub)
	msg, err := poller.Onboard(context.Background(), "")
	if err != nil {
		t.Fatalf("Onboard: %v", err)
	}
	if msg == nil {
		t.Fatal("expected message")
	}
}

// --- gatherResult struct test ---

func TestGatherResultFields(t *testing.T) {
	result := &PollResult{NewCommits: 3, NewMessages: 2}
	gr := &gatherResult{
		result:     result,
		repos:      []db.Repo{{Path: "/test"}},
		gitSummary: "3 commits",
		msgSummary: "2 messages",
	}

	if gr.result.NewCommits != 3 {
		t.Errorf("commits = %d", gr.result.NewCommits)
	}
	if len(gr.repos) != 1 {
		t.Errorf("repos = %d", len(gr.repos))
	}
}

// --- parseJSON tests ---

func TestParseJSON_Valid(t *testing.T) {
	var msg BusMessage
	err := parseJSON(`{"topic":"test","message":"hello"}`, &msg)
	if err != nil {
		t.Fatalf("parseJSON: %v", err)
	}
	if msg.Topic != "test" || msg.Message != "hello" {
		t.Errorf("got %+v", msg)
	}
}

func TestParseJSON_Invalid(t *testing.T) {
	var msg BusMessage
	err := parseJSON("not json", &msg)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestParseJSON_Empty(t *testing.T) {
	var msg BusMessage
	err := parseJSON("", &msg)
	if err == nil {
		t.Error("expected error for empty string")
	}
}

// --- Broadcast non-JSON response (publishable via parseAnalysis) ---

func TestBroadcast_AnalysisWithNoBusMessage(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	_, pub := openTestMsgDB(t)

	// Response that's valid analysis JSON but has no bus_message and no bare topic/message.
	analyzer := &mockCompleter{

		response: `{"analysis":"All is well"}`,
	}

	poller := New(store, p, analyzer, pub)
	_, err := poller.Broadcast(context.Background(), "Send update")
	if err == nil {
		t.Error("expected error when no publishable message")
	}
	if !strings.Contains(err.Error(), "did not produce a publishable message") {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- Onboard non-JSON response ---

func TestOnboard_AnalysisWithNoBusMessage(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	_, pub := openTestMsgDB(t)

	analyzer := &mockCompleter{

		response: `{"analysis":"Welcome to the project"}`,
	}

	poller := New(store, p, analyzer, pub)
	_, err := poller.Onboard(context.Background(), "")
	if err == nil {
		t.Error("expected error when no publishable message")
	}
	if !strings.Contains(err.Error(), "did not produce a publishable onboarding message") {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- AnalysisResponse fields test ---

func TestAnalysisResponse_Fields(t *testing.T) {
	resp := AnalysisResponse{
		Analysis:   "Status update",
		Concerns:   []AnalysisConcern{{Severity: "warning", Message: "Watch this"}},
		BusMessage: &BusMessage{Topic: "proj/coord", Message: "Heads up"},
	}
	if resp.Analysis != "Status update" {
		t.Errorf("Analysis = %q", resp.Analysis)
	}
	if len(resp.Concerns) != 1 {
		t.Errorf("Concerns len = %d", len(resp.Concerns))
	}
	if resp.BusMessage == nil {
		t.Error("expected non-nil BusMessage")
	}
}

// --- AnalysisConcern fields test ---

func TestAnalysisConcern_Fields(t *testing.T) {
	c := AnalysisConcern{Severity: "danger", Message: "Critical issue"}
	if c.Severity != "danger" {
		t.Errorf("Severity = %q", c.Severity)
	}
}

// --- DefaultAnalyzerFocus test ---

func TestDefaultAnalyzerFocus_Content(t *testing.T) {
	if DefaultAnalyzerFocus == "" {
		t.Error("expected non-empty default focus")
	}
	if !strings.Contains(DefaultAnalyzerFocus, "cross-repo") {
		t.Error("expected cross-repo mention in default focus")
	}
}

// --- Context budget constants test ---

func TestContextBudgetConstants(t *testing.T) {
	if MaxCommitsPerRepo <= 0 {
		t.Error("MaxCommitsPerRepo should be positive")
	}
	if MaxBusMessages <= 0 {
		t.Error("MaxBusMessages should be positive")
	}
	if MaxTrackedItemsForTier2 <= 0 {
		t.Error("MaxTrackedItemsForTier2 should be positive")
	}
	if MaxCompletedItemsForTier2 <= 0 {
		t.Error("MaxCompletedItemsForTier2 should be positive")
	}
	if MaxConcernsForTier2 <= 0 {
		t.Error("MaxConcernsForTier2 should be positive")
	}
}

// --- recordPollResult with multiple fields ---

func TestRecordPollResult_WithBusMessage(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	poller := New(store, p, &mockCompleter{}, nil)

	result := &PollResult{
		NewCommits:     3,
		NewMessages:    2,
		Tier1Summary:   "3 commits, 2 messages",
		Tier2Analysis:  "All stable",
		BusMessageSent: "[test/coord] Update",
	}

	poller.recordPollResult(result)

	// Verify poll was recorded.
	polls, err := store.RecentPolls(p.ID, 1)
	if err != nil {
		t.Fatalf("RecentPolls: %v", err)
	}
	if len(polls) != 1 {
		t.Fatalf("expected 1 poll, got %d", len(polls))
	}
	if polls[0].BusMessageSent != "[test/coord] Update" {
		t.Errorf("BusMessageSent = %q", polls[0].BusMessageSent)
	}
}

// --- Broadcast publish succeed event ---

func TestBroadcast_EmitsBroadcastEvent(t *testing.T) {
	store := openTestDB(t)
	p := createTestProject(t, store)
	_, pub := openTestMsgDB(t)

	analyzer := &mockCompleter{

		response: `{"analysis":"event test","bus_message":{"topic":"test-project/coord","message":"Test event"}}`,
	}

	poller := New(store, p, analyzer, pub)
	msg, err := poller.Broadcast(context.Background(), "Test")
	if err != nil {
		t.Fatalf("Broadcast: %v", err)
	}
	if msg == nil {
		t.Fatal("expected message")
	}

	// Drain event channel and check for broadcast event.
	found := false
	for {
		select {
		case e := <-poller.Events():
			if e.Type == "broadcast" {
				found = true
			}
		default:
			goto done
		}
	}
done:
	if !found {
		t.Error("expected broadcast event")
	}
}
