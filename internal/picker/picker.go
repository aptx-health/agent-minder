// Package picker provides a shared interactive job selector using huh.
package picker

import (
	"fmt"

	"github.com/charmbracelet/huh"

	"github.com/aptx-health/agent-minder/internal/daemon"
	"github.com/aptx-health/agent-minder/internal/db"
)

// Action represents what the user wants to do with a selected job.
type Action string

const (
	ActionCheckout Action = "checkout"
	ActionResume   Action = "resume"
	ActionLogs     Action = "logs"
)

// PickAction presents an action menu after job selection.
func PickAction(job *db.Job) (Action, error) {
	options := []huh.Option[Action]{
		huh.NewOption("Checkout worktree", ActionCheckout),
		huh.NewOption("Resume with Claude (gather context, launch claude)", ActionResume),
		huh.NewOption("View logs", ActionLogs),
	}

	title := fmt.Sprintf("#%d [%s] %s", job.IssueNumber, job.Agent, truncate(job.IssueTitle.String, 40))
	if job.IssueNumber == 0 {
		title = fmt.Sprintf("[%s] %s", job.Agent, truncate(job.Name, 40))
	}

	var selected Action
	err := huh.NewSelect[Action]().
		Title(title).
		Options(options...).
		Value(&selected).
		Run()
	if err != nil {
		return "", err
	}
	return selected, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

// PickJob presents an interactive select list of jobs and returns the chosen one.
// Jobs are displayed with issue number, agent, title, status, PR, and cost.
func PickJob(jobs []*db.Job, title string) (*db.Job, error) {
	if len(jobs) == 0 {
		return nil, fmt.Errorf("no jobs to select from")
	}

	options := make([]huh.Option[*db.Job], len(jobs))
	for i, j := range jobs {
		options[i] = huh.NewOption(formatJobLine(j), j)
	}

	var selected *db.Job
	err := huh.NewSelect[*db.Job]().
		Title(title).
		Options(options...).
		Value(&selected).
		Run()
	if err != nil {
		return nil, err
	}

	return selected, nil
}

func formatJobLine(j *db.Job) string {
	// Issue or job name.
	var label string
	if j.IssueNumber > 0 {
		label = fmt.Sprintf("#%-4d", j.IssueNumber)
	} else {
		name := j.Name
		if len(name) > 20 {
			name = name[:17] + "..."
		}
		label = fmt.Sprintf("%-5s", name)
	}

	// Agent type.
	agent := j.Agent
	if len(agent) > 12 {
		agent = agent[:12]
	}

	// Title.
	title := j.IssueTitle.String
	if title == "" {
		title = j.Name
	}
	if len(title) > 35 {
		title = title[:32] + "..."
	}

	// PR.
	pr := "      "
	if j.PRNumber.Valid && j.PRNumber.Int64 > 0 {
		pr = fmt.Sprintf("PR#%-3d", j.PRNumber.Int64)
	}

	// Cost.
	cost := ""
	if j.CostUSD > 0 {
		cost = fmt.Sprintf("$%.2f", j.CostUSD)
	}

	return fmt.Sprintf("%s  [%-12s]  %-35s  %-10s  %s  %s",
		label, agent, title, j.Status, pr, cost)
}

// PickRemoteJob presents an interactive select list of remote API jobs.
func PickRemoteJob(jobs []daemon.JobResponse, title string) (*daemon.JobResponse, error) {
	if len(jobs) == 0 {
		return nil, fmt.Errorf("no jobs to select from")
	}

	options := make([]huh.Option[*daemon.JobResponse], len(jobs))
	for i := range jobs {
		options[i] = huh.NewOption(formatRemoteJobLine(&jobs[i]), &jobs[i])
	}

	var selected *daemon.JobResponse
	err := huh.NewSelect[*daemon.JobResponse]().
		Title(title).
		Options(options...).
		Value(&selected).
		Run()
	if err != nil {
		return nil, err
	}

	return selected, nil
}

func formatRemoteJobLine(j *daemon.JobResponse) string {
	var label string
	if j.IssueNumber > 0 {
		label = fmt.Sprintf("#%-4d", j.IssueNumber)
	} else {
		name := j.Name
		if len(name) > 20 {
			name = name[:17] + "..."
		}
		label = fmt.Sprintf("%-5s", name)
	}

	agent := j.Agent
	if len(agent) > 12 {
		agent = agent[:12]
	}

	title := j.Title
	if title == "" {
		title = j.Name
	}
	if len(title) > 35 {
		title = title[:32] + "..."
	}

	pr := "      "
	if j.PRNumber > 0 {
		pr = fmt.Sprintf("PR#%-3d", j.PRNumber)
	}

	cost := ""
	if j.CostUSD > 0 {
		cost = fmt.Sprintf("$%.2f", j.CostUSD)
	}

	return fmt.Sprintf("%s  [%-12s]  %-35s  %-10s  %s  %s",
		label, agent, title, j.Status, pr, cost)
}
