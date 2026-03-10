package cmd

import (
	"fmt"
	"time"

	"github.com/dustinlange/agent-minder/internal/claude"
	"github.com/dustinlange/agent-minder/internal/config"
	gitpkg "github.com/dustinlange/agent-minder/internal/git"
	"github.com/dustinlange/agent-minder/internal/msgbus"
	"github.com/dustinlange/agent-minder/internal/prompt"
	"github.com/dustinlange/agent-minder/internal/state"
	"github.com/spf13/cobra"
)

var resumeCmd = &cobra.Command{
	Use:   "resume <project>",
	Short: "Restart monitoring from saved state",
	Args:  cobra.ExactArgs(1),
	RunE:  runResume,
}

func init() {
	rootCmd.AddCommand(resumeCmd)
}

func runResume(cmd *cobra.Command, args []string) error {
	project := args[0]

	if !claude.IsAvailable() {
		return fmt.Errorf("claude CLI not found in PATH")
	}

	cfg, err := config.Load(project)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if cfg.ClaudeSessionID != "" {
		return fmt.Errorf("project %q appears to be already running (session: %s)", project, cfg.ClaudeSessionID)
	}

	st, err := state.Load(project)
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}

	// Determine pause time from state.
	pausedAt := st.LastPollTime
	if pausedAt == "" {
		pausedAt = "unknown"
	}

	// Calculate time since pause.
	var timeSincePause string
	if pausedAt != "unknown" {
		if t, err := time.Parse("2006-01-02 15:04:05 UTC", pausedAt); err == nil {
			timeSincePause = time.Since(t).Round(time.Minute).String()
		}
	}
	if timeSincePause == "" {
		timeSincePause = "unknown"
	}

	// Gather what happened while paused.
	resumeData := &prompt.ResumeData{
		Project:        cfg.Name,
		Identity:       cfg.MinderIdentity,
		StateContent:   st.Raw,
		PausedAt:       pausedAt,
		TimeSincePause: timeSincePause,
	}

	// New commits since last known activity.
	for _, repo := range cfg.Repos {
		entries, err := gitpkg.LogSince(repo.Path, time.Now().Add(-cfg.MessageTTL))
		if err != nil || len(entries) == 0 {
			continue
		}
		resumeData.NewCommits = append(resumeData.NewCommits, prompt.RepoCommits{
			ShortName: repo.ShortName,
			Commits:   entries,
		})
	}

	// New messages.
	dbPath := msgbus.DefaultDBPath()
	client, err := msgbus.Open(dbPath)
	if err == nil {
		defer client.Close()
		resumeData.NewMessages, _ = client.UnreadMessages(cfg.MinderIdentity, cfg.Name)
	}

	promptText, err := prompt.RenderResume(resumeData)
	if err != nil {
		return fmt.Errorf("rendering resume prompt: %w", err)
	}

	fmt.Printf("Resuming minder for project %q...\n", project)

	workDir := ""
	if len(cfg.Repos) > 0 {
		workDir = cfg.Repos[0].Path
	}

	// Start fresh session with resume context (claude --resume requires
	// the session to still be alive, which it likely isn't after pause).
	result, err := claude.Start(promptText, workDir, []string{
		"Bash", "Read", "Write", "Edit", "Glob", "Grep",
	})
	if err != nil {
		return fmt.Errorf("launching claude: %w", err)
	}

	if result.SessionID != "" {
		cfg.ClaudeSessionID = result.SessionID
		if err := config.Save(cfg); err != nil {
			fmt.Printf("Warning: could not save session ID: %v\n", err)
		}
	}

	fmt.Printf("Session resumed: %s\n", result.SessionID)
	if result.Output != "" {
		fmt.Println("\n--- Claude's response ---")
		fmt.Println(result.Output)
	}

	return nil
}
