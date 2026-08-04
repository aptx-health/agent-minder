// Package supervisor manages concurrent Claude Code agents working on GitHub issues.
package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aptx-health/agent-minder/internal/db"
	"github.com/aptx-health/agent-minder/internal/eventbus"
	gitpkg "github.com/aptx-health/agent-minder/internal/git"
	ghpkg "github.com/aptx-health/agent-minder/internal/github"
	"github.com/aptx-health/agent-minder/internal/reaper"
	runtimepkg "github.com/aptx-health/agent-minder/internal/runtime"
)

// debugLogger is a structured JSON logger for supervisor tracing.
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

// Event is emitted by the supervisor for the TUI/API to consume.
type Event struct {
	Time    time.Time
	Type    string // "info", "started", "completed", "bailed", "stopped", "finished", "error", "warning"
	Summary string
	TaskID  int64 // kept as TaskID for API backward compat
}

// RunInfo describes a currently running job.
type RunInfo struct {
	JobID       int64
	Agent       string
	IssueNumber int
	IssueTitle  string
	Branch      string
	RunningFor  time.Duration
	Status      string // "running"
	IsReview    bool
	CurrentTool string
	ToolInput   string
	StepCount   int
}

// runState tracks a running job manager goroutine.
type runState struct {
	job           *db.Job
	startedAt     time.Time
	cancelFunc    context.CancelFunc
	stoppedByUser bool
	hitUsageLimit bool // set by the runtime's EventSink on usage-limit events
	liveStatus    LiveStatus
	currentRunID  int64 // active agent_runs row for the current stage (0 if none)
	runStepCount  int   // assistant steps observed for the current run (reset per run)
}

// beginAgentRunTracking records the agent_runs row that live-status updates
// should touch and resets the per-run step counter.
func (s *Supervisor) beginAgentRunTracking(jobID, runID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rs, ok := s.running[jobID]; ok {
		rs.currentRunID = runID
		rs.runStepCount = 0
	}
}

// endAgentRunTracking clears the active run row and returns the final per-run
// step count.
func (s *Supervisor) endAgentRunTracking(jobID int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rs, ok := s.running[jobID]; ok {
		steps := rs.runStepCount
		rs.currentRunID = 0
		return steps
	}
	return 0
}

// Supervisor manages concurrent agent jobs.
type Supervisor struct {
	store   *db.Store
	deploy  *db.Deployment
	repoDir string
	owner   string
	repo    string
	ghToken string

	mu                 sync.Mutex
	gitSetupMu         sync.Mutex
	running            map[int64]*runState // keyed by job ID
	maxAgents          int
	active             bool
	daemonMode         bool
	watchMode          bool
	triggerRoutes      []TriggerRoute // label→agent routing from jobs.yaml
	paused             bool
	budgetPaused       bool
	budgetWarned       bool
	waitingHintEmitted bool
	lastGHError        time.Time // throttle GitHub API error reporting
	ghPollSucceeded    bool      // true after first successful poll
	offline            bool
	offlineSince       time.Time
	offlineBackoffIdx  int
	nextProbeAt        time.Time
	eventBusOnce       sync.Once
	events             *eventbus.Bus[Event]
	eventDrainMu       sync.Mutex
	eventDrainCursor   uint64
	parentCtx          context.Context
	cancel             context.CancelFunc
	done               chan struct{}
	ghClientFactory    func(token string) *ghpkg.Client
	fetchFn            func() error        // overridable for tests
	sparedLogAt        map[int64]time.Time // throttle "spared by reaper" events per job
	runtime            runtimepkg.AgentRuntime
}

// SetRuntime selects the AgentRuntime that DefaultJobManager uses to execute
// stages. Passing nil reverts to the legacy Claude Code in-process exec path,
// which is what tests rely on. Production callers (cmd/deploy) inject a
// concrete runtime (typically claudecode.New()).
func (s *Supervisor) SetRuntime(rt runtimepkg.AgentRuntime) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtime = rt
}

// Runtime returns the configured AgentRuntime, or nil if none was set.
func (s *Supervisor) Runtime() runtimepkg.AgentRuntime {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runtime
}

// RuntimeForJob returns the job override runtime when present, otherwise the
// deployment runtime configured on the supervisor.
func (s *Supervisor) RuntimeForJob(job *db.Job) (runtimepkg.AgentRuntime, error) {
	if job != nil && job.Runtime.Valid && job.Runtime.String != "" {
		return runtimepkg.Lookup(job.Runtime.String)
	}
	if rt := s.Runtime(); rt != nil {
		return rt, nil
	}
	if s.deploy != nil && s.deploy.Runtime != "" {
		return runtimepkg.Lookup(s.deploy.Runtime)
	}
	return nil, nil
}

