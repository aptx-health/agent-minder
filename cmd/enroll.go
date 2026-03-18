package cmd

import (
	"fmt"
	"os/exec"

	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/dustinlange/agent-minder/internal/discovery"
	"github.com/spf13/cobra"
)

var enrollCmd = &cobra.Command{
	Use:   "enroll <project> <repo-dir>",
	Short: "Add a repo or worktree to an active project",
	Long: `Add a new repository or worktree to an existing project. The directory
is scanned for git metadata, a topic is created, and the repo is
registered in the project database. A notification is published to
the coordination topic if agent-pub is available.`,
	Example: `  # Add a new repo to an existing project
  agent-minder enroll my-project ~/repos/new-service

  # Add a worktree directory
  agent-minder enroll my-project ~/repos/my-app-feature-branch`,
	Args: cobra.ExactArgs(2),
	RunE: runEnroll,
}

func init() {
	rootCmd.AddCommand(enrollCmd)
}

func runEnroll(cmd *cobra.Command, args []string) error {
	projectName := args[0]
	repoDir := args[1]

	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer func() { _ = conn.Close() }()
	store := db.NewStore(conn)

	project, err := store.GetProject(projectName)
	if err != nil {
		return fmt.Errorf("project %q not found — run 'agent-minder list' to see available projects", projectName)
	}

	// Scan the new directory.
	info, err := discovery.ScanRepo(repoDir)
	if err != nil {
		return fmt.Errorf("scanning %s: %w", repoDir, err)
	}

	// Check if already enrolled.
	repos, _ := store.GetRepos(project.ID)
	for _, repo := range repos {
		if repo.Path == info.Path {
			return fmt.Errorf("repo %s is already enrolled as %q", info.Path, repo.ShortName)
		}
	}

	// Add repo.
	repo := &db.Repo{
		ProjectID: project.ID,
		Path:      info.Path,
		ShortName: info.ShortName,
	}
	if err := store.AddRepo(repo); err != nil {
		return fmt.Errorf("adding repo: %w", err)
	}

	// Add worktrees.
	var wts []db.Worktree
	for _, wt := range info.Worktrees {
		wts = append(wts, db.Worktree{
			Path:   wt.Path,
			Branch: wt.Branch,
		})
	}
	if len(wts) > 0 {
		if err := store.ReplaceWorktrees(repo.ID, wts); err != nil {
			return fmt.Errorf("replacing worktrees: %w", err)
		}
	}

	// Add a topic.
	newTopic := project.Name + "/" + info.ShortName
	if err := store.AddTopic(&db.Topic{ProjectID: project.ID, Name: newTopic}); err != nil {
		return fmt.Errorf("adding topic: %w", err)
	}

	fmt.Printf("Enrolled %s as %q in project %q\n", info.Path, info.ShortName, projectName)
	fmt.Printf("  Branch: %s\n", info.Branch)
	fmt.Printf("  Topic:  %s\n", newTopic)
	fmt.Printf("  Commits: %d recent\n", len(info.RecentLogs))

	notifyCoord(project, fmt.Sprintf("New repo enrolled: %s (%s, branch: %s)", info.ShortName, info.Path, info.Branch))

	return nil
}

// notifyCoord publishes a message to the coord topic if agent-pub is available.
func notifyCoord(project *db.Project, message string) {
	coordTopic := project.Name + "/coord"
	agentPub, err := exec.LookPath("agent-pub")
	if err != nil {
		return
	}
	cmd := exec.Command(agentPub, coordTopic, message)
	cmd.Env = append(cmd.Environ(), "AGENT_NAME="+project.MinderIdentity)
	if err := cmd.Run(); err != nil {
		fmt.Printf("Warning: could not notify on %s: %v\n", coordTopic, err)
	}
}
