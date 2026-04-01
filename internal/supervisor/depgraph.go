package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/aptx-health/agent-minder/internal/claudecli"
	"github.com/aptx-health/agent-minder/internal/db"
	ghpkg "github.com/aptx-health/agent-minder/internal/github"
)

// DepOption represents one ranked dependency graph option produced by the LLM.
type DepOption struct {
	Name      string                     // e.g., "Conservative — minimal deps"
	Rationale string                     // why this ordering makes sense
	Graph     map[string]json.RawMessage // issue# → deps array or "skip"
	Unblocked int                        // pre-computed count of unblocked tasks
}

// PrepareResult holds the outcome of a Prepare() call.
type PrepareResult struct {
	Total     int
	Options   []DepOption
	AgentDef  AgentDefSource
	WatchMode bool
}

// Prepare fetches issue details, creates tasks, and builds a dependency graph.
// Returns ranked dep graph options for the user to select.
func Prepare(ctx context.Context, store *db.Store, completer claudecli.Completer,
	deploy *db.Deployment, issues []int, ghToken string) (*PrepareResult, error) {

	if len(issues) == 0 {
		return &PrepareResult{WatchMode: true}, nil
	}

	// Fetch issue details from GitHub and create tasks.
	ghClient := newGHClientForToken(ghToken)
	var tasks []*db.Task
	for _, num := range issues {
		item, err := ghClient.FetchItem(ctx, deploy.Owner, deploy.Repo, num)
		if err != nil {
			return nil, fmt.Errorf("fetch issue #%d: %w", num, err)
		}

		content, _ := ghClient.FetchItemContent(ctx, deploy.Owner, deploy.Repo, num, "issue")
		body := ""
		if content != nil {
			body = content.Body
		}

		t := &db.Task{
			DeploymentID: deploy.ID,
			IssueNumber:  num,
			Owner:        deploy.Owner,
			Repo:         deploy.Repo,
			Status:       db.StatusQueued,
		}
		t.IssueTitle.String = item.Title
		t.IssueTitle.Valid = true
		t.IssueBody.String = body
		t.IssueBody.Valid = body != ""

		tasks = append(tasks, t)
	}

	if err := store.BulkCreateTasks(tasks); err != nil {
		return nil, fmt.Errorf("create tasks: %w", err)
	}

	// Detect agent definition.
	agentDef := AgentDefBuiltIn
	if deploy.RepoDir != "" {
		if src, err := ensureAgentDef(deploy.RepoDir); err == nil {
			agentDef = src
		}
	}

	// If only one task, no dep graph needed.
	if len(tasks) == 1 {
		return &PrepareResult{
			Total:    1,
			AgentDef: agentDef,
		}, nil
	}

	// Build dep graph via LLM.
	options, err := buildDepOptions(ctx, completer, deploy, tasks)
	if err != nil {
		return nil, fmt.Errorf("build dep graph: %w", err)
	}

	return &PrepareResult{
		Total:    len(tasks),
		Options:  options,
		AgentDef: agentDef,
	}, nil
}

// ApplyDepOption applies a selected dependency graph option to the tasks.
func ApplyDepOption(store *db.Store, deploy *db.Deployment, opt DepOption) error {
	tasks, err := store.GetTasks(deploy.ID)
	if err != nil {
		return err
	}

	for _, task := range tasks {
		key := strconv.Itoa(task.IssueNumber)
		raw, exists := opt.Graph[key]
		if !exists {
			continue
		}

		// Check for "skip" sentinel.
		var skip string
		if json.Unmarshal(raw, &skip) == nil && skip == "skip" {
			_ = store.UpdateTaskStatus(task.ID, "skipped")
			continue
		}

		// Parse deps array.
		var deps []int
		if err := json.Unmarshal(raw, &deps); err == nil && len(deps) > 0 {
			_ = store.UpdateTaskDeps(task.ID, deps)
			_ = store.UpdateTaskStatus(task.ID, db.StatusBlocked)
		}
	}

	// Save the graph.
	graphJSON, _ := json.Marshal(opt.Graph)
	return store.SaveDepGraph(deploy.ID, string(graphJSON), opt.Name)
}

