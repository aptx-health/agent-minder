package cmd

import (
	"embed"
	"fmt"
	"os"
	"strings"
	"text/template"
	"time"

	"github.com/dustinlange/agent-minder/internal/config"
	gitpkg "github.com/dustinlange/agent-minder/internal/git"
	"github.com/dustinlange/agent-minder/internal/msgbus"
	"github.com/dustinlange/agent-minder/internal/state"
	"github.com/spf13/cobra"
)

//go:embed templates
var templateFS embed.FS

// StatusData holds all data passed to the status template.
type StatusData struct {
	Project        string
	Running        bool
	LastPoll       string
	Repos          []RepoStatus
	Messages       []msgbus.Message
	UnreadCount    int
	Concerns       []string
	StaleWorktrees []StaleWorktree
	Agents         []string
}

// RepoStatus holds per-repo status info.
type RepoStatus struct {
	ShortName      string
	Path           string
	Branch         string
	WorktreeCount  int
	ExtraWorktrees int
	RecentCommits  int
	CommitSummary  string
}

// StaleWorktree represents a worktree with no recent activity.
type StaleWorktree struct {
	Path string
	Note string
}

var statusCmd = &cobra.Command{
	Use:   "status <project>",
	Short: "Show catch-up summary for the human (no AI call)",
	Args:  cobra.ExactArgs(1),
	RunE:  runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	project := args[0]

	cfg, err := config.Load(project)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	st, err := state.Load(project)
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}

	data := StatusData{
		Project:  cfg.Name,
		Running:  cfg.ClaudeSessionID != "",
		LastPoll: st.LastPollTime,
		Concerns: formatConcerns(st.ActiveConcerns),
	}

	// Gather repo status.
	yesterday := time.Now().Add(-24 * time.Hour)
	for _, repo := range cfg.Repos {
		rs := RepoStatus{
			ShortName:     repo.ShortName,
			Path:          repo.Path,
			WorktreeCount: len(repo.Worktrees),
		}
		if rs.WorktreeCount > 1 {
			rs.ExtraWorktrees = rs.WorktreeCount - 1
		}

		// Current branch.
		rs.Branch, _ = gitpkg.CurrentBranch(repo.Path)

		// Recent commits.
		entries, _ := gitpkg.LogSince(repo.Path, yesterday)
		rs.RecentCommits = len(entries)
		if len(entries) > 0 {
			var subjects []string
			limit := 3
			if len(entries) < limit {
				limit = len(entries)
			}
			for _, e := range entries[:limit] {
				subjects = append(subjects, e.Subject)
			}
			rs.CommitSummary = strings.Join(subjects, ", ")
		}

		data.Repos = append(data.Repos, rs)
	}

	// Gather messages from the bus.
	data.Messages, data.UnreadCount = gatherMessages(cfg)

	// Gather active agents.
	data.Agents = gatherAgents(cfg)

	// Detect stale worktrees (no commits in 3+ days).
	data.StaleWorktrees = detectStaleWorktrees(cfg)

	return renderStatus(data)
}

func gatherMessages(cfg *config.Project) ([]msgbus.Message, int) {
	dbPath := msgbus.DefaultDBPath()
	client, err := msgbus.Open(dbPath)
	if err != nil {
		// Silently skip if agent-msg DB is unavailable.
		return nil, 0
	}
	defer client.Close()

	msgs, err := client.RecentMessages(cfg.MessageTTL, cfg.Name)
	if err != nil {
		return nil, 0
	}

	unread, err := client.UnreadMessages(cfg.MinderIdentity, cfg.Name)
	if err != nil {
		return msgs, 0
	}

	return msgs, len(unread)
}

func gatherAgents(cfg *config.Project) []string {
	dbPath := msgbus.DefaultDBPath()
	client, err := msgbus.Open(dbPath)
	if err != nil {
		return nil
	}
	defer client.Close()

	agents, _ := client.ActiveAgents(24*time.Hour, cfg.Name)
	return agents
}

func detectStaleWorktrees(cfg *config.Project) []StaleWorktree {
	var stale []StaleWorktree
	threeDaysAgo := time.Now().Add(-72 * time.Hour)

	for _, repo := range cfg.Repos {
		for _, wt := range repo.Worktrees {
			entries, _ := gitpkg.LogSince(wt.Path, threeDaysAgo)
			if len(entries) == 0 {
				// Check if it's the main worktree — don't flag main as stale.
				branch := wt.Branch
				if branch == "main" || branch == "master" {
					continue
				}
				stale = append(stale, StaleWorktree{
					Path: fmt.Sprintf("%s/%s", repo.ShortName, branch),
					Note: "no activity in 3+ days, consider pruning",
				})
			}
		}
	}

	return stale
}

func formatConcerns(concerns []string) []string {
	var formatted []string
	for _, c := range concerns {
		// Prefix with warning emoji equivalent.
		formatted = append(formatted, "! "+c)
	}
	return formatted
}

func renderStatus(data StatusData) error {
	tmplData, err := templateFS.ReadFile("templates/status.md.tmpl")
	if err != nil {
		return fmt.Errorf("reading status template: %w", err)
	}

	tmpl, err := template.New("status").Parse(string(tmplData))
	if err != nil {
		return fmt.Errorf("parsing status template: %w", err)
	}

	return tmpl.Execute(os.Stdout, data)
}
