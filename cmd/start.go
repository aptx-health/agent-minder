package cmd

import (
	"fmt"
	"time"

	"github.com/dustinlange/agent-minder/internal/claude"
	"github.com/dustinlange/agent-minder/internal/config"
	"github.com/dustinlange/agent-minder/internal/discovery"
	gitpkg "github.com/dustinlange/agent-minder/internal/git"
	"github.com/dustinlange/agent-minder/internal/msgbus"
	"github.com/dustinlange/agent-minder/internal/prompt"
	"github.com/dustinlange/agent-minder/internal/state"
	"github.com/spf13/cobra"
)

var startCmd = &cobra.Command{
	Use:   "start <project>",
	Short: "Launch monitoring loop via Claude CLI",
	Args:  cobra.ExactArgs(1),
	RunE:  runStart,
}

func init() {
	rootCmd.AddCommand(startCmd)
}

func runStart(cmd *cobra.Command, args []string) error {
	project := args[0]

	if !claude.IsAvailable() {
		return fmt.Errorf("claude CLI not found in PATH — install it first")
	}

	cfg, err := config.Load(project)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if cfg.ClaudeSessionID != "" {
		return fmt.Errorf("project %q appears to be already running (session: %s) — use 'pause' first or 'resume' instead", project, cfg.ClaudeSessionID)
	}

	fmt.Printf("Starting minder for project %q...\n", project)

	// Build initial prompt with full repo context.
	initData, err := buildInitData(cfg)
	if err != nil {
		return fmt.Errorf("building init data: %w", err)
	}

	promptText, err := prompt.RenderInit(initData)
	if err != nil {
		return fmt.Errorf("rendering init prompt: %w", err)
	}

	fmt.Printf("Launching Claude session (identity: %s)...\n", cfg.MinderIdentity)

	// Use the first repo as the working directory for claude.
	workDir := ""
	if len(cfg.Repos) > 0 {
		workDir = cfg.Repos[0].Path
	}

	result, err := claude.Start(promptText, workDir, []string{
		"Bash", "Read", "Write", "Edit", "Glob", "Grep",
	})
	if err != nil {
		return fmt.Errorf("launching claude: %w", err)
	}

	// Save session ID for resume/pause.
	if result.SessionID != "" {
		cfg.ClaudeSessionID = result.SessionID
		if err := config.Save(cfg); err != nil {
			fmt.Printf("Warning: could not save session ID: %v\n", err)
		}
	}

	fmt.Printf("Session started: %s\n", result.SessionID)
	if result.Output != "" {
		fmt.Println("\n--- Claude's initial response ---")
		fmt.Println(result.Output)
	}

	return nil
}

func buildInitData(cfg *config.Project) (*prompt.InitData, error) {
	data := &prompt.InitData{
		Project:         cfg.Name,
		Identity:        cfg.MinderIdentity,
		Topics:          cfg.Topics,
		RefreshInterval: cfg.RefreshInterval.String(),
	}

	// Scan each repo for context.
	for _, repo := range cfg.Repos {
		info, err := discovery.ScanRepo(repo.Path)
		if err != nil {
			fmt.Printf("Warning: could not scan %s: %v\n", repo.Path, err)
			continue
		}

		rc := prompt.RepoContext{
			ShortName:  repo.ShortName,
			Path:       repo.Path,
			Branch:     info.Branch,
			Readme:     info.Readme,
			ClaudeMD:   info.ClaudeMD,
			RecentLogs: info.RecentLogs,
			Worktrees:  repo.Worktrees,
		}

		// Convert branch info.
		for _, b := range info.Branches {
			rc.Branches = append(rc.Branches, gitpkg.BranchInfo{
				Name:      b.Name,
				IsRemote:  b.IsRemote,
				IsCurrent: b.IsCurrent,
			})
		}

		data.Repos = append(data.Repos, rc)
	}

	// Load existing state if available.
	st, err := state.Load(cfg.Name)
	if err == nil && st.Raw != "" {
		data.StateContent = st.Raw
	}

	// Gather messages from the bus.
	dbPath := msgbus.DefaultDBPath()
	client, err := msgbus.Open(dbPath)
	if err == nil {
		defer client.Close()
		data.Messages, _ = client.RecentMessages(cfg.MessageTTL, cfg.Name)
		data.Agents, _ = client.ActiveAgents(24*time.Hour, cfg.Name)
	}

	return data, nil
}
