package discord

import (
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/dustinlange/agent-minder/internal/api"
)

// Embed colors.
const (
	ColorBlue   = 0x3498db
	ColorGreen  = 0x2ecc71
	ColorYellow = 0xf1c40f
	ColorOrange = 0xe67e22
	ColorRed    = 0xe74c3c
	ColorGray   = 0x95a5a6
)

// analysisEmbed builds an embed for an analysis result.
func analysisEmbed(r *api.AnalysisResponse) *discordgo.MessageEmbed {
	fields := make([]*discordgo.MessageEmbedField, 0, 3)
	if r.NewCommits > 0 || r.NewMessages > 0 {
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   "Activity",
			Value:  fmt.Sprintf("%d commits, %d messages", r.NewCommits, r.NewMessages),
			Inline: true,
		})
	}
	if r.ConcernsRaised > 0 {
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   "Concerns",
			Value:  fmt.Sprintf("%d raised", r.ConcernsRaised),
			Inline: true,
		})
	}

	description := r.Analysis
	if len(description) > 4000 {
		description = description[:4000] + "\n\n*...truncated*"
	}

	return &discordgo.MessageEmbed{
		Title:       "Analysis",
		Description: description,
		Color:       ColorBlue,
		Fields:      fields,
		Footer: &discordgo.MessageEmbedFooter{
			Text: fmt.Sprintf("Polled at %s", r.PolledAt),
		},
	}
}

// statusEmbed builds an embed showing current task status.
func statusEmbed(tasks []api.TaskResponse) *discordgo.MessageEmbed {
	if len(tasks) == 0 {
		return &discordgo.MessageEmbed{
			Title:       "Task Status",
			Description: "No tasks found.",
			Color:       ColorGray,
		}
	}

	// Group by status.
	groups := make(map[string][]api.TaskResponse)
	for _, t := range tasks {
		groups[t.Status] = append(groups[t.Status], t)
	}

	// Order: running, review, reviewing, queued, blocked, done, bailed, failed, manual, skipped, stopped
	statusOrder := []string{
		"running", "review", "reviewing", "reviewed",
		"queued", "blocked",
		"done", "bailed", "failed", "manual", "skipped", "stopped",
	}

	var fields []*discordgo.MessageEmbedField
	for _, status := range statusOrder {
		tasksInGroup, ok := groups[status]
		if !ok {
			continue
		}

		var lines []string
		for _, t := range tasksInGroup {
			line := fmt.Sprintf("#%d %s", t.IssueNumber, t.IssueTitle)
			if t.PRNumber > 0 {
				line += fmt.Sprintf(" → PR #%d", t.PRNumber)
			}
			if t.CostUSD > 0 {
				line += fmt.Sprintf(" ($%.2f)", t.CostUSD)
			}
			lines = append(lines, line)
		}

		value := strings.Join(lines, "\n")
		if len(value) > 1024 {
			value = value[:1020] + "\n..."
		}

		fields = append(fields, &discordgo.MessageEmbedField{
			Name:  fmt.Sprintf("%s %s (%d)", statusIcon(status), capitalizeFirst(status), len(tasksInGroup)),
			Value: value,
		})
	}

	// Summary line.
	description := fmt.Sprintf("**%d total tasks**", len(tasks))

	return &discordgo.MessageEmbed{
		Title:       "Task Status",
		Description: description,
		Color:       statusColor(groups),
		Fields:      fields,
		Timestamp:   time.Now().Format(time.RFC3339),
	}
}

// settingsEmbed builds an embed showing project settings.
func settingsEmbed(status *api.StatusResponse) *discordgo.MessageEmbed {
	cfg := status.Config

	fields := []*discordgo.MessageEmbedField{
		{Name: "Max Agents", Value: fmt.Sprintf("%d", cfg.MaxAgents), Inline: true},
		{Name: "Max Turns", Value: fmt.Sprintf("%d", cfg.MaxTurns), Inline: true},
		{Name: "Max Budget", Value: fmt.Sprintf("$%.2f", cfg.MaxBudget), Inline: true},
		{Name: "Analyzer Model", Value: cfg.Analyzer, Inline: true},
		{Name: "Skip Label", Value: cfg.SkipLabel, Inline: true},
		{Name: "Auto Merge", Value: fmt.Sprintf("%v", cfg.AutoMerge), Inline: true},
	}
	if cfg.BaseBranch != "" {
		fields = append(fields, &discordgo.MessageEmbedField{
			Name: "Base Branch", Value: cfg.BaseBranch, Inline: true,
		})
	}

	description := fmt.Sprintf("Deploy: **%s**\nUptime: %s",
		status.DeployID, formatDuration(time.Duration(status.UptimeSec)*time.Second))

	return &discordgo.MessageEmbed{
		Title:       "Settings",
		Description: description,
		Color:       ColorBlue,
		Fields:      fields,
		Timestamp:   time.Now().Format(time.RFC3339),
	}
}

