package cmd

import (
	"fmt"
	"os"

	"github.com/dustinlange/agent-minder/internal/db"
	gitpkg "github.com/dustinlange/agent-minder/internal/git"
	"github.com/spf13/cobra"
)

var whoamiVerbose bool

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Print the project name for the current directory",
	Args:  cobra.NoArgs,
	RunE:  runWhoami,
}

func init() {
	whoamiCmd.Flags().BoolVarP(&whoamiVerbose, "verbose", "v", false, "Also print repo list and goal")
	rootCmd.AddCommand(whoamiCmd)
}

func runWhoami(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	repoRoot, err := gitpkg.TopLevel(cwd)
	if err != nil {
		return fmt.Errorf("not inside a git repository")
	}

	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer func() { _ = conn.Close() }()
	store := db.NewStore(conn)

	project, err := store.GetProjectByRepoPath(repoRoot)
	if err != nil {
		return fmt.Errorf("no project found for this directory")
	}

	fmt.Println(project.Name)

	if whoamiVerbose {
		fmt.Printf("Goal: %s — %s\n", project.GoalType, project.GoalDescription)

		repos, _ := store.GetRepos(project.ID)
		if len(repos) > 0 {
			fmt.Println("Repos:")
			for _, r := range repos {
				fmt.Printf("  %s (%s)\n", r.ShortName, r.Path)
			}
		}
	}

	return nil
}