// New creates a new Supervisor.
func New(store *db.Store, deploy *db.Deployment, repoDir, owner, repo, ghToken string) *Supervisor {
	maxAgents := deploy.MaxAgents
	if maxAgents < 1 {
		maxAgents = 3
	}
	s := &Supervisor{
		store:       store,
		deploy:      deploy,
		repoDir:     repoDir,
		owner:       owner,
		repo:        repo,
		ghToken:     ghToken,
		running:     make(map[int64]*runState),
		maxAgents:   maxAgents,
		events:      eventbus.New[Event](eventbus.DefaultSubscriberBuffer),
		sparedLogAt: make(map[int64]time.Time),
	}
	s.fetchFn = func() error { return gitpkg.Fetch(s.repoDir) }
	return s
}

func (s *Supervisor) newGHClient() *ghpkg.Client {
	if s.ghClientFactory != nil {
		return s.ghClientFactory(s.ghToken)
	}
	return ghpkg.NewClient(s.ghToken)
}

// SetDaemonMode keeps the supervisor alive when all work completes.
func (s *Supervisor) SetDaemonMode(daemon bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.daemonMode = daemon
}

// Subscribe returns supervisor events strictly after afterCursor, followed by
// live events. Each caller receives an independent subscription.
func (s *Supervisor) Subscribe(afterCursor uint64) (*eventbus.Subscription[Event], error) {
	return s.eventBus().Subscribe(afterCursor)
}

func (s *Supervisor) eventBus() *eventbus.Bus[Event] {
	s.eventBusOnce.Do(func() {
		if s.events == nil {
			s.events = eventbus.New[Event](eventbus.DefaultSubscriberBuffer)
		}
	})
	return s.events
}

// Done returns a channel that closes when the supervisor exits.
func (s *Supervisor) Done() <-chan struct{} { return s.done }

// IsActive returns whether the supervisor is running.
func (s *Supervisor) IsActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// IsBudgetPaused returns true if paused due to budget ceiling.
func (s *Supervisor) IsBudgetPaused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.budgetPaused
}

// ResumeBudget clears the budget-paused state.
func (s *Supervisor) ResumeBudget() {
	s.mu.Lock()
	s.budgetPaused = false
	s.budgetWarned = false
	s.mu.Unlock()
}

// Pause prevents new agents from launching.
func (s *Supervisor) Pause() {
	s.mu.Lock()
	s.paused = true
	s.mu.Unlock()
}

// Resume allows new agents to launch.
func (s *Supervisor) Resume() {
	s.mu.Lock()
	s.paused = false
	s.mu.Unlock()
}

// sortedRunningKeys returns the keys of s.running sorted by job ID.
// Must be called with s.mu held.
func (s *Supervisor) sortedRunningKeys() []int64 {
	keys := make([]int64, 0, len(s.running))
	for id := range s.running {
		keys = append(keys, id)
	}
	slices.Sort(keys)
	return keys
}

// RunningJobs returns info about all currently running jobs.
func (s *Supervisor) RunningJobs() []RunInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	var infos []RunInfo
	for _, id := range s.sortedRunningKeys() {
		rs := s.running[id]
		infos = append(infos, RunInfo{
			JobID:       rs.job.ID,
			Agent:       rs.job.Agent,
			IssueNumber: rs.job.IssueNumber,
			IssueTitle:  rs.job.IssueTitle.String,
			Branch:      rs.job.Branch.String,
			RunningFor:  time.Since(rs.startedAt),
			Status:      "running",
			IsReview:    rs.job.Status == db.StatusReviewing,
			CurrentTool: rs.liveStatus.CurrentTool,
			ToolInput:   rs.liveStatus.ToolInput,
			StepCount:   rs.liveStatus.StepCount,
		})
	}
	return infos
}

