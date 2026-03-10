// Package poller implements the periodic monitoring loop.
// It checks git repos and the message bus for changes, then asks an LLM
// to analyze anything new and produce insights.
package poller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dustinlange/agent-minder/internal/db"
	gitpkg "github.com/dustinlange/agent-minder/internal/git"
	"github.com/dustinlange/agent-minder/internal/llm"
	"github.com/dustinlange/agent-minder/internal/msgbus"
)

// Event is emitted by the poller for the TUI to consume.
type Event struct {
	Time       time.Time
	Type       string // "poll", "error", "paused", "resumed"
	Summary    string
	PollResult *PollResult
}

// PollResult holds the outcome of a single poll cycle.
type PollResult struct {
	NewCommits  int
	NewMessages int
	LLMResponse string
	Concerns    []string
	Duration    time.Duration
}

// Poller runs the monitoring loop.
type Poller struct {
	store    *db.Store
	project  *db.Project
	provider llm.Provider
	events   chan Event

	mu      sync.Mutex
	paused  bool
	cancel  context.CancelFunc
	stopped chan struct{}
}

// New creates a new Poller.
func New(store *db.Store, project *db.Project, provider llm.Provider) *Poller {
	return &Poller{
		store:   store,
		project: project,
		provider: provider,
		events:  make(chan Event, 64),
	}
}

// Events returns the channel of events for the TUI.
func (p *Poller) Events() <-chan Event {
	return p.events
}

// Start begins the polling loop in a goroutine.
func (p *Poller) Start(ctx context.Context) {
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
	result, err := p.doPoll(ctx)
	if err != nil {
		p.emit("error", err.Error(), nil)
		return
	}
	p.emit("poll", p.summarize(result), result)
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

	repos, err := p.store.GetRepos(p.project.ID)
	if err != nil {
		return nil, fmt.Errorf("get repos: %w", err)
	}

	// Gather git activity.
	var gitSummary strings.Builder
	since := time.Now().Add(-p.project.RefreshInterval() * 2) // Look back 2 intervals.
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

	// Gather message bus activity.
	var msgSummary strings.Builder
	dbPath := msgbus.DefaultDBPath()
	client, err := msgbus.Open(dbPath)
	if err == nil {
		defer client.Close()
		msgs, _ := client.RecentMessages(p.project.RefreshInterval()*2, p.project.Name)
		result.NewMessages = len(msgs)
		if len(msgs) > 0 {
			msgSummary.WriteString("\n### Recent Messages\n")
			for _, m := range msgs {
				fmt.Fprintf(&msgSummary, "- [%s] %s: %s\n", m.Topic, m.Sender, m.Message)
			}
		}
	}

	// If nothing happened, skip LLM call.
	if result.NewCommits == 0 && result.NewMessages == 0 {
		result.Duration = time.Since(start)
		result.LLMResponse = "No new activity."
		return result, nil
	}

	// Get active concerns for context.
	concerns, _ := p.store.ActiveConcerns(p.project.ID)

	// Build LLM prompt.
	prompt := p.buildPrompt(repos, gitSummary.String(), msgSummary.String(), concerns)

	resp, err := p.provider.Complete(ctx, &llm.Request{
		Model:     p.project.LLMModel,
		System:    p.systemPrompt(),
		Messages:  []llm.Message{{Role: "user", Content: prompt}},
		MaxTokens: 1024,
	})
	if err != nil {
		result.Duration = time.Since(start)
		result.LLMResponse = fmt.Sprintf("LLM error: %v", err)
		return result, nil // Don't fail the poll cycle on LLM errors.
	}

	result.LLMResponse = resp.Content
	result.Duration = time.Since(start)

	// Record poll in DB.
	p.store.RecordPoll(&db.Poll{
		ProjectID:   p.project.ID,
		NewCommits:  result.NewCommits,
		NewMessages: result.NewMessages,
		LLMResponse: result.LLMResponse,
	})

	return result, nil
}

func (p *Poller) systemPrompt() string {
	return fmt.Sprintf(`You are an AI project minder monitoring a software project called %q.
Your goal type is %q: %s

Your job is to:
1. Analyze recent git activity and messages across all repos
2. Identify cross-repo dependencies, conflicts, or coordination needs
3. Flag concerns (schema drift, stale branches, missing updates)
4. Provide a brief, actionable summary

Keep responses concise — 2-4 sentences unless something critical is happening.
Format concerns as bullet points starting with WARNING: or INFO:.`,
		p.project.Name,
		p.project.GoalType,
		p.project.GoalDescription,
	)
}

func (p *Poller) buildPrompt(repos []db.Repo, gitActivity, msgActivity string, concerns []db.Concern) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## Current State\n\n")
	fmt.Fprintf(&b, "**Project:** %s\n", p.project.Name)
	fmt.Fprintf(&b, "**Goal:** %s — %s\n", p.project.GoalType, p.project.GoalDescription)
	fmt.Fprintf(&b, "**Repos:** %d\n\n", len(repos))

	if len(concerns) > 0 {
		b.WriteString("## Active Concerns\n")
		for _, c := range concerns {
			fmt.Fprintf(&b, "- [%s] %s\n", c.Severity, c.Message)
		}
		b.WriteString("\n")
	}

	if gitActivity != "" {
		b.WriteString("## Git Activity Since Last Poll\n")
		b.WriteString(gitActivity)
		b.WriteString("\n")
	}

	if msgActivity != "" {
		b.WriteString("## Message Bus Activity\n")
		b.WriteString(msgActivity)
		b.WriteString("\n")
	}

	if gitActivity == "" && msgActivity == "" {
		b.WriteString("No new activity since last poll.\n")
	}

	b.WriteString("\nProvide a brief status update. Flag any concerns that need attention.")

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
	if len(parts) == 0 {
		return "No new activity"
	}
	return strings.Join(parts, ", ")
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
