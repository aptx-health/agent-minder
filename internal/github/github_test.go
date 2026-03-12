package github

import "testing"

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
