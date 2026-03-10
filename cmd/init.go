package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dustinlange/agent-minder/internal/config"
	"github.com/dustinlange/agent-minder/internal/discovery"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init [repo-dir ...]",
	Short: "Bootstrap a new project from one or more repo directories",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
}

func runInit(cmd *cobra.Command, args []string) error {
	reader := bufio.NewReader(os.Stdin)

	// 1. Scan each repo directory.
	fmt.Println("Scanning repositories...")
	var repos []*discovery.RepoInfo
	for _, dir := range args {
		info, err := discovery.ScanRepo(dir)
		if err != nil {
			fmt.Printf("  Warning: skipping %s: %v\n", dir, err)
			continue
		}
		fmt.Printf("  Found: %s (%s, branch: %s, %d recent commits)\n",
			info.Name, info.Path, info.Branch, len(info.RecentLogs))
		repos = append(repos, info)
	}

	if len(repos) == 0 {
		return fmt.Errorf("no valid repositories found")
	}

	// 2. Derive project name.
	suggested := discovery.DeriveProjectName(repos)
	fmt.Printf("\nProject name [%s]: ", suggested)
	projectName := readLine(reader)
	if projectName == "" {
		projectName = suggested
	}

	// Check if project already exists.
	projDir, _ := config.ProjectDir(projectName)
	if _, err := os.Stat(projDir); err == nil {
		fmt.Printf("Warning: project %q already exists at %s\n", projectName, projDir)
		fmt.Print("Overwrite? [y/N]: ")
		answer := readLine(reader)
		if !strings.HasPrefix(strings.ToLower(answer), "y") {
			return fmt.Errorf("aborted")
		}
	}

	// 3. Suggest topics.
	topics := discovery.SuggestTopics(projectName, repos)
	fmt.Printf("\nSuggested topics:\n")
	for _, t := range topics {
		fmt.Printf("  - %s\n", t)
	}
	fmt.Print("Accept? [Y/n]: ")
	answer := readLine(reader)
	if strings.HasPrefix(strings.ToLower(answer), "n") {
		fmt.Print("Enter topics (comma-separated): ")
		input := readLine(reader)
		topics = nil
		for _, t := range strings.Split(input, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				topics = append(topics, t)
			}
		}
		// Ensure coord topic exists.
		coordTopic := projectName + "/coord"
		hasCoord := false
		for _, t := range topics {
			if t == coordTopic {
				hasCoord = true
				break
			}
		}
		if !hasCoord {
			topics = append(topics, coordTopic)
			fmt.Printf("  Added coordination topic: %s\n", coordTopic)
		}
	}

	// 4. Configure settings.
	fmt.Printf("\nRefresh interval [5m]: ")
	intervalStr := readLine(reader)
	if intervalStr == "" {
		intervalStr = "5m"
	}

	fmt.Printf("Message TTL [48h]: ")
	ttlStr := readLine(reader)
	if ttlStr == "" {
		ttlStr = "48h"
	}

	fmt.Printf("Auto-enroll new worktrees? [Y/n]: ")
	autoEnroll := readLine(reader)
	autoEnrollBool := !strings.HasPrefix(strings.ToLower(autoEnroll), "n")

	// 5. Build and save config.
	proj := config.NewProject(projectName)

	interval, err := time.ParseDuration(intervalStr)
	if err != nil {
		return fmt.Errorf("invalid refresh interval %q: %w", intervalStr, err)
	}
	proj.RefreshInterval = interval

	ttl, err := time.ParseDuration(ttlStr)
	if err != nil {
		return fmt.Errorf("invalid message TTL %q: %w", ttlStr, err)
	}
	proj.MessageTTL = ttl

	proj.AutoEnrollWorktrees = autoEnrollBool
	proj.Repos = discovery.BuildRepoConfigs(repos)
	proj.Topics = topics

	if err := config.Save(proj); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	// 6. Write initial state file.
	stateContent := buildInitialState(proj, repos)
	if err := writeInitialState(proj.Name, stateContent); err != nil {
		return fmt.Errorf("writing state: %w", err)
	}

	configDir, _ := config.ProjectDir(projectName)
	fmt.Printf("\nProject %q initialized!\n", projectName)
	fmt.Printf("  Config: %s/config.yaml\n", configDir)
	fmt.Printf("  State:  %s/state.md\n", configDir)
	fmt.Printf("\nNext: run 'agent-minder start %s' to begin monitoring.\n", projectName)

	return nil
}

func buildInitialState(proj *config.Project, repos []*discovery.RepoInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Minder State: %s\n\n", proj.Name)

	b.WriteString("## Watched Repos\n")
	for _, r := range repos {
		desc := r.Name
		if r.Readme != "" {
			for _, line := range strings.Split(r.Readme, "\n") {
				trimmed := strings.TrimSpace(line)
				if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
					desc = r.Name + " — " + trimmed
					break
				}
			}
		}
		fmt.Fprintf(&b, "- %s — %s, branch: %s\n", r.Path, desc, r.Branch)
	}

	b.WriteString("\n## Active Concerns\n")
	b.WriteString("- (none yet — monitoring will populate this)\n")

	b.WriteString("\n## Recent Activity\n")
	for _, r := range repos {
		if len(r.RecentLogs) > 0 {
			entry := r.RecentLogs[0]
			fmt.Fprintf(&b, "- %s: %s (%s)\n", r.Name, entry.Subject, entry.Author)
		}
	}

	b.WriteString("\n## Monitoring Plan\n")
	b.WriteString("- Watch for new messages on project topics\n")
	b.WriteString("- Watch for git activity across all repos\n")
	b.WriteString("- Detect cross-repo conflicts (shared types, schemas, APIs)\n")

	b.WriteString("\n## Last Poll\n")
	b.WriteString("- Time: (not yet started)\n")

	return b.String()
}

func writeInitialState(project string, content string) error {
	dir, err := config.ProjectDir(project)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(dir+"/state.md", []byte(content), 0644)
}

func readLine(reader *bufio.Reader) string {
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}
