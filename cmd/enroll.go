package cmd

import (
	"fmt"
	"os/exec"

	"github.com/dustinlange/agent-minder/internal/config"
	"github.com/dustinlange/agent-minder/internal/discovery"
	"github.com/spf13/cobra"
)

var enrollCmd = &cobra.Command{
	Use:   "enroll <project> <repo-dir>",
	Short: "Add a repo or worktree to an active project",
	Args:  cobra.ExactArgs(2),
	RunE:  runEnroll,
}

func init() {
	rootCmd.AddCommand(enrollCmd)
}

func runEnroll(cmd *cobra.Command, args []string) error {
	project := args[0]
	repoDir := args[1]

	cfg, err := config.Load(project)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// Scan the new directory.
	info, err := discovery.ScanRepo(repoDir)
	if err != nil {
		return fmt.Errorf("scanning %s: %w", repoDir, err)
	}

	// Check if this repo (or a worktree of it) is already enrolled.
	for i, repo := range cfg.Repos {
		if repo.Path == info.Path {
			return fmt.Errorf("repo %s is already enrolled as %q", info.Path, repo.ShortName)
		}
		// Check if it's a worktree of an already-enrolled repo.
		for _, wt := range info.Worktrees {
			if wt.Path == repo.Path {
				// It's a worktree of an existing repo — add it there.
				fmt.Printf("Detected as worktree of %s (branch: %s)\n", repo.ShortName, info.Branch)
				cfg.Repos[i].Worktrees = append(cfg.Repos[i].Worktrees, config.Worktree{
					Path:   info.Path,
					Branch: info.Branch,
				})
				if err := config.Save(cfg); err != nil {
					return fmt.Errorf("saving config: %w", err)
				}
				fmt.Printf("Added worktree %s to %s\n", info.Path, repo.ShortName)
				notifyMinder(cfg, fmt.Sprintf("New worktree enrolled: %s/%s (branch: %s)", repo.ShortName, info.Branch, info.Branch))
				return nil
			}
		}
	}

	// New repo — add it.
	newRepo := config.Repo{
		Path:      info.Path,
		ShortName: info.ShortName,
		Worktrees: info.Worktrees,
	}
	cfg.Repos = append(cfg.Repos, newRepo)

	// Add a topic for it.
	newTopic := cfg.Name + "/" + info.ShortName
	hasTopic := false
	for _, t := range cfg.Topics {
		if t == newTopic {
			hasTopic = true
			break
		}
	}
	if !hasTopic {
		cfg.Topics = append(cfg.Topics, newTopic)
	}

	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	fmt.Printf("Enrolled %s as %q in project %q\n", info.Path, info.ShortName, project)
	fmt.Printf("  Branch: %s\n", info.Branch)
	fmt.Printf("  Topic:  %s\n", newTopic)
	fmt.Printf("  Commits: %d recent\n", len(info.RecentLogs))

	notifyMinder(cfg, fmt.Sprintf("New repo enrolled: %s (%s, branch: %s)", info.ShortName, info.Path, info.Branch))

	return nil
}

// notifyMinder publishes a message to the coord topic if agent-pub is available.
func notifyMinder(cfg *config.Project, message string) {
	coordTopic := cfg.Name + "/coord"
	agentPub, err := exec.LookPath("agent-pub")
	if err != nil {
		return
	}
	cmd := exec.Command(agentPub, coordTopic, message)
	cmd.Env = append(cmd.Environ(), "AGENT_NAME="+cfg.MinderIdentity)
	if err := cmd.Run(); err != nil {
		fmt.Printf("Warning: could not notify on %s: %v\n", coordTopic, err)
	}
}
