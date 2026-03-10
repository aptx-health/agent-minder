package state

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dustinlange/agent-minder/internal/config"
)

// State represents the parsed minder state file.
type State struct {
	Project        string
	WatchedRepos   []string
	ActiveConcerns []string
	RecentActivity []string
	MonitoringPlan []string
	LastPollTime   string
	LastPollNotes  []string
	Raw            string
}

// Path returns the state file path for a project.
func Path(project string) (string, error) {
	dir, err := config.ProjectDir(project)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.md"), nil
}

// Load reads and parses the state file for a project.
func Load(project string) (*State, error) {
	path, err := Path(project)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &State{Project: project}, nil
		}
		return nil, fmt.Errorf("reading state file: %w", err)
	}

	return Parse(project, string(data)), nil
}

// Save writes the state file for a project.
func Save(project string, content string) error {
	path, err := Path(project)
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating state directory: %w", err)
	}

	return os.WriteFile(path, []byte(content), 0644)
}

// Parse extracts structured data from the markdown state file.
func Parse(project string, raw string) *State {
	s := &State{
		Project: project,
		Raw:     raw,
	}

	sections := splitSections(raw)

	for header, body := range sections {
		items := extractListItems(body)
		lower := strings.ToLower(header)

		switch {
		case strings.Contains(lower, "watched repos"):
			s.WatchedRepos = items
		case strings.Contains(lower, "active concerns"):
			s.ActiveConcerns = items
		case strings.Contains(lower, "recent activity"):
			s.RecentActivity = items
		case strings.Contains(lower, "monitoring plan"):
			s.MonitoringPlan = items
		case strings.Contains(lower, "last poll"):
			s.LastPollNotes = items
			// Try to extract time from the first "Time:" item.
			for _, item := range items {
				if strings.HasPrefix(item, "Time:") {
					s.LastPollTime = strings.TrimSpace(strings.TrimPrefix(item, "Time:"))
				}
			}
		}
	}

	return s
}

// Exists checks if a state file exists for the project.
func Exists(project string) (bool, error) {
	path, err := Path(project)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

var sectionHeader = regexp.MustCompile(`(?m)^##\s+(.+)$`)

// splitSections breaks markdown into header→body pairs.
func splitSections(text string) map[string]string {
	matches := sectionHeader.FindAllStringSubmatchIndex(text, -1)
	result := make(map[string]string)

	for i, match := range matches {
		header := text[match[2]:match[3]]
		bodyStart := match[1]
		var bodyEnd int
		if i+1 < len(matches) {
			bodyEnd = matches[i+1][0]
		} else {
			bodyEnd = len(text)
		}
		result[header] = text[bodyStart:bodyEnd]
	}

	return result
}

// extractListItems pulls "- item" lines from a markdown section body.
func extractListItems(body string) []string {
	var items []string
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") {
			items = append(items, strings.TrimPrefix(trimmed, "- "))
		}
	}
	return items
}
