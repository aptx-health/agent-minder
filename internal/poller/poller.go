// Package poller implements the periodic monitoring loop.
// It checks git repos and the message bus for changes, then runs an LLM
// analysis pipeline via the Claude Code CLI and optionally publishes to
// the agent-msg bus.
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

	"github.com/google/uuid"

	"github.com/dustinlange/agent-minder/internal/claudecli"
	"github.com/dustinlange/agent-minder/internal/config"
	"github.com/dustinlange/agent-minder/internal/db"
	gitpkg "github.com/dustinlange/agent-minder/internal/git"
	ghpkg "github.com/dustinlange/agent-minder/internal/github"
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
	logPath := os.Getenv("MINDER_LOG")
	if logPath == "" {
		logPath = filepath.Join(home, ".agent-minder", "debug.log")
	}
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
	NewCommits         int
	NewMessages        int
	NewWorktrees       int
	TrackedItemChanges []TrackedItemChange
	Tier1Summary       string
	Tier2Analysis      string
	BusMessageSent     string
	Duration           time.Duration
	StatusOnly         bool // true for status-only polls (no LLM analysis)
	NoNewActivity      bool // true when manual poll found nothing new
}

// LLMResponse returns the best available response for display.
func (r *PollResult) LLMResponse() string {
	if r.Tier2Analysis != "" {
		return r.Tier2Analysis
	}
	return r.Tier1Summary
}

// Poller runs the monitoring loop with separate status and analysis tickers.
type Poller struct {
	store     *db.Store
	project   *db.Project
	completer claudecli.Completer // claude CLI for all LLM calls
	publisher *msgbus.Publisher
	events    chan Event

	mu                    sync.Mutex
	paused                bool
	cancel                context.CancelFunc
	stopped               chan struct{}
	statusIntervalChanged chan time.Duration

	autopilotDepGraph func() string // optional callback for autopilot dependency graph
}

