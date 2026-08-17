package supervisor

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/aptx-health/agent-minder/internal/db"
	ghpkg "github.com/aptx-health/agent-minder/internal/github"
	"github.com/jmoiron/sqlx"
)

// WatchFilter represents a parsed watch filter.
type WatchFilter struct {
	Type   string   // "label" or "milestone"
	Values []string // label names (AND logic) or single milestone name
}

// TriggerRoute maps one or more labels to an agent type.
// When an issue has ALL matching labels (AND logic), it's routed to the
// specified agent instead of the default autopilot.
type TriggerRoute struct {
	Name      string   // automation name (jobs.yaml key), recorded as job provenance
	Labels    []string // GitHub labels to match (AND logic)
	Milestone string   // GitHub milestone to match (mutually exclusive with Labels)
	Kind      string   // "agent" or "script"
	Agent     string   // agent type to use
	Runtime   string   // optional job-level runtime override
	Model     string   // optional job-level model override
	Budget    float64  // optional job-level max budget (USD) override
	MaxTurns  int      // optional job-level max turns override
	Command   string   // shell command for kind=script
	Timeout   string   // optional script timeout
	EnvJSON   string   // optional script env JSON
	WorkDir   string   // optional script working directory
}

// FilterString returns the watch-filter form of this route ("milestone:<name>"
// or "label:<a>,<b>"), matching the syntax pollFilter and ParseWatchFilter
// understand. Milestone routes take precedence when both fields are set.
func (r TriggerRoute) FilterString() string {
	if r.Milestone != "" {
		return "milestone:" + r.Milestone
	}
	return "label:" + strings.Join(r.Labels, ",")
}

// SetTriggerRoutes configures label→agent routing from jobs.yaml triggers.
// Routes with more labels (more specific) are sorted first so they match
// before less specific routes.
func (s *Supervisor) SetTriggerRoutes(routes []TriggerRoute) {
	sorted := make([]TriggerRoute, len(routes))
	copy(sorted, routes)
	sort.SliceStable(sorted, func(i, j int) bool {
		return len(sorted[i].Labels) > len(sorted[j].Labels)
	})
	s.mu.Lock()
	s.triggerRoutes = sorted
	s.mu.Unlock()
}

// ParseWatchFilter parses a filter string like "label:ready", "label:agent-ready,ux",
// or "milestone:v2.0". Multiple comma-separated labels use AND logic.
func ParseWatchFilter(filter string) (*WatchFilter, error) {
	parts := strings.SplitN(filter, ":", 2)
	if len(parts) != 2 || parts[1] == "" {
		return nil, fmt.Errorf("invalid watch filter %q (expected label:<name> or milestone:<name>)", filter)
	}
	typ := strings.ToLower(parts[0])
	if typ != "label" && typ != "milestone" {
		return nil, fmt.Errorf("unsupported filter type %q (expected label or milestone)", typ)
	}

	values := splitFilterValues(typ, parts[1])
	for _, v := range values {
		if v == "" {
			return nil, fmt.Errorf("invalid watch filter %q: empty label in comma-separated list", filter)
		}
		if !isValidFilterValue(v) {
			return nil, fmt.Errorf("invalid watch filter value %q (must contain only alphanumeric, hyphen, underscore, dot, or space characters)", v)
		}
	}
	return &WatchFilter{Type: typ, Values: values}, nil
}

// splitFilterValues splits on comma for labels (AND logic) but keeps
// milestone values intact (commas not meaningful for milestones).
func splitFilterValues(typ, raw string) []string {
	if typ == "label" && strings.Contains(raw, ",") {
		return strings.Split(raw, ",")
	}
	return []string{raw}
}

