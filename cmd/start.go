package cmd

import (
	"context"
	"fmt"
	"log"

	tea "charm.land/bubbletea/v2"
	"github.com/dustinlange/agent-minder/internal/config"
	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/dustinlange/agent-minder/internal/llm"
	"github.com/dustinlange/agent-minder/internal/msgbus"
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

	// Create LLM provider, using config-sourced API key if available.
	var providerOpts []llm.Option
	if apiKey := config.GetProviderAPIKey(project.LLMProvider); apiKey != "" {
		providerOpts = append(providerOpts, llm.WithAPIKey(apiKey))
	}
	if baseURL := config.GetProviderBaseURL(project.LLMProvider); baseURL != "" {
		providerOpts = append(providerOpts, llm.WithBaseURL(baseURL))
	}
	provider, err := llm.NewProvider(project.LLMProvider, providerOpts...)
	if err != nil {
		return fmt.Errorf("creating LLM provider: %w", err)
	}

	// Create bus publisher (non-fatal if unavailable).
	var publisher *msgbus.Publisher
	msgDBPath := msgbus.DefaultDBPath()
	pub, err := msgbus.NewPublisher(msgDBPath)
	if err != nil {
		log.Printf("Warning: bus publishing unavailable: %v", err)
	} else {
		publisher = pub
		defer publisher.Close()
	}

	// Create poller.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := poller.New(store, project, provider, publisher)
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
