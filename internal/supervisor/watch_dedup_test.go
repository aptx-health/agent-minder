package supervisor

import (
	"testing"
)

// simulateWatchPoll reproduces the core logic of watchPoll without GitHub I/O.
// Given a set of issues (keyed by which filter found them), routes, and a watch
// filter, it returns the set of (issue, agent) pairs that would be created.
func simulateWatchPoll(
	sup *Supervisor,
	watchFilter string, // e.g. "label:agent-ready"
	watchIssues []testIssue, // issues returned by the --watch filter
	routeIssues map[string][]testIssue, // label filter string → issues returned
	skipLabel string,
) []issueAgentPair {
	type issueAgent struct {
		issue int
		agent string
	}
	knownJobs := make(map[issueAgent]bool)
	var created []issueAgentPair

	// Step 1: --watch filter.
	if watchFilter != "" {
		for _, issue := range watchIssues {
			agent := sup.resolveAgentForIssue(issue.Labels)
			if knownJobs[issueAgent{issue.Number, agent}] || hasLabel(issue.Labels, skipLabel) {
				continue
			}
			knownJobs[issueAgent{issue.Number, agent}] = true
			created = append(created, issueAgentPair{issue.Number, agent})
		}
	}

	// Step 2: trigger routes.
	sup.mu.Lock()
	routes := sup.triggerRoutes
	sup.mu.Unlock()

	for _, route := range routes {
		filterKey := "label:" + joinLabels(route.Labels)
		issues := routeIssues[filterKey]
		for _, issue := range issues {
			if sup.resolveAgentForIssue(issue.Labels) != route.Agent {
				continue
			}
			if knownJobs[issueAgent{issue.Number, route.Agent}] || hasLabel(issue.Labels, skipLabel) {
				continue
			}
			knownJobs[issueAgent{issue.Number, route.Agent}] = true
			created = append(created, issueAgentPair{issue.Number, route.Agent})
		}
	}

	return created
}

func joinLabels(labels []string) string {
	result := ""
	for i, l := range labels {
		if i > 0 {
			result += ","
		}
		result += l
	}
	return result
}

type testIssue struct {
	Number int
	Labels []string
}

type issueAgentPair struct {
	Issue int
	Agent string
}

// --- Tests ---

// TestWatchDedup_ExactScenario reproduces the exact failure: issue #514 with
// labels [agent-ready, ux, bug] spawning 3 agents instead of 1.
func TestWatchDedup_ExactScenario(t *testing.T) {
	store := testStoreForMultiAgent(t)
	deploy := testDeployForMultiAgent(t, store)
	sup := NewTestSupervisor(store, deploy, "/tmp/repo")

	sup.SetTriggerRoutes([]TriggerRoute{
		{Labels: []string{"agent-ready", "ux"}, Agent: "ux-autopilot"},
		{Labels: []string{"bug"}, Agent: "bug-fixer"},
		{Labels: []string{"agent-ready"}, Agent: "autopilot"},
	})

	issue := testIssue{Number: 514, Labels: []string{"agent-ready", "ux", "bug"}}

	created := simulateWatchPoll(sup, "label:agent-ready",
		[]testIssue{issue}, // --watch finds it
		map[string][]testIssue{
			"label:agent-ready,ux": {issue},
			"label:bug":            {issue},
			"label:agent-ready":    {issue},
		},
		"no-agent",
	)

	if len(created) != 1 {
		t.Fatalf("expected 1 job, got %d: %v", len(created), created)
	}
	if created[0].Agent != "ux-autopilot" {
		t.Errorf("expected agent ux-autopilot, got %q", created[0].Agent)
	}
}

// TestWatchDedup_SingleLabelIssue verifies that an issue with only [agent-ready]
// gets exactly one autopilot job.
func TestWatchDedup_SingleLabelIssue(t *testing.T) {
	store := testStoreForMultiAgent(t)
	deploy := testDeployForMultiAgent(t, store)
	sup := NewTestSupervisor(store, deploy, "/tmp/repo")

	sup.SetTriggerRoutes([]TriggerRoute{
		{Labels: []string{"agent-ready", "ux"}, Agent: "ux-autopilot"},
		{Labels: []string{"bug"}, Agent: "bug-fixer"},
		{Labels: []string{"agent-ready"}, Agent: "autopilot"},
	})

	issue := testIssue{Number: 100, Labels: []string{"agent-ready"}}

	created := simulateWatchPoll(sup, "label:agent-ready",
		[]testIssue{issue},
		map[string][]testIssue{
			"label:agent-ready,ux": {}, // not returned (missing ux label)
			"label:bug":            {}, // not returned
			"label:agent-ready":    {issue},
		},
		"no-agent",
	)

	if len(created) != 1 {
		t.Fatalf("expected 1 job, got %d: %v", len(created), created)
	}
	if created[0].Agent != "autopilot" {
		t.Errorf("expected agent autopilot, got %q", created[0].Agent)
	}
}

