// Package poller implements the periodic monitoring loop.
// It checks git repos and the message bus for changes, then runs a two-tier
// LLM pipeline: tier 1 (Haiku) summarizes, tier 2 (Sonnet) analyzes and
// optionally publishes to the agent-msg bus.
// THIS FILE CONTAINS PROMPTS FOR MINDER AGENTS
package poller

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dustinlange/agent-minder/internal/config"
	"github.com/dustinlange/agent-minder/internal/db"
	gitpkg "github.com/dustinlange/agent-minder/internal/git"
	ghpkg "github.com/dustinlange/agent-minder/internal/github"
	"github.com/dustinlange/agent-minder/internal/llm"
	"github.com/dustinlange/agent-minder/internal/msgbus"
)

// debugEnabled returns true when MINDER_DEBUG is set to any non-empty value.
func debugEnabled() bool {
	return os.Getenv("MINDER_DEBUG") != ""
}

// debugLogger is a structured JSON logger for LLM prompt/response tracing.
// Nil when MINDER_DEBUG is not set.
var debugLogger *slog.Logger
var debugLogFile *os.File

// initDebugLog sets up structured JSON logging to ~/.agent-minder/debug.log.
// Safe to call multiple times; only initializes once.
func initDebugLog() {
	if debugLogger != nil || !debugEnabled() {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	logPath := filepath.Join(home, ".agent-minder", "debug.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return
	}
	debugLogFile = f
	debugLogger = slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// closeDebugLog closes the debug log file.
func closeDebugLog() {
	if debugLogFile != nil {
		_ = debugLogFile.Close()
	}
}

// debugLog logs a structured message to the debug log file.
// Does nothing when MINDER_DEBUG is not set.
func debugLog(msg string, attrs ...any) {
	if debugLogger == nil {
		return
	}
	debugLogger.Info(msg, attrs...)
}

// relativeAge returns a human-readable duration string (e.g., "5m", "2h") from
// an ISO datetime string to now. Returns "??" if the timestamp cannot be parsed.
func relativeAge(isoTime string) string {
	t, err := time.Parse("2006-01-02 15:04:05", isoTime)
	if err != nil {
		return "??"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m > 0 {
			return fmt.Sprintf("%dh%dm", h, m)
		}
		return fmt.Sprintf("%dh", h)
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// Event is emitted by the poller for the TUI to consume.
type Event struct {
	Time       time.Time
	Type       string // "poll", "error", "paused", "resumed", "broadcast", "user"
	Summary    string
	PollResult *PollResult
}

// TrackedItemChange records a status change for a tracked item.
type TrackedItemChange struct {
	Ref       string // "owner/repo#number"
	Title     string
	OldStatus string
	NewStatus string
}

// PollResult holds the outcome of a single poll cycle.
type PollResult struct {
	NewCommits          int
	NewMessages         int
	NewWorktrees        int
	TrackedItemChanges  []TrackedItemChange
	Tier1Summary        string
	Tier2Analysis       string
	BusMessageSent      string
	Concerns            []string
	Duration            time.Duration
}

// LLMResponse returns the best available response for display.
func (r *PollResult) LLMResponse() string {
	if r.Tier2Analysis != "" {
		return r.Tier2Analysis
	}
	return r.Tier1Summary
}

// Poller runs the monitoring loop.
type Poller struct {
	store     *db.Store
	project   *db.Project
	provider  llm.Provider
	publisher *msgbus.Publisher
	events    chan Event

	mu              sync.Mutex
	paused          bool
	cancel          context.CancelFunc
	stopped         chan struct{}
	intervalChanged chan time.Duration // signals run loop to reset ticker

	autopilotStatus func() string // optional callback for autopilot status injection
}

// New creates a new Poller. Publisher may be nil if bus publishing is not available.
func New(store *db.Store, project *db.Project, provider llm.Provider, publisher *msgbus.Publisher) *Poller {
	return &Poller{
		store:           store,
		project:         project,
		provider:        provider,
		publisher:       publisher,
		events:          make(chan Event, 64),
		intervalChanged: make(chan time.Duration, 1),
	}
}

// Events returns the channel of events for the TUI.
func (p *Poller) Events() <-chan Event {
	return p.events
}

// Project returns the poller's project.
func (p *Poller) Project() *db.Project {
	return p.project
}

// Provider returns the poller's LLM provider.
func (p *Poller) Provider() llm.Provider {
	return p.provider
}

// SetAutopilotStatusFunc sets a callback that returns autopilot status text
// for injection into the tier 2 analyzer prompt.
func (p *Poller) SetAutopilotStatusFunc(fn func() string) {
	p.mu.Lock()
	p.autopilotStatus = fn
	p.mu.Unlock()
}

// SetRefreshInterval updates the poll interval at runtime.
// The change takes effect on the next tick cycle.
func (p *Poller) SetRefreshInterval(d time.Duration) {
	p.mu.Lock()
	p.project.RefreshIntervalSec = int(d.Seconds())
	p.mu.Unlock()
	// Non-blocking send to signal the run loop.
	select {
	case p.intervalChanged <- d:
	default:
	}
}

// Start begins the polling loop in a goroutine.
func (p *Poller) Start(ctx context.Context) {
	initDebugLog()
	ctx, p.cancel = context.WithCancel(ctx)
	p.stopped = make(chan struct{})

	go p.run(ctx)
}

// Stop halts the polling loop.
func (p *Poller) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	if p.stopped != nil {
		<-p.stopped
	}
	closeDebugLog()
}

// Pause temporarily stops polling without killing the loop.
func (p *Poller) Pause() {
	p.mu.Lock()
	p.paused = true
	p.mu.Unlock()
	p.emit("paused", "Polling paused", nil)
}

// Resume restarts polling after a pause.
func (p *Poller) Resume() {
	p.mu.Lock()
	p.paused = false
	p.mu.Unlock()
	p.emit("resumed", "Polling resumed", nil)
}

// IsPaused returns whether the poller is currently paused.
func (p *Poller) IsPaused() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.paused
}

// PollNow triggers an immediate poll cycle.
func (p *Poller) PollNow(ctx context.Context) {
	p.emit("polling", "Polling...", nil)
	result, err := p.doPoll(ctx)
	if err != nil {
		p.emit("error", err.Error(), nil)
		return
	}
	p.emit("poll", p.summarize(result), result)
}

// Broadcast sends a user-initiated message through the tier 2 LLM and publishes to the bus.
func (p *Poller) Broadcast(ctx context.Context, userPrompt string) (*BusMessage, error) {
	if p.publisher == nil {
		return nil, fmt.Errorf("bus publishing not available")
	}

	// Gather context.
	repos, _ := p.store.GetRepos(p.project.ID)
	concerns, _ := p.store.ActiveConcerns(p.project.ID)
	recentPolls, _ := p.store.RecentPolls(p.project.ID, 3)

	var contextBuf strings.Builder
	fmt.Fprintf(&contextBuf, "Project: %s\nGoal: %s — %s\n", p.project.Name, p.project.GoalType, p.project.GoalDescription)
	fmt.Fprintf(&contextBuf, "Repos: %d\n", len(repos))
	if len(concerns) > 0 {
		contextBuf.WriteString("\nActive concerns:\n")
		for _, c := range concerns {
			fmt.Fprintf(&contextBuf, "- [%s] %s\n", c.Severity, c.Message)
		}
	}
	if len(recentPolls) > 0 {
		contextBuf.WriteString("\nRecent poll summaries:\n")
		for _, poll := range recentPolls {
			resp := poll.LLMResponse()
			if len(resp) > 200 {
				resp = resp[:200] + "..."
			}
			fmt.Fprintf(&contextBuf, "- %s\n", resp)
		}
	}

	model := p.project.LLMAnalyzerModel
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	system := `You are an AI project coordinator. The user wants to broadcast a message to other agents working on this project.

Given the project context and the user's intent, write a concise, helpful bus message.

Respond with ONLY a JSON object:
{
  "topic": "<project>/coord",
  "message": "your message here"
}

Keep messages actionable and concise. Use the project's coordination topic.`

	prompt := fmt.Sprintf("## Project Context\n%s\n\n## User Request\n%s", contextBuf.String(), userPrompt)

	debugLog("llm call", "stage", "broadcast", "step", "input", "component", "analyzer", "model", model, "system_prompt", system, "user_prompt", prompt)
	resp, err := p.provider.Complete(ctx, &llm.Request{
		Model:     model,
		System:    system,
		Messages:  []llm.Message{{Role: "user", Content: prompt}},
		MaxTokens: 512,
	})
	if err != nil {
		debugLog("llm error", "stage", "broadcast", "step", "error", "component", "analyzer", "error", err.Error())
		return nil, fmt.Errorf("LLM broadcast call: %w", err)
	}
	debugLog("llm response", "stage", "broadcast", "step", "output", "component", "analyzer", "response", resp.Content)

	parsed := parseAnalysis(resp.Content)
	if parsed.BusMessage != nil {
		if err := p.publisher.Publish(parsed.BusMessage.Topic, p.project.MinderIdentity, parsed.BusMessage.Message); err != nil {
			return nil, fmt.Errorf("publishing broadcast: %w", err)
		}
		p.emit("broadcast", fmt.Sprintf("Sent to %s", parsed.BusMessage.Topic), nil)
		return parsed.BusMessage, nil
	}

	// Fallback: the LLM returned a plain message structure (topic+message at top level).
	// Try parsing as a BusMessage directly.
	var msg BusMessage
	raw := strings.TrimSpace(resp.Content)
	if idx := strings.Index(raw, "```"); idx >= 0 {
		start := idx + 3
		if jIdx := strings.Index(raw[idx:], "json"); jIdx >= 0 && jIdx < 10 {
			start = idx + jIdx + 4
		}
		if end := strings.Index(raw[start:], "```"); end >= 0 {
			raw = strings.TrimSpace(raw[start : start+end])
		}
	}
	if err := parseJSON(raw, &msg); err == nil && msg.Topic != "" && msg.Message != "" {
		if err := p.publisher.Publish(msg.Topic, p.project.MinderIdentity, msg.Message); err != nil {
			return nil, fmt.Errorf("publishing broadcast: %w", err)
		}
		p.emit("broadcast", fmt.Sprintf("Sent to %s", msg.Topic), nil)
		return &msg, nil
	}

	return nil, fmt.Errorf("LLM did not produce a publishable message")
}

// Onboard generates an onboarding message for new agents joining the project and
// publishes it to <project>/onboarding using replace semantics (single canonical message).
// The optional userGuidance lets the user steer the message (e.g., "focus on test writing").
func (p *Poller) Onboard(ctx context.Context, userGuidance string) (*BusMessage, error) {
	if p.publisher == nil {
		return nil, fmt.Errorf("bus publishing not available")
	}

	// Gather rich project context.
	repos, _ := p.store.GetRepos(p.project.ID)
	concerns, _ := p.store.ActiveConcerns(p.project.ID)
	recentPolls, _ := p.store.RecentPolls(p.project.ID, 5)
	topics, _ := p.store.GetTopics(p.project.ID)

	var contextBuf strings.Builder
	fmt.Fprintf(&contextBuf, "Project: %s\nGoal: %s — %s\n", p.project.Name, p.project.GoalType, p.project.GoalDescription)
	fmt.Fprintf(&contextBuf, "Minder Identity: %s\n", p.project.MinderIdentity)

	if len(repos) > 0 {
		contextBuf.WriteString("\nRepos:\n")
		for _, r := range repos {
			fmt.Fprintf(&contextBuf, "- %s (%s)\n", r.ShortName, r.Path)
		}
	}

	if len(topics) > 0 {
		contextBuf.WriteString("\nActive topics:\n")
		for _, t := range topics {
			fmt.Fprintf(&contextBuf, "- %s\n", t.Name)
		}
	}

	if len(concerns) > 0 {
		contextBuf.WriteString("\nActive concerns:\n")
		for _, c := range concerns {
			fmt.Fprintf(&contextBuf, "- [%s] (since %s) %s\n", c.Severity, c.CreatedAt, c.Message)
		}
	}

	if len(recentPolls) > 0 {
		contextBuf.WriteString("\nRecent activity summaries:\n")
		for _, poll := range recentPolls {
			resp := poll.LLMResponse()
			if len(resp) > 300 {
				resp = resp[:300] + "..."
			}
			fmt.Fprintf(&contextBuf, "- %s\n", resp)
		}
	}

	model := p.project.LLMAnalyzerModel
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	system := fmt.Sprintf(`You are an AI project coordinator for %q. Generate an onboarding message for a new AI agent joining this project.

The message should help the new agent understand:
1. What the project is about and its current goal
2. What repos are involved and what they do
3. Who else is working (other agents) and their focus areas
4. Current state: recent progress, active concerns, what needs attention
5. How to communicate (message bus topics, coordination patterns)

Write a clear, actionable onboarding briefing. Be concise but thorough — this is the new agent's primary orientation document.

Respond with ONLY a JSON object:
{
  "topic": "%s/onboarding",
  "message": "your onboarding message here"
}`, p.project.Name, p.project.Name)

	var prompt string
	if strings.TrimSpace(userGuidance) != "" {
		prompt = fmt.Sprintf("## Project Context\n%s\n\n## User Guidance for Onboarding\n%s\n\nGenerate the onboarding message incorporating the user's guidance.", contextBuf.String(), userGuidance)
	} else {
		prompt = fmt.Sprintf("## Project Context\n%s\n\nGenerate a general onboarding message for any new agent joining this project.", contextBuf.String())
	}

	debugLog("llm call", "stage", "onboard", "step", "input", "component", "analyzer", "model", model, "system_prompt", system, "user_prompt", prompt)
	resp, err := p.provider.Complete(ctx, &llm.Request{
		Model:     model,
		System:    system,
		Messages:  []llm.Message{{Role: "user", Content: prompt}},
		MaxTokens: 1024,
	})
	if err != nil {
		debugLog("llm error", "stage", "onboard", "step", "error", "component", "analyzer", "error", err.Error())
		return nil, fmt.Errorf("LLM onboard call: %w", err)
	}
	debugLog("llm response", "stage", "onboard", "step", "output", "component", "analyzer", "response", resp.Content)

	parsed := parseAnalysis(resp.Content)
	if parsed.BusMessage != nil {
		if err := p.publisher.PublishReplace(parsed.BusMessage.Topic, p.project.MinderIdentity, parsed.BusMessage.Message); err != nil {
			return nil, fmt.Errorf("publishing onboarding: %w", err)
		}
		p.emit("broadcast", fmt.Sprintf("Onboarding published to %s", parsed.BusMessage.Topic), nil)
		return parsed.BusMessage, nil
	}

	// Fallback: try parsing as a bare BusMessage.
	var msg BusMessage
	raw := strings.TrimSpace(resp.Content)
	if idx := strings.Index(raw, "```"); idx >= 0 {
		start := idx + 3
		if jIdx := strings.Index(raw[idx:], "json"); jIdx >= 0 && jIdx < 10 {
			start = idx + jIdx + 4
		}
		if end := strings.Index(raw[start:], "```"); end >= 0 {
			raw = strings.TrimSpace(raw[start : start+end])
		}
	}
	if err := parseJSON(raw, &msg); err == nil && msg.Topic != "" && msg.Message != "" {
		if err := p.publisher.PublishReplace(msg.Topic, p.project.MinderIdentity, msg.Message); err != nil {
			return nil, fmt.Errorf("publishing onboarding: %w", err)
		}
		p.emit("broadcast", fmt.Sprintf("Onboarding published to %s", msg.Topic), nil)
		return &msg, nil
	}

	return nil, fmt.Errorf("LLM did not produce a publishable onboarding message")
}

// PostUserMessage publishes a verbatim user message to the bus without LLM processing.
// The sender is "user@<minder-identity>" so doPoll picks it up as bus activity.
func (p *Poller) PostUserMessage(ctx context.Context, message string) error {
	if p.publisher == nil {
		return fmt.Errorf("bus publishing not available")
	}

	topic := p.project.Name + "/coord"
	sender := "user@" + p.project.MinderIdentity

	if err := p.publisher.Publish(topic, sender, message); err != nil {
		return fmt.Errorf("publishing user message: %w", err)
	}

	p.emit("user", fmt.Sprintf("Posted to %s", topic), nil)
	return nil
}

func (p *Poller) run(ctx context.Context) {
	defer close(p.stopped)

	// Do an initial poll immediately.
	p.PollNow(ctx)

	ticker := time.NewTicker(p.project.RefreshInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case d := <-p.intervalChanged:
			ticker.Reset(d)
		case <-ticker.C:
			if p.IsPaused() {
				continue
			}
			p.PollNow(ctx)
		}
	}
}

func (p *Poller) doPoll(ctx context.Context) (*PollResult, error) {
	start := time.Now()
	result := &PollResult{}
	debugLog("poll start", "stage", "gather", "step", "start", "project", p.project.Name)

	repos, err := p.store.GetRepos(p.project.ID)
	if err != nil {
		return nil, fmt.Errorf("get repos: %w", err)
	}

	// Gather git activity and sync worktrees.
	// Use last poll time as the lookback boundary so commits aren't double-counted.
	// Fall back to 2x refresh interval on first poll (no prior poll exists).
	var gitSummary strings.Builder
	since := time.Now().Add(-p.project.RefreshInterval() * 2)
	if lastPoll, err := p.store.LastPoll(p.project.ID); err == nil && lastPoll != nil {
		if t, err := time.Parse("2006-01-02 15:04:05", lastPoll.PolledAt); err == nil {
			since = t
		}
	}
	for _, repo := range repos {
		entries, err := gitpkg.LogSince(repo.Path, since)
		if err != nil || len(entries) == 0 {
			continue
		}
		result.NewCommits += len(entries)
		fmt.Fprintf(&gitSummary, "\n### %s (%d new commits)\n", repo.ShortName, len(entries))
		for _, e := range entries {
			fmt.Fprintf(&gitSummary, "- %s: %s (%s)\n", e.Hash[:7], e.Subject, e.Author)
		}
	}

	// Sync worktrees for each repo and include active branches in git summary.
	// Track newly-added worktree paths so we can skip their commits below —
	// a new worktree appearing is mechanical, not new activity (#39).
	// Also collect non-main worktree branches for PR status lookup.
	var worktreeBranchData []repoWorktreeBranches
	newWorktreePaths := make(map[string]bool)
	for _, repo := range repos {
		wtEntries, err := gitpkg.Worktrees(repo.Path)
		if err != nil {
			// Repo path may be invalid — clear stale worktrees from DB.
			existing, _ := p.store.GetWorktrees(repo.ID)
			if len(existing) > 0 {
				_, removed := diffWorktrees(existing, nil)
				if clearErr := p.store.ReplaceWorktrees(repo.ID, nil); clearErr == nil {
					for _, branch := range removed {
						p.emit("worktree", fmt.Sprintf("Removed worktree: %s/%s (repo unavailable)", repo.ShortName, branch), nil)
					}
				}
			}
			p.emit("worktree", fmt.Sprintf("Failed to list worktrees for %s: %v", repo.ShortName, err), nil)
			continue
		}
		// Convert to db.Worktree and check for changes.
		var dbWorktrees []db.Worktree
		var branchNames []string
		for _, wt := range wtEntries {
			dbWorktrees = append(dbWorktrees, db.Worktree{
				RepoID: repo.ID,
				Path:   wt.Path,
				Branch: wt.Branch,
			})
			if wt.Branch != "" {
				branchNames = append(branchNames, wt.Branch)
			}
		}
		// Compare with stored worktrees.
		existing, err := p.store.GetWorktrees(repo.ID)
		if err != nil {
			p.emit("worktree", fmt.Sprintf("Failed to load stored worktrees for %s: %v", repo.ShortName, err), nil)
		}
		if worktreesChanged(existing, dbWorktrees) {
			added, removed := diffWorktrees(existing, dbWorktrees)
			if err := p.store.ReplaceWorktrees(repo.ID, dbWorktrees); err == nil {
				for _, branch := range added {
					p.emit("worktree", fmt.Sprintf("New worktree: %s/%s", repo.ShortName, branch), nil)
					result.NewWorktrees++
				}
				for _, branch := range removed {
					p.emit("worktree", fmt.Sprintf("Removed worktree: %s/%s", repo.ShortName, branch), nil)
				}
			}
			// Record newly-added worktree paths so we skip their commits.
			addedSet := make(map[string]bool, len(added))
			for _, b := range added {
				addedSet[b] = true
			}
			for _, wt := range dbWorktrees {
				branch := wt.Branch
				if branch == "" {
					branch = "(detached)"
				}
				if addedSet[branch] {
					newWorktreePaths[wt.Path] = true
				}
			}
		}
		// Gather commits from non-main worktrees (main repo path already covered above).
		// Skip newly-added worktrees — their existing commits aren't new activity (#39).
		for _, wt := range wtEntries {
			if wt.IsMain || wt.Path == repo.Path {
				continue
			}
			if newWorktreePaths[wt.Path] {
				continue
			}
			entries, err := gitpkg.LogSince(wt.Path, since)
			if err != nil || len(entries) == 0 {
				continue
			}
			result.NewCommits += len(entries)
			label := wt.Branch
			if label == "" {
				label = "detached"
			}
			fmt.Fprintf(&gitSummary, "\n### %s [%s] (%d new commits)\n", repo.ShortName, label, len(entries))
			for _, e := range entries {
				fmt.Fprintf(&gitSummary, "- %s: %s (%s)\n", e.Hash[:7], e.Subject, e.Author)
			}
		}
		// Append active branches to git summary for LLM context.
		if len(branchNames) > 1 { // Only interesting if more than just main.
			fmt.Fprintf(&gitSummary, "\n### %s active branches: %s\n", repo.ShortName, strings.Join(branchNames, ", "))
		}
		// Collect non-main worktree branches for PR status lookup.
		owner, rp := parseGitHubRemote(gitpkg.RemoteURL(repo.Path))
		if owner != "" {
			var branches []string
			for _, wt := range wtEntries {
				if !wt.IsMain && wt.Branch != "" {
					branches = append(branches, wt.Branch)
				}
			}
			if len(branches) > 0 {
				worktreeBranchData = append(worktreeBranchData, repoWorktreeBranches{owner: owner, repo: rp, branches: branches})
			}
		}
	}

	debugLog("gather complete", "stage", "gather", "step", "complete", "component", "git", "commits", result.NewCommits, "worktrees", result.NewWorktrees)

	// Gather message bus activity.
	var msgSummary strings.Builder
	dbPath := msgbus.DefaultDBPath()
	client, err := msgbus.Open(dbPath)
	if err == nil {
		defer func() { _ = client.Close() }()
		msgs, _ := client.RecentMessages(p.project.RefreshInterval()*2, p.project.Name)
		// Filter out our own messages so the minder only sees other agents.
		filtered := msgs[:0]
		for _, m := range msgs {
			if m.Sender != p.project.MinderIdentity {
				filtered = append(filtered, m)
			}
		}
		result.NewMessages = len(filtered)
		if len(filtered) > 0 {
			msgSummary.WriteString("\n### Recent Messages\n")
			for _, m := range filtered {
				age := relativeAge(m.CreatedAt)
				fmt.Fprintf(&msgSummary, "- (%s ago) [%s] %s: %s\n", age, m.Topic, m.Sender, m.Message)
			}
		}
	}

	debugLog("gather complete", "stage", "gather", "step", "complete", "component", "bus", "messages", result.NewMessages)

	// Sweep tracked items: fetch metadata + content, hash check, Haiku summarize.
	trackedItems, err := p.store.GetTrackedItems(p.project.ID)
	if err != nil {
		p.emit("error", fmt.Sprintf("loading tracked items: %v", err), nil)
	}
	var sweepResults []SweepResult
	var trackedChanges string
	if len(trackedItems) > 0 {
		token := config.GetIntegrationToken("github")
		if token != "" {
			gh := ghpkg.NewClient(token)
			sweepResults, trackedChanges = p.sweepTrackedItems(ctx, trackedItems, gh, repos)
			for _, sr := range sweepResults {
				if sr.Changed {
					result.TrackedItemChanges = append(result.TrackedItemChanges, TrackedItemChange{
						Ref:       sr.Item.DisplayRef(),
						Title:     sr.Item.Title,
						OldStatus: sr.OldStatus,
						NewStatus: sr.NewStatus,
					})
					// Emit per-item status change event for the TUI event log.
					p.emit("tracked", fmt.Sprintf("%s: %s → %s", sr.Item.DisplayRef(), sr.OldStatus, sr.NewStatus), nil)
				}
			}
		}
	}
	debugLog("sweep complete", "stage", "sweep", "step", "complete", "items", len(sweepResults))

	// Check if any tracked items had content updates (Haiku ran).
	sweepHadUpdates := false
	for _, sr := range sweepResults {
		if sr.HaikuRan || sr.Changed {
			sweepHadUpdates = true
			break
		}
	}

	// Gather worktree PR status (mechanical, no LLM) — must run before
	// the early return so PR state changes can trigger analysis.
	prStatusSection := p.gatherWorktreePRStatus(ctx, worktreeBranchData)

	// If nothing happened, skip LLM calls.
	if result.NewCommits == 0 && result.NewMessages == 0 && len(result.TrackedItemChanges) == 0 && !sweepHadUpdates && prStatusSection == "" {
		result.Duration = time.Since(start)
		result.Tier1Summary = "No new activity."
		debugLog("poll skip", "stage", "gather", "step", "skip", "project", p.project.Name)
		return result, nil
	}

	// Get active concerns for context.
	concerns, _ := p.store.ActiveConcerns(p.project.ID)

	// --- Tier 1: Parallel Haiku summarizers (git + bus) ---
	tier1Model := p.project.LLMSummarizerModel
	if tier1Model == "" {
		tier1Model = p.project.LLMModel
	}

	var gitTier1, busTier1 string
	var gitErr, busErr error
	var tier1WG sync.WaitGroup

	// Git summarizer agent.
	if result.NewCommits > 0 {
		tier1WG.Add(1)
		go func() {
			defer tier1WG.Done()
			prompt := buildGitSummaryPrompt(p.project, repos, gitSummary.String())
			debugLog("llm call", "stage", "tier1", "step", "input", "component", "git_summarizer", "model", tier1Model, "system_prompt", gitSummarizerSystemPrompt(), "user_prompt", prompt)
			resp, err := p.provider.Complete(ctx, &llm.Request{
				Model:     tier1Model,
				System:    gitSummarizerSystemPrompt(),
				Messages:  []llm.Message{{Role: "user", Content: prompt}},
				MaxTokens: 256,
			})
			if err != nil {
				gitErr = err
				debugLog("llm error", "stage", "tier1", "step", "error", "component", "git_summarizer", "error", err.Error())
				return
			}
			gitTier1 = resp.Content
			debugLog("llm response", "stage", "tier1", "step", "output", "component", "git_summarizer", "response", resp.Content)
		}()
	}

	// Bus summarizer agent.
	if result.NewMessages > 0 {
		tier1WG.Add(1)
		go func() {
			defer tier1WG.Done()
			prompt := buildBusSummaryPrompt(p.project, msgSummary.String())
			debugLog("llm call", "stage", "tier1", "step", "input", "component", "bus_summarizer", "model", tier1Model, "system_prompt", busSummarizerSystemPrompt(), "user_prompt", prompt)
			resp, err := p.provider.Complete(ctx, &llm.Request{
				Model:     tier1Model,
				System:    busSummarizerSystemPrompt(),
				Messages:  []llm.Message{{Role: "user", Content: prompt}},
				MaxTokens: 256,
			})
			if err != nil {
				busErr = err
				debugLog("llm error", "stage", "tier1", "step", "error", "component", "bus_summarizer", "error", err.Error())
				return
			}
			busTier1 = resp.Content
			debugLog("llm response", "stage", "tier1", "step", "output", "component", "bus_summarizer", "response", resp.Content)
		}()
	}

	tier1WG.Wait()

	// Combine tier 1 results for the record.
	var tier1Parts []string
	if gitErr != nil {
		tier1Parts = append(tier1Parts, fmt.Sprintf("Git summarizer error: %v", gitErr))
	} else if gitTier1 != "" {
		tier1Parts = append(tier1Parts, gitTier1)
	}
	if busErr != nil {
		tier1Parts = append(tier1Parts, fmt.Sprintf("Bus summarizer error: %v", busErr))
	} else if busTier1 != "" {
		tier1Parts = append(tier1Parts, busTier1)
	}
	if trackedChanges != "" {
		tier1Parts = append(tier1Parts, "Tracked item status changes: "+trackedChanges)
	}
	if len(tier1Parts) == 0 {
		tier1Parts = append(tier1Parts, "Activity detected but no summaries produced.")
	}
	result.Tier1Summary = strings.Join(tier1Parts, "\n\n")

	// --- Tier 2: Analyzer (Opus) ---
	tier2Model := p.project.LLMAnalyzerModel
	if tier2Model == "" {
		tier2Model = "claude-opus-4-6"
	}

	// Fetch recently completed items for progress context.
	ttl := p.project.MessageTTLSec
	if ttl <= 0 {
		ttl = 14 * 24 * 3600
	}
	completedItems, err := p.store.RecentCompletedItems(p.project.ID, ttl)
	if err != nil {
		p.emit("error", fmt.Sprintf("fetching completed items: %v", err), nil)
	}

	tier2Prompt := p.buildTier2Prompt(gitTier1, busTier1, trackedChanges, prStatusSection, concerns, trackedItems, completedItems)
	debugLog("llm call", "stage", "tier2", "step", "input", "component", "analyzer", "model", tier2Model, "system_prompt", tier2SystemPrompt(p.project.Name, p.project.AnalyzerFocus), "user_prompt", tier2Prompt)

	tier2Resp, err := p.provider.Complete(ctx, &llm.Request{
		Model:     tier2Model,
		System:    tier2SystemPrompt(p.project.Name, p.project.AnalyzerFocus),
		Messages:  []llm.Message{{Role: "user", Content: tier2Prompt}},
		MaxTokens: 1024,
	})
	if err != nil {
		debugLog("llm error", "stage", "tier2", "step", "error", "component", "analyzer", "error", err.Error())
		result.Duration = time.Since(start)
		// Tier 2 failed but tier 1 succeeded — still usable.
		p.recordPollResult(result)
		return result, nil
	}

	debugLog("llm response", "stage", "tier2", "step", "output", "component", "analyzer", "response", tier2Resp.Content)

	// Parse tier 2 structured response.
	analysis := parseAnalysis(tier2Resp.Content)
	result.Tier2Analysis = analysis.Analysis

	// Publish bus message if the analyzer decided one is warranted.
	if analysis.BusMessage != nil && p.publisher != nil {
		topic := analysis.BusMessage.Topic
		msg := analysis.BusMessage.Message
		if err := p.publisher.Publish(topic, p.project.MinderIdentity, msg); err == nil {
			result.BusMessageSent = fmt.Sprintf("[%s] %s", topic, msg)
			debugLog("bus publish", "stage", "publish", "step", "complete", "topic", topic)
		}
	}

	// Reconcile concerns: the analyzer returns the full desired list.
	result.Concerns = reconcileConcerns(p.store, p.project.ID, concerns, analysis.Concerns)
	debugLog("concerns reconciled", "stage", "reconcile", "step", "complete", "active", len(result.Concerns), "previous", len(concerns))

	result.Duration = time.Since(start)
	debugLog("poll complete", "stage", "gather", "step", "complete", "project", p.project.Name, "duration_ms", result.Duration.Milliseconds(), "commits", result.NewCommits, "messages", result.NewMessages)
	p.recordPollResult(result)
	return result, nil
}

func (p *Poller) recordPollResult(result *PollResult) {
	_ = p.store.RecordPoll(&db.Poll{
		ProjectID:      p.project.ID,
		NewCommits:     result.NewCommits,
		NewMessages:    result.NewMessages,
		ConcernsRaised: len(result.Concerns),
		LLMResponseRaw: result.LLMResponse(),
		Tier1Response:  result.Tier1Summary,
		Tier2Response:  result.Tier2Analysis,
		BusMessageSent: result.BusMessageSent,
	})
}

// repoWorktreeBranches holds owner/repo and non-main worktree branches for PR lookup.
type repoWorktreeBranches struct {
	owner    string
	repo     string
	branches []string
}

// gatherWorktreePRStatus checks each non-main worktree branch for an open GitHub PR.
// Returns a formatted section for the tier 2 prompt, or "" if no token or no branches.
func (p *Poller) gatherWorktreePRStatus(ctx context.Context, branchData []repoWorktreeBranches) string {
	debugLog("pr status start", "stage", "gather", "step", "start", "component", "pr_status")
	if len(branchData) == 0 {
		debugLog("pr status skip", "stage", "gather", "step", "skip", "component", "pr_status", "reason", "no branch data")
		return ""
	}
	token := config.GetIntegrationToken("github")
	if token == "" {
		debugLog("pr status skip", "stage", "gather", "step", "skip", "component", "pr_status", "reason", "no github token")
		return ""
	}
	debugLog("pr status checking", "stage", "gather", "step", "input", "component", "pr_status", "repos", len(branchData))
	gh := ghpkg.NewClient(token)

	var b strings.Builder
	b.WriteString("## Worktree PR Status")
	found := false

	for _, rd := range branchData {
		debugLog("pr status repo", "stage", "gather", "component", "pr_status", "repo", fmt.Sprintf("%s/%s", rd.owner, rd.repo), "branches", strings.Join(rd.branches, ","))
		for _, branch := range rd.branches {
			pr, err := gh.FetchPRForBranch(ctx, rd.owner, rd.repo, branch)
			if err != nil {
				debugLog("pr status error", "stage", "gather", "step", "error", "component", "pr_status", "branch", branch, "error", err.Error())
				p.emit("error", fmt.Sprintf("PR lookup for %s/%s branch %q: %v", rd.owner, rd.repo, branch, err), nil)
				fmt.Fprintf(&b, "\n- %s: error checking PR", branch)
				found = true
				continue
			}
			if pr == nil {
				debugLog("pr status none", "stage", "gather", "component", "pr_status", "branch", branch, "result", "no PR")
				fmt.Fprintf(&b, "\n- %s: no PR", branch)
				found = true
				continue
			}
			debugLog("pr status found", "stage", "gather", "step", "output", "component", "pr_status", "branch", branch, "pr_number", pr.Number, "title", pr.Title, "state", pr.State, "draft", pr.Draft, "review", pr.ReviewState)
			parts := []string{pr.State}
			if pr.Draft {
				parts = append(parts, "draft")
			}
			if pr.ReviewState != "" {
				parts = append(parts, "review: "+pr.ReviewState)
			}
			fmt.Fprintf(&b, "\n- %s: PR #%d %q (%s)", branch, pr.Number, pr.Title, strings.Join(parts, ", "))
			found = true
		}
	}

	if !found {
		debugLog("pr status empty", "stage", "gather", "step", "complete", "component", "pr_status", "result", "no branches found")
		return ""
	}
	debugLog("pr status complete", "stage", "gather", "step", "complete", "component", "pr_status", "result", b.String())
	return b.String()
}

// --- Tier 1 Haiku Agent Prompts (focused, parallel) ---

func gitSummarizerSystemPrompt() string {
	return `You are a concise git activity summarizer. Given recent commits across one or more repos for a software project, produce a brief factual summary.

Rules:
- Be terse: 1-3 sentences max
- Focus on what changed, who did it, and which repos
- Note cross-repo patterns or dependencies if visible
- Do NOT provide recommendations — just summarize the facts`
}

func buildGitSummaryPrompt(project *db.Project, repos []db.Repo, gitActivity string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**Project:** %s\n", project.Name)
	fmt.Fprintf(&b, "**Repos:** %d\n\n", len(repos))
	b.WriteString("## Git Commits Since Last Poll\n")
	b.WriteString(gitActivity)
	return b.String()
}

func busSummarizerSystemPrompt() string {
	return `You are a concise message bus summarizer. Given recent inter-agent messages for a software project, produce a brief factual summary.

Rules:
- Be terse: 1-3 sentences max
- Focus on what was communicated, by whom, and any action items or coordination signals
- Note cross-agent patterns or requests
- Each message includes a relative age (e.g., "5m ago", "2h ago"). Prioritize recent messages over older ones. Older messages may have been superseded by newer activity — note their age when summarizing so the analyzer can weigh recency.
- Do NOT provide recommendations — just summarize the facts`
}

func buildBusSummaryPrompt(project *db.Project, msgActivity string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**Project:** %s\n\n", project.Name)
	b.WriteString("## Message Bus Activity\n")
	b.WriteString(msgActivity)
	return b.String()
}

// --- Tier 2 System Prompt (Opus analyzer) ---

func tier2SystemPrompt(projectName, analyzerFocus string) string {
	base := fmt.Sprintf(`You are an AI project analyzer for %q. You receive focused summaries of recent git activity and bus messages (from separate summarizer agents), plus full tracked issue/PR context from sweep agents. Produce a structured analysis.

Respond with a JSON object (no markdown fences):
{
  "analysis": "Your 2-4 sentence analysis with actionable insights",
  "concerns": [
    {"severity": "info|warning|danger", "message": "concise description of concern"}
  ],
  "bus_message": {
    "topic": "%s/coord",
    "message": "message to broadcast to other agents"
  }
}

Rules:
- "analysis": Provide a clear, actionable status update. Synthesize across git, bus, and tracked item context.

- "concerns": Return the FULL list of currently valid concerns. You are given the existing active concerns with timestamps — use them as your starting point. ACTIVELY reconcile each existing concern against ALL current evidence before carrying it forward:
  - If ANY part of a concern is contradicted by new evidence (e.g., a branch now has a PR, an item was merged), you MUST either rewrite the concern to remove the resolved part or drop it entirely.
  - Do NOT carry forward concerns verbatim if the facts have changed — always rewrite to reflect current state.
  - Remove concerns that are fully resolved. Rewrite concerns that are partially resolved (e.g., if a concern mentions 2 branches without PRs but 1 now has a PR, rewrite to mention only the remaining branch).
  - Add new concerns as needed. Update severity or wording if the situation has changed. If there are no concerns, return an empty array.
  - Severity levels: "info" (awareness, no action needed), "warning" (potential issue, monitor), "danger" (blocking or critical, needs immediate attention)
  - Keep each concern to 1-2 sentences. Be specific, not exhaustive.
  - Do NOT raise concerns about closed/merged items — they are done.
  - A "Recently Completed Work" section may be present showing items that were previously tracked and have since been completed. Use this as evidence of forward progress — do NOT flag lack of progress if completed items show recent work.
  - For PRs: Use draft/review state to assess lifecycle. A draft PR is work-in-progress. "changes_requested" means the author needs to address feedback. "approved" means ready to merge. "pending" means awaiting review.

- "bus_message": ONLY include when there is something genuinely actionable that other agents need to know (e.g., breaking changes, coordination needed, blocking issues). Most polls should NOT produce a bus message. Omit this field if not needed.

Recency standards:
- Bus messages include relative ages. Messages older than the current poll window are historical context only — do NOT treat them as current state.
- When a newer message or git activity contradicts an older bus message, the newer evidence takes precedence (e.g., if a merge happened after a "waiting on merge" message, the blocker is resolved).
- Do NOT raise concerns based solely on stale bus messages that may have been superseded by newer activity.

Evidence standards:
- Related git commits in tracked items are the strongest signal of active development. If an item has recent commits, it IS being worked on.
- Only claim something is "actively being worked on" if there is direct evidence: recent commits, PR activity, or explicit comments. Detailed specs alone do not mean work has started.
- Base all claims on the data provided. Do not infer activity that isn't evidenced.
- "Worktree PR Status" is authoritative for branch-to-PR association. Cross-reference it against every existing concern that mentions branches or PRs. A branch with an open PR means work has been submitted for review — you MUST remove or rewrite any concern claiming that branch has no PR, is stalled, or has unclear status. Use the PR review state (approved, changes_requested, pending) to assess readiness.

Keep analysis concise and focused on cross-repo coordination.`, projectName, projectName)

	focus := analyzerFocus
	if focus == "" {
		focus = DefaultAnalyzerFocus
	}
	base += fmt.Sprintf("\n\n## Analyzer Focus\nThe user has configured the following focus for your analysis. This shapes how you interpret data, what you pay attention to, and how you communicate:\n\n%s", focus)

	return base
}

// DefaultAnalyzerFocus is the default analyzer focus used when none is configured.
// It reflects the analyzer's built-in engineering coordinator persona.
const DefaultAnalyzerFocus = `Focus on cross-repo coordination and engineering progress. Be concise, evidence-based, and actionable. Prioritize blockers and coordination needs. Use direct, professional language.`

func (p *Poller) buildTier2Prompt(gitSummary, busSummary, trackedChanges, prStatus string, concerns []db.Concern, trackedItems []db.TrackedItem, completedItems []db.CompletedItem) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## Project Context\n")
	fmt.Fprintf(&b, "Project: %s\n", p.project.Name)
	fmt.Fprintf(&b, "Goal: %s — %s\n\n", p.project.GoalType, p.project.GoalDescription)

	// Separate sections from each tier 1 agent.
	if gitSummary != "" {
		fmt.Fprintf(&b, "## Git Activity Summary\n%s\n\n", gitSummary)
	}
	if busSummary != "" {
		fmt.Fprintf(&b, "## Bus Activity Summary\n%s\n\n", busSummary)
	}
	if trackedChanges != "" {
		fmt.Fprintf(&b, "## Tracked Item Status Changes\n%s\n\n", trackedChanges)
	}
	if prStatus != "" {
		fmt.Fprintf(&b, "%s\n\n", prStatus)
	}

	// Tracked items with full sweep summaries — direct from sweep agents, not compressed.
	if len(trackedItems) > 0 {
		b.WriteString("## Tracked Issues/PRs\n")
		for _, item := range trackedItems {
			typeTag := "issue"
			if item.ItemType == "pull_request" {
				typeTag = "PR"
			}
			fmt.Fprintf(&b, "- [%s] [%s] %s: %s\n", item.LastStatus, typeTag, item.DisplayRef(), item.Title)
			if item.ItemType == "pull_request" && item.State == "open" {
				if item.IsDraft {
					b.WriteString("  Draft: yes\n")
				}
				if item.ReviewState != "" {
					fmt.Fprintf(&b, "  Review: %s\n", item.ReviewState)
				}
			}
			if item.ObjectiveSummary != "" {
				fmt.Fprintf(&b, "  Objective: %s\n", item.ObjectiveSummary)
			}
			if item.ProgressSummary != "" {
				fmt.Fprintf(&b, "  Progress: %s\n", item.ProgressSummary)
			}
		}
		b.WriteString("\n")
	}

	if len(completedItems) > 0 {
		b.WriteString("## Recently Completed Work\n")
		b.WriteString("These items were previously tracked and have been completed. They provide context on recent progress.\n")
		for _, ci := range completedItems {
			typeTag := "issue"
			if ci.ItemType == "pull_request" {
				typeTag = "PR"
			}
			fmt.Fprintf(&b, "- [%s] [%s] %s: %s\n", ci.FinalStatus, typeTag, ci.DisplayRef(), ci.Title)
			if ci.Summary != "" {
				fmt.Fprintf(&b, "  %s\n", ci.Summary)
			}
		}
		b.WriteString("\n")
	}

	// Autopilot status injection (mechanical, from supervisor callback).
	p.mu.Lock()
	autopilotFn := p.autopilotStatus
	p.mu.Unlock()
	if autopilotFn != nil {
		if statusBlock := autopilotFn(); statusBlock != "" {
			b.WriteString(statusBlock)
			b.WriteString("\n")
		}
	}

	b.WriteString("## Active Concerns\n")
	if len(concerns) > 0 {
		b.WriteString("Review and return the updated full list. Remove resolved ones, adjust severity/wording as needed, add new ones.\n")
		for _, c := range concerns {
			fmt.Fprintf(&b, "- [%s] (since %s) %s\n", c.Severity, c.CreatedAt, c.Message)
		}
	} else {
		b.WriteString("No active concerns. Add any if warranted by the activity.\n")
	}
	b.WriteString("\n")

	b.WriteString("Analyze the above and respond with JSON.")
	return b.String()
}

