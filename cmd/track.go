package cmd

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dustinlange/agent-minder/internal/config"
	"github.com/dustinlange/agent-minder/internal/db"
	ghpkg "github.com/dustinlange/agent-minder/internal/github"
	"github.com/spf13/cobra"
)

var trackCmd = &cobra.Command{
	Use:   "track <project> <owner/repo#number> | <project> <owner/repo> <number> [number...]",
	Short: "Track GitHub issues or PRs",
	Long: `Track one or more GitHub issues or pull requests for a project.
Tracked items appear in the TUI dashboard and status output, and are
included in the LLM analysis context. Requires a GitHub token — run
'agent-minder setup' first if you haven't configured one.

Two reference formats are supported:
  owner/repo#number    — track a single item
  owner/repo N N N     — track multiple items from the same repo`,
	Example: `  # Track a single issue
  agent-minder track my-project octocat/hello-world#42

  # Track multiple issues from the same repo
  agent-minder track my-project octocat/hello-world 42 55 78`,
	Args: cobra.MinimumNArgs(2),
	RunE: runTrack,
}

func init() {
	rootCmd.AddCommand(trackCmd)
}

func runTrack(cmd *cobra.Command, args []string) error {
	projectName := args[0]

	// Parse the two forms:
	//   1. owner/repo#number  (single item)
	//   2. owner/repo 1 2 3   (bulk — repo prefix + numbers)
	var owner, repo string
	var numbers []int

	if strings.Contains(args[1], "#") {
		// Single-item form: owner/repo#number
		o, r, n, err := parseItemRef(args[1])
		if err != nil {
			return err
		}
		owner, repo = o, r
		numbers = []int{n}
	} else {
		// Bulk form: owner/repo followed by numbers
		o, r, err := parseRepoRef(args[1])
		if err != nil {
			return err
		}
		owner, repo = o, r

		if len(args) < 3 {
			return fmt.Errorf("expected issue/PR numbers after %s", args[1])
		}
		for _, arg := range args[2:] {
			n, err := strconv.Atoi(arg)
			if err != nil {
				return fmt.Errorf("invalid issue number %q", arg)
			}
			numbers = append(numbers, n)
		}
	}

	token := config.GetIntegrationToken("github")
	if token == "" {
		return fmt.Errorf("no GitHub token configured — run: agent-minder setup")
	}

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

	gh := ghpkg.NewClient(token)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var errs []string
	for _, number := range numbers {
		status, err := gh.FetchItem(ctx, owner, repo, number)
		if err != nil {
			errs = append(errs, fmt.Sprintf("#%d: %v", number, err))
			continue
		}

		item := &db.TrackedItem{
			ProjectID:     project.ID,
			Source:        "github",
			Owner:         owner,
			Repo:          repo,
			Number:        number,
			ItemType:      status.ItemType,
			Title:         status.Title,
			State:         status.State,
			Labels:        strings.Join(status.Labels, ","),
			LastStatus:    status.CompactStatus(),
			LastCheckedAt: time.Now().UTC().Format(time.RFC3339),
		}

		if err := store.AddTrackedItem(item); err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				errs = append(errs, fmt.Sprintf("#%d: already tracked", number))
			} else {
				errs = append(errs, fmt.Sprintf("#%d: %v", number, err))
			}
			continue
		}

		// Also create an autopilot_task for open issues.
		if status.ItemType == "issue" && status.State == "open" {
			body := ""
			if content, fetchErr := gh.FetchItemContent(ctx, owner, repo, number, "issue"); fetchErr == nil {
				body = content.Body
			}
			task := &db.AutopilotTask{
				ProjectID:    project.ID,
				Owner:        owner,
				Repo:         repo,
				IssueNumber:  number,
				IssueTitle:   status.Title,
				IssueBody:    body,
				Dependencies: "[]",
				Status:       "queued",
			}
			_ = store.CreateAutopilotTask(task) // ignore UNIQUE if task already exists
		}

		fmt.Printf("Tracking %s/%s#%d [%s] %s\n", owner, repo, number, status.CompactStatus(), status.Title)
	}

	if len(errs) > 0 {
		fmt.Println("\nErrors:")
		for _, e := range errs {
			fmt.Printf("  %s\n", e)
		}
	}

	return nil
}

// parseItemRef parses "owner/repo#number" into its components.
func parseItemRef(ref string) (owner, repo string, number int, err error) {
	parts := strings.SplitN(ref, "#", 2)
	if len(parts) != 2 {
		return "", "", 0, fmt.Errorf("invalid reference %q — expected owner/repo#number", ref)
	}

	number, err = strconv.Atoi(parts[1])
	if err != nil {
		return "", "", 0, fmt.Errorf("invalid issue number %q", parts[1])
	}

	owner, repo, err = parseRepoRef(parts[0])
	if err != nil {
		return "", "", 0, fmt.Errorf("invalid reference %q — expected owner/repo#number", ref)
	}

	return owner, repo, number, nil
}

// parseRepoRef parses "owner/repo" into its components.
func parseRepoRef(ref string) (owner, repo string, err error) {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo reference %q — expected owner/repo", ref)
	}
	return parts[0], parts[1], nil
}
