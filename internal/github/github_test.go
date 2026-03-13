package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/go-github/v72/github"
)

func TestCompactStatus(t *testing.T) {
	tests := []struct {
		name   string
		status ItemStatus
		want   string
	}{
		{"merged", ItemStatus{State: "merged"}, "Mrgd"},
		{"closed", ItemStatus{State: "closed"}, "Closd"},
		{"open", ItemStatus{State: "open"}, "Open"},
		{"blocked label", ItemStatus{State: "open", Labels: []string{"blocked"}}, "Blckd"},
		{"in progress", ItemStatus{State: "open", Labels: []string{"in progress"}}, "InProg"},
		{"in-progress", ItemStatus{State: "open", Labels: []string{"bug", "in-progress"}}, "InProg"},
		{"wip", ItemStatus{State: "open", Labels: []string{"WIP"}}, "InProg"},
		{"draft PR", ItemStatus{State: "open", Draft: true}, "Draft"},
		{"approved PR", ItemStatus{State: "open", ReviewState: "approved"}, "Appvd"},
		{"changes requested PR", ItemStatus{State: "open", ReviewState: "changes_requested"}, "ChReq"},
		{"blocked beats draft", ItemStatus{State: "open", Labels: []string{"blocked"}, Draft: true}, "Blckd"},
		{"draft beats approved", ItemStatus{State: "open", Draft: true, ReviewState: "approved"}, "Draft"},
		{"merged takes priority", ItemStatus{State: "merged", Labels: []string{"blocked"}}, "Mrgd"},
		{"closed takes priority over labels", ItemStatus{State: "closed", Labels: []string{"in progress"}}, "Closd"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.status.CompactStatus()
			if got != tt.want {
				t.Errorf("CompactStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestItemContentStruct(t *testing.T) {
	content := &ItemContent{
		Body:     "This is the issue body",
		Comments: []string{"First comment", "Second comment"},
	}

	if content.Body != "This is the issue body" {
		t.Errorf("Body = %q", content.Body)
	}
	if len(content.Comments) != 2 {
		t.Errorf("Comments len = %d, want 2", len(content.Comments))
	}
	if content.Comments[0] != "First comment" {
		t.Errorf("Comments[0] = %q", content.Comments[0])
	}

	// Empty content.
	empty := &ItemContent{}
	if empty.Body != "" {
		t.Error("expected empty body")
	}
	if len(empty.Comments) != 0 {
		t.Error("expected no comments")
	}
}

func TestHasLabel(t *testing.T) {
	labels := []string{"bug", "In Progress", "v2"}

	if !hasLabel(labels, "in progress") {
		t.Error("expected case-insensitive match for 'in progress'")
	}
	if !hasLabel(labels, "BUG") {
		t.Error("expected case-insensitive match for 'BUG'")
	}
	if hasLabel(labels, "feature") {
		t.Error("should not match 'feature'")
	}
}

func TestSearchIssuesQueryConstruction(t *testing.T) {
	tests := []struct {
		name        string
		filterType  FilterType
		filterValue string
		wantQuery   string
	}{
		{
			name:        "label filter",
			filterType:  FilterLabel,
			filterValue: "bug",
			wantQuery:   `repo:myorg/myrepo is:issue is:open label:"bug"`,
		},
		{
			name:        "milestone filter",
			filterType:  FilterMilestone,
			filterValue: "v2.0",
			wantQuery:   `repo:myorg/myrepo is:issue is:open milestone:"v2.0"`,
		},
		{
			name:        "project filter",
			filterType:  FilterProject,
			filterValue: "myorg/3",
			wantQuery:   `repo:myorg/myrepo is:issue is:open project:myorg/3`,
		},
		{
			name:        "assignee filter",
			filterType:  FilterAssignee,
			filterValue: "octocat",
			wantQuery:   `repo:myorg/myrepo is:issue is:open assignee:octocat`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedQuery string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedQuery = r.URL.Query().Get("q")
				result := github.IssuesSearchResult{
					Total:             github.Ptr(0),
					IncompleteResults: github.Ptr(false),
					Issues:            []*github.Issue{},
				}
				json.NewEncoder(w).Encode(result)
			}))
			defer srv.Close()

			srvURL, _ := url.Parse(srv.URL + "/")
			ghClient := github.NewClient(nil)
			ghClient.BaseURL = srvURL
			client := &Client{gh: ghClient}

			_, err := client.SearchIssues(context.Background(), "myorg", "myrepo", tt.filterType, tt.filterValue)
			if err != nil {
				t.Fatalf("SearchIssues: %v", err)
			}
			if capturedQuery != tt.wantQuery {
				t.Errorf("query = %q, want %q", capturedQuery, tt.wantQuery)
			}
		})
	}
}

func TestSearchIssuesResultMapping(t *testing.T) {
	num := 42
	title := "Test issue"
	state := "open"
	labelName := "bug"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := github.IssuesSearchResult{
			Total:             github.Ptr(1),
			IncompleteResults: github.Ptr(false),
			Issues: []*github.Issue{
				{
					Number: &num,
					Title:  &title,
					State:  &state,
					Labels: []*github.Label{{Name: &labelName}},
				},
			},
		}
		json.NewEncoder(w).Encode(result)
	}))
	defer srv.Close()

	srvURL, _ := url.Parse(srv.URL + "/")
	ghClient := github.NewClient(nil)
	ghClient.BaseURL = srvURL
	client := &Client{gh: ghClient}

	result, err := client.SearchIssues(context.Background(), "org", "repo", FilterLabel, "bug")
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if result.TotalCount != 1 {
		t.Errorf("TotalCount = %d, want 1", result.TotalCount)
	}
	if len(result.Items) != 1 {
		t.Fatalf("Items len = %d, want 1", len(result.Items))
	}
	item := result.Items[0]
	if item.Number != 42 {
		t.Errorf("Number = %d, want 42", item.Number)
	}
	if item.Title != "Test issue" {
		t.Errorf("Title = %q", item.Title)
	}
	if item.ItemType != "issue" {
		t.Errorf("ItemType = %q, want issue", item.ItemType)
	}
	if len(item.Labels) != 1 || item.Labels[0] != "bug" {
		t.Errorf("Labels = %v", item.Labels)
	}
}
