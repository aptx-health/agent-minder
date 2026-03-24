package cmd

import (
	"context"
	"fmt"
	"log"

	tea "charm.land/bubbletea/v2"
	"github.com/dustinlange/agent-minder/internal/claudecli"
	"github.com/dustinlange/agent-minder/internal/db"
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
concerns. It uses the Claude Code CLI for all LLM calls.

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
	startCmd.Flags().String("watch-milestone", "", "Watch a GitHub milestone for new issues during autopilot (e.g., \"v1.0\")")
	startCmd.Flags().String("watch-label", "", "Watch a GitHub label for new issues during autopilot (e.g., \"ready-for-agent\")")
	rootCmd.AddCommand(startCmd)
}

func runStart(cmd *cobra.Command, args []string) error {
	projectName := args[0]

	// Verify claude CLI is installed.
	version, err := claudecli.CheckVersion("")
	if err != nil {
		return fmt.Errorf("claude CLI not found: %w\nInstall from https://claude.ai/code", err)
	}
	log.Printf("Using Claude Code CLI: %s", version)

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
		return fmt.Errorf("project %q not found — run 'agent-minder list' to see available projects", projectName)
	}

	// Apply optional watch filter flags.
	watchMilestone, _ := cmd.Flags().GetString("watch-milestone")
	watchLabel, _ := cmd.Flags().GetString("watch-label")
	if watchMilestone != "" && watchLabel != "" {
		return fmt.Errorf("cannot specify both --watch-milestone and --watch-label")
	}
	if watchMilestone != "" {
		project.AutopilotFilterType = "milestone"
		project.AutopilotFilterValue = watchMilestone
		if err := store.UpdateProject(project); err != nil {
			return fmt.Errorf("saving watch filter: %w", err)
		}
		log.Printf("Watch filter: milestone %q", watchMilestone)
	} else if watchLabel != "" {
		project.AutopilotFilterType = "label"
		project.AutopilotFilterValue = watchLabel
		if err := store.UpdateProject(project); err != nil {
			return fmt.Errorf("saving watch filter: %w", err)
		}
		log.Printf("Watch filter: label %q", watchLabel)
	}

	// Create Claude CLI completer for all LLM calls.
	completer := claudecli.NewCLICompleter()

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

	p := poller.New(store, project, completer, publisher)
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
