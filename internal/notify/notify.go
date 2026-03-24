// Package notify provides webhook notifications for autopilot task state changes.
// It supports Slack-compatible incoming webhooks out of the box, as well as a
// generic JSON format for other integrations. Rapid-fire events are batched
// into a single notification to avoid spamming.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// EventType classifies a notification event.
type EventType string

const (
	EventTaskStarted   EventType = "task.started"
	EventTaskCompleted EventType = "task.completed"
	EventTaskBailed    EventType = "task.bailed"
	EventTaskFailed    EventType = "task.failed"
	EventTaskStopped   EventType = "task.stopped"
	EventBudgetLimit   EventType = "budget.limit"
	EventAgentError    EventType = "agent.error"
	EventDiscovered    EventType = "task.discovered"
	EventFinished      EventType = "autopilot.finished"
)

// AllEvents lists every supported event type for use as a default filter.
var AllEvents = []EventType{
	EventTaskStarted,
	EventTaskCompleted,
	EventTaskBailed,
	EventTaskFailed,
	EventTaskStopped,
	EventBudgetLimit,
	EventAgentError,
	EventDiscovered,
	EventFinished,
}

// Event is a single notification event.
type Event struct {
	Type        EventType `json:"type"`
	Project     string    `json:"project"`
	Summary     string    `json:"summary"`
	IssueNumber int       `json:"issue_number,omitempty"`
	IssueTitle  string    `json:"issue_title,omitempty"`
	PRNumber    int       `json:"pr_number,omitempty"`
	Timestamp   time.Time `json:"timestamp"`
}

// Config holds notification configuration for a project.
type Config struct {
	WebhookURL string        // URL to POST notifications to
	Format     string        // "slack" (default) or "generic"
	Events     []EventType   // which events to notify on; nil/empty = all
	BatchDelay time.Duration // how long to wait before flushing batched events (default 5s)
}

// Notifier sends webhook notifications for task state changes.
type Notifier struct {
	cfg    Config
	client *http.Client

	mu      sync.Mutex
	pending []Event
	timer   *time.Timer
	closed  bool
	doneCh  chan struct{}
}

// debugLogger for the notify package.
var debugLogger *slog.Logger

func init() {
	if os.Getenv("MINDER_DEBUG") == "" {
		return
	}
	logPath := os.Getenv("MINDER_LOG")
	if logPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		logPath = filepath.Join(home, ".agent-minder", "debug.log")
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	debugLogger = slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func debugLog(msg string, attrs ...any) {
	if debugLogger == nil {
		return
	}
	debugLogger.Info(msg, attrs...)
}

// New creates a new Notifier. Returns nil if webhook URL is empty.
func New(cfg Config) *Notifier {
	if cfg.WebhookURL == "" {
		return nil
	}
	if cfg.Format == "" {
		cfg.Format = "slack"
	}
	if cfg.BatchDelay <= 0 {
		cfg.BatchDelay = 5 * time.Second
	}
	if len(cfg.Events) == 0 {
		cfg.Events = AllEvents
	}
	return &Notifier{
		cfg: cfg,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		doneCh: make(chan struct{}),
	}
}

// Notify enqueues an event for delivery. Events are batched for up to
// BatchDelay before being sent as a single webhook call.
func (n *Notifier) Notify(evt Event) {
	if n == nil {
		return
	}

	// Check if this event type is in the filter list.
	if !n.shouldNotify(evt.Type) {
		return
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	if n.closed {
		return
	}

	n.pending = append(n.pending, evt)

	// Start or reset the batch timer.
	// Start the batch timer on the first event; subsequent events ride
	// the existing timer so delivery happens within BatchDelay of the first.
	if n.timer == nil {
		n.timer = time.AfterFunc(n.cfg.BatchDelay, n.flush)
	}
}

// Flush sends any pending events immediately.
func (n *Notifier) Flush() {
	if n == nil {
		return
	}
	n.flush()
}

// Close flushes pending events and shuts down the notifier.
func (n *Notifier) Close() {
	if n == nil {
		return
	}
	n.mu.Lock()
	n.closed = true
	if n.timer != nil {
		n.timer.Stop()
		n.timer = nil
	}
	n.mu.Unlock()
	n.flush()
}

func (n *Notifier) shouldNotify(typ EventType) bool {
	for _, e := range n.cfg.Events {
		if e == typ {
			return true
		}
	}
	return false
}

func (n *Notifier) flush() {
	n.mu.Lock()
	events := n.pending
	n.pending = nil
	if n.timer != nil {
		n.timer.Stop()
		n.timer = nil
	}
	n.mu.Unlock()

	if len(events) == 0 {
		return
	}

	debugLog("notify: flushing events",
		"stage", "notify",
		"step", "flush",
		"count", len(events),
	)

	var payload []byte
	var err error

	switch n.cfg.Format {
	case "slack":
		payload, err = buildSlackPayload(events)
	case "discord":
		payload, err = buildDiscordPayload(events)
	default:
		payload, err = buildGenericPayload(events)
	}

	if err != nil {
		debugLog("notify: failed to build payload",
			"stage", "notify",
			"step", "error",
			"error", err.Error(),
		)
		return
	}

	n.send(context.Background(), payload)
}

func (n *Notifier) send(ctx context.Context, payload []byte) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.cfg.WebhookURL, bytes.NewReader(payload))
	if err != nil {
		debugLog("notify: failed to create request",
			"stage", "notify",
			"step", "error",
			"error", err.Error(),
		)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		debugLog("notify: webhook delivery failed",
			"stage", "notify",
			"step", "error",
			"error", err.Error(),
		)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		debugLog("notify: webhook returned non-2xx",
			"stage", "notify",
			"step", "error",
			"status", resp.StatusCode,
		)
	} else {
		debugLog("notify: webhook delivered",
			"stage", "notify",
			"step", "complete",
			"status", resp.StatusCode,
			"events", len(payload),
		)
	}
}

