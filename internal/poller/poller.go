// Package poller implements the periodic monitoring loop.
// It checks git repos and the message bus for changes, then runs a two-tier
// LLM pipeline: tier 1 (Haiku) summarizes, tier 2 (Sonnet) analyzes and
// optionally publishes to the agent-msg bus.
// THIS FILE CONTAINS PROMPTS FOR MINDER AGENTS
package poller

import (
	"context"
	"fmt"
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

	mu      sync.Mutex
	paused  bool
	cancel  context.CancelFunc
	stopped chan struct{}
}

// New creates a new Poller. Publisher may be nil if bus publishing is not available.
func New(store *db.Store, project *db.Project, provider llm.Provider, publisher *msgbus.Publisher) *Poller {
	return &Poller{
		store:     store,
		project:   project,
		provider:  provider,
		publisher: publisher,
		events:    make(chan Event, 64),
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

	resp, err := p.provider.Complete(ctx, &llm.Request{
		Model:     model,
		System:    system,
		Messages:  []llm.Message{{Role: "user", Content: prompt}},
		MaxTokens: 512,
	})
	if err != nil {
		return nil, fmt.Errorf("LLM broadcast call: %w", err)
	}

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

	resp, err := p.provider.Complete(ctx, &llm.Request{
		Model:     model,
		System:    system,
		Messages:  []llm.Message{{Role: "user", Content: prompt}},
		MaxTokens: 1024,
	})
	if err != nil {
		return nil, fmt.Errorf("LLM onboard call: %w", err)
	}

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
				fmt.Fprintf(&msgSummary, "- [%s] %s: %s\n", m.Topic, m.Sender, m.Message)
			}
		}
	}

	// Check tracked items for status changes.
	var trackedSummary strings.Builder
	trackedItems, err := p.store.GetTrackedItems(p.project.ID)
	if err != nil {
		p.emit("error", fmt.Sprintf("loading tracked items: %v", err), nil)
	}
	if len(trackedItems) > 0 {
		token := config.GetIntegrationToken("github")
		if token != "" {
			gh := ghpkg.NewClient(token)
			for i := range trackedItems {
				item := &trackedItems[i]
				status, err := gh.FetchItemWithHint(ctx, item.Owner, item.Repo, item.Number, item.ItemType)
				if err != nil {
					p.emit("error", fmt.Sprintf("checking %s: %v", item.DisplayRef(), err), nil)
					continue
				}
				newStatus := status.CompactStatus()
				oldStatus := item.LastStatus

				// Update the item regardless.
				item.Title = status.Title
				item.State = status.State
				item.Labels = strings.Join(status.Labels, ",")
				item.LastStatus = newStatus
				item.LastCheckedAt = time.Now().UTC().Format(time.RFC3339)
				if err := p.store.UpdateTrackedItem(item); err != nil {
					p.emit("error", fmt.Sprintf("updating %s: %v", item.DisplayRef(), err), nil)
				}

				if oldStatus != newStatus {
					ref := item.DisplayRef()
					result.TrackedItemChanges = append(result.TrackedItemChanges, TrackedItemChange{
						Ref:       ref,
						Title:     status.Title,
						OldStatus: oldStatus,
						NewStatus: newStatus,
					})
					fmt.Fprintf(&trackedSummary, "- %s: %s → %s (%s)\n", ref, oldStatus, newStatus, status.Title)
				}
			}
		}
	}

	// If nothing happened, skip LLM calls.
	if result.NewCommits == 0 && result.NewMessages == 0 && len(result.TrackedItemChanges) == 0 {
		result.Duration = time.Since(start)
		result.Tier1Summary = "No new activity."
		return result, nil
	}

	// Get active concerns for context.
	concerns, _ := p.store.ActiveConcerns(p.project.ID)

	// Build raw data prompt for tier 1.
	rawPrompt := p.buildPrompt(repos, gitSummary.String(), msgSummary.String(), trackedSummary.String(), trackedItems, concerns)

	// --- Tier 1: Summarizer (Haiku) ---
	tier1Model := p.project.LLMSummarizerModel
	if tier1Model == "" {
		tier1Model = p.project.LLMModel
	}

	tier1Resp, err := p.provider.Complete(ctx, &llm.Request{
		Model:     tier1Model,
		System:    tier1SystemPrompt(),
		Messages:  []llm.Message{{Role: "user", Content: rawPrompt}},
		MaxTokens: 512,
	})
	if err != nil {
		result.Duration = time.Since(start)
		result.Tier1Summary = fmt.Sprintf("Tier 1 LLM error: %v", err)
		// Still record what we have.
		p.recordPollResult(result)
		return result, nil
	}
	result.Tier1Summary = tier1Resp.Content

	// --- Tier 2: Analyzer (Sonnet) ---
	tier2Model := p.project.LLMAnalyzerModel
	if tier2Model == "" {
		tier2Model = "claude-sonnet-4-6"
	}

	tier2Prompt := p.buildTier2Prompt(result.Tier1Summary, concerns)

	tier2Resp, err := p.provider.Complete(ctx, &llm.Request{
		Model:     tier2Model,
		System:    tier2SystemPrompt(p.project.Name),
		Messages:  []llm.Message{{Role: "user", Content: tier2Prompt}},
		MaxTokens: 1024,
	})
	if err != nil {
		result.Duration = time.Since(start)
		// Tier 2 failed but tier 1 succeeded — still usable.
		p.recordPollResult(result)
		return result, nil
	}

	// Parse tier 2 structured response.
	analysis := parseAnalysis(tier2Resp.Content)
	result.Tier2Analysis = analysis.Analysis

	// Publish bus message if the analyzer decided one is warranted.
	if analysis.BusMessage != nil && p.publisher != nil {
		topic := analysis.BusMessage.Topic
		msg := analysis.BusMessage.Message
		if err := p.publisher.Publish(topic, p.project.MinderIdentity, msg); err == nil {
			result.BusMessageSent = fmt.Sprintf("[%s] %s", topic, msg)
		}
	}

	// Reconcile concerns: the analyzer returns the full desired list.
	// Resolve any existing concerns not present in the new list,
	// and add any new ones.
	result.Concerns = reconcileConcerns(p.store, p.project.ID, concerns, analysis.Concerns)

	result.Duration = time.Since(start)
	p.recordPollResult(result)
	return result, nil
}

