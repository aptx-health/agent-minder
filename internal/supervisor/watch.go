package supervisor

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode"

	"github.com/aptx-health/agent-minder/internal/db"
	ghpkg "github.com/aptx-health/agent-minder/internal/github"
)

// WatchFilter represents a parsed watch filter.
type WatchFilter struct {
	Type  string // "label" or "milestone"
	Value string
}

// TriggerRoute maps a label to an agent type.
// When an issue has the matching label, it's routed to the specified agent
// instead of the default autopilot.
type TriggerRoute struct {
	Label string // GitHub label to match
	Agent string // agent type to use
}

// SetTriggerRoutes configures label→agent routing from jobs.yaml triggers.
func (s *Supervisor) SetTriggerRoutes(routes []TriggerRoute) {
	s.mu.Lock()
	s.triggerRoutes = routes
	s.mu.Unlock()
}

// ParseWatchFilter parses a filter string like "label:ready" or "milestone:v2.0".
func ParseWatchFilter(filter string) (*WatchFilter, error) {
	parts := strings.SplitN(filter, ":", 2)
	if len(parts) != 2 || parts[1] == "" {
		return nil, fmt.Errorf("invalid watch filter %q (expected label:<name> or milestone:<name>)", filter)
	}
	typ := strings.ToLower(parts[0])
	if typ != "label" && typ != "milestone" {
		return nil, fmt.Errorf("unsupported filter type %q (expected label or milestone)", typ)
	}
	value := parts[1]
	if !isValidFilterValue(value) {
		return nil, fmt.Errorf("invalid watch filter value %q (must contain only alphanumeric, hyphen, underscore, dot, or space characters)", value)
	}
	return &WatchFilter{Type: typ, Value: value}, nil
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

	// 1. Poll the --watch filter (routes to default agent).
	if s.deploy.WatchFilter.Valid && s.deploy.WatchFilter.String != "" {
		issues := s.pollFilter(ctx, ghClient, s.deploy.WatchFilter.String)
		for _, issue := range issues {
			agent := s.resolveAgentForIssue(issue.Labels)
			if knownJobs[issueAgent{issue.Number, agent}] || issue.State != "open" || hasLabel(issue.Labels, skipLabel) {
				continue
			}
			if n := s.createJobForIssue(ctx, ghClient, issue, agent); n > 0 {
				knownJobs[issueAgent{issue.Number, agent}] = true
				discovered += n
			}
		}
	}

	// 2. Poll trigger routes (label→agent mapping from jobs.yaml).
	s.mu.Lock()
	routes := s.triggerRoutes
	s.mu.Unlock()

	for _, route := range routes {
		issues := s.pollFilter(ctx, ghClient, "label:"+route.Label)
		for _, issue := range issues {
			if knownJobs[issueAgent{issue.Number, route.Agent}] || issue.State != "open" || hasLabel(issue.Labels, skipLabel) {
				continue
			}
			if n := s.createJobForIssue(ctx, ghClient, issue, route.Agent); n > 0 {
				knownJobs[issueAgent{issue.Number, route.Agent}] = true
				discovered += n
			}
		}
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
		searchResult, err = ghClient.ListIssuesByLabel(ctx, s.owner, s.repo, filter.Value)
	case "milestone":
		msNum, msErr := ghClient.FindMilestoneNumber(ctx, s.owner, s.repo, filter.Value)
		if msErr != nil {
			return nil
		}
		searchResult, err = ghClient.ListIssuesByMilestone(ctx, s.owner, s.repo, msNum)
	}
	if err != nil || searchResult == nil {
		return nil
	}
	return searchResult.Items
}

// resolveAgentForIssue checks the issue's labels against trigger routes.
// Returns the matching agent, or "autopilot" as default.
func (s *Supervisor) resolveAgentForIssue(labels []string) string {
	s.mu.Lock()
	routes := s.triggerRoutes
	s.mu.Unlock()

	for _, route := range routes {
		if hasLabel(labels, route.Label) {
			return route.Agent
		}
	}
	return "autopilot"
}

// createJobForIssue fetches issue content and inserts a job row.
func (s *Supervisor) createJobForIssue(ctx context.Context, ghClient *ghpkg.Client, issue ghpkg.ItemStatus, agent string) int {
	content, _ := ghClient.FetchItemContent(ctx, s.owner, s.repo, issue.Number, "issue")
	body := ""
	if content != nil {
		body = content.Body
	}

	j := &db.Job{
		DeploymentID: s.deploy.ID,
		Agent:        agent,
		Name:         fmt.Sprintf("%s-issue-%d", agent, issue.Number),
		IssueNumber:  issue.Number,
		IssueTitle:   sql.NullString{String: issue.Title, Valid: true},
		IssueBody:    sql.NullString{String: body, Valid: body != ""},
		Owner:        s.owner,
		Repo:         s.repo,
		Status:       db.StatusQueued,
	}

	if err := s.store.CreateJob(j); err != nil {
		return 0
	}

	s.emitEvent("info", fmt.Sprintf("Discovered #%d: %s (agent: %s)", issue.Number, issue.Title, agent), j.ID)
	return 1
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
