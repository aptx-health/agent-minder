package cmd

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/dustinlange/agent-minder/internal/llm"
	"github.com/dustinlange/agent-minder/internal/poller"
	"github.com/dustinlange/agent-minder/internal/tui"
	"github.com/spf13/cobra"
)

var startCmd = &cobra.Command{
	Use:   "start <project>",
	Short: "Launch the monitoring dashboard",
	Args:  cobra.ExactArgs(1),
	RunE:  runStart,
}

func init() {
	rootCmd.AddCommand(startCmd)
}

func runStart(cmd *cobra.Command, args []string) error {
	projectName := args[0]

	// Open database.
	dbPath := db.DefaultDBPath()
	conn, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer conn.Close()
	store := db.NewStore(conn)

	// Load project.
	project, err := store.GetProject(projectName)
	if err != nil {
		return fmt.Errorf("project %q not found — run 'agent-minder init' first", projectName)
	}

	// Create LLM provider.
	provider, err := llm.NewProvider(project.LLMProvider)
	if err != nil {
		return fmt.Errorf("creating LLM provider: %w", err)
	}

	// Create poller.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := poller.New(store, project, provider)
	p.Start(ctx)
	defer p.Stop()

	// Launch TUI.
	model := tui.New(project, store, p)
	program := tea.NewProgram(model)
	if _, err := program.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	return nil
}
