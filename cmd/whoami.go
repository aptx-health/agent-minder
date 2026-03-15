package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/spf13/cobra"
)

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Print the current project name based on working directory",
	Args:  cobra.NoArgs,
	RunE:  runWhoami,
}

func init() {
	rootCmd.AddCommand(whoamiCmd)
}

func runWhoami(cmd *cobra.Command, _ []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		return fmt.Errorf("resolving symlinks: %w", err)
	}

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

	for _, p := range projects {
		repos, _ := store.GetRepos(p.ID)
		for _, r := range repos {
			resolved, err := filepath.EvalSymlinks(r.Path)
			if err != nil {
				resolved = r.Path
			}
			if isSubpath(cwd, resolved) {
				fmt.Fprintln(cmd.OutOrStdout(), p.Name)
				return nil
			}

			wts, _ := store.GetWorktrees(r.ID)
			for _, wt := range wts {
				resolved, err := filepath.EvalSymlinks(wt.Path)
				if err != nil {
					resolved = wt.Path
				}
				if isSubpath(cwd, resolved) {
					fmt.Fprintln(cmd.OutOrStdout(), p.Name)
					return nil
				}
			}
		}
	}

	return fmt.Errorf("no project found for directory %s", cwd)
}

// isSubpath reports whether child is equal to or under parent.
func isSubpath(child, parent string) bool {
	child = filepath.Clean(child)
	parent = filepath.Clean(parent)
	if child == parent {
		return true
	}
	return strings.HasPrefix(child, parent+string(filepath.Separator))
}