// watchPoll queries GitHub for issues matching the watch filter and trigger routes,
// and creates jobs for new ones. Returns the number of newly discovered issues.
func (s *Supervisor) watchPoll(ctx context.Context) int {
	ghClient := s.newGHClient()

	// Get existing jobs to find new issues.
	// Key by (issue number, agent) so the same issue can be worked by different
	// agents (e.g., spike researches, then autopilot implements).
	existing, _ := s.store.GetJobs(s.deploy.ID)
	type issueAgent struct {
		issue int
		agent string
	}
	knownJobs := make(map[issueAgent]bool)
	for _, j := range existing {
		knownJobs[issueAgent{j.IssueNumber, j.Agent}] = true
	}

	skipLabel := s.deploy.SkipLabel
	discovered := 0
	totalPolled := 0

	// 1. Poll the --watch filter (routes to default agent).
	if s.deploy.WatchFilter.Valid && s.deploy.WatchFilter.String != "" {
		issues := s.pollFilter(ctx, ghClient, s.deploy.WatchFilter.String)
		totalPolled += len(issues)
		for _, issue := range issues {
			route := s.resolveRouteForIssue(issue.Labels)
			if knownJobs[issueAgent{issue.Number, route.dedupeAgent()}] || issue.State != "open" || hasLabel(issue.Labels, skipLabel) {
				continue
			}
			if n := s.createJobForIssue(ctx, ghClient, issue, route, "watch"); n > 0 {
				knownJobs[issueAgent{issue.Number, route.dedupeAgent()}] = true
				discovered += n
			}
		}
	}

	// 2. Poll trigger routes (label→agent mapping from jobs.yaml).
	s.mu.Lock()
	routes := s.triggerRoutes
	s.mu.Unlock()

	for _, route := range routes {
		issues := s.pollFilter(ctx, ghClient, route.FilterString())
		totalPolled += len(issues)
		for _, issue := range issues {
			// Skip if a more-specific label route should handle this issue.
			// Milestone routes match on the milestone directly, so label-based
			// specificity resolution does not apply to them.
			if len(route.Labels) > 0 && s.resolveRouteForIssue(issue.Labels).dedupeAgent() != route.dedupeAgent() {
				continue
			}
			if knownJobs[issueAgent{issue.Number, route.dedupeAgent()}] || issue.State != "open" || hasLabel(issue.Labels, skipLabel) {
				continue
			}
			if n := s.createJobForIssue(ctx, ghClient, issue, route, "trigger"); n > 0 {
				knownJobs[issueAgent{issue.Number, route.dedupeAgent()}] = true
				discovered += n
			}
		}
	}

	// Report first successful poll with 0 results — helps diagnose API issues
	// like GitHub outages that return 200 with empty arrays.
	s.mu.Lock()
	firstPoll := !s.ghPollSucceeded
	s.ghPollSucceeded = true
	s.mu.Unlock()
	if firstPoll && totalPolled == 0 && len(routes) > 0 {
		s.emitEvent(EventInfo, fmt.Sprintf("Watch poll: 0 issues found across %d trigger routes — if unexpected, check GitHub status", len(routes)), 0)
	}

	return discovered
}

// pollFilter queries GitHub for issues matching a filter string.
func (s *Supervisor) pollFilter(ctx context.Context, ghClient *ghpkg.Client, filterStr string) []ghpkg.ItemStatus {
	filter, err := ParseWatchFilter(filterStr)
	if err != nil {
		return nil
	}

	var searchResult *ghpkg.SearchResult
	switch filter.Type {
	case "label":
		searchResult, err = ghClient.ListIssuesByLabels(ctx, s.owner, s.repo, filter.Values)
	case "milestone":
		msNum, msErr := ghClient.FindMilestoneNumber(ctx, s.owner, s.repo, filter.Values[0])
		if msErr != nil {
			s.reportGHError(fmt.Sprintf("GitHub API error (milestone %q): %v", filter.Values[0], msErr))
			return nil
		}
		searchResult, err = ghClient.ListIssuesByMilestone(ctx, s.owner, s.repo, msNum)
	}
	if err != nil {
		s.reportGHError(fmt.Sprintf("GitHub API error (filter %s): %v", filterStr, err))
		return nil
	}
	if searchResult == nil {
		return nil
	}
	return searchResult.Items
}

// reportGHError emits a GitHub API error event, throttled to avoid spamming
// during sustained outages (at most once per 10 minutes per unique message prefix).
func (s *Supervisor) reportGHError(msg string) {
	s.mu.Lock()
	if s.lastGHError.IsZero() || time.Since(s.lastGHError) > 10*time.Minute {
		s.lastGHError = time.Now()
		s.mu.Unlock()
		s.emitEvent(EventError, msg, 0)
	} else {
		s.mu.Unlock()
	}
}

// resolveAgentForIssue checks the issue's labels against trigger routes.
// Returns the matching agent, or "autopilot" as default.
// Routes with more labels are checked first so specific routes take priority.
func (s *Supervisor) resolveAgentForIssue(labels []string) string {
	return s.resolveRouteForIssue(labels).Agent
}

func (s *Supervisor) resolveRouteForIssue(labels []string) TriggerRoute {
	s.mu.Lock()
	routes := s.triggerRoutes
	s.mu.Unlock()

	for _, route := range routes {
		if hasAllLabels(labels, route.Labels) {
			return route
		}
	}
	return TriggerRoute{Agent: "autopilot"}
}

// hasAllLabels returns true if issueLabels contains every label in required (case-insensitive).
func hasAllLabels(issueLabels []string, required []string) bool {
	for _, req := range required {
		if !hasLabel(issueLabels, req) {
			return false
		}
	}
	return len(required) > 0
}

