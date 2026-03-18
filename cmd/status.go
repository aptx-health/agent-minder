package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/dustinlange/agent-minder/internal/db"
	gitpkg "github.com/dustinlange/agent-minder/internal/git"
	"github.com/dustinlange/agent-minder/internal/msgbus"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status <project>",
	Short: "Show catch-up summary for the human (no AI call)",
	Long: `Display a quick catch-up summary for a project without making any LLM
calls. Shows the project goal, enrolled repos with recent commits,
message bus traffic, tracked GitHub items, active concerns, and the
last LLM analysis.

Useful for checking project state before launching the full TUI.`,
	Example: `  # Show status for a project
  agent-minder status my-project`,
	Args: cobra.ExactArgs(1),
	RunE: runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	projectName := args[0]

	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer func() { _ = conn.Close() }()
	store := db.NewStore(conn)

	project, err := store.GetProject(projectName)
	if err != nil {
		return fmt.Errorf("project %q not found — run 'agent-minder init' first", projectName)
	}

	repos, _ := store.GetRepos(project.ID)
	concerns, _ := store.ActiveConcerns(project.ID)
	lastPoll, _ := store.LastPoll(project.ID)

	// Header.
	fmt.Printf("Minder: %s\n", project.Name)
	fmt.Printf("Goal: %s — %s\n", project.GoalType, project.GoalDescription)
	if lastPoll != nil {
		fmt.Printf("Last poll: %s\n", lastPoll.PolledAt)
	} else {
		fmt.Printf("Last poll: (never)\n")
	}
	fmt.Println()

	// Repos.
	fmt.Println("Repos:")
	yesterday := time.Now().Add(-24 * time.Hour)
	for _, repo := range repos {
		branch, _ := gitpkg.CurrentBranch(repo.Path)
		entries, _ := gitpkg.LogSince(repo.Path, yesterday)

		wts, _ := store.GetWorktrees(repo.ID)
		wtNote := ""
		if len(wts) > 1 {
			wtNote = fmt.Sprintf(" + %d worktrees", len(wts)-1)
		}

		fmt.Printf("  %s (%s%s)\n", repo.ShortName, branch, wtNote)
		if len(entries) > 0 {
			var subjects []string
			limit := 3
			if len(entries) < limit {
				limit = len(entries)
			}
			for _, e := range entries[:limit] {
				subjects = append(subjects, e.Subject)
			}
			fmt.Printf("    %d new commits since yesterday (%s)\n", len(entries), strings.Join(subjects, ", "))
		} else {
			fmt.Printf("    no new commits since yesterday\n")
		}
	}
	fmt.Println()

	// Messages.
	dbPath := msgbus.DefaultDBPath()
	client, err := msgbus.Open(dbPath)
	if err == nil {
		defer func() { _ = client.Close() }()
		ttl := time.Duration(project.MessageTTLSec) * time.Second
		msgs, _ := client.RecentMessages(ttl, project.Name)
		if len(msgs) > 0 {
			fmt.Printf("Messages (%d):\n", len(msgs))
			limit := 5
			if len(msgs) < limit {
				limit = len(msgs)
			}
			for _, m := range msgs[:limit] {
				fmt.Printf("  [%s] %s: %s\n", m.Topic, m.Sender, truncate(m.Message, 80))
			}
			fmt.Println()
		}
	}

	// Tracked items.
	trackedItems, err := store.GetTrackedItems(project.ID)
	if err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: loading tracked items: %v\n", err)
	}
	if len(trackedItems) > 0 {
		fmt.Printf("Tracked Items (%d):\n", len(trackedItems))
		for _, item := range trackedItems {
			fmt.Printf("  [%s] %s — %s\n", item.LastStatus, item.DisplayRef(), item.Title)
		}
		fmt.Println()
	}

	// Concerns.
	if len(concerns) > 0 {
		fmt.Println("Active Concerns:")
		for _, c := range concerns {
			prefix := "INFO"
			if c.Severity == "warning" {
				prefix = "WARN"
			}
			fmt.Printf("  [%s] %s\n", prefix, c.Message)
		}
		fmt.Println()
	}

	// Last LLM response.
	if lastPoll != nil && lastPoll.LLMResponse() != "" {
		fmt.Println("Last Analysis:")
		fmt.Printf("  %s\n", lastPoll.LLMResponse())
		fmt.Println()
	}

	return nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
