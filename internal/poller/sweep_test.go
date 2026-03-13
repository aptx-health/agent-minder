package poller

import (
	"strings"
	"testing"
	"time"

	"github.com/dustinlange/agent-minder/internal/db"
	gitpkg "github.com/dustinlange/agent-minder/internal/git"
	ghpkg "github.com/dustinlange/agent-minder/internal/github"
)

func TestComputeContentHash_Deterministic(t *testing.T) {
	h1 := computeContentHash("open", "bug,enhancement", "fix the thing", []string{"comment 1", "comment 2"}, nil, false, "")
	h2 := computeContentHash("open", "bug,enhancement", "fix the thing", []string{"comment 1", "comment 2"}, nil, false, "")
	if h1 != h2 {
		t.Error("same input should produce same hash")
	}
	if len(h1) != 64 {
		t.Errorf("expected 64-char hex SHA-256, got %d chars", len(h1))
	}
}

func TestComputeContentHash_LabelOrderIndependent(t *testing.T) {
	h1 := computeContentHash("open", "bug,enhancement", "body", nil, nil, false, "")
	h2 := computeContentHash("open", "enhancement,bug", "body", nil, nil, false, "")
	if h1 != h2 {
		t.Error("label order should not affect hash")
	}
}

func TestComputeContentHash_FieldChangeInvalidates(t *testing.T) {
	base := computeContentHash("open", "bug", "body", []string{"c1"}, nil, false, "")

	// Change state.
	if computeContentHash("closed", "bug", "body", []string{"c1"}, nil, false, "") == base {
		t.Error("state change should invalidate hash")
	}

	// Change labels.
	if computeContentHash("open", "bug,wip", "body", []string{"c1"}, nil, false, "") == base {
		t.Error("label change should invalidate hash")
	}

	// Change body.
	if computeContentHash("open", "bug", "different body", []string{"c1"}, nil, false, "") == base {
		t.Error("body change should invalidate hash")
	}

	// Change comments.
	if computeContentHash("open", "bug", "body", []string{"c1", "c2"}, nil, false, "") == base {
		t.Error("comment change should invalidate hash")
	}

	// Change related commits.
	if computeContentHash("open", "bug", "body", []string{"c1"}, []string{"abc123:fix thing"}, false, "") == base {
		t.Error("commit change should invalidate hash")
	}

	// Change draft status.
	if computeContentHash("open", "bug", "body", []string{"c1"}, nil, true, "") == base {
		t.Error("draft change should invalidate hash")
	}

	// Change review state.
	if computeContentHash("open", "bug", "body", []string{"c1"}, nil, false, "approved") == base {
		t.Error("review state change should invalidate hash")
	}
}

func TestParseItemSweep_ValidJSON(t *testing.T) {
	raw := `{"objective":"Add rate limiting to API","progress":"PR open, 2 approvals pending CI"}`
	resp := parseItemSweep(raw)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Objective != "Add rate limiting to API" {
		t.Errorf("objective = %q", resp.Objective)
	}
	if resp.Progress != "PR open, 2 approvals pending CI" {
		t.Errorf("progress = %q", resp.Progress)
	}
}

func TestParseItemSweep_FencedJSON(t *testing.T) {
	raw := "Here's the summary:\n```json\n{\"objective\":\"Fix auth bug\",\"progress\":\"Under review\"}\n```\n"
	resp := parseItemSweep(raw)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Objective != "Fix auth bug" {
		t.Errorf("objective = %q", resp.Objective)
	}
}

func TestParseItemSweep_PlainTextFallback(t *testing.T) {
	raw := "This issue is about improving performance."
	resp := parseItemSweep(raw)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Progress != raw {
		t.Errorf("progress = %q, want %q", resp.Progress, raw)
	}
	if resp.Objective != "" {
		t.Errorf("objective should be empty for plain text, got %q", resp.Objective)
	}
}

func TestParseItemSweep_Empty(t *testing.T) {
	resp := parseItemSweep("")
	if resp != nil {
		t.Error("expected nil for empty input")
	}
}

