package prompt

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"
	"time"

	"github.com/dustinlange/agent-minder/internal/config"
	gitpkg "github.com/dustinlange/agent-minder/internal/git"
	"github.com/dustinlange/agent-minder/internal/msgbus"
)

//go:embed templates
var templateFS embed.FS

// InitData holds data for the initial monitoring prompt.
type InitData struct {
	Project         string
	Identity        string
	Repos           []RepoContext
	Topics          []string
	RefreshInterval string
	Messages        []msgbus.Message
	Agents          []string
	StateContent    string
	Timestamp       string
}

// PollData holds data for a per-cycle poll prompt.
type PollData struct {
	Project      string
	Identity     string
	NewCommits   []RepoCommits
	NewMessages  []msgbus.Message
	StateContent string
	Timestamp    string
}

// ResumeData holds data for the resume-after-pause prompt.
type ResumeData struct {
	Project       string
	Identity      string
	StateContent  string
	PausedAt      string
	TimeSincePause string
	NewCommits    []RepoCommits
	NewMessages   []msgbus.Message
	Timestamp     string
}

// RepoContext holds repo info for the init prompt.
type RepoContext struct {
	ShortName  string
	Path       string
	Branch     string
	Readme     string
	ClaudeMD   string
	RecentLogs []gitpkg.LogEntry
	Branches   []gitpkg.BranchInfo
	Worktrees  []config.Worktree
}

// RepoCommits holds new commits for a repo since last check.
type RepoCommits struct {
	ShortName string
	Commits   []gitpkg.LogEntry
}

// RenderInit renders the initial monitoring prompt.
func RenderInit(data *InitData) (string, error) {
	data.Timestamp = time.Now().UTC().Format(time.RFC3339)
	return render("templates/init_prompt.md.tmpl", data)
}

// RenderPoll renders the per-cycle poll prompt.
func RenderPoll(data *PollData) (string, error) {
	data.Timestamp = time.Now().UTC().Format(time.RFC3339)
	return render("templates/poll_prompt.md.tmpl", data)
}

// RenderResume renders the resume-after-pause prompt.
func RenderResume(data *ResumeData) (string, error) {
	data.Timestamp = time.Now().UTC().Format(time.RFC3339)
	return render("templates/resume_prompt.md.tmpl", data)
}

func render(name string, data interface{}) (string, error) {
	tmplData, err := templateFS.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("reading template %s: %w", name, err)
	}

	funcMap := template.FuncMap{
		"truncate": func(s string, n int) string {
			if len(s) <= n {
				return s
			}
			return s[:n] + "..."
		},
	}

	tmpl, err := template.New(name).Funcs(funcMap).Parse(string(tmplData))
	if err != nil {
		return "", fmt.Errorf("parsing template %s: %w", name, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing template %s: %w", name, err)
	}

	return buf.String(), nil
}
