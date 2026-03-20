package cmd

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/dustinlange/agent-minder/internal/tui"
	"github.com/spf13/cobra"
)

var deployOpenCmd = &cobra.Command{
	Use:   "open <deploy-id>",
	Short: "Open interactive viewer for a deployment",
	Args:  cobra.ExactArgs(1),
	RunE:  runDeployOpen,
}

func init() {
	deployCmd.AddCommand(deployOpenCmd)
}

func runDeployOpen(cmd *cobra.Command, args []string) error {
	id := args[0]

	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() { _ = conn.Close() }()
	store := db.NewStore(conn)

	project, err := store.GetProject(id)
	if err != nil {
		return fmt.Errorf("deploy %q not found", id)
	}
	if !project.IsDeploy {
		return fmt.Errorf("%q is not a deploy project", id)
	}

	model := tui.NewDeployViewer(store, project)
	program := tea.NewProgram(model)
	if _, err := program.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}
	return nil
}