// New creates a new Poller. Publisher may be nil if bus publishing is not available.
// The completer is used for all LLM calls (analysis, broadcast, onboard, sweep).
func New(store *db.Store, project *db.Project, completer claudecli.Completer, publisher *msgbus.Publisher) *Poller {
	return &Poller{
		store:                 store,
		project:               project,
		completer:             completer,
		publisher:             publisher,
		events:                make(chan Event, 64),
		statusIntervalChanged: make(chan time.Duration, 1),
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

// Completer returns the poller's CLI completer.
// Used by autopilot for dependency analysis calls.
func (p *Poller) Completer() claudecli.Completer {
	return p.completer
}

// SetAutopilotDepGraphFunc sets a callback that returns the autopilot dependency graph
// for injection into the tier 2 analyzer prompt.
func (p *Poller) SetAutopilotDepGraphFunc(fn func() string) {
	p.mu.Lock()
	p.autopilotDepGraph = fn
	p.mu.Unlock()
}

// SetStatusInterval updates the status poll interval at runtime.
func (p *Poller) SetStatusInterval(d time.Duration) {
	p.mu.Lock()
	p.project.StatusIntervalSec = int(d.Seconds())
	p.mu.Unlock()
	select {
	case p.statusIntervalChanged <- d:
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

// PollNow triggers an immediate full poll cycle (status + analysis).
func (p *Poller) PollNow(ctx context.Context) {
	p.emit("polling", "Polling...", nil)
	result, err := p.doPoll(ctx)
	if err != nil {
		p.emit("error", err.Error(), nil)
		return
	}
	p.emit("poll", p.summarize(result), result)
}

// StatusNow triggers an immediate status-only poll cycle (no LLM analysis).
func (p *Poller) StatusNow(ctx context.Context) {
	p.emit("polling", "Status check...", nil)
	result, err := p.doStatusPoll(ctx)
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
		model = "opus"
	}

	system := `You are an AI project coordinator. The user wants to broadcast a message to other agents working on this project.

Given the project context and the user's intent, write a concise, helpful bus message.

Keep messages actionable and concise. Use the project's coordination topic.`

	prompt := fmt.Sprintf("## Project Context\n%s\n\n## User Request\n%s", contextBuf.String(), userPrompt)

	busMessageSchema := `{"type":"object","properties":{"topic":{"type":"string"},"message":{"type":"string"}},"required":["topic","message"]}`

	debugLog("llm call", "stage", "broadcast", "step", "input", "component", "analyzer", "model", model,
		"system_prompt", system, "user_prompt", prompt,
		"est_system_tokens", estimateTokens(system), "est_user_tokens", estimateTokens(prompt))
	resp, err := p.completer.Complete(ctx, &claudecli.Request{
		SystemPrompt: system,
		Prompt:       prompt,
		Model:        model,
		JSONSchema:   busMessageSchema,
	})
	if err != nil {
		debugLog("llm error", "stage", "broadcast", "step", "error", "component", "analyzer", "error", err.Error())
		return nil, fmt.Errorf("LLM broadcast call: %w", err)
	}
	debugLog("llm response", "stage", "broadcast", "step", "output", "component", "analyzer", "response", resp.Content(), "input_tokens", resp.InputTokens, "output_tokens", resp.OutputTokens)

	// Parse the structured output directly as a BusMessage.
	var msg BusMessage
	content := resp.Content()
	if err := parseJSON(content, &msg); err == nil && msg.Topic != "" && msg.Message != "" {
		if err := p.publisher.Publish(msg.Topic, p.project.MinderIdentity, msg.Message); err != nil {
			return nil, fmt.Errorf("publishing broadcast: %w", err)
		}
		p.emit("broadcast", fmt.Sprintf("Sent to %s", msg.Topic), nil)
		return &msg, nil
	}

	// Fallback: try parsing from the full analysis envelope.
	parsed := parseAnalysis(content)
	if parsed.BusMessage != nil {
		if err := p.publisher.Publish(parsed.BusMessage.Topic, p.project.MinderIdentity, parsed.BusMessage.Message); err != nil {
			return nil, fmt.Errorf("publishing broadcast: %w", err)
		}
		p.emit("broadcast", fmt.Sprintf("Sent to %s", parsed.BusMessage.Topic), nil)
		return parsed.BusMessage, nil
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
		model = "opus"
	}

	system := fmt.Sprintf(`You are an AI project coordinator for %q. Generate an onboarding message for a new AI agent joining this project.

The message should help the new agent understand:
1. What the project is about and its current goal
2. What repos are involved and what they do
3. Who else is working (other agents) and their focus areas
4. Current state: recent progress, active concerns, what needs attention
5. How to communicate (message bus topics, coordination patterns)

Write a clear, actionable onboarding briefing. Be concise but thorough — this is the new agent's primary orientation document.`, p.project.Name)

	var prompt string
	if strings.TrimSpace(userGuidance) != "" {
		prompt = fmt.Sprintf("## Project Context\n%s\n\n## User Guidance for Onboarding\n%s\n\nGenerate the onboarding message incorporating the user's guidance.", contextBuf.String(), userGuidance)
	} else {
		prompt = fmt.Sprintf("## Project Context\n%s\n\nGenerate a general onboarding message for any new agent joining this project.", contextBuf.String())
	}

	busMessageSchema := `{"type":"object","properties":{"topic":{"type":"string"},"message":{"type":"string"}},"required":["topic","message"]}`

	debugLog("llm call", "stage", "onboard", "step", "input", "component", "analyzer", "model", model,
		"system_prompt", system, "user_prompt", prompt,
		"est_system_tokens", estimateTokens(system), "est_user_tokens", estimateTokens(prompt))
	resp, err := p.completer.Complete(ctx, &claudecli.Request{
		SystemPrompt: system,
		Prompt:       prompt,
		Model:        model,
		JSONSchema:   busMessageSchema,
	})
	if err != nil {
		debugLog("llm error", "stage", "onboard", "step", "error", "component", "analyzer", "error", err.Error())
		return nil, fmt.Errorf("LLM onboard call: %w", err)
	}
	debugLog("llm response", "stage", "onboard", "step", "output", "component", "analyzer", "response", resp.Content(), "input_tokens", resp.InputTokens, "output_tokens", resp.OutputTokens)

	// Parse the structured output directly as a BusMessage.
	var msg BusMessage
	content := resp.Content()
	if err := parseJSON(content, &msg); err == nil && msg.Topic != "" && msg.Message != "" {
		if err := p.publisher.PublishReplace(msg.Topic, p.project.MinderIdentity, msg.Message); err != nil {
			return nil, fmt.Errorf("publishing onboarding: %w", err)
		}
		p.emit("broadcast", fmt.Sprintf("Onboarding published to %s", msg.Topic), nil)
		return &msg, nil
	}

	// Fallback: try parsing from the full analysis envelope.
	parsed := parseAnalysis(content)
	if parsed.BusMessage != nil {
		if err := p.publisher.PublishReplace(parsed.BusMessage.Topic, p.project.MinderIdentity, parsed.BusMessage.Message); err != nil {
			return nil, fmt.Errorf("publishing onboarding: %w", err)
		}
		p.emit("broadcast", fmt.Sprintf("Onboarding published to %s", parsed.BusMessage.Topic), nil)
		return parsed.BusMessage, nil
	}

	return nil, fmt.Errorf("LLM did not produce a publishable onboarding message")
}

// QueryAnalyzer sends a user message to the persistent analyzer session and returns
// the response text. Requires an existing session (run analysis first via PollNow).
func (p *Poller) QueryAnalyzer(ctx context.Context, message string) (string, error) {
	if p.project.AnalyzerSessionID == "" {
		return "", fmt.Errorf("no analyzer session — run analysis first (R)")
	}

	analyzerModel := p.project.LLMAnalyzerModel
	if analyzerModel == "" {
		analyzerModel = "sonnet"
	}

	prompt := fmt.Sprintf("Current time: %s\n\n%s", time.Now().Format("2006-01-02 15:04:05"), message)

	debugLog("llm call", "stage", "query", "step", "input", "component", "analyzer", "model", analyzerModel,
		"user_prompt", prompt, "resume_session", p.project.AnalyzerSessionID)

	resp, err := p.completer.Complete(ctx, &claudecli.Request{
		Prompt:          prompt,
		Model:           analyzerModel,
		ResumeSessionID: p.project.AnalyzerSessionID,
	})
	if err != nil {
		// Session may have expired — clear and report.
		debugLog("llm error", "stage", "query", "step", "error", "component", "analyzer", "error", err.Error())
		p.project.AnalyzerSessionID = ""
		_ = p.store.UpdateAnalyzerSessionID(p.project.ID, "")
		return "", fmt.Errorf("analyzer session expired, run analysis again (R): %w", err)
	}

	debugLog("llm response", "stage", "query", "step", "output", "component", "analyzer",
		"response", resp.Result, "input_tokens", resp.InputTokens, "output_tokens", resp.OutputTokens)

	return resp.Result, nil
}

func (p *Poller) run(ctx context.Context) {
	defer close(p.stopped)

	// Initial status-only check (no LLM analysis) to avoid spending tokens on startup.
	p.StatusNow(ctx)

	// Status ticker for mechanical checks. Analysis is user-initiated only (R key).
	statusTicker := time.NewTicker(p.project.StatusInterval())
	defer statusTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case d := <-p.statusIntervalChanged:
			statusTicker.Reset(d)
		case <-statusTicker.C:
			if p.IsPaused() {
				continue
			}
			p.StatusNow(ctx)
		}
	}
}

// gatherResult holds the output of the gather phase, shared between status and analysis polls.
type gatherResult struct {
	result          *PollResult
	repos           []db.Repo
	gitSummary      string
	msgSummary      string
	trackedItems    []db.TrackedItem
	trackedChanges  string
	sweepResults    []SweepResult
	sweepHadUpdates bool
}

// gatherActivity runs the mechanical status checks: git, bus, tracked items, PR status.
func (p *Poller) gatherActivity(ctx context.Context) (*gatherResult, error) {
	result := &PollResult{}
	debugLog("poll start", "stage", "gather", "step", "start", "project", p.project.Name)

	repos, err := p.store.GetRepos(p.project.ID)
	if err != nil {
		return nil, fmt.Errorf("get repos: %w", err)
	}

	// Gather git activity and sync worktrees.
	// Use last poll time as the lookback boundary so commits aren't double-counted.
	// Fall back to 2x analysis interval on first poll (no prior poll exists).
	var gitSummary strings.Builder
	since := time.Now().Add(-p.project.AnalysisInterval() * 2)
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
		displayed, overflow := truncateSlice(entries, MaxCommitsPerRepo)
		for _, e := range displayed {
			fmt.Fprintf(&gitSummary, "- %s: %s (%s)\n", e.Hash[:7], e.Subject, e.Author)
		}
		if overflow != "" {
			fmt.Fprintf(&gitSummary, "%s\n", overflow)
		}
	}

	// Sync worktrees for each repo and include active branches in git summary.
	// Track newly-added worktree paths so we can skip their commits below —
	// a new worktree appearing is mechanical, not new activity (#39).
	// Also collect non-main worktree branches for PR status lookup.
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
			displayed, overflow := truncateSlice(entries, MaxCommitsPerRepo)
			for _, e := range displayed {
				fmt.Fprintf(&gitSummary, "- %s: %s (%s)\n", e.Hash[:7], e.Subject, e.Author)
			}
			if overflow != "" {
				fmt.Fprintf(&gitSummary, "%s\n", overflow)
			}
		}
		// Append active branches to git summary for LLM context.
		if len(branchNames) > 1 { // Only interesting if more than just main.
			fmt.Fprintf(&gitSummary, "\n### %s active branches: %s\n", repo.ShortName, strings.Join(branchNames, ", "))
		}
	}

	debugLog("gather complete", "stage", "gather", "step", "complete", "component", "git", "commits", result.NewCommits, "worktrees", result.NewWorktrees)

	// Gather message bus activity.
	var msgSummary strings.Builder
	dbPath := msgbus.DefaultDBPath()
	client, err := msgbus.Open(dbPath)
	if err == nil {
		defer func() { _ = client.Close() }()
		msgs, _ := client.RecentMessages(p.project.AnalysisInterval()*2, p.project.Name)
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
			displayed, overflow := truncateSlice(filtered, MaxBusMessages)
			for _, m := range displayed {
				age := relativeAge(m.CreatedAt)
				fmt.Fprintf(&msgSummary, "- (%s ago) [%s] %s: %s\n", age, m.Topic, m.Sender, m.Message)
			}
			if overflow != "" {
				fmt.Fprintf(&msgSummary, "%s\n", overflow)
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

	return &gatherResult{
		result:          result,
		repos:           repos,
		gitSummary:      gitSummary.String(),
		msgSummary:      msgSummary.String(),
		trackedItems:    trackedItems,
		trackedChanges:  trackedChanges,
		sweepResults:    sweepResults,
		sweepHadUpdates: sweepHadUpdates,
	}, nil
}

// doStatusPoll runs a status-only poll: gather activity without LLM analysis.
func (p *Poller) doStatusPoll(ctx context.Context) (*PollResult, error) {
	start := time.Now()
	debugLog("status poll start", "stage", "status", "step", "start", "project", p.project.Name)

	gathered, err := p.gatherActivity(ctx)
	if err != nil {
		return nil, err
	}

	result := gathered.result
	result.StatusOnly = true
	result.Duration = time.Since(start)

	if result.NewCommits == 0 && result.NewMessages == 0 && len(result.TrackedItemChanges) == 0 && !gathered.sweepHadUpdates {
		result.Tier1Summary = "No new activity."
		debugLog("status poll skip", "stage", "status", "step", "skip", "project", p.project.Name)
	} else {
		// Build a brief summary so the event log isn't blank.
		result.Tier1Summary = "Status: " + p.summarize(result)
	}

	debugLog("status poll complete", "stage", "status", "step", "complete", "project", p.project.Name, "duration_ms", result.Duration.Milliseconds())
	return result, nil
}

func (p *Poller) doPoll(ctx context.Context) (*PollResult, error) {
	start := time.Now()

	gathered, err := p.gatherActivity(ctx)
	if err != nil {
		return nil, err
	}

	result := gathered.result

	// Build a summary of raw data for the record.
	var tier1Parts []string
	if gathered.gitSummary != "" {
		tier1Parts = append(tier1Parts, gathered.gitSummary)
	}
	if gathered.msgSummary != "" {
		tier1Parts = append(tier1Parts, gathered.msgSummary)
	}
	if gathered.trackedChanges != "" {
		tier1Parts = append(tier1Parts, "Tracked item status changes: "+gathered.trackedChanges)
	}
	if len(tier1Parts) == 0 {
		tier1Parts = append(tier1Parts, "No new activity.")
	}
	result.Tier1Summary = strings.Join(tier1Parts, "\n\n")

	// Skip analyzer if there's nothing new.
	if result.NewCommits == 0 && result.NewMessages == 0 && len(result.TrackedItemChanges) == 0 && !gathered.sweepHadUpdates {
		result.NoNewActivity = true
		result.Duration = time.Since(start)
		debugLog("poll skip", "stage", "analysis", "step", "skip", "reason", "no new activity")
		return result, nil
	}

	// --- Persistent analyzer session ---
	analyzerModel := p.project.LLMAnalyzerModel
	if analyzerModel == "" {
		analyzerModel = "sonnet"
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

	// Fetch autopilot tasks to tag tracked items as autopilot-managed vs manual.
	autopilotTasks, _ := p.store.GetAutopilotTasks(p.project.ID)

	// Build the user prompt with raw data.
	analysisPrompt := p.buildAnalysisPrompt(gathered, completedItems, autopilotTasks)

	var req *claudecli.Request
	if p.project.AnalyzerSessionID == "" {
		// First call: create a new session with system prompt.
		sessionID := uuid.New().String()
		analysisSystem := analysisSystemPrompt(p.project.Name, p.project.AnalyzerFocus)

		debugLog("llm call", "stage", "analysis", "step", "input", "component", "analyzer", "model", analyzerModel,
			"system_prompt", analysisSystem, "user_prompt", analysisPrompt,
			"session_id", sessionID, "new_session", true)

		req = &claudecli.Request{
			SystemPrompt: analysisSystem,
			Prompt:       analysisPrompt,
			Model:        analyzerModel,
			SessionID:    sessionID,
		}
	} else {
		// Resume existing session.
		debugLog("llm call", "stage", "analysis", "step", "input", "component", "analyzer", "model", analyzerModel,
			"user_prompt", analysisPrompt, "resume_session", p.project.AnalyzerSessionID)

		req = &claudecli.Request{
			Prompt:          analysisPrompt,
			Model:           analyzerModel,
			ResumeSessionID: p.project.AnalyzerSessionID,
		}
	}

	analysisResp, err := p.completer.Complete(ctx, req)
	if err != nil {
		debugLog("llm error", "stage", "analysis", "step", "error", "component", "analyzer", "error", err.Error())

		// If resuming failed, try a fresh session (session may have expired).
		if p.project.AnalyzerSessionID != "" {
			debugLog("session recovery", "stage", "analysis", "step", "retry", "component", "analyzer")
			p.project.AnalyzerSessionID = ""
			_ = p.store.UpdateAnalyzerSessionID(p.project.ID, "")

			sessionID := uuid.New().String()
			analysisSystem := analysisSystemPrompt(p.project.Name, p.project.AnalyzerFocus)
			req = &claudecli.Request{
				SystemPrompt: analysisSystem,
				Prompt:       analysisPrompt,
				Model:        analyzerModel,
				SessionID:    sessionID,
			}
			analysisResp, err = p.completer.Complete(ctx, req)
			if err != nil {
				debugLog("llm error", "stage", "analysis", "step", "error", "component", "analyzer", "error", err.Error())
				result.Duration = time.Since(start)
				p.recordPollResult(result)
				return result, nil
			}
		} else {
			result.Duration = time.Since(start)
			p.recordPollResult(result)
			return result, nil
		}
	}

	// Store the session ID from the response.
	if analysisResp.SessionID != "" && analysisResp.SessionID != p.project.AnalyzerSessionID {
		p.project.AnalyzerSessionID = analysisResp.SessionID
		_ = p.store.UpdateAnalyzerSessionID(p.project.ID, analysisResp.SessionID)
		debugLog("session stored", "stage", "analysis", "step", "complete", "session_id", analysisResp.SessionID)
	}

	debugLog("llm response", "stage", "analysis", "step", "output", "component", "analyzer",
		"response", analysisResp.Result, "input_tokens", analysisResp.InputTokens, "output_tokens", analysisResp.OutputTokens)

	// Response is markdown text, stored directly.
	result.Tier2Analysis = analysisResp.Result

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
		LLMResponseRaw: result.LLMResponse(),
		Tier1Response:  result.Tier1Summary,
		Tier2Response:  result.Tier2Analysis,
		BusMessageSent: result.BusMessageSent,
	})
}

