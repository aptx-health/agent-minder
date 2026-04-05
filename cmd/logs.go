package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/aptx-health/agent-minder/internal/daemon"
	"github.com/aptx-health/agent-minder/internal/db"
	"github.com/aptx-health/agent-minder/internal/picker"
	"github.com/spf13/cobra"
)

var logsCmd = &cobra.Command{
	Use:   "logs [issue-or-job-id]",
	Short: "View an agent's log output",
	Long: `View the stream-json log from an agent run.

If no argument is given, presents an interactive picker of jobs
for the current repository.

In remote mode (--remote), streams the log from the daemon's HTTP API.

Examples:
  minder logs                  # interactive picker
  minder logs #42              # most recent job for issue 42
  minder logs --job 7          # by job ID
  minder logs --follow         # tail a running job
  minder logs --remote :7749   # stream from remote daemon`,
	Args: cobra.MaximumNArgs(1),
	RunE: runLogs,
}

var (
	flagLogsRepo   string
	flagLogsJob    int64
	flagLogsFollow bool
	flagLogsRemote string
	flagLogsKey    string
	flagLogsRaw    bool
)

func init() {
	rootCmd.AddCommand(logsCmd)
	logsCmd.Flags().StringVar(&flagLogsRepo, "repo", ".", "Repository directory")
	logsCmd.Flags().Int64Var(&flagLogsJob, "job", 0, "Job ID (skip picker)")
	logsCmd.Flags().BoolVarP(&flagLogsFollow, "follow", "f", false, "Follow log output (like tail -f)")
	logsCmd.Flags().StringVar(&flagLogsRemote, "remote", "", "Remote daemon address (host:port)")
	logsCmd.Flags().StringVar(&flagLogsKey, "api-key", "", "API key for remote access")
	logsCmd.Flags().BoolVar(&flagLogsRaw, "raw", false, "Output raw stream-json (no formatting)")
}

func runLogs(cmd *cobra.Command, args []string) error {
	if flagLogsRemote != "" {
		return runLogsRemote(args)
	}
	return runLogsLocal(args)
}

func runLogsLocal(args []string) error {
	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	store := db.NewStore(conn)
	defer func() { _ = store.Close() }()

	repoDir, err := resolveRepoDir(flagLogsRepo)
	if err != nil {
		return fmt.Errorf("resolve repo: %w", err)
	}

	owner, repo, err := resolveOwnerRepo(repoDir)
	if err != nil {
		return fmt.Errorf("resolve owner/repo: %w", err)
	}

	var job *db.Job

	// Direct job ID.
	if flagLogsJob > 0 {
		job, err = store.GetJob(flagLogsJob)
		if err != nil {
			return fmt.Errorf("job %d not found", flagLogsJob)
		}
	}

	// Issue number from arg.
	if job == nil && len(args) > 0 {
		arg := strings.TrimPrefix(args[0], "#")
		issueNum, parseErr := strconv.Atoi(arg)
		if parseErr != nil {
			return fmt.Errorf("invalid argument %q — use #<issue> or a job ID", args[0])
		}
		jobs, _ := store.GetJobsByRepo(owner, repo)
		var candidates []*db.Job
		for _, j := range jobs {
			if j.IssueNumber == issueNum {
				candidates = append(candidates, j)
			}
		}
		if len(candidates) == 0 {
			return fmt.Errorf("no jobs found for issue #%d", issueNum)
		}
		if len(candidates) > 1 {
			job, err = picker.PickJob(candidates, fmt.Sprintf("Multiple jobs for #%d", issueNum))
			if err != nil {
				return err
			}
		} else {
			job = candidates[0]
		}
	}

	// Interactive picker.
	if job == nil {
		jobs, err := store.GetJobsByRepo(owner, repo)
		if err != nil {
			return fmt.Errorf("list jobs: %w", err)
		}

		var candidates []*db.Job
		for _, j := range jobs {
			if j.Status != db.StatusQueued && j.Status != db.StatusBlocked {
				candidates = append(candidates, j)
			}
		}

		if len(candidates) == 0 {
			fmt.Println("No jobs with logs found for this repository.")
			return nil
		}

		job, err = picker.PickJob(candidates, fmt.Sprintf("Select a job (%s/%s)", owner, repo))
		if err != nil {
			return err
		}
	}

	// Find log path.
	logPath := job.AgentLog.String
	if logPath == "" {
		// Try to construct from conventions.
		logPath = fmt.Sprintf("%s/.agent-minder/agents/%s-%s.log",
			os.Getenv("HOME"), job.DeploymentID, job.Name)
	}

	f, err := os.Open(logPath)
	if err != nil {
		return fmt.Errorf("log not found at %s", logPath)
	}
	defer func() { _ = f.Close() }()

	if job.Status == "running" || job.Status == "reviewing" {
		fmt.Fprintf(os.Stderr, "Agent is %s — showing live output\n\n", job.Status)
	}

	return streamLog(f, flagLogsRaw)
}