// SlotStatus returns backward-compatible slot info (for API/xbar).
func (s *Supervisor) SlotStatus() []SlotInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	var infos []SlotInfo
	keys := s.sortedRunningKeys()
	for i, id := range keys {
		rs := s.running[id]
		infos = append(infos, SlotInfo{
			SlotNum:     i,
			IssueNumber: rs.job.IssueNumber,
			IssueTitle:  rs.job.IssueTitle.String,
			Branch:      rs.job.Branch.String,
			RunningFor:  time.Since(rs.startedAt),
			Status:      "running",
			IsReview:    rs.job.Status == db.StatusReviewing,
			CurrentTool: rs.liveStatus.CurrentTool,
			ToolInput:   rs.liveStatus.ToolInput,
			StepCount:   rs.liveStatus.StepCount,
		})
	}
	// Pad with idle slots up to maxAgents.
	for i := len(keys); i < s.maxAgents; i++ {
		infos = append(infos, SlotInfo{SlotNum: i, Status: "idle", Paused: s.paused || s.budgetPaused})
	}
	return infos
}

// SlotInfo describes the current state of an agent slot (backward compat).
type SlotInfo struct {
	SlotNum     int
	IssueNumber int
	IssueTitle  string
	Branch      string
	RunningFor  time.Duration
	Status      string // "running" or "idle"
	Paused      bool
	IsReview    bool
	CurrentTool string
	ToolInput   string
	StepCount   int
}

// StopJob stops a running job by its ID.
func (s *Supervisor) StopJob(jobID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.running[jobID]
	if !ok {
		return
	}
	rs.stoppedByUser = true
	if rs.cancelFunc != nil {
		rs.cancelFunc()
	}
}

// StopAgent stops a specific agent by slot index (backward compat).
func (s *Supervisor) StopAgent(slotIdx int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := s.sortedRunningKeys()
	if slotIdx < 0 || slotIdx >= len(keys) {
		return
	}
	rs := s.running[keys[slotIdx]]
	rs.stoppedByUser = true
	if rs.cancelFunc != nil {
		rs.cancelFunc()
	}
}

// Launch starts the main supervisor loop.
func (s *Supervisor) Launch(ctx context.Context) {
	s.mu.Lock()
	if s.active {
		s.mu.Unlock()
		return
	}
	ctx, s.cancel = context.WithCancel(ctx)
	s.parentCtx = ctx
	s.active = true
	s.done = make(chan struct{})
	s.mu.Unlock()

	go func() {
		defer close(s.done)

		// Startup sweep: reap anything left from prior daemon runs.
		s.sweepAllInactiveWorktrees()

		s.fillCapacity(ctx)

		reviewTicker := time.NewTicker(30 * time.Second)
		defer reviewTicker.Stop()

		watchTicker := time.NewTicker(2 * time.Minute)
		defer watchTicker.Stop()
		if s.addWatchTickerToLoop() {
			s.WatchTick(ctx)
		}

		reaperTicker := time.NewTicker(5 * time.Minute)
		defer reaperTicker.Stop()

		for {
			select {
			case <-ctx.Done():
				s.mu.Lock()
				s.active = false
				s.mu.Unlock()
				s.emitEvent("finished", "Supervisor cancelled", 0)
				return

			case <-watchTicker.C:
				if s.addWatchTickerToLoop() {
					s.WatchTick(ctx)
				}

			case <-reviewTicker.C:
				s.checkMergedPRs(ctx)
				if s.hasCapacity() {
					s.fillCapacity(ctx)
				}

			case <-reaperTicker.C:
				s.sweepAllInactiveWorktrees()

			default:
				if s.hasCapacity() {
					s.fillCapacity(ctx)
				}
			}

			if !s.hasWork() {
				s.mu.Lock()
				isDaemon := s.daemonMode
				s.mu.Unlock()
				if !isDaemon {
					break
				}
			}

			time.Sleep(2 * time.Second)
		}

		s.mu.Lock()
		s.active = false
		s.mu.Unlock()
		s.emitEvent("finished", "All agents finished", 0)
	}()
}

// Stop cancels all agents and waits for completion.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		return
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()

	if s.done != nil {
		<-s.done
	}

	// In-flight work has drained; reap any process-level runtime resources
	// (e.g. opencode's shared `opencode serve`). Decoupled via the registry so
	// the supervisor never imports a concrete runtime package; stateless
	// runtimes register no hook and this no-ops.
	runtimepkg.ShutdownAll()
}

func (s *Supervisor) emitEvent(typ, summary string, taskID int64) {
	evt := Event{Time: time.Now(), Type: typ, Summary: summary, TaskID: taskID}
	if _, err := s.eventBus().Publish(evt); err != nil {
		debugLog("publish supervisor event", "type", typ, "task_id", taskID, "error", err)
	}
}

func (s *Supervisor) hasCapacity() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.running) < s.maxAgents
}

