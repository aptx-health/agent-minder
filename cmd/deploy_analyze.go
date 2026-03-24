package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/dustinlange/agent-minder/internal/api"
	"github.com/spf13/cobra"
)

var deployAnalyzeCmd = &cobra.Command{
	Use:   "analyze",
	Short: "Trigger on-demand LLM analysis of the deployment",
	Long: `Trigger an on-demand LLM analysis poll on a remote deploy daemon.
The analysis gathers current git activity, tracked items, and autopilot state,
then runs the analyzer LLM to produce an intelligent briefing.

Requires --remote (or MINDER_REMOTE) to connect to the daemon.`,
	Example: `  # Trigger analysis and wait for result
  agent-minder deploy analyze --remote vps:7749

  # Just trigger (don't wait)
  agent-minder deploy analyze --remote vps:7749 --no-wait

  # Show recent analysis results without triggering
  agent-minder deploy analyze --remote vps:7749 --history`,
	RunE: runDeployAnalyze,
}

var (
	analyzeNoWait  bool
	analyzeHistory bool
)

func init() {
	deployCmd.AddCommand(deployAnalyzeCmd)
	deployAnalyzeCmd.Flags().BoolVar(&analyzeNoWait, "no-wait", false, "Trigger analysis without waiting for result")
	deployAnalyzeCmd.Flags().BoolVar(&analyzeHistory, "history", false, "Show recent analysis results without triggering a new one")
}

func runDeployAnalyze(_ *cobra.Command, _ []string) error {
	client := remoteClient()
	if client == nil {
		return fmt.Errorf("--remote (or MINDER_REMOTE) required for deploy analyze")
	}

	// History-only mode: just show recent results.
	if analyzeHistory {
		return showAnalysisResults(client)
	}

	// Trigger a new analysis poll.
	if err := client.TriggerPoll(); err != nil {
		if strings.Contains(err.Error(), "409") || strings.Contains(err.Error(), "conflict") {
			fmt.Println("Analysis already in progress — use --history to check results.")
			return nil
		}
		return fmt.Errorf("trigger analysis: %w", err)
	}

	if analyzeNoWait {
		fmt.Println("Analysis triggered. Check results with: deploy analyze --history --remote ...")
		return nil
	}

	// Poll for results — the analysis typically takes 30-60s.
	fmt.Print("Analysis triggered, waiting for result")
	startTime := time.Now()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		fmt.Print(".")
		results, err := client.GetAnalysis(1)
		if err != nil {
			continue
		}
		if len(results) > 0 {
			polledAt, err := time.Parse("2006-01-02 15:04:05", results[0].PolledAt)
			if err == nil && polledAt.After(startTime.Add(-2*time.Second)) {
				fmt.Println()
				printAnalysisResult(results[0])
				return nil
			}
		}
		// Timeout after 3 minutes.
		if time.Since(startTime) > 3*time.Minute {
			fmt.Println()
			fmt.Println("Timed out waiting for analysis. Check results with: deploy analyze --history --remote ...")
			return nil
		}
	}
	return nil
}

func showAnalysisResults(client *api.Client) error {
	results, err := client.GetAnalysis(5)
	if err != nil {
		return fmt.Errorf("get analysis: %w", err)
	}
	if len(results) == 0 {
		fmt.Println("No analysis results yet. Trigger one with: deploy analyze --remote ...")
		return nil
	}

	for i, r := range results {
		if i > 0 {
			fmt.Println(strings.Repeat("─", 60))
		}
		printAnalysisResult(r)
	}
	return nil
}

func printAnalysisResult(r api.AnalysisResponse) {
	fmt.Printf("Analysis at %s\n", r.PolledAt)
	if r.NewCommits > 0 || r.NewMessages > 0 {
		fmt.Printf("  Activity: %d commits, %d messages\n", r.NewCommits, r.NewMessages)
	}
	if r.Analysis != "" {
		fmt.Println()
		fmt.Println(r.Analysis)
	} else {
		fmt.Println("  (no analysis text)")
	}
	fmt.Println()
}
