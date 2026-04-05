package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/aptx-health/agent-minder/internal/discovery"
	gitpkg "github.com/aptx-health/agent-minder/internal/git"
	"github.com/aptx-health/agent-minder/internal/supervisor"
	"github.com/spf13/cobra"
)

var agentsAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Create a new agent definition interactively",
	Long: `Launches an interactive Claude session that interviews you about the new agent,
then creates the agent definition (.claude/agents/<name>.md) and updates
jobs.yaml with the appropriate trigger or schedule.

The session will ask about:
- What the agent should do
- Whether it's reactive (triggered by issues) or proactive (runs on a schedule)
- For reactive: which label triggers it
- For proactive: what cron schedule to use
- Budget and turn limits`,
	RunE: runAgentsAdd,
}

func init() {
	agentsCmd.AddCommand(agentsAddCmd)
}

func runAgentsAdd(cmd *cobra.Command, args []string) error {
	repoDir, err := resolveRepoDir(flagAgentsRepo)
	if err != nil {
		return fmt.Errorf("resolve repo: %w", err)
	}

	if !gitpkg.IsRepo(repoDir) {
		return fmt.Errorf("%s is not a git repository", repoDir)
	}

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude CLI not found — install from https://claude.ai/code")
	}

	// Gather context for the prompt.
	info, err := discovery.ScanRepo(repoDir)
	if err != nil {
		return fmt.Errorf("scan repo: %w", err)
	}

	existing := supervisor.DiscoverAgents(repoDir)
	var existingList []string
	for _, a := range existing {
		existingList = append(existingList, fmt.Sprintf("- %s (%s, %s)", a.Name, a.Contract.Mode, a.Contract.Output))
	}

	// Build template reference.
	var templateRef strings.Builder
	for _, tmpl := range supervisor.AgentTemplates() {
		fmt.Fprintf(&templateRef, "\n### %s\n```yaml\n---\n%s\n---\n```\n",
			tmpl.Name, tmpl.Frontmatter)
	}

	systemPrompt := buildAgentAddPrompt(info, repoDir, existingList, templateRef.String())

	agentCmd := exec.Command(claudePath, "--append-system-prompt", systemPrompt,
		"I'd like to add a new agent to this repository.")
	agentCmd.Dir = repoDir
	agentCmd.Stdin = os.Stdin
	agentCmd.Stdout = os.Stdout
	agentCmd.Stderr = os.Stderr

	fmt.Println("Launching agent creation session...")
	fmt.Println("The agent will interview you and create the agent definition + jobs.yaml entry.")

	if err := agentCmd.Run(); err != nil {
		fmt.Printf("\nSession exited with error: %v\n", err)
		return nil
	}

	// Validate.
	if errs := supervisor.ValidateAgentDefs(repoDir); len(errs) > 0 {
		fmt.Println("\nAgent definition validation errors:")
		for _, e := range errs {
			fmt.Printf("  - %s\n", e)
		}
	} else {
		fmt.Println("\nAll agent definitions validated successfully.")
	}

	return nil
}

func buildAgentAddPrompt(info *discovery.RepoInfo, repoDir string, existing []string, templateRef string) string {
	return fmt.Sprintf(`You are an agent creation assistant for agent-minder. Your job is to interview
the user about a new agent they want to add, then create the agent definition file
and update jobs.yaml.

Repository: %s
Languages: %v

## Existing agents
%s

## Interview process

### 1. Understand the agent
Ask the user:
- What should this agent do? (one sentence)
- Give it a name (lowercase, hyphenated — e.g., "api-tester", "changelog-writer")

### 2. Determine mode and output
Based on the description, determine:
- **Mode**: Is this reactive (triggered by issues/labels) or proactive (runs on a schedule)?
  - Reactive: needs an issue to work on, triggered by a label
  - Proactive: runs periodically, scans the repo, creates output on its own
- **Output**: What does this agent produce?
  - "pr" — opens a pull request with changes
  - "issue" — creates or comments on a GitHub issue
  - "comment" — posts findings on the triggering issue (like spike)
  - "none" — no external output (internal tooling)

### 3. Configure trigger or schedule
- For **reactive** agents: ask which GitHub label triggers it
- For **proactive** agents: ask what schedule (suggest sensible defaults based on the task)

### 4. Run a research sub-agent
Before writing the agent definition, run a research agent to analyze the codebase:

  claude -p --model sonnet "Research this codebase for writing an optimized <agent-name> agent definition that <description>. Focus on: relevant code patterns, tools, commands, and conventions. Output ONLY the instruction body markdown — no frontmatter, no preamble."

Read the output and use it as the foundation for the instruction body.

### 5. Create the agent definition
Write the file to .claude/agents/<name>.md with:
- YAML frontmatter with the correct contract fields
- Instruction body based on the research + user's requirements

Use existing agent templates as reference for the frontmatter structure:
%s

CRITICAL: The YAML frontmatter must follow the exact structure shown above.
The orchestrator parses it — incorrect fields will break the agent.

Key frontmatter fields:
- name, description, tools, mode, output
- stages (with name, agent, on_failure, retries)
- context (list of providers: issue, repo_info, file_list, recent_commits:<days>, lessons, sibling_jobs, dep_graph)
- dedup (for proactive: branch_exists, open_pr_with_label:<label>, recent_run:<hours>)

### 6. Update jobs.yaml
Read the existing .agent-minder/jobs.yaml (if it exists) and add an entry for the new agent.

For reactive agents:
  <job-name>:
    trigger: "label:<label>"
    agent: <agent-name>
    description: "<description>"
    budget: 5.0

For proactive agents:
  <job-name>:
    schedule: "<cron expression>"
    agent: <agent-name>
    description: "<description>"
    budget: 3.0

### 7. Validate
- Run: minder agents list --repo .
- Run: minder jobs list --repo .
  If minder is not on PATH, use go run ./cmd/minder.
  Do NOT use Python or Ruby YAML parsers.
- If validation fails, fix and re-validate.

### 8. Summary
Print a summary of what was created:
- Agent definition file path
- Mode and trigger/schedule
- jobs.yaml entry

Be concise and efficient.`, repoDir, info.Inventory.Languages, strings.Join(existing, "\n"), templateRef)
}
