package controlapi

import (
	"encoding/json"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"
)

func ptr[T any](value T) *T { return &value }

func assertJSONGoldenFile(t *testing.T, path string, value any) {
	t.Helper()
	wantData, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	gotData, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal value: %v", err)
	}
	var got, want any
	if err := json.Unmarshal(gotData, &got); err != nil {
		t.Fatalf("decode actual: %v", err)
	}
	if err := json.Unmarshal(wantData, &want); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		prettyGot, _ := json.MarshalIndent(got, "", "  ")
		prettyWant, _ := json.MarshalIndent(want, "", "  ")
		t.Fatalf("contract changed\n got: %s\nwant: %s", prettyGot, prettyWant)
	}
}

func TestReadContractGolden(t *testing.T) {
	at := Timestamp("2026-08-15T18:34:56.123456789Z")
	snapshot := Snapshot{Watermark: 42, LogEpoch: "epoch-1", Incarnation: "incarnation-1"}
	deployment := Deployment{
		ID: "deploy-1", Owner: "acme", Repository: "widgets", Mode: "issues",
		ActivationPolicy: "explicit", StartedAt: at,
		ConfigRevision: ConfigurationRevision{Available: false, Valid: true},
	}
	model := "gpt-5.6"
	contract := map[string]any{
		"single_resource": NewResourceEnvelope(deployment, snapshot),
		"collection":      NewCollectionEnvelope[Job](nil, snapshot, Page{}),
		"error":           NewErrorEnvelope(ErrorNotFound, "resource was not found"),
		"automation": Automation{
			ID: "cron:daily", DeploymentID: "deploy-1", Kind: "cron", Name: "daily",
			Expression: "0 9 * * *", Labels: []string{},
			Effective: Execution{Agent: "autopilot", Runtime: "codex", Model: &model, MaxTurns: 50, MaxBudgetUSD: 5},
			Enabled:   true,
		},
		"job": Job{
			ID: 7, DeploymentID: "deploy-1", Name: "issue-647", Owner: "acme", Repository: "widgets",
			Provenance: Provenance{}, Lifecycle: JobLifecycle{Status: "queued"},
			Effective: Execution{Agent: "autopilot", Runtime: "codex", MaxTurns: 50, MaxBudgetUSD: 5},
			Failure:   Failure{}, Review: Review{}, Timestamps: JobTimestamps{QueuedAt: &at},
		},
		"agent_run": AgentRun{
			ID: 9, DeploymentID: "deploy-1", JobID: 7, Stage: "implement", Attempt: 1,
			Agent: "autopilot", Activity: RunActivity{StepCount: 3, LastActivityAt: &at},
			FinalUsage: FinalUsage{}, Limits: RunLimits{}, Outcome: RunOutcome{Status: "running"},
			Timestamps: RunTimestamps{StartedAt: &at},
		},
		"deliverable": Deliverable{
			ID: 11, DeploymentID: "deploy-1", JobID: 7, Kind: DeliverableReport,
			PathAvailable: false, Metadata: json.RawMessage(`{"format":"markdown"}`), CreatedAt: at,
		},
		"log": LogDescriptor{
			ID: "job-7-run-9", DeploymentID: "deploy-1", JobID: 7, RunID: 9,
			Stage: "implement", Attempt: 1, Available: true,
		},
		"event": DurableEvent{
			ID: 42, Timestamp: at, DeploymentID: "deploy-1", JobID: ptr(int64(7)),
			Type: EventStarted, Severity: SeverityInfo, Summary: "agent started",
			Data: json.RawMessage(`{"agent":"autopilot"}`),
		},
	}
	assertJSONGoldenFile(t, "testdata/read_contract.golden.json", contract)
}

func TestTimestampAlwaysUTCNano(t *testing.T) {
	zone := time.FixedZone("MDT", -6*60*60)
	got := NewTimestamp(time.Date(2026, 8, 15, 12, 34, 56, 123456789, zone))
	if got != "2026-08-15T18:34:56.123456789Z" {
		t.Fatalf("timestamp = %q", got)
	}
}

type pageFixture struct {
	ID   int
	Name string
}