// slackPayload is the Slack incoming webhook format.
type slackPayload struct {
	Text   string       `json:"text"`
	Blocks []slackBlock `json:"blocks,omitempty"`
}

type slackBlock struct {
	Type string     `json:"type"`
	Text *slackText `json:"text,omitempty"`
}

type slackText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func buildSlackPayload(events []Event) ([]byte, error) {
	if len(events) == 1 {
		evt := events[0]
		text := formatEventText(evt)
		p := slackPayload{
			Text: text,
			Blocks: []slackBlock{
				{
					Type: "section",
					Text: &slackText{Type: "mrkdwn", Text: text},
				},
			},
		}
		return json.Marshal(p)
	}

	// Batch: header + individual lines.
	var lines []string
	for _, evt := range events {
		lines = append(lines, formatEventText(evt))
	}

	text := fmt.Sprintf("*%d agent-minder events*\n%s", len(events), strings.Join(lines, "\n"))
	p := slackPayload{
		Text: text,
		Blocks: []slackBlock{
			{
				Type: "section",
				Text: &slackText{Type: "mrkdwn", Text: text},
			},
		},
	}
	return json.Marshal(p)
}

// genericPayload is a simple JSON envelope for non-Slack webhooks.
type genericPayload struct {
	Events []Event `json:"events"`
}

func buildGenericPayload(events []Event) ([]byte, error) {
	return json.Marshal(genericPayload{Events: events})
}

func formatEventText(evt Event) string {
	icon := eventIcon(evt.Type)
	if evt.IssueNumber > 0 {
		ref := fmt.Sprintf("#%d", evt.IssueNumber)
		if evt.IssueTitle != "" {
			ref = fmt.Sprintf("#%d (%s)", evt.IssueNumber, evt.IssueTitle)
		}
		return fmt.Sprintf("%s *[%s]* %s — %s", icon, evt.Project, evt.Summary, ref)
	}
	return fmt.Sprintf("%s *[%s]* %s", icon, evt.Project, evt.Summary)
}

func eventIcon(typ EventType) string {
	switch typ {
	case EventTaskStarted:
		return "🚀"
	case EventTaskCompleted:
		return "✅"
	case EventTaskBailed:
		return "🏳️"
	case EventTaskFailed:
		return "❌"
	case EventTaskStopped:
		return "⏹️"
	case EventBudgetLimit:
		return "💰"
	case EventAgentError:
		return "⚠️"
	case EventDiscovered:
		return "🔍"
	case EventFinished:
		return "🏁"
	default:
		return "📋"
	}
}

// MapEventType maps autopilot event type strings (as used in supervisor.go)
// to notification EventType constants. Returns empty string if the event
// type should not trigger a notification.
func MapEventType(supervisorType string) EventType {
	switch supervisorType {
	case "started":
		return EventTaskStarted
	case "completed":
		return EventTaskCompleted
	case "bailed":
		return EventTaskBailed
	case "failed":
		return EventTaskFailed
	case "stopped":
		return EventTaskStopped
	case "finished":
		return EventFinished
	case "discovered":
		return EventDiscovered
	case "error":
		return EventAgentError
	default:
		return ""
	}
}

// --- Discord webhook payload ---

// discordWebhookPayload is the Discord incoming webhook format with embeds.
type discordWebhookPayload struct {
	Embeds []discordEmbed `json:"embeds"`
}

type discordEmbed struct {
	Title       string              `json:"title,omitempty"`
	Description string              `json:"description"`
	Color       int                 `json:"color"`
	Footer      *discordEmbedFooter `json:"footer,omitempty"`
	Timestamp   string              `json:"timestamp,omitempty"`
}

type discordEmbedFooter struct {
	Text string `json:"text"`
}

func buildDiscordPayload(events []Event) ([]byte, error) {
	embeds := make([]discordEmbed, 0, len(events))
	for _, evt := range events {
		embeds = append(embeds, discordEmbed{
			Title:       string(evt.Type),
			Description: formatEventText(evt),
			Color:       discordEventColor(evt.Type),
			Footer:      &discordEmbedFooter{Text: evt.Project},
			Timestamp:   evt.Timestamp.Format("2006-01-02T15:04:05Z"),
		})
	}

	// Discord webhooks allow max 10 embeds per message.
	if len(embeds) > 10 {
		embeds = embeds[:10]
	}

	return json.Marshal(discordWebhookPayload{Embeds: embeds})
}

func discordEventColor(typ EventType) int {
	switch typ {
	case EventTaskStarted, EventDiscovered:
		return 0x3498db // blue
	case EventTaskCompleted, EventFinished:
		return 0x2ecc71 // green
	case EventTaskBailed:
		return 0xe67e22 // orange
	case EventTaskFailed, EventTaskStopped:
		return 0xe74c3c // red
	case EventBudgetLimit, EventAgentError:
		return 0xf1c40f // yellow
	default:
		return 0x95a5a6 // gray
	}
}

// ParseEventTypes parses a comma-separated list of event type strings.
// Returns nil (meaning "all events") if the input is empty.
func ParseEventTypes(s string) []EventType {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var result []EventType
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		result = append(result, EventType(p))
	}
	return result
}
