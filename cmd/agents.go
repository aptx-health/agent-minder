package cmd

import (
	"fmt"
	"strings"

	"github.com/aptx-health/agent-minder/internal/supervisor"
	"github.com/spf13/cobra"
)

var agentsCmd = &cobra.Command{
	Use:   "agents",
	Short: "List and inspect agent definitions",
}

var agentsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all available agent definitions",
	RunE:  runAgentsList,
}

var agentsShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show full agent definition and parsed contract",
	Args:  cobra.ExactArgs(1),
	RunE:  runAgentsShow,
}

var flagAgentsRepo string

func init() {
	rootCmd.AddCommand(agentsCmd)
	agentsCmd.AddCommand(agentsListCmd)
	agentsCmd.AddCommand(agentsShowCmd)

	agentsCmd.PersistentFlags().StringVar(&flagAgentsRepo, "repo", ".", "Repository directory")
}

func runAgentsList(cmd *cobra.Command, args []string) error {
	repoDir, err := resolveRepoDir(flagAgentsRepo)
	if err != nil {
		repoDir = flagAgentsRepo
	}

	agents := supervisor.DiscoverAgents(repoDir)
	if len(agents) == 0 {
		fmt.Println("No agent definitions found.")
		return nil
	}

	fmt.Printf("%-20s %-10s %-12s %-8s %s\n", "NAME", "SOURCE", "MODE", "OUTPUT", "DESCRIPTION")
	fmt.Printf("%-20s %-10s %-12s %-8s %s\n", "----", "------", "----", "------", "-----------")
	for _, a := range agents {
		desc := a.Contract.Description
		if len(desc) > 50 {
			desc = desc[:47] + "..."
		}
		fmt.Printf("%-20s %-10s %-12s %-8s %s\n",
			a.Name, a.Source, a.Contract.Mode, a.Contract.Output, desc)
	}
	return nil
}

func runAgentsShow(cmd *cobra.Command, args []string) error {
	repoDir, err := resolveRepoDir(flagAgentsRepo)
	if err != nil {
		repoDir = flagAgentsRepo
	}

	name := args[0]
	contract, err := supervisor.ResolveContract(repoDir, name)
	if err != nil {
		return fmt.Errorf("agent %q: %w", name, err)
	}

	// Find source info.
	var source, path string
	for _, a := range supervisor.DiscoverAgents(repoDir) {
		if a.Name == name {
			source = a.Source
			path = a.Path
			break
		}
	}
	if source == "" {
		source = "built-in"
	}

	fmt.Printf("Agent: %s\n", contract.Name)
	fmt.Printf("Source: %s\n", source)
	if path != "" {
		fmt.Printf("Path: %s\n", path)
	}
	if contract.Description != "" {
		fmt.Printf("Description: %s\n", contract.Description)
	}
	fmt.Printf("Mode: %s\n", contract.Mode)
	fmt.Printf("Output: %s\n", contract.Output)

	if len(contract.Context) > 0 {
		fmt.Printf("Context: %s\n", strings.Join(contract.Context, ", "))
	}

	if len(contract.Dedup) > 0 {
		fmt.Printf("Dedup: %s\n", strings.Join(contract.Dedup, ", "))
	}

	if contract.Timeout != "" {
		fmt.Printf("Timeout: %s\n", contract.Timeout)
	}

	if len(contract.Stages) > 0 {
		fmt.Println("\nStages:")
		for i, s := range contract.Stages {
			fmt.Printf("  %d. %s (agent: %s, on_failure: %s)\n", i+1, s.Name, s.Agent, s.OnFailure)
		}
	}

	return nil
}