func (p *Poller) summarize(result *PollResult) string {
	parts := []string{}
	if result.NewCommits > 0 {
		parts = append(parts, fmt.Sprintf("%d new commits", result.NewCommits))
	}
	if result.NewMessages > 0 {
		parts = append(parts, fmt.Sprintf("%d new messages", result.NewMessages))
	}
	if result.NewWorktrees > 0 {
		parts = append(parts, fmt.Sprintf("%d new worktrees", result.NewWorktrees))
	}
	if len(result.TrackedItemChanges) > 0 {
		parts = append(parts, fmt.Sprintf("%d tracked item changes", len(result.TrackedItemChanges)))
	}
	if result.BusMessageSent != "" {
		parts = append(parts, "bus message sent")
	}
	if len(parts) == 0 {
		return "No new activity"
	}
	return strings.Join(parts, ", ")
}

// worktreesChanged returns true if the set of worktrees (by path+branch) differs.
func worktreesChanged(existing []db.Worktree, incoming []db.Worktree) bool {
	if len(existing) != len(incoming) {
		return true
	}
	set := make(map[string]bool, len(existing))
	for _, w := range existing {
		set[w.Path+"\x00"+w.Branch] = true
	}
	for _, w := range incoming {
		if !set[w.Path+"\x00"+w.Branch] {
			return true
		}
	}
	return false
}

// diffWorktrees returns branches that were added and removed between existing and incoming sets.
func diffWorktrees(existing, incoming []db.Worktree) (added, removed []string) {
	oldSet := make(map[string]bool, len(existing))
	for _, w := range existing {
		oldSet[w.Path+"\x00"+w.Branch] = true
	}
	newSet := make(map[string]bool, len(incoming))
	for _, w := range incoming {
		newSet[w.Path+"\x00"+w.Branch] = true
	}
	for _, w := range incoming {
		if !oldSet[w.Path+"\x00"+w.Branch] {
			name := w.Branch
			if name == "" {
				name = "(detached)"
			}
			added = append(added, name)
		}
	}
	for _, w := range existing {
		if !newSet[w.Path+"\x00"+w.Branch] {
			name := w.Branch
			if name == "" {
				name = "(detached)"
			}
			removed = append(removed, name)
		}
	}
	return
}

func (p *Poller) emit(typ, summary string, result *PollResult) {
	select {
	case p.events <- Event{
		Time:       time.Now(),
		Type:       typ,
		Summary:    summary,
		PollResult: result,
	}:
	default:
		// Drop event if channel is full.
	}
}
