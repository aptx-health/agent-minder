package cmd

import (
	"fmt"

	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List all projects",
	Args:    cobra.NoArgs,
	RunE:    runList,
}

func init() {
	rootCmd.AddCommand(listCmd)
}

func runList(cmd *cobra.Command, args []string) error {
	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer func() { _ = conn.Close() }()
	store := db.NewStore(conn)

	projects, err := store.ListProjects()
	if err != nil {
		return fmt.Errorf("listing projects: %w", err)
	}

	if len(projects) == 0 {
		fmt.Println("No projects. Run 'agent-minder init <repo-dir>' to create one.")
		return nil
	}

	for _, p := range projects {
		repos, _ := store.GetRepos(p.ID)
		fmt.Printf("%-20s  %-15s  %d repos  poll: %dm\n",
			p.Name, p.GoalType, len(repos), p.RefreshIntervalSec/60)
	}

	return nil
}
