package cmd

import (
	"fmt"

	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/spf13/cobra"
)

var untrackCmd = &cobra.Command{
	Use:   "untrack <project> <owner/repo#number>",
	Short: "Stop tracking a GitHub issue or PR",
	Args:  cobra.ExactArgs(2),
	RunE:  runUntrack,
}

func init() {
	rootCmd.AddCommand(untrackCmd)
}

func runUntrack(cmd *cobra.Command, args []string) error {
	projectName := args[0]
	ref := args[1]

	owner, repo, number, err := parseItemRef(ref)
	if err != nil {
		return err
	}

	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer func() { _ = conn.Close() }()
	store := db.NewStore(conn)

	project, err := store.GetProject(projectName)
	if err != nil {
		return fmt.Errorf("project %q not found", projectName)
	}

	if err := store.RemoveTrackedItem(project.ID, owner, repo, number); err != nil {
		return err
	}

	fmt.Printf("Untracked %s/%s#%d from project %q\n", owner, repo, number, projectName)
	return nil
}