// TestWatchDedup_BugOnlyIssue verifies that an issue with only [bug]
// gets exactly one bug-fixer job (not discovered by --watch at all).
func TestWatchDedup_BugOnlyIssue(t *testing.T) {
	store := testStoreForMultiAgent(t)
	deploy := testDeployForMultiAgent(t, store)
	sup := NewTestSupervisor(store, deploy, "/tmp/repo")

	sup.SetTriggerRoutes([]TriggerRoute{
		{Labels: []string{"agent-ready", "ux"}, Agent: "ux-autopilot"},
		{Labels: []string{"bug"}, Agent: "bug-fixer"},
		{Labels: []string{"agent-ready"}, Agent: "autopilot"},
	})

	issue := testIssue{Number: 200, Labels: []string{"bug"}}

	created := simulateWatchPoll(sup, "label:agent-ready",
		[]testIssue{}, // --watch doesn't find it (no agent-ready label)
		map[string][]testIssue{
			"label:agent-ready,ux": {},
			"label:bug":            {issue},
			"label:agent-ready":    {},
		},
		"no-agent",
	)

	if len(created) != 1 {
		t.Fatalf("expected 1 job, got %d: %v", len(created), created)
	}
	if created[0].Agent != "bug-fixer" {
		t.Errorf("expected agent bug-fixer, got %q", created[0].Agent)
	}
}

// TestWatchDedup_MultipleIssuesDifferentRoutes verifies that multiple distinct
// issues each get exactly one job from the correct agent.
func TestWatchDedup_MultipleIssuesDifferentRoutes(t *testing.T) {
	store := testStoreForMultiAgent(t)
	deploy := testDeployForMultiAgent(t, store)
	sup := NewTestSupervisor(store, deploy, "/tmp/repo")

	sup.SetTriggerRoutes([]TriggerRoute{
		{Labels: []string{"agent-ready", "ux"}, Agent: "ux-autopilot"},
		{Labels: []string{"bug"}, Agent: "bug-fixer"},
		{Labels: []string{"agent-ready"}, Agent: "autopilot"},
	})

	uxIssue := testIssue{Number: 10, Labels: []string{"agent-ready", "ux"}}
	bugIssue := testIssue{Number: 20, Labels: []string{"bug"}}
	plainIssue := testIssue{Number: 30, Labels: []string{"agent-ready"}}

	created := simulateWatchPoll(sup, "label:agent-ready",
		[]testIssue{uxIssue, plainIssue}, // --watch finds these (both have agent-ready)
		map[string][]testIssue{
			"label:agent-ready,ux": {uxIssue},
			"label:bug":            {bugIssue},
			"label:agent-ready":    {uxIssue, plainIssue},
		},
		"no-agent",
	)

	if len(created) != 3 {
		t.Fatalf("expected 3 jobs, got %d: %v", len(created), created)
	}

	// Build a lookup.
	agentFor := make(map[int]string)
	for _, c := range created {
		agentFor[c.Issue] = c.Agent
	}

	if agentFor[10] != "ux-autopilot" {
		t.Errorf("#10 expected ux-autopilot, got %q", agentFor[10])
	}
	if agentFor[20] != "bug-fixer" {
		t.Errorf("#20 expected bug-fixer, got %q", agentFor[20])
	}
	if agentFor[30] != "autopilot" {
		t.Errorf("#30 expected autopilot, got %q", agentFor[30])
	}
}

// TestWatchDedup_NoWatchFilterOnlyTriggers verifies routing works when there's
// no --watch filter and only trigger routes discover issues.
func TestWatchDedup_NoWatchFilterOnlyTriggers(t *testing.T) {
	store := testStoreForMultiAgent(t)
	deploy := testDeployForMultiAgent(t, store)
	sup := NewTestSupervisor(store, deploy, "/tmp/repo")

	sup.SetTriggerRoutes([]TriggerRoute{
		{Labels: []string{"agent-ready", "ux"}, Agent: "ux-autopilot"},
		{Labels: []string{"agent-ready"}, Agent: "autopilot"},
	})

	issue := testIssue{Number: 42, Labels: []string{"agent-ready", "ux"}}

	created := simulateWatchPoll(sup, "", // no --watch filter
		nil,
		map[string][]testIssue{
			"label:agent-ready,ux": {issue},
			"label:agent-ready":    {issue},
		},
		"no-agent",
	)

	if len(created) != 1 {
		t.Fatalf("expected 1 job, got %d: %v", len(created), created)
	}
	if created[0].Agent != "ux-autopilot" {
		t.Errorf("expected ux-autopilot, got %q", created[0].Agent)
	}
}

// TestWatchDedup_SkipLabelRespected verifies that issues with the skip label
// are never routed to any agent.
func TestWatchDedup_SkipLabelRespected(t *testing.T) {
	store := testStoreForMultiAgent(t)
	deploy := testDeployForMultiAgent(t, store)
	sup := NewTestSupervisor(store, deploy, "/tmp/repo")

	sup.SetTriggerRoutes([]TriggerRoute{
		{Labels: []string{"agent-ready"}, Agent: "autopilot"},
	})

	issue := testIssue{Number: 99, Labels: []string{"agent-ready", "no-agent"}}

	created := simulateWatchPoll(sup, "label:agent-ready",
		[]testIssue{issue},
		map[string][]testIssue{
			"label:agent-ready": {issue},
		},
		"no-agent",
	)

	if len(created) != 0 {
		t.Fatalf("expected 0 jobs (skip label), got %d: %v", len(created), created)
	}
}