func (s *Supervisor) hasWork() bool {
	s.mu.Lock()
	anyRunning := len(s.running) > 0
	isPaused := s.paused
	s.mu.Unlock()

	if anyRunning {
		return true
	}

	jobs, _ := s.store.GetJobs(s.deploy.ID)
	waitingOnMerge := false
	for _, j := range jobs {
		switch j.Status {
		case db.StatusQueued, db.StatusBlocked, db.StatusReviewing, db.StatusManual:
			return true
		case db.StatusReview, db.StatusReviewed:
			waitingOnMerge = true
		}
	}
	if waitingOnMerge {
		s.emitWaitingForMerge()
		return true
	}
	return isPaused
}

func (s *Supervisor) emitWaitingForMerge() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.waitingHintEmitted {
		return
	}
	s.waitingHintEmitted = true
	go s.emitEvent("info", "Waiting for PR(s) to be merged (checking every 30s, ctrl+c to exit)", 0)
}

func (s *Supervisor) fillCapacity(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.paused || s.budgetPaused || ctx.Err() != nil {
		return
	}

	if s.deploy.TotalBudgetUSD > 0 {
		s.mu.Unlock()
		exceeded := s.checkBudgetCeiling()
		s.mu.Lock()
		if exceeded || s.paused || s.budgetPaused || ctx.Err() != nil {
			return
		}
	}

	// Fetch once before launching, with offline-aware backoff.
	if len(s.running) < s.maxAgents {
		s.mu.Unlock()
		s.tryFetch()
		s.mu.Lock()
	}

	for len(s.running) < s.maxAgents {
		jobs, err := s.store.QueuedUnblockedJobs(s.deploy.ID)
		if err != nil || len(jobs) == 0 {
			break
		}
		s.launchJob(ctx, jobs[0])
	}
}

// offlineBackoffSchedule is the delay-until-next-probe sequence while offline.
// Index advances on each consecutive failure and caps at the last entry.
var offlineBackoffSchedule = []time.Duration{
	30 * time.Second,
	1 * time.Minute,
	2 * time.Minute,
	5 * time.Minute,
	10 * time.Minute,
	15 * time.Minute,
}

// tryFetch attempts a git fetch with offline-aware backoff.
// While offline, it skips entirely until nextProbeAt; on the probe attempt
// it does a single fetch. Logs only on offline↔online transitions or on
// non-network errors.
func (s *Supervisor) tryFetch() {
	s.mu.Lock()
	if s.offline && time.Now().Before(s.nextProbeAt) {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	err := s.fetchFn()

	s.mu.Lock()
	defer s.mu.Unlock()

	if err == nil {
		if s.offline {
			downFor := time.Since(s.offlineSince).Round(time.Second)
			s.offline = false
			s.offlineBackoffIdx = 0
			s.nextProbeAt = time.Time{}
			s.emitEvent("info", fmt.Sprintf("Network online (offline for %s) — resuming", downFor), 0)
		}
		return
	}

	if !gitpkg.IsNetworkError(err) {
		// Non-network error: surface immediately, don't enter offline state.
		s.emitEvent("warning", fmt.Sprintf("Git fetch failed: %v", err), 0)
		return
	}

	if !s.offline {
		s.offline = true
		s.offlineSince = time.Now()
		s.offlineBackoffIdx = 0
		s.emitEvent("warning", "Network offline (git fetch failed) — backing off, will retry quietly", 0)
	} else if s.offlineBackoffIdx < len(offlineBackoffSchedule)-1 {
		s.offlineBackoffIdx++
	}
	s.nextProbeAt = time.Now().Add(offlineBackoffSchedule[s.offlineBackoffIdx])
}

// isOnline reports whether the supervisor considers the network up.
func (s *Supervisor) isOnline() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.offline
}

func (s *Supervisor) checkBudgetCeiling() bool {
	spent, err := s.store.TotalSpend(s.deploy.ID)
	if err != nil {
		return false
	}
	ceiling := s.deploy.TotalBudgetUSD
	if ceiling <= 0 {
		return false
	}

	if spent >= ceiling {
		s.mu.Lock()
		s.budgetPaused = true
		s.mu.Unlock()
		s.emitEvent("warning", fmt.Sprintf("Budget ceiling reached: $%.2f / $%.2f", spent, ceiling), 0)
		return true
	}

	if !s.budgetWarned && spent >= ceiling*0.8 {
		s.mu.Lock()
		s.budgetWarned = true
		s.mu.Unlock()
		s.emitEvent("warning", fmt.Sprintf("Budget at 80%%: $%.2f / $%.2f", spent, ceiling), 0)
	}
	return false
}