// createJobForIssue fetches issue content and inserts a job row. sourceType
// records how the issue was discovered: "trigger" for a jobs.yaml label→agent
// route match, "watch" for a --watch filter discovery.
func (s *Supervisor) createJobForIssue(ctx context.Context, ghClient *ghpkg.Client, issue ghpkg.ItemStatus, route TriggerRoute, sourceType string) int {
	content, err := ghClient.FetchItemContent(ctx, s.owner, s.repo, issue.Number, "issue")
	if err != nil {
		// Don't create a job with an empty body on a transient fetch error;
		// knownJobs only records created jobs, so the next watch poll retries.
		s.emitEvent(EventWarning, fmt.Sprintf("Skipping #%d: fetch issue content failed: %v", issue.Number, err), 0)
		return 0
	}
	body := ""
	if content != nil {
		body = content.Body
	}
	agent := route.dedupeAgent()
	if agent == "" {
		agent = "autopilot"
	}

	// A --watch discovery may still resolve to a named trigger route (a more
	// specific label match); keep the source_name/source_ref pointing at that
	// route in that case, otherwise fall back to the watch filter itself.
	sourceName := route.Name
	sourceRef := route.FilterString()
	if sourceType == "watch" && route.Name == "" {
		sourceRef = s.deploy.WatchFilter.String
	}

	j := &db.Job{
		DeploymentID:  s.deploy.ID,
		Kind:          route.kind(),
		Agent:         agent,
		Name:          fmt.Sprintf("%s-issue-%d", agent, issue.Number),
		Runtime:       sql.NullString{String: route.Runtime, Valid: route.Runtime != ""},
		Model:         sql.NullString{String: route.Model, Valid: route.Model != ""},
		ScriptCommand: sql.NullString{String: route.Command, Valid: route.Command != ""},
		ScriptTimeout: sql.NullString{String: route.Timeout, Valid: route.Timeout != ""},
		ScriptEnv:     sql.NullString{String: route.EnvJSON, Valid: route.EnvJSON != ""},
		ScriptWorkDir: sql.NullString{String: route.WorkDir, Valid: route.WorkDir != ""},
		MaxTurns:      sql.NullInt64{Int64: int64(route.MaxTurns), Valid: route.MaxTurns > 0},
		MaxBudgetOv:   sql.NullFloat64{Float64: route.Budget, Valid: route.Budget > 0},
		IssueNumber:   issue.Number,
		IssueTitle:    sql.NullString{String: issue.Title, Valid: true},
		IssueBody:     sql.NullString{String: body, Valid: body != ""},
		Owner:         s.owner,
		Repo:          s.repo,
		Status:        db.StatusQueued,
		SourceType:    sql.NullString{String: sourceType, Valid: sourceType != ""},
		SourceName:    sql.NullString{String: sourceName, Valid: sourceName != ""},
		SourceRef:     sql.NullString{String: sourceRef, Valid: sourceRef != ""},
	}

	// Discovery of new work is durable (Expedition IV §5): the job row and its
	// discovery event commit in one transaction, published only after commit.
	// The event's job attribution is only known after the insert assigns j.ID,
	// so this site composes the store-first pairing directly.
	record := &db.Event{DeploymentID: s.deploy.ID, Type: string(EventInfo), Severity: string(SeverityInfo),
		Summary: fmt.Sprintf("Discovered #%d: %s (agent: %s)", issue.Number, issue.Title, agent)}
	err = s.store.WithTx(func(tx *sqlx.Tx) error {
		if err := db.CreateJobTx(tx, j); err != nil {
			return err
		}
		record.JobID = j.ID
		return db.AppendEventTx(tx, record)
	})
	if err != nil {
		return 0
	}

	envelope := s.buildEnvelope(EventInfo, record.Summary, j.ID, 0)
	envelope.ID = record.ID
	envelope.Time = record.Time
	s.publishEnvelope(envelope)
	return 1
}

func (r TriggerRoute) kind() string {
	if r.Kind != "" {
		return r.Kind
	}
	return db.JobKindAgent
}

func (r TriggerRoute) dedupeAgent() string {
	if r.kind() == db.JobKindScript {
		return db.JobKindScript
	}
	return r.Agent
}

// isValidFilterValue checks that a filter value contains only safe characters:
// alphanumeric, hyphens, underscores, dots, and spaces.
func isValidFilterValue(value string) bool {
	for _, r := range value {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' && r != '.' && r != ' ' {
			return false
		}
	}
	return true
}

// hasLabel checks if a label list contains a specific label (case-insensitive).
func hasLabel(labels []string, name string) bool {
	for _, l := range labels {
		if strings.EqualFold(l, name) {
			return true
		}
	}
	return false
}

// EnableWatch configures the supervisor for watch mode with the given filter.
func (s *Supervisor) EnableWatch(filter string) error {
	if _, err := ParseWatchFilter(filter); err != nil {
		return err
	}
	s.mu.Lock()
	s.watchMode = true
	s.mu.Unlock()
	return nil
}

// WatchTick is called periodically by the main loop to check for new issues.
// It returns the number of newly discovered + queued tasks.
func (s *Supervisor) WatchTick(ctx context.Context) int {
	discovered := s.watchPoll(ctx)
	if discovered > 0 {
		s.fillCapacity(ctx)
	}
	return discovered
}

// addWatchTickerToLoop enables watch polling in the supervisor loop.
// Returns true if there's a --watch filter OR trigger routes from jobs.yaml.
func (s *Supervisor) addWatchTickerToLoop() bool {
	if s.deploy.WatchFilter.Valid && s.deploy.WatchFilter.String != "" {
		return true
	}
	s.mu.Lock()
	hasTriggers := len(s.triggerRoutes) > 0
	s.mu.Unlock()
	return hasTriggers
}