// TestWatchDedup_EqualSpecificityStableOrder verifies that routes with equal
// label counts preserve their original ordering (first defined wins).
func TestWatchDedup_EqualSpecificityStableOrder(t *testing.T) {
	store := testStoreForMultiAgent(t)
	deploy := testDeployForMultiAgent(t, store)
	sup := NewTestSupervisor(store, deploy, "/tmp/repo")

	// Both routes have 1 label — bug-fixer is defined first.
	sup.SetTriggerRoutes([]TriggerRoute{
		{Labels: []string{"bug"}, Agent: "bug-fixer"},
		{Labels: []string{"agent-ready"}, Agent: "autopilot"},
	})

	// Issue has BOTH labels. The first-defined 1-label route should win.
	issue := testIssue{Number: 50, Labels: []string{"agent-ready", "bug"}}

	created := simulateWatchPoll(sup, "label:agent-ready",
		[]testIssue{issue},
		map[string][]testIssue{
			"label:bug":         {issue},
			"label:agent-ready": {issue},
		},
		"no-agent",
	)

	if len(created) != 1 {
		t.Fatalf("expected 1 job, got %d: %v", len(created), created)
	}
	if created[0].Agent != "bug-fixer" {
		t.Errorf("expected bug-fixer (first defined), got %q", created[0].Agent)
	}
}

// TestResolveAgentForIssue_Comprehensive tests resolveAgentForIssue against
// many label combinations with a realistic route table.
func TestResolveAgentForIssue_Comprehensive(t *testing.T) {
	store := testStoreForMultiAgent(t)
	deploy := testDeployForMultiAgent(t, store)
	sup := NewTestSupervisor(store, deploy, "/tmp/repo")

	sup.SetTriggerRoutes([]TriggerRoute{
		{Labels: []string{"agent-ready", "ux"}, Agent: "ux-autopilot"},
		{Labels: []string{"bug"}, Agent: "bug-fixer"},
		{Labels: []string{"agent-ready"}, Agent: "autopilot"},
	})

	tests := []struct {
		name   string
		labels []string
		want   string
	}{
		{"agent-ready only", []string{"agent-ready"}, "autopilot"},
		{"agent-ready+ux", []string{"agent-ready", "ux"}, "ux-autopilot"},
		{"agent-ready+ux+bug", []string{"agent-ready", "ux", "bug"}, "ux-autopilot"},
		{"bug only", []string{"bug"}, "bug-fixer"},
		{"agent-ready+bug", []string{"agent-ready", "bug"}, "bug-fixer"},
		{"ux only (no route)", []string{"ux"}, "autopilot"},
		{"no labels", nil, "autopilot"},
		{"unknown label", []string{"enhancement"}, "autopilot"},
		{"case insensitive", []string{"Agent-Ready", "UX"}, "ux-autopilot"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sup.resolveAgentForIssue(tt.labels)
			if got != tt.want {
				t.Errorf("resolveAgentForIssue(%v) = %q, want %q", tt.labels, got, tt.want)
			}
		})
	}
}

// TestSetTriggerRoutes_SortStability verifies that sort is stable — routes with
// equal label counts preserve their original definition order.
func TestSetTriggerRoutes_SortStability(t *testing.T) {
	store := testStoreForMultiAgent(t)
	deploy := testDeployForMultiAgent(t, store)
	sup := NewTestSupervisor(store, deploy, "/tmp/repo")

	// Three 1-label routes followed by a 2-label route.
	sup.SetTriggerRoutes([]TriggerRoute{
		{Labels: []string{"bug"}, Agent: "bug-fixer"},
		{Labels: []string{"spike"}, Agent: "spike"},
		{Labels: []string{"agent-ready"}, Agent: "autopilot"},
		{Labels: []string{"agent-ready", "ux"}, Agent: "ux-autopilot"},
	})

	sup.mu.Lock()
	routes := sup.triggerRoutes
	sup.mu.Unlock()

	// 2-label route should be first.
	if routes[0].Agent != "ux-autopilot" {
		t.Errorf("routes[0] = %q, want ux-autopilot", routes[0].Agent)
	}
	// Remaining 1-label routes should preserve original order.
	if routes[1].Agent != "bug-fixer" {
		t.Errorf("routes[1] = %q, want bug-fixer", routes[1].Agent)
	}
	if routes[2].Agent != "spike" {
		t.Errorf("routes[2] = %q, want spike", routes[2].Agent)
	}
	if routes[3].Agent != "autopilot" {
		t.Errorf("routes[3] = %q, want autopilot", routes[3].Agent)
	}
}
