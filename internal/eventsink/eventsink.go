// Package eventsink delivers supervisor events matching a jobs.yaml `sinks:`
// subscription to an external webhook or a local command. It is the
// deliberate lightweight alternative to a plugin system: enough to wire up
// Slack/Discord notifications, cost export, or approval glue without a
// process boundary.
//
// Delivery is always best-effort and must never slow down the supervisor:
// each sink has a small bounded queue, a delivery goroutine independent of
// the dispatch loop, and a per-delivery timeout. A slow or dead sink drops
// events (counted and logged) instead of applying backpressure to the event
// bus.
package eventsink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aptx-health/agent-minder/internal/eventbus"
	"github.com/aptx-health/agent-minder/internal/supervisor"
)

const (
	// DefaultQueueSize bounds the per-sink backlog. Once full, new events for
	// that sink are dropped (and counted) rather than blocking dispatch.
	DefaultQueueSize = 32
	// DefaultTimeout bounds a single delivery attempt (including retries for
	// webhook sinks).
	DefaultTimeout = 10 * time.Second
	// webhookRetries is the small retry budget for a failing webhook POST.
	webhookRetries = 2
)

// debugLogger is a structured JSON logger for sink delivery tracing, matching
// the supervisor package's MINDER_DEBUG/MINDER_LOG convention.
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

// Config is one sink subscription: the event-type patterns it matches and
// exactly one delivery destination.
type Config struct {
	// Events is a set of glob patterns (path.Match semantics: "*" matches any
	// run of characters) matched against the event's Type. A sink fires if
	// any pattern matches.
	Events []string
	// Webhook is a URL to POST the event JSON to. Mutually exclusive with Exec.
	Webhook string
	// Exec is a local command run with the event JSON on stdin. Mutually
	// exclusive with Webhook.
	Exec string
}

// Validate checks that the sink is well-formed: at least one event pattern,
// all patterns syntactically valid, and exactly one of Webhook/Exec set.
func (c Config) Validate() error {
	if len(c.Events) == 0 {
		return errors.New("events is required")
	}
	for _, pattern := range c.Events {
		if pattern == "" {
			return errors.New("event pattern cannot be empty")
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf("invalid event pattern %q: %w", pattern, err)
		}
	}
	hasWebhook := c.Webhook != ""
	hasExec := c.Exec != ""
	if hasWebhook == hasExec {
		return errors.New("exactly one of webhook or exec is required")
	}
	return nil
}

// Matches reports whether eventType satisfies any of the sink's patterns.
func (c Config) Matches(eventType string) bool {
	for _, pattern := range c.Events {
		if ok, err := path.Match(pattern, eventType); err == nil && ok {
			return true
		}
	}
	return false
}

func (c Config) label() string {
	if c.Webhook != "" {
		return "webhook:" + c.Webhook
	}
	return "exec:" + c.Exec
}

// Payload is the redacted, stable JSON shape delivered to sinks. It carries
// only identifiers and the human-readable summary — never the raw Data field
// on supervisor.Envelope, agent logs, prompts, or tokens, so a sink can never
// leak them even if a future emit site starts populating richer payloads.
type Payload struct {
	ID           int64     `json:"id,omitempty"`
	Time         time.Time `json:"time"`
	DeploymentID string    `json:"deployment_id,omitempty"`
	JobID        int64     `json:"job_id,omitempty"`
	RunID        int64     `json:"run_id,omitempty"`
	Type         string    `json:"type"`
	Severity     string    `json:"severity"`
	Summary      string    `json:"summary"`
}

func buildPayload(env supervisor.Envelope) ([]byte, error) {
	return json.Marshal(Payload{
		ID:           env.ID,
		Time:         env.Time,
		DeploymentID: env.DeploymentID,
		JobID:        env.JobID,
		RunID:        env.RunID,
		Type:         string(env.Type),
		Severity:     string(env.Severity),
		Summary:      env.Summary,
	})
}

// Stats is a point-in-time snapshot of one sink's delivery counters.
type Stats struct {
	Label     string
	Delivered int64
	Failed    int64
	Dropped   int64
}

// deliverFunc sends a payload to a sink's destination. ctx bounds the whole
// attempt, including any retries.
type deliverFunc func(ctx context.Context, payload []byte) error

type sink struct {
	cfg     Config
	queue   chan supervisor.Envelope
	deliver deliverFunc

	delivered atomic.Int64
	failed    atomic.Int64
	dropped   atomic.Int64
}

// Options configures a Manager. Zero values use the package defaults.
type Options struct {
	QueueSize  int
	Timeout    time.Duration
	HTTPClient *http.Client
}

func (o Options) withDefaults() Options {
	if o.QueueSize <= 0 {
		o.QueueSize = DefaultQueueSize
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: DefaultTimeout}
	}
	return o
}

