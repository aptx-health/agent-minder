// Package supervisor manages concurrent Claude Code agents working on GitHub issues.
package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aptx-health/agent-minder/internal/db"
	gitpkg "github.com/aptx-health/agent-minder/internal/git"
	ghpkg "github.com/aptx-health/agent-minder/internal/github"
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
	cmd           *exec.Cmd
	cancelFunc    context.CancelFunc
	stoppedByUser bool
	liveStatus    LiveStatus
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
	paused             bool
	budgetPaused       bool
	budgetWarned       bool
	waitingHintEmitted bool
	events             chan Event
	parentCtx          context.Context
	cancel             context.CancelFunc
	done               chan struct{}
	ghClientFactory    func(token string) *ghpkg.Client
}

// New creates a new Supervisor.
func New(store *db.Store, deploy *db.Deployment, repoDir, owner, repo, ghToken string) *Supervisor {
	maxAgents := deploy.MaxAgents
	if maxAgents < 1 {
		maxAgents = 3
	}
	return &Supervisor{
		store:     store,
		deploy:    deploy,
		repoDir:   repoDir,
		owner:     owner,
		repo:      repo,
		ghToken:   ghToken,
		running:   make(map[int64]*runState),
		maxAgents: maxAgents,
		events:    make(chan Event, 64),
	}
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

// Events returns the event channel.
func (s *Supervisor) Events() <-chan Event { return s.events }

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

// RunningJobs returns info about all currently running jobs.
func (s *Supervisor) RunningJobs() []RunInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	var infos []RunInfo
	for _, rs := range s.running {
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
	i := 0
	for _, rs := range s.running {
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
		i++
	}
	// Pad with idle slots up to maxAgents.
	for i < s.maxAgents {
		infos = append(infos, SlotInfo{SlotNum: i, Status: "idle", Paused: s.paused || s.budgetPaused})
		i++
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
	i := 0
	for _, rs := range s.running {
		if i == slotIdx {
			rs.stoppedByUser = true
			if rs.cancelFunc != nil {
				rs.cancelFunc()
			}
			return
		}
		i++
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
		s.fillCapacity(ctx)

		reviewTicker := time.NewTicker(30 * time.Second)
		defer reviewTicker.Stop()

		watchTicker := time.NewTicker(2 * time.Minute)
		defer watchTicker.Stop()
		if s.addWatchTickerToLoop() {
			s.WatchTick(ctx)
		}

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
}

func (s *Supervisor) emitEvent(typ, summary string, taskID int64) {
	evt := Event{Time: time.Now(), Type: typ, Summary: summary, TaskID: taskID}
	select {
	case s.events <- evt:
	default:
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

	// Fetch once before launching.
	if len(s.running) < s.maxAgents {
		if err := gitpkg.Fetch(s.repoDir); err != nil {
			s.emitEvent("warning", fmt.Sprintf("Fetch failed: %v", err), 0)
		}
	}

	for len(s.running) < s.maxAgents {
		jobs, err := s.store.QueuedUnblockedJobs(s.deploy.ID)
		if err != nil || len(jobs) == 0 {
			break
		}
		s.launchJob(ctx, jobs[0])
	}
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
func (s *Supervisor) checkMergedPRs(ctx context.Context) {
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
	target := fmt.Sprintf("github.com/%s/%s/pull/", owner, repo)
	content := string(data)
	idx := strings.LastIndex(content, target)
	if idx < 0 {
		return 0
	}
	rest := content[idx+len(target):]
	end := strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' })
	if end <= 0 {
		return 0
	}
	num, _ := strconv.Atoi(rest[:end])
	return num
}
