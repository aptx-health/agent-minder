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
	"github.com/aptx-health/agent-minder/internal/logresolve"
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
  minder logs -g spike         # filter to spike jobs
  minder logs -g 529           # find issue #529 or PR #529
  minder logs --remote :7749   # stream from remote daemon
  minder logs #42 --stage review          # a specific stage's latest run
  minder logs #42 --stage review --attempt 2  # a specific retry`,
	Args: cobra.MaximumNArgs(1),
	RunE: runLogs,
}

var (
	flagLogsRepo    string
	flagLogsJob     int64
	flagLogsFollow  bool
	flagLogsRemote  string
	flagLogsKey     string
	flagLogsRaw     bool
	flagLogsGrep    string
	flagLogsStage   string
	flagLogsAttempt int
)

func init() {
	rootCmd.AddCommand(logsCmd)
	logsCmd.Flags().StringVar(&flagLogsRepo, "repo", ".", "Repository directory")
	logsCmd.Flags().Int64Var(&flagLogsJob, "job", 0, "Job ID (skip picker)")
	logsCmd.Flags().BoolVarP(&flagLogsFollow, "follow", "f", false, "Follow log output (like tail -f)")
	logsCmd.Flags().StringVar(&flagLogsRemote, "remote", "", "Remote daemon address (host:port)")
	logsCmd.Flags().StringVar(&flagLogsKey, "api-key", "", "API key for remote access")
	logsCmd.Flags().BoolVar(&flagLogsRaw, "raw", false, "Output raw stream-json (no formatting)")
	logsCmd.Flags().StringVarP(&flagLogsGrep, "grep", "g", "", "Filter jobs by substring (matches issue, agent, title, PR, status)")
	logsCmd.Flags().StringVar(&flagLogsStage, "stage", "", "Show the log for a specific pipeline stage (default: latest)")
	logsCmd.Flags().IntVar(&flagLogsAttempt, "attempt", 0, "Show the log for a specific attempt of --stage (default: latest)")
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

	detectRepoRename(store, repoDir, owner, repo)

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

		candidates = picker.FilterJobs(candidates, flagLogsGrep)

		if len(candidates) == 0 {
			fmt.Println("No matching jobs found for this repository.")
			return nil
		}

		job, err = picker.PickJob(candidates, fmt.Sprintf("Select a job (%s/%s)", owner, repo))
		if err != nil {
			return err
		}
	}

	sel := logresolve.Selector{Stage: flagLogsStage, Attempt: flagLogsAttempt}
	logPath, _, err := logresolve.Resolve(store, job, sel)
	if err != nil || !fileExists(logPath) {
		if sel.IsZero() {
			return fmt.Errorf("log not found for job %s (deploy %s)", job.Name, job.DeploymentID)
		}
		return fmt.Errorf("log not found for job %s stage=%q attempt=%d", job.Name, sel.Stage, sel.Attempt)
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
		jobs = picker.FilterRemoteJobs(jobs, flagLogsGrep)
		if len(jobs) == 0 {
			fmt.Println("No matching jobs found.")
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
	return streamLogTo(os.Stdout, r, raw)
}

func streamLogTo(out io.Writer, r io.Reader, raw bool) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()

		if raw {
			writeLogLine(out, string(line))
			continue
		}

		var evt formattedLogEvent

		if json.Unmarshal(line, &evt) != nil {
			// Non-JSON line (e.g., "--- REVIEW AGENT ---").
			writeLogLine(out, string(line))
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
					writeLogf(out, "\033[36m> %s\033[0m %s\n", block.Name, input)
				case "text":
					if block.Text != "" {
						writeLogf(out, "\033[33m%s\033[0m\n", block.Text)
					}
				}
			}

		case "result":
			writeLogLine(out)
			if evt.IsError {
				writeLogf(out, "\033[31m--- Result (error) ---\033[0m\n")
			} else {
				writeLogf(out, "\033[32m--- Result ---\033[0m\n")
			}
			if evt.Result != "" {
				result := evt.Result
				if len(result) > 500 {
					result = result[:497] + "..."
				}
				writeLogLine(out, result)
			}
			writeLogf(out, "Turns: %d  Cost: $%.2f\n", evt.NumTurns, evt.TotalCost)

		case "system":
			if evt.Error != "" {
				writeLogf(out, "\033[31m[system] %s: %s\033[0m\n", evt.Subtype, evt.Error)
			}

		case "thread.started":
			if evt.ThreadID != "" {
				writeLogf(out, "\033[90mSession: %s\033[0m\n", evt.ThreadID)
			}

		case "turn.started":
			writeLogLine(out, "\033[90m--- Turn started ---\033[0m")

		case "turn.completed":
			writeLogLine(out)
			writeLogLine(out, "\033[32m--- Turn complete ---\033[0m")
			if evt.Usage != nil {
				writeLogf(out, "Tokens: input=%d cached=%d output=%d reasoning=%d\n",
					evt.Usage.InputTokens,
					evt.Usage.CachedInputTokens,
					evt.Usage.OutputTokens,
					evt.Usage.ReasoningOutputTokens,
				)
			}

		case "turn.failed", "error":
			msg := evt.Error
			if msg == "" {
				msg = evt.Result
			}
			if msg == "" && evt.Item != nil {
				msg = codexLogItemError(evt.Item.Error)
			}
			writeLogf(out, "\033[31m[%s] %s\033[0m\n", evt.Type, msg)

		case "item.started":
			formatCodexLogItem(out, "started", evt.Item)

		case "item.completed":
			formatCodexLogItem(out, "completed", evt.Item)

		case "response_item":
			formatCodexLogItem(out, "response", evt.Item)

		default:
			// Skip other event types (tool_result, etc.)
		}
	}

	return scanner.Err()
}