// MetricsResponse matches the JSON shape returned by GET /metrics.
type MetricsResponse struct {
	Spend        SpendInfo `json:"spend"`
	Tasks        TasksInfo `json:"tasks"`
	BudgetPaused bool      `json:"budget_paused"`
	UptimeSec    int       `json:"uptime_sec"`
}

// SpendInfo is the spend portion of the metrics response.
type SpendInfo struct {
	Total       float64     `json:"total"`
	Daily       *CostDetail `json:"daily"`
	Weekly      *CostDetail `json:"weekly"`
	Overall     *CostDetail `json:"overall"`
	Ceiling     float64     `json:"ceiling,omitempty"`
	Remaining   float64     `json:"remaining,omitempty"`
	Utilization float64     `json:"utilization,omitempty"`
}

// CostDetail represents a cost aggregation period.
type CostDetail struct {
	TotalCost float64 `json:"total_cost"`
	TaskCount int     `json:"task_count"`
}

// TasksInfo is the tasks portion of the metrics response.
type TasksInfo struct {
	Total       int            `json:"total"`
	ByStatus    map[string]int `json:"by_status"`
	SuccessRate float64        `json:"success_rate"`
}

// costEmbed builds an embed showing cost overview.
func costEmbed(metrics *MetricsResponse) *discordgo.MessageEmbed {
	spend := metrics.Spend

	fields := []*discordgo.MessageEmbedField{
		{Name: "Total Spend", Value: fmt.Sprintf("$%.2f", spend.Total), Inline: true},
	}

	if spend.Daily != nil {
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   "Today",
			Value:  fmt.Sprintf("$%.2f (%d tasks)", spend.Daily.TotalCost, spend.Daily.TaskCount),
			Inline: true,
		})
	}
	if spend.Weekly != nil {
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   "This Week",
			Value:  fmt.Sprintf("$%.2f (%d tasks)", spend.Weekly.TotalCost, spend.Weekly.TaskCount),
			Inline: true,
		})
	}
	if spend.Overall != nil {
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   "Overall",
			Value:  fmt.Sprintf("$%.2f (%d tasks)", spend.Overall.TotalCost, spend.Overall.TaskCount),
			Inline: true,
		})
	}

	if spend.Ceiling > 0 {
		bar := budgetBar(spend.Utilization)
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:  "Budget",
			Value: fmt.Sprintf("%s\n$%.2f / $%.2f (%.0f%%)", bar, spend.Total, spend.Ceiling, spend.Utilization*100),
		})
	}

	// Task success rate.
	fields = append(fields, &discordgo.MessageEmbedField{
		Name:   "Success Rate",
		Value:  fmt.Sprintf("%.0f%% (%d tasks)", metrics.Tasks.SuccessRate*100, metrics.Tasks.Total),
		Inline: true,
	})

	color := ColorGreen
	if metrics.BudgetPaused {
		color = ColorRed
	} else if spend.Utilization > 0.8 {
		color = ColorYellow
	}

	description := ""
	if metrics.BudgetPaused {
		description = "⚠️ **Budget paused** — new task launches are suspended"
	}

	return &discordgo.MessageEmbed{
		Title:       "Cost Overview",
		Description: description,
		Color:       color,
		Fields:      fields,
		Timestamp:   time.Now().Format(time.RFC3339),
	}
}

// errorEmbed builds a simple red error embed.
func errorEmbed(title, message string) *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title:       title,
		Description: message,
		Color:       ColorRed,
	}
}

// --- Helpers ---

func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func statusIcon(status string) string {
	switch status {
	case "running":
		return "🔄"
	case "review", "reviewing", "reviewed":
		return "👀"
	case "queued":
		return "📋"
	case "blocked":
		return "🚧"
	case "done":
		return "✅"
	case "bailed":
		return "🏳️"
	case "failed":
		return "❌"
	case "manual":
		return "🔧"
	case "skipped":
		return "⏭️"
	case "stopped":
		return "⏹️"
	default:
		return "📋"
	}
}

func statusColor(groups map[string][]api.TaskResponse) int {
	if len(groups["failed"]) > 0 {
		return ColorRed
	}
	if len(groups["bailed"]) > 0 {
		return ColorOrange
	}
	if len(groups["running"]) > 0 {
		return ColorBlue
	}
	return ColorGreen
}

func budgetBar(utilization float64) string {
	filled := int(utilization * 20)
	if filled > 20 {
		filled = 20
	}
	if filled < 0 {
		filled = 0
	}
	return "`" + strings.Repeat("█", filled) + strings.Repeat("░", 20-filled) + "`"
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	if hours < 24 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	days := hours / 24
	hours = hours % 24
	return fmt.Sprintf("%dd %dh", days, hours)
}