func TestPaginationContract(t *testing.T) {
	params, err := ParsePagination(url.Values{})
	if err != nil || params.Limit != DefaultLimit {
		t.Fatalf("default pagination = %#v, %v", params, err)
	}
	params, err = ParsePagination(url.Values{"limit": {strconv.Itoa(MaxLimit)}})
	if err != nil || params.Limit != MaxLimit {
		t.Fatalf("max pagination = %#v, %v", params, err)
	}
	for _, raw := range []string{"0", "201", "nope"} {
		_, err := ParsePagination(url.Values{"limit": {raw}})
		var contractErr *ContractError
		if !reflect.TypeOf(err).AssignableTo(reflect.TypeOf(contractErr)) || err.(*ContractError).Code != ErrorInvalidLimit {
			t.Fatalf("limit %q error = %T %v", raw, err, err)
		}
	}
	if _, _, err := Paginate([]pageFixture{{1, "a"}}, EndpointJobs, Pagination{Limit: MaxLimit + 1},
		func(a, b pageFixture) bool { return a.ID < b.ID },
		func(item pageFixture) CursorPosition { return CursorPosition{SortKey: "1", FinalID: "1"} }); err == nil {
		t.Fatal("direct pagination accepted a limit above the maximum")
	}

	items := []pageFixture{{3, "c"}, {1, "a"}, {4, "d"}, {2, "b"}}
	less := func(a, b pageFixture) bool { return a.ID < b.ID }
	position := func(item pageFixture) CursorPosition {
		id := strconv.Itoa(item.ID)
		return CursorPosition{SortKey: id, FinalID: id}
	}
	first, page, err := Paginate(items, EndpointJobs, Pagination{Limit: 2}, less, position)
	if err != nil || len(first) != 2 || first[0].ID != 1 || first[1].ID != 2 || !page.HasMore || page.NextCursor == nil {
		t.Fatalf("first page = %#v, %#v, %v", first, page, err)
	}
	second, final, err := Paginate(items, EndpointJobs, Pagination{Limit: 2, Cursor: *page.NextCursor}, less, position)
	if err != nil || len(second) != 2 || second[0].ID != 3 || second[1].ID != 4 || final.HasMore || final.NextCursor != nil {
		t.Fatalf("second page = %#v, %#v, %v", second, final, err)
	}
	seen := append(first, second...)
	for i, item := range seen {
		if item.ID != i+1 {
			t.Fatalf("duplicate or skipped pagination result: %#v", seen)
		}
	}

	foreign, _ := EncodeCursor(EndpointRuns, CursorPosition{SortKey: "1", FinalID: "1"})
	for _, cursor := range []string{"not-base64", foreign} {
		_, _, err := Paginate(items, EndpointJobs, Pagination{Limit: 2, Cursor: cursor}, less, position)
		if contractErr, ok := err.(*ContractError); !ok || contractErr.Code != ErrorInvalidCursor {
			t.Fatalf("cursor %q error = %T %v", cursor, err, err)
		}
	}
}

func TestEndpointSortOrders(t *testing.T) {
	deployments, _, err := PaginateDeployments([]Deployment{{ID: "z"}, {ID: "a"}}, Pagination{Limit: 50})
	if err != nil || deployments[0].ID != "a" {
		t.Fatalf("deployment order = %#v, %v", deployments, err)
	}
	automations, _, err := PaginateAutomations([]Automation{
		{ID: "trigger:z", Kind: "trigger", Name: "z"},
		{ID: "cron:b", Kind: "cron", Name: "b"},
		{ID: "cron:a", Kind: "cron", Name: "a"},
	}, Pagination{Limit: 50})
	if err != nil || automations[0].ID != "cron:a" || automations[1].ID != "cron:b" || automations[2].ID != "trigger:z" {
		t.Fatalf("automation order = %#v, %v", automations, err)
	}
	jobs, _, err := PaginateJobs([]Job{{ID: 10}, {ID: 2}}, Pagination{Limit: 50})
	if err != nil || jobs[0].ID != 2 || jobs[1].ID != 10 {
		t.Fatalf("job order = %#v, %v", jobs, err)
	}
	runs, _, err := PaginateRuns([]AgentRun{{ID: 10}, {ID: 2}}, Pagination{Limit: 50})
	if err != nil || runs[0].ID != 2 || runs[1].ID != 10 {
		t.Fatalf("run order = %#v, %v", runs, err)
	}
	deliverables, _, err := PaginateDeliverables([]Deliverable{{ID: 10}, {ID: 2}}, Pagination{Limit: 50})
	if err != nil || deliverables[0].ID != 2 || deliverables[1].ID != 10 {
		t.Fatalf("deliverable order = %#v, %v", deliverables, err)
	}
	logs, _, err := PaginateLogs([]LogDescriptor{{ID: "job-7-run-10", RunID: 10}, {ID: "job-7-run-2", RunID: 2}}, Pagination{Limit: 50})
	if err != nil || logs[0].RunID != 2 || logs[1].RunID != 10 {
		t.Fatalf("log order = %#v, %v", logs, err)
	}
}

func TestCapabilityNegotiation(t *testing.T) {
	implemented := ImplementedCapabilities()
	pending := PendingCapabilities()
	if slices.Contains(implemented, CapabilityDeliverables) {
		t.Fatalf("deliverables advertised as implemented: %v", implemented)
	}
	if !slices.Contains(pending, CapabilityDeliverables) {
		t.Fatalf("deliverables not advertised as pending: %v", pending)
	}
}
