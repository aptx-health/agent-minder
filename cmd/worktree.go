package cmd

import (
	"fmt"

	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/spf13/cobra"
)

var worktreeCmd = &cobra.Command{
	Use:   "worktree <project> [issue-number]",
	Short: "Print worktree path for running autopilot agents",
	Long: `Print the worktree path for running autopilot agents.

With an issue number, prints only that agent's worktree path (pipe-friendly):
  cd $(agent-minder worktree myproject 42)

Without an issue number, lists all running agents and their paths.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runWorktree,
}

func init() {
	rootCmd.AddCommand(worktreeCmd)
}

func runWorktree(cmd *cobra.Command, args []string) error {
	projectName := args[0]

	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer conn.Close()
	store := db.NewStore(conn)

	project, err := store.GetProject(projectName)
	if err != nil {
		return fmt.Errorf("project %q not found — run 'agent-minder init' first", projectName)
	}

	tasks, err := store.RunningAutopilotTasks(project.ID)
	if err != nil {
		return fmt.Errorf("querying running tasks: %w", err)
	}

	// If an issue number is specified, print just that worktree path.
	if len(args) == 2 {
		var issueNum int
		if _, err := fmt.Sscanf(args[1], "%d", &issueNum); err != nil {
			return fmt.Errorf("invalid issue number: %s", args[1])
		}

		for _, t := range tasks {
			if t.IssueNumber == issueNum {
				fmt.Println(t.WorktreePath)
				return nil
			}
		}
		return fmt.Errorf("no running agent for issue #%d", issueNum)
	}

	// No issue number — list all running agents.
	if len(tasks) == 0 {
		fmt.Println("No running autopilot agents.")
		return nil
	}

	for _, t := range tasks {
		fmt.Printf("#%-6d %s\n", t.IssueNumber, t.WorktreePath)
	}
	return nil
}