func TestBuildItemSweepPrompt(t *testing.T) {
	item := &db.TrackedItem{
		Owner:    "octocat",
		Repo:     "hello-world",
		Number:   42,
		ItemType: "issue",
		Title:    "Fix the thing",
		State:    "open",
		Labels:   "bug,urgent",
	}
	content := &ghpkg.ItemContent{
		Body:     "This is broken",
		Comments: []string{"I can reproduce this", "Working on a fix"},
	}

	prompt := buildItemSweepPrompt(item, content, nil)

	if !strings.Contains(prompt, "octocat/hello-world#42") {
		t.Error("prompt should contain item reference")
	}
	if !strings.Contains(prompt, "Fix the thing") {
		t.Error("prompt should contain title")
	}
	if !strings.Contains(prompt, "open") {
		t.Error("prompt should contain state")
	}
	if !strings.Contains(prompt, "bug,urgent") {
		t.Error("prompt should contain labels")
	}
	if !strings.Contains(prompt, "This is broken") {
		t.Error("prompt should contain body")
	}
	if !strings.Contains(prompt, "I can reproduce this") {
		t.Error("prompt should contain comments")
	}
	if !strings.Contains(prompt, "Comment 1") {
		t.Error("prompt should contain comment numbers")
	}
}

func TestBuildItemSweepPrompt_WithRelatedCommits(t *testing.T) {
	item := &db.TrackedItem{
		Owner:    "octocat",
		Repo:     "hello-world",
		Number:   42,
		ItemType: "issue",
		Title:    "Fix the thing",
		State:    "open",
	}
	content := &ghpkg.ItemContent{
		Body: "This is broken",
	}
	commits := []gitpkg.LogEntry{
		{Hash: "abc1234", Subject: "Add content hashing (#42)", Author: "Dev", Date: time.Date(2026, 3, 12, 0, 0, 0, 0, time.UTC)},
		{Hash: "def5678", Subject: "Fix sweep prompt (#42)", Author: "Dev", Date: time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC)},
	}

	prompt := buildItemSweepPrompt(item, content, commits)

	if !strings.Contains(prompt, "Related Git Commits") {
		t.Error("prompt should contain related commits section")
	}
	if !strings.Contains(prompt, "abc1234") {
		t.Error("prompt should contain commit hash")
	}
	if !strings.Contains(prompt, "Add content hashing (#42)") {
		t.Error("prompt should contain commit subject")
	}
}

func TestBuildItemSweepPrompt_PRMetadata(t *testing.T) {
	item := &db.TrackedItem{
		Owner:       "octocat",
		Repo:        "hello-world",
		Number:      10,
		ItemType:    "pull_request",
		Title:       "Add feature",
		State:       "open",
		IsDraft:     true,
		ReviewState: "changes_requested",
	}
	content := &ghpkg.ItemContent{Body: "New feature"}

	prompt := buildItemSweepPrompt(item, content, nil)

	if !strings.Contains(prompt, "Draft:** yes") {
		t.Error("prompt should contain draft status for draft PR")
	}
	if !strings.Contains(prompt, "Review:** changes_requested") {
		t.Error("prompt should contain review state for PR")
	}

	// Non-PR should not have draft/review.
	item.ItemType = "issue"
	prompt = buildItemSweepPrompt(item, content, nil)
	if strings.Contains(prompt, "Draft:") {
		t.Error("issue prompt should not contain draft status")
	}
}

func TestBuildItemSweepPrompt_TruncatesLongBody(t *testing.T) {
	item := &db.TrackedItem{
		Owner:    "o",
		Repo:     "r",
		Number:   1,
		ItemType: "issue",
		Title:    "Test",
		State:    "open",
	}
	longBody := strings.Repeat("x", 3000)
	content := &ghpkg.ItemContent{
		Body: longBody,
	}

	prompt := buildItemSweepPrompt(item, content, nil)
	if !strings.Contains(prompt, "[truncated]") {
		t.Error("long body should be truncated")
	}
}