// launchJob creates a SlotContext + JobManager and spawns a goroutine.
// Must be called with s.mu held.
func (s *Supervisor) launchJob(ctx context.Context, job *db.Job) {
	_ = s.store.UpdateJobRunning(job.ID)
	job.Status = db.StatusRunning

	jobCtx, jobCancel := context.WithCancel(ctx)
	rs := &runState{
		job:        job,
		startedAt:  time.Now(),
		cancelFunc: jobCancel,
	}
	s.running[job.ID] = rs

	go s.runJobManager(jobCtx, job)
}

// runJobManager runs a JobManager for the given job, then cleans up.
func (s *Supervisor) runJobManager(ctx context.Context, job *db.Job) {
	defer func() {
		s.mu.Lock()
		delete(s.running, job.ID)
		s.mu.Unlock()
		s.sweepJobWorktree(job)
		s.fillCapacity(s.parentCtx)
	}()

	// Create SlotContext.
	sc := s.newSlotContext(job.ID, job)

	// Resolve contract and check dedup.
	contract, err := ResolveContract(s.repoDir, job.Agent)
	if err != nil {
		contract = DefaultContract(job.Agent)
	}

	// Check dedup strategies before running.
	if len(contract.Dedup) > 0 {
		result := EvaluateDedup(ctx, sc, contract.Dedup)
		if result.Skip {
			sc.EmitEvent("info", fmt.Sprintf("Skipped %s: %s", sc.JobLabel(), result.Reason))
			_ = s.store.UpdateJobStatus(job.ID, "skipped")
			return
		}
	}

	// Create and run JobManager.
	mgr := NewDefaultJobManager(sc, contract)
	if err := mgr.Run(ctx); err != nil {
		debugLog("job manager error", "job", job.ID, "issue", job.IssueNumber, "error", err.Error())
	}
}

// checkMergedPRs checks if any review/reviewed jobs had their PRs merged.
// Skipped while offline to avoid hammering the GitHub API during outages.
func (s *Supervisor) checkMergedPRs(ctx context.Context) {
	if !s.isOnline() {
		return
	}
	jobs, err := s.store.GetJobs(s.deploy.ID)
	if err != nil {
		return
	}

	ghClient := s.newGHClient()

	for _, j := range jobs {
		if j.Status != db.StatusReview && j.Status != db.StatusReviewed {
			continue
		}
		if !j.PRNumber.Valid {
			continue
		}

		merged, err := ghClient.IsPRMerged(ctx, s.owner, s.repo, int(j.PRNumber.Int64))
		if err == nil && merged {
			_ = s.store.CompleteJob(j.ID, db.StatusDone)
			ghClient.RemoveLabel(ctx, s.owner, s.repo, j.IssueNumber, "needs-review")
			s.emitEvent("completed", fmt.Sprintf("PR #%d merged — #%d done", j.PRNumber.Int64, j.IssueNumber), j.ID)
		}
	}
}

// UpdateLiveStatus is called by the scanner goroutine to update job live status.
func (s *Supervisor) UpdateLiveStatus(jobID int64, status LiveStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rs, ok := s.running[jobID]; ok {
		rs.liveStatus = status
	}
}

// --- Utility functions used by SlotContext and JobManager ---

// parseCostFromLog extracts cost from the agent log (stream-json format).
func parseCostFromLog(logPath string) float64 {
	data, err := os.ReadFile(logPath)
	if err != nil {
		return 0
	}
	lines := strings.Split(string(data), "\n")
	var cost float64
	for _, line := range lines {
		if idx := strings.Index(line, `"total_cost_usd"`); idx >= 0 {
			rest := line[idx+len(`"total_cost_usd"`):]
			rest = strings.TrimLeft(rest, `: `)
			end := strings.IndexAny(rest, ",}")
			if end > 0 {
				if v, err := strconv.ParseFloat(strings.TrimSpace(rest[:end]), 64); err == nil && v > cost {
					cost = v
				}
			}
		}
	}
	return cost
}

// detectPRFromLog scans the log for PR creation output.
func detectPRFromLog(logPath, owner, repo string) int {
	data, err := os.ReadFile(logPath)
	if err != nil {
		return 0
	}
	content := string(data)

	// Try exact owner/repo match first.
	target := fmt.Sprintf("github.com/%s/%s/pull/", owner, repo)
	if num := lastPRNumberAfter(content, target); num > 0 {
		return num
	}

	// Fall back to any "/pull/<number>" in the log. This handles renamed repos
	// where GitHub redirects the URL (e.g., eternal-fitness → ripit-fitness)
	// but the deployment record still has the old name.
	if num := lastPRNumberAfter(content, "/pull/"); num > 0 {
		return num
	}

	return 0
}