func runLogsRemote(args []string) error {
	addr := flagLogsRemote
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	client := daemon.NewClient(addr, flagLogsKey)

	var jobID int64

	if flagLogsJob > 0 {
		jobID = flagLogsJob
	}

	// Issue number from arg.
	if jobID == 0 && len(args) > 0 {
		arg := strings.TrimPrefix(args[0], "#")
		issueNum, parseErr := strconv.Atoi(arg)
		if parseErr != nil {
			return fmt.Errorf("invalid argument %q", args[0])
		}
		jobs, err := client.GetJobs()
		if err != nil {
			return fmt.Errorf("fetch jobs: %w", err)
		}
		var candidates []daemon.JobResponse
		for _, j := range jobs {
			if j.IssueNumber == issueNum {
				candidates = append(candidates, j)
			}
		}
		if len(candidates) == 0 {
			return fmt.Errorf("no jobs found for issue #%d", issueNum)
		}
		if len(candidates) > 1 {
			selected, err := picker.PickRemoteJob(candidates, fmt.Sprintf("Multiple jobs for #%d", issueNum))
			if err != nil {
				return err
			}
			jobID = selected.ID
		} else {
			jobID = candidates[0].ID
		}
	}

	// Interactive picker.
	if jobID == 0 {
		jobs, err := client.GetJobs()
		if err != nil {
			return fmt.Errorf("fetch jobs: %w", err)
		}
		if len(jobs) == 0 {
			fmt.Println("No jobs found.")
			return nil
		}
		selected, err := picker.PickRemoteJob(jobs, "Select a job")
		if err != nil {
			return err
		}
		jobID = selected.ID
	}

	resp, err := client.GetJobLogStream(jobID)
	if err != nil {
		return fmt.Errorf("fetch log: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	return streamLog(resp.Body, flagLogsRaw)
}

// streamLog reads stream-json from r and prints formatted output.
func streamLog(r io.Reader, raw bool) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()

		if raw {
			fmt.Println(string(line))
			continue
		}

		var evt struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype,omitempty"`
			Message *struct {
				Content []struct {
					Type  string          `json:"type"`
					Name  string          `json:"name,omitempty"`
					Text  string          `json:"text,omitempty"`
					Input json.RawMessage `json:"input,omitempty"`
				} `json:"content"`
			} `json:"message,omitempty"`
			NumTurns  int     `json:"num_turns,omitempty"`
			TotalCost float64 `json:"total_cost_usd,omitempty"`
			IsError   bool    `json:"is_error,omitempty"`
			Result    string  `json:"result,omitempty"`
			Error     string  `json:"error,omitempty"`
		}

		if json.Unmarshal(line, &evt) != nil {
			// Non-JSON line (e.g., "--- REVIEW AGENT ---").
			fmt.Println(string(line))
			continue
		}

		switch evt.Type {
		case "assistant":
			if evt.Message == nil {
				continue
			}
			for _, block := range evt.Message.Content {
				switch block.Type {
				case "tool_use":
					input := truncateLogStr(string(block.Input), 120)
					fmt.Printf("\033[36m> %s\033[0m %s\n", block.Name, input)
				case "text":
					if block.Text != "" {
						fmt.Printf("\033[33m%s\033[0m\n", block.Text)
					}
				}
			}

		case "result":
			fmt.Println()
			if evt.IsError {
				fmt.Printf("\033[31m--- Result (error) ---\033[0m\n")
			} else {
				fmt.Printf("\033[32m--- Result ---\033[0m\n")
			}
			if evt.Result != "" {
				result := evt.Result
				if len(result) > 500 {
					result = result[:497] + "..."
				}
				fmt.Println(result)
			}
			fmt.Printf("Turns: %d  Cost: $%.2f\n", evt.NumTurns, evt.TotalCost)

		case "system":
			if evt.Error != "" {
				fmt.Printf("\033[31m[system] %s: %s\033[0m\n", evt.Subtype, evt.Error)
			}

		default:
			// Skip other event types (tool_result, etc.)
		}
	}

	return scanner.Err()
}

func truncateLogStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