// Manager subscribes to a supervisor event bus and fans matching events out
// to configured sinks, each with its own bounded queue and delivery
// goroutine.
type Manager struct {
	bus     *eventbus.Bus[supervisor.Envelope]
	sinks   []*sink
	timeout time.Duration

	sub    *eventbus.Subscription[supervisor.Envelope]
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewManager validates configs and builds a Manager bound to bus. It returns
// an error if any config is invalid. A Manager with no sinks is valid and
// Start is a no-op for it.
func NewManager(bus *eventbus.Bus[supervisor.Envelope], configs []Config, opts Options) (*Manager, error) {
	opts = opts.withDefaults()
	m := &Manager{bus: bus, timeout: opts.Timeout}
	for _, cfg := range configs {
		if err := cfg.Validate(); err != nil {
			return nil, fmt.Errorf("sink %s: %w", cfg.label(), err)
		}
		s := &sink{cfg: cfg, queue: make(chan supervisor.Envelope, opts.QueueSize)}
		switch {
		case cfg.Webhook != "":
			s.deliver = webhookDeliver(opts.HTTPClient, cfg.Webhook)
		case cfg.Exec != "":
			s.deliver = execDeliver(cfg.Exec)
		}
		m.sinks = append(m.sinks, s)
	}
	return m, nil
}

// Start subscribes to the bus and launches the dispatch and per-sink
// delivery goroutines. It does not block. Only events published after Start
// is called are considered — sinks never replay history. Safe to call once;
// a Manager with no sinks does nothing and returns nil.
func (m *Manager) Start(ctx context.Context) error {
	if len(m.sinks) == 0 {
		return nil
	}
	cursor := m.bus.Cursor()
	sub, err := m.bus.Subscribe(cursor)
	if err != nil {
		return fmt.Errorf("subscribe event sinks: %w", err)
	}
	m.sub = sub

	runCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel

	for _, s := range m.sinks {
		m.wg.Add(1)
		go m.runSink(runCtx, s)
	}
	m.wg.Add(1)
	go m.dispatch(runCtx, sub)
	return nil
}

// Stop halts dispatch and delivery and waits for in-flight deliveries to
// observe cancellation. It does not wait for a hung delivery to return —
// each delivery is bounded by the manager's timeout, so Stop returns once
// that timeout (plus a small grace period for goroutine teardown) elapses.
func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	if m.sub != nil {
		m.sub.Close()
	}
	m.wg.Wait()
}

// Stats returns a point-in-time snapshot of every sink's delivery counters.
func (m *Manager) Stats() []Stats {
	stats := make([]Stats, 0, len(m.sinks))
	for _, s := range m.sinks {
		stats = append(stats, Stats{
			Label:     s.cfg.label(),
			Delivered: s.delivered.Load(),
			Failed:    s.failed.Load(),
			Dropped:   s.dropped.Load(),
		})
	}
	return stats
}

func (m *Manager) dispatch(ctx context.Context, sub *eventbus.Subscription[supervisor.Envelope]) {
	defer m.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.Events():
			if !ok {
				return
			}
			for _, s := range m.sinks {
				if !s.cfg.Matches(string(ev.Value.Type)) {
					continue
				}
				select {
				case s.queue <- ev.Value:
				default:
					n := s.dropped.Add(1)
					debugLog("event sink queue full, dropping event",
						"sink", s.cfg.label(), "type", string(ev.Value.Type), "dropped_total", n)
				}
			}
		}
	}
}

func (m *Manager) runSink(ctx context.Context, s *sink) {
	defer m.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case env, ok := <-s.queue:
			if !ok {
				return
			}
			m.deliverOne(ctx, s, env)
		}
	}
}

func (m *Manager) deliverOne(ctx context.Context, s *sink, env supervisor.Envelope) {
	payload, err := buildPayload(env)
	if err != nil {
		s.failed.Add(1)
		debugLog("event sink payload build failed", "sink", s.cfg.label(), "error", err.Error())
		return
	}

	deliverCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	if err := s.deliver(deliverCtx, payload); err != nil {
		s.failed.Add(1)
		debugLog("event sink delivery failed", "sink", s.cfg.label(), "type", string(env.Type), "error", err.Error())
		return
	}
	s.delivered.Add(1)
}

// webhookDeliver POSTs payload as JSON, retrying a small, fixed number of
// times on transport errors or non-2xx responses. All attempts share ctx's
// deadline.
func webhookDeliver(client *http.Client, url string) deliverFunc {
	return func(ctx context.Context, payload []byte) error {
		var lastErr error
		for attempt := 0; attempt <= webhookRetries; attempt++ {
			if attempt > 0 {
				select {
				case <-time.After(time.Duration(attempt) * 200 * time.Millisecond):
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
			if err != nil {
				return fmt.Errorf("build webhook request: %w", err)
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				lastErr = err
				continue
			}
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("webhook %s: unexpected status %d", url, resp.StatusCode)
		}
		return lastErr
	}
}

// execDeliver runs command through the shell with payload on stdin. The
// command's own stdout/stderr are discarded save for error logging; only its
// exit status determines success.
func execDeliver(command string) deliverFunc {
	return func(ctx context.Context, payload []byte) error {
		cmd := exec.CommandContext(ctx, "sh", "-c", command)
		cmd.Stdin = bytes.NewReader(payload)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("exec %q: %w (output: %s)", command, err, bytes.TrimSpace(out))
		}
		return nil
	}
}