func lastPRNumberAfter(content, marker string) int {
	idx := strings.LastIndex(content, marker)
	if idx < 0 {
		return 0
	}
	rest := content[idx+len(marker):]
	end := strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' })
	if end < 0 {
		end = len(rest) // number runs to end of string
	}
	if end == 0 {
		return 0
	}
	num, _ := strconv.Atoi(rest[:end])
	return num
}

// sweepJobWorktree reaps any orphaned processes left in a single job's
// worktree. Safe to call after the job's agent process has exited.
func (s *Supervisor) sweepJobWorktree(job *db.Job) {
	if job == nil || !job.WorktreePath.Valid || job.WorktreePath.String == "" {
		return
	}
	reaped, err := reaper.Sweep(job.WorktreePath.String)
	if err != nil {
		debugLog("reaper error", "job", job.ID, "worktree", job.WorktreePath.String, "error", err.Error())
		return
	}
	s.reportReaped(reaped, job.ID, job.IssueNumber)
}

// sweepAllInactiveWorktrees finds every worktree whose job is NOT currently
// running and reaps stragglers. Used at startup and on a periodic ticker.
func (s *Supervisor) sweepAllInactiveWorktrees() {
	jobs, err := s.store.GetJobs(s.deploy.ID)
	if err != nil {
		return
	}

	s.mu.Lock()
	running := make(map[int64]bool, len(s.running))
	for id := range s.running {
		running[id] = true
	}
	s.mu.Unlock()

	const completionGrace = time.Hour
	for _, j := range jobs {
		if running[j.ID] {
			continue // active agent; do not touch
		}
		if j.Status == db.StatusRunning {
			continue // crash-recovery / stale row; skip to be safe
		}
		if !j.WorktreePath.Valid || j.WorktreePath.String == "" {
			continue
		}
		// Spare recently-completed jobs so manual testing in the worktree
		// (dev servers etc.) isn't nuked. Held worktrees are spared in Sweep itself.
		var sparedReason string
		switch {
		case reaper.IsHeld(j.WorktreePath.String):
			sparedReason = "hold-file"
		case j.CompletedAt.Valid && time.Since(j.CompletedAt.Time) < completionGrace:
			sparedReason = "grace"
		}
		if sparedReason != "" {
			s.reportSpared(j, sparedReason)
			continue
		}
		reaped, err := reaper.Sweep(j.WorktreePath.String)
		if err != nil {
			continue
		}
		s.reportReaped(reaped, j.ID, j.IssueNumber)
	}
}

// reportSpared emits one event per ticker pass (throttled to once an hour
// per job) when the reaper skips a worktree so users can see accumulating
// held/recently-completed worktrees in the event stream.
func (s *Supervisor) reportSpared(j *db.Job, reason string) {
	s.mu.Lock()
	last := s.sparedLogAt[j.ID]
	if time.Since(last) < time.Hour {
		s.mu.Unlock()
		return
	}
	s.sparedLogAt[j.ID] = time.Now()
	s.mu.Unlock()

	age := ""
	if j.CompletedAt.Valid {
		age = fmt.Sprintf(" age=%s", time.Since(j.CompletedAt.Time).Truncate(time.Minute))
	}
	msg := fmt.Sprintf("[reaper] spared job#%d issue-%d reason=%s%s worktree=%s",
		j.ID, j.IssueNumber, reason, age, j.WorktreePath.String)
	s.emitEvent("spared", msg, j.ID)
	debugLog("reaper spared", "job", j.ID, "issue", j.IssueNumber,
		"reason", reason, "worktree", j.WorktreePath.String)
}

// reportReaped emits one event per killed process so the foreground CLI and
// TUI both surface what the reaper did.
func (s *Supervisor) reportReaped(reaped []reaper.Reaped, jobID int64, issueNum int) {
	for _, r := range reaped {
		msg := fmt.Sprintf("[reaper] job#%d issue-%d: killed %s", jobID, issueNum, r)
		s.emitEvent("reaped", msg, jobID)
		debugLog("reaper killed", "job", jobID, "issue", issueNum,
			"pid", r.PID, "ppid", r.PPID, "command", r.Command,
			"age_seconds", r.Age.Seconds(), "cpu_percent", r.CPU, "signal", r.Signal)
	}
}