// buildDepOptions calls the LLM to generate ranked dependency graph options.
func buildDepOptions(ctx context.Context, completer claudecli.Completer,
	deploy *db.Deployment, tasks []*db.Task) ([]DepOption, error) {

	// Build task summaries for the LLM.
	var summaries []string
	for _, t := range tasks {
		summary := fmt.Sprintf("- #%d: %s", t.IssueNumber, t.IssueTitle.String)
		if t.IssueBody.Valid && t.IssueBody.String != "" {
			// Truncate body to first 200 chars.
			body := t.IssueBody.String
			if len(body) > 200 {
				body = body[:200] + "..."
			}
			summary += "\n  " + body
		}
		summaries = append(summaries, summary)
	}

	issueList := strings.Join(summaries, "\n")
	issueNums := make([]string, len(tasks))
	for i, t := range tasks {
		issueNums[i] = strconv.Itoa(t.IssueNumber)
	}

	prompt := fmt.Sprintf(`Analyze these GitHub issues and produce 2-3 ranked dependency graph options.

Repository: %s/%s

Issues:
%s

For each option, produce a JSON object where keys are issue numbers (as strings) and values are either:
- An array of issue numbers this issue depends on (can be empty [])
- The string "skip" if the issue should be excluded

Provide options ranked from most conservative (more dependencies, safer ordering) to most aggressive (fewer dependencies, more parallelism).

Respond with JSON matching this schema:
{
  "options": [
    {
      "name": "Option name",
      "rationale": "Why this ordering",
      "graph": { "42": [], "43": [42], "44": "skip" }
    }
  ]
}`, deploy.Owner, deploy.Repo, issueList)

	schema := `{
		"type": "object",
		"properties": {
			"options": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"name": {"type": "string"},
						"rationale": {"type": "string"},
						"graph": {"type": "object"}
					},
					"required": ["name", "rationale", "graph"]
				}
			}
		},
		"required": ["options"]
	}`

	resp, err := completer.Complete(ctx, &claudecli.Request{
		SystemPrompt: "You are a dependency analysis assistant. Analyze GitHub issues and determine execution ordering.",
		Prompt:       prompt,
		Model:        deploy.AnalyzerModel,
		JSONSchema:   schema,
		DisableTools: true,
	})
	if err != nil {
		return nil, err
	}

	// Parse response.
	var result struct {
		Options []struct {
			Name      string                     `json:"name"`
			Rationale string                     `json:"rationale"`
			Graph     map[string]json.RawMessage `json:"graph"`
		} `json:"options"`
	}

	content := resp.Content()
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, fmt.Errorf("parse dep graph response: %w", err)
	}

	// Convert to DepOption and count unblocked.
	var options []DepOption
	for _, opt := range result.Options {
		unblocked := countUnblocked(opt.Graph)
		options = append(options, DepOption{
			Name:      opt.Name,
			Rationale: opt.Rationale,
			Graph:     opt.Graph,
			Unblocked: unblocked,
		})
	}

	return options, nil
}

// countUnblocked counts how many tasks have no dependencies (empty array).
func countUnblocked(graph map[string]json.RawMessage) int {
	count := 0
	for _, raw := range graph {
		var deps []int
		if json.Unmarshal(raw, &deps) == nil && len(deps) == 0 {
			count++
		}
	}
	return count
}

// BuildDepOptionsFromStore builds dep graph options from tasks already in the DB.
func BuildDepOptionsFromStore(ctx context.Context, completer claudecli.Completer,
	store *db.Store, deploy *db.Deployment) ([]DepOption, error) {

	tasks, err := store.GetTasks(deploy.ID)
	if err != nil {
		return nil, err
	}
	return buildDepOptions(ctx, completer, deploy, tasks)
}

// newGHClientForToken creates a GitHub client from a token.
func newGHClientForToken(token string) *ghpkg.Client {
	return ghpkg.NewClient(token)
}