func (p *Poller) recordPollResult(result *PollResult) {
	p.store.RecordPoll(&db.Poll{
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

func tier1SystemPrompt() string {
	return `You are a concise technical summarizer. Given raw git commits and message bus activity for a software project, produce a brief summary of what happened.

Rules:
- Be terse: 2-4 sentences max
- Focus on what changed and who did it
- Note any cross-repo patterns or dependencies
- Do NOT provide recommendations — just summarize the facts`
}

// Tier 2 System Prompt
func tier2SystemPrompt(projectName string) string {
	return fmt.Sprintf(`You are an AI project analyzer for %q. You receive a summary of recent activity and must produce a structured analysis.

Respond with a JSON object (no markdown fences):
{
  "analysis": "Your 2-4 sentence analysis with actionable insights",
  "concerns": [
    {"severity": "info|warning|danger", "message": "description of concern"}
  ],
  "bus_message": {
    "topic": "%s/coord",
    "message": "message to broadcast to other agents"
  }
}

Rules:
- "analysis": Always provide a clear, actionable status update
- "concerns": Return the FULL list of currently valid concerns. You are given the existing active concerns with timestamps — use them as your starting point. Remove concerns that are resolved or no longer relevant. Add new concerns as needed. Update severity or wording if the situation has changed. If there are no concerns, return an empty array.
  - Severity levels: "info" (awareness, no action needed), "warning" (potential issue, monitor), "danger" (blocking or critical, needs immediate attention)
- "bus_message": ONLY include when there is something genuinely actionable that other agents need to know (e.g., breaking changes, coordination needed, blocking issues). Most polls should NOT produce a bus message. Omit this field if not needed.

Keep analysis concise and focused on cross-repo coordination.`, projectName, projectName)
}

func (p *Poller) buildPrompt(repos []db.Repo, gitActivity, msgActivity, trackedChanges string, trackedItems []db.TrackedItem, concerns []db.Concern) string {
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

	// Include tracked items status.
	if len(trackedItems) > 0 {
		b.WriteString("## Tracked Issues/PRs\n")
		for _, item := range trackedItems {
			fmt.Fprintf(&b, "- [%s] %s: %s\n", item.LastStatus, item.DisplayRef(), item.Title)
		}
		if trackedChanges != "" {
			b.WriteString("\n### Status Changes This Cycle\n")
			b.WriteString(trackedChanges)
		}
		b.WriteString("\n")
	}

	if gitActivity == "" && msgActivity == "" && len(trackedItems) == 0 {
		b.WriteString("No new activity since last poll.\n")
	}

	return b.String()
}

func (p *Poller) buildTier2Prompt(tier1Summary string, concerns []db.Concern) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## Project Context\n")
	fmt.Fprintf(&b, "Project: %s\n", p.project.Name)
	fmt.Fprintf(&b, "Goal: %s — %s\n\n", p.project.GoalType, p.project.GoalDescription)

	fmt.Fprintf(&b, "## Tier 1 Summary\n%s\n\n", tier1Summary)

	b.WriteString("## Active Concerns\n")
	if len(concerns) > 0 {
		b.WriteString("Review and return the updated full list. Remove resolved ones, adjust severity/wording as needed, add new ones.\n")
		for _, c := range concerns {
			fmt.Fprintf(&b, "- [%s] (since %s) %s\n", c.Severity, c.CreatedAt, c.Message)
		}
	} else {
		b.WriteString("No active concerns. Add any if warranted by the activity summary.\n")
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