// --- Combined Analysis System Prompt ---

func analysisSystemPrompt(projectName, analyzerFocus string) string {
	base := fmt.Sprintf(`You are the project analyst for %q. You maintain situational awareness across this project's repos, issues, PRs, and autopilot agents.

You'll receive periodic updates with the latest project state. Between updates, you remember what you've seen — use that continuity to track trends, notice changes, and give the operator useful briefings.

Keep responses concise (TUI viewport, ~20-30 lines). Use markdown for structure. Focus on:
- What changed since the last update
- What needs the operator's attention (reviews, manual tasks, blockers)
- Dependency graph progress and what's coming next
- Cross-repo coordination needs

## Formatting rules
Your output renders in a narrow terminal viewport (~100 chars wide). Follow these rules:
- Use headers (##, ###), bold, bullets, and inline code freely
- NEVER use markdown tables — they break in the narrow viewport. Use bulleted lists instead
- Keep lines short. Prefer multiple short bullets over long paragraphs

When the operator asks questions, answer from your accumulated context.

## Work tracking context
- The project owner routinely clears completed work from tracking. A smaller tracked items list indicates progress, not stagnation.
- Items tagged [autopilot] have an explicit dependency graph and execution plan. Do not raise velocity concerns about queued autopilot tasks — they are intentionally waiting on dependencies.
- Items tagged [manual] are not managed by autopilot and may need human attention.
- When a dependency graph is present, use it to reason about blockers and ordering.`, projectName)

	focus := analyzerFocus
	if focus == "" {
		focus = DefaultAnalyzerFocus
	}
	base += fmt.Sprintf("\n\n## Analyzer Focus\n%s", focus)

	return base
}

