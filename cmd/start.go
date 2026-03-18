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
	Long: `Launch the TUI monitoring dashboard for a project. The dashboard shows
real-time git activity, message bus traffic, LLM analysis, and active
concerns. It runs a two-tier LLM pipeline (Haiku summarizer → Sonnet
analyzer) on each poll cycle.

Key bindings: p=pause, r=poll now, e=expand, u=user msg, m=broadcast,
o=onboard, a=autopilot, A=stop autopilot, t=theme, q=quit.`,
	Example: `  # Start monitoring a project
  agent-minder start my-project

  # Start with a custom database (useful for testing)
  MINDER_DB=~/test.db agent-minder start my-project

  # Start with debug logging enabled
  MINDER_DEBUG=1 agent-minder start my-project`,
	Args: cobra.ExactArgs(1),
	RunE: runStart,
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
	defer func() { _ = conn.Close() }()
	store := db.NewStore(conn)

	// Load project.
	project, err := store.GetProject(projectName)
	if err != nil {
		return fmt.Errorf("project %q not found — run 'agent-minder init' first", projectName)
	}

	// Resolve effective provider names for each tier.
	// Per-tier overrides fall back to the project-level provider.
	summarizerProviderName := project.LLMSummarizerProvider
	if summarizerProviderName == "" {
		summarizerProviderName = project.LLMProvider
	}
	analyzerProviderName := project.LLMAnalyzerProvider
	if analyzerProviderName == "" {
		analyzerProviderName = project.LLMProvider
	}

	// Create summarizer (tier 1) provider.
	summarizerProvider, err := createProvider(summarizerProviderName)
	if err != nil {
		return fmt.Errorf("creating summarizer provider: %w", err)
	}

	// Reuse the same instance if both tiers use the same provider,
	// otherwise create a separate analyzer (tier 2) provider.
	analyzerProvider := summarizerProvider
	if analyzerProviderName != summarizerProviderName {
		analyzerProvider, err = createProvider(analyzerProviderName)
		if err != nil {
			return fmt.Errorf("creating analyzer provider: %w", err)
		}
	}

	// Log provider configuration when different providers are used per tier.
	if summarizerProviderName != analyzerProviderName {
		log.Printf("Using %s for summarizer (tier 1), %s for analyzer (tier 2)", summarizerProviderName, analyzerProviderName)
	}

	// Create bus publisher (non-fatal if unavailable).
	var publisher *msgbus.Publisher
	msgDBPath := msgbus.DefaultDBPath()
	pub, err := msgbus.NewPublisher(msgDBPath)
	if err != nil {
		log.Printf("Warning: bus publishing unavailable: %v", err)
	} else {
		publisher = pub
		defer func() { _ = publisher.Close() }()
	}

	// Create poller.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := poller.New(store, project, summarizerProvider, analyzerProvider, publisher)
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

// createProvider builds an LLM provider by name, sourcing credentials from config.
func createProvider(name string) (llm.Provider, error) {
	var opts []llm.Option
	if apiKey := config.GetProviderAPIKey(name); apiKey != "" {
		opts = append(opts, llm.WithAPIKey(apiKey))
	}
	if baseURL := config.GetProviderBaseURL(name); baseURL != "" {
		opts = append(opts, llm.WithBaseURL(baseURL))
	}
	return llm.NewProvider(name, opts...)
}
