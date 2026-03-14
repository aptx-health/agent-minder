package cmd

import "testing"

func TestParseItemRef(t *testing.T) {
	tests := []struct {
		ref     string
		owner   string
		repo    string
		number  int
		wantErr bool
	}{
		{"octocat/hello-world#42", "octocat", "hello-world", 42, false},
		{"myorg/myrepo#7", "myorg", "myrepo", 7, false},
		{"org/repo#0", "org", "repo", 0, false},
		{"bad", "", "", 0, true},
		{"no-hash/repo", "", "", 0, true},
		{"owner/repo#abc", "", "", 0, true},
		{"/repo#1", "", "", 0, true},
		{"owner/#1", "", "", 0, true},
		{"#1", "", "", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			owner, repo, number, err := parseItemRef(tt.ref)
			if tt.wantErr {
				if err == nil {
					t.Errorf("parseItemRef(%q) expected error", tt.ref)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseItemRef(%q) unexpected error: %v", tt.ref, err)
			}
			if owner != tt.owner || repo != tt.repo || number != tt.number {
				t.Errorf("parseItemRef(%q) = (%q, %q, %d), want (%q, %q, %d)",
					tt.ref, owner, repo, number, tt.owner, tt.repo, tt.number)
			}
		})
	}
}

func TestParseRepoRef(t *testing.T) {
	tests := []struct {
		ref     string
		owner   string
		repo    string
		wantErr bool
	}{
		{"octocat/hello-world", "octocat", "hello-world", false},
		{"myorg/myrepo", "myorg", "myrepo", false},
		{"bad", "", "", true},
		{"/repo", "", "", true},
		{"owner/", "", "", true},
		{"", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			owner, repo, err := parseRepoRef(tt.ref)
			if tt.wantErr {
				if err == nil {
					t.Errorf("parseRepoRef(%q) expected error", tt.ref)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRepoRef(%q) unexpected error: %v", tt.ref, err)
			}
			if owner != tt.owner || repo != tt.repo {
				t.Errorf("parseRepoRef(%q) = (%q, %q), want (%q, %q)",
					tt.ref, owner, repo, tt.owner, tt.repo)
			}
		})
	}
}