// DefaultAnalyzerFocus is the default analyzer focus used when none is configured.
// It reflects the analyzer's built-in engineering coordinator persona.
const DefaultAnalyzerFocus = `Focus on cross-repo coordination and engineering progress. Be concise, evidence-based, and actionable. Prioritize blockers and coordination needs. Use direct, professional language.`

func (p *Poller) buildAnalysisPrompt(gathered *gatherResult, completedItems []db.CompletedItem, autopilotTasks []db.AutopilotTask) string {
	trackedItems := gathered.trackedItems

	// Build lookup of autopilot-managed issues: owner/repo#number → status.
	autopilotIndex := make(map[string]string, len(autopilotTasks))
	for _, t := range autopilotTasks {
		key := fmt.Sprintf("%s/%s#%d", t.Owner, t.Repo, t.IssueNumber)
		autopilotIndex[key] = t.Status
	}

	var b strings.Builder

	fmt.Fprintf(&b, "Current time: %s\n\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "## Project Context\n")
	fmt.Fprintf(&b, "Project: %s\n", p.project.Name)
	fmt.Fprintf(&b, "Goal: %s — %s\n\n", p.project.GoalType, p.project.GoalDescription)

	// Include raw git data directly (no tier 1 summarization).
	if gathered.gitSummary != "" {
		fmt.Fprintf(&b, "## Git Activity Since Last Poll\n%s\n\n", gathered.gitSummary)
	}
	// Include raw bus messages directly.
	if gathered.msgSummary != "" {
		fmt.Fprintf(&b, "## Message Bus Activity\n%s\n\n", gathered.msgSummary)
	}
	// Include tracked item status changes.
	if gathered.trackedChanges != "" {
		fmt.Fprintf(&b, "## Tracked Item Status Changes\n%s\n\n", gathered.trackedChanges)
	}

	// Tracked items with autopilot/manual tags.
	if len(trackedItems) > 0 {
		b.WriteString("## Tracked Issues/PRs\n")
		displayedItems, itemOverflow := truncateSlice(trackedItems, MaxTrackedItemsForTier2)
		for _, item := range displayedItems {
			typeTag := "issue"
			if item.ItemType == "pull_request" {
				typeTag = "PR"
			}
			// Tag as autopilot-managed or manual.
			ref := item.DisplayRef()
			managedTag := "manual"
			if apStatus, ok := autopilotIndex[ref]; ok {
				managedTag = "autopilot:" + apStatus
			}
			fmt.Fprintf(&b, "- [%s] [%s] [%s] %s: %s\n", item.LastStatus, typeTag, managedTag, ref, item.Title)
			if item.ItemType == "pull_request" && item.State == "open" {
				if item.IsDraft {
					b.WriteString("  Draft: yes\n")
				}
				if item.ReviewState != "" {
					fmt.Fprintf(&b, "  Review: %s\n", item.ReviewState)
				}
			}
			if item.ProgressSummary != "" {
				fmt.Fprintf(&b, "  Progress: %s\n", item.ProgressSummary)
			}
		}
		if itemOverflow != "" {
			fmt.Fprintf(&b, "%s\n", itemOverflow)
		}
		b.WriteString("\n")
	}

	if len(completedItems) > 0 {
		b.WriteString("## Recently Completed Work\n")
		displayedCompleted, completedOverflow := truncateSlice(completedItems, MaxCompletedItemsForTier2)
		for _, ci := range displayedCompleted {
			typeTag := "issue"
			if ci.ItemType == "pull_request" {
				typeTag = "PR"
			}
			fmt.Fprintf(&b, "- [%s] [%s] %s: %s\n", ci.FinalStatus, typeTag, ci.DisplayRef(), ci.Title)
			if ci.Summary != "" {
				fmt.Fprintf(&b, "  %s\n", ci.Summary)
			}
		}
		if completedOverflow != "" {
			fmt.Fprintf(&b, "%s\n", completedOverflow)
		}
		b.WriteString("\n")
	}

	// Autopilot dependency graph (from supervisor callback).
	p.mu.Lock()
	depGraphFn := p.autopilotDepGraph
	p.mu.Unlock()
	if depGraphFn != nil {
		if graph := depGraphFn(); graph != "" {
			b.WriteString(graph)
			b.WriteString("\n")
		}
	}

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