type formattedLogEvent struct {
	Type      string         `json:"type"`
	Subtype   string         `json:"subtype,omitempty"`
	ThreadID  string         `json:"thread_id,omitempty"`
	Message   *claudeLogMsg  `json:"message,omitempty"`
	NumTurns  int            `json:"num_turns,omitempty"`
	TotalCost float64        `json:"total_cost_usd,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`
	Result    string         `json:"result,omitempty"`
	Error     string         `json:"error,omitempty"`
	Item      *codexLogItem  `json:"item,omitempty"`
	Usage     *codexLogUsage `json:"usage,omitempty"`
}

type claudeLogMsg struct {
	Content []claudeLogContent `json:"content"`
}

type claudeLogContent struct {
	Type  string          `json:"type"`
	Name  string          `json:"name,omitempty"`
	Text  string          `json:"text,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type codexLogUsage struct {
	InputTokens           int `json:"input_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	ReasoningOutputTokens int `json:"reasoning_output_tokens"`
}

func formatCodexLogItem(out io.Writer, eventStatus string, item *codexLogItem) {
	if item == nil {
		return
	}
	formatCodexLogItemValue(out, eventStatus, *item)
}

type codexLogItem struct {
	Type             string           `json:"type"`
	Name             string           `json:"name,omitempty"`
	Text             string           `json:"text,omitempty"`
	Command          string           `json:"command,omitempty"`
	AggregatedOutput string           `json:"aggregated_output,omitempty"`
	ExitCode         *int             `json:"exit_code,omitempty"`
	Status           string           `json:"status,omitempty"`
	Input            json.RawMessage  `json:"input,omitempty"`
	Arguments        json.RawMessage  `json:"arguments,omitempty"`
	Error            json.RawMessage  `json:"error,omitempty"`
	Changes          []codexLogChange `json:"changes,omitempty"`
}

type codexLogChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

func formatCodexLogItemValue(out io.Writer, eventStatus string, item codexLogItem) {
	switch item.Type {
	case "agent_message":
		if item.Text != "" {
			writeLogf(out, "\033[33m%s\033[0m\n", item.Text)
		}

	case "command_execution":
		if eventStatus == "started" {
			writeLogf(out, "\033[36m> %s\033[0m\n", item.Command)
			return
		}
		if item.AggregatedOutput != "" {
			writeLog(out, item.AggregatedOutput)
			if !strings.HasSuffix(item.AggregatedOutput, "\n") {
				writeLogLine(out)
			}
		}
		if item.ExitCode != nil && *item.ExitCode != 0 {
			writeLogf(out, "\033[31mexit code %d\033[0m\n", *item.ExitCode)
		}

	case "file_change":
		if eventStatus == "started" {
			writeLogLine(out, "\033[36m> file_change\033[0m")
		}
		if eventStatus == "completed" {
			for _, change := range item.Changes {
				writeLogf(out, "\033[36m%s\033[0m %s\n", change.Kind, change.Path)
			}
		}

	case "function_call", "custom_tool_call", "mcp_tool_call":
		name := item.Name
		if name == "" {
			name = item.Type
		}
		writeLogf(out, "\033[36m> %s\033[0m %s\n", name, codexLogInputSummary(item, 120))

	case "function_call_output", "custom_tool_call_output":
		if item.Text != "" {
			writeLogLine(out, item.Text)
		}

	case "error":
		msg := item.Text
		if msg == "" {
			msg = codexLogItemError(item.Error)
		}
		writeLogf(out, "\033[31m[error] %s\033[0m\n", msg)
	}
}

func writeLog(out io.Writer, text string) {
	_, _ = fmt.Fprint(out, text)
}

func writeLogLine(out io.Writer, args ...any) {
	_, _ = fmt.Fprintln(out, args...)
}

func writeLogf(out io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(out, format, args...)
}

func codexLogInputSummary(item codexLogItem, maxLen int) string {
	for _, raw := range []json.RawMessage{item.Input, item.Arguments} {
		if len(raw) > 0 {
			return truncateLogStr(string(raw), maxLen)
		}
	}
	if item.Command != "" {
		return truncateLogStr(item.Command, maxLen)
	}
	return ""
}

func codexLogItemError(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var obj struct {
		Message string `json:"message"`
		Code    string `json:"code"`
		Type    string `json:"type"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		switch {
		case obj.Message != "":
			return obj.Message
		case obj.Code != "":
			return obj.Code
		case obj.Type != "":
			return obj.Type
		}
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

func truncateLogStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
