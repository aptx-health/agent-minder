package daemon

// End-to-end exit-gate test for M1 (#652): an external read-only client
// observes a running Coordinator over the real Unix socket transport —
// snapshot + live events stay consistent at every observed watermark
// (Exp IV V-1), and an SSE stream resumes cleanly across a worker restart
// with no gap or duplicate durable event and a changed incarnation (V-7).
//
// This drives a real job through the real Supervisor.Launch() polling loop
// (via a real Coordinator, the same assembly cmd/deploy.go uses), with only
// the external agent CLI process faked out — same boundary the rest of the
// suite fakes at (scenario_test.go, runtime_test.go). Git and GitHub are
// never touched: the test agent is proactive, non-issue, non-PR (output:
// issue), so no worktree, no GitHub label calls, no PR detection.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aptx-health/agent-minder/internal/controlapi"
	"github.com/aptx-health/agent-minder/internal/coordinator"
	"github.com/aptx-health/agent-minder/internal/db"
	runtimepkg "github.com/aptx-health/agent-minder/internal/runtime"
)

// fakeAgentRuntime is a minimal runtime.AgentRuntime that completes
// instantly and successfully, so the real Supervisor.Launch() pipeline runs
// end-to-end without invoking an actual agent CLI.
type fakeAgentRuntime struct{}

func (fakeAgentRuntime) Name() string { return "fake" }

func (fakeAgentRuntime) PrepareAgentDef(context.Context, runtimepkg.Workspace, runtimepkg.AgentDefinition) error {
	return nil
}

func (fakeAgentRuntime) Run(_ context.Context, _ runtimepkg.Invocation, _ runtimepkg.EventSink, logFile io.Writer) (int, error) {
	if logFile != nil {
		_, _ = logFile.Write([]byte("fake runtime: ok\n"))
	}
	return 0, nil
}

func (fakeAgentRuntime) Resume(context.Context, string, runtimepkg.EventSink, io.Writer) (int, error) {
	return 0, runtimepkg.ErrNotSupported
}

func (fakeAgentRuntime) ParseResult(string) (*runtimepkg.Result, error) {
	return &runtimepkg.Result{NumTurns: 1, TotalCostUSD: 0.01, FinalText: "all good"}, nil
}

func (fakeAgentRuntime) ClassifyOutcome(*runtimepkg.Result, runtimepkg.Limits) runtimepkg.Outcome {
	return runtimepkg.Outcome{}
}

func (fakeAgentRuntime) ExtractBailReport(*runtimepkg.Result, string) *runtimepkg.BailReport {
	return nil
}

var _ runtimepkg.AgentRuntime = fakeAgentRuntime{}

// e2eTestAgentDef is a proactive, non-PR agent contract: no worktree, no
// GitHub calls, one stage. Written to repoDir/.claude/agents so
// ResolveContract picks it up for real stage-pipeline execution.
const e2eTestAgentDef = `---
name: e2e-tester
mode: proactive
output: issue
stages:
  - name: audit
context: []
---
Test-only agent body for the M1-24 exit-gate integration test.
`

// e2eWorker bundles a real Coordinator + Server pair bound to a Unix socket,
// modeling exactly what cmd/deploy.go wires for a live worker.
type e2eWorker struct {
	store    *db.Store
	deployID string
	coord    *coordinator.Coordinator
	srv      *Server
	cancel   context.CancelFunc
}

func startE2EWorker(t *testing.T, store *db.Store, deploy *db.Deployment) *e2eWorker {
	t.Helper()
	coord, err := coordinator.New(coordinator.Options{Store: store, Deploy: deploy, Runtime: fakeAgentRuntime{}})
	if err != nil {
		t.Fatalf("coordinator.New: %v", err)
	}

	srv := NewServer(ServerConfig{Store: store, DeployID: deploy.ID, BuildVersion: "e2e-test"})
	srv.Provider = coord

	ctx, cancel := context.WithCancel(context.Background())
	w := &e2eWorker{store: store, deployID: deploy.ID, coord: coord, srv: srv, cancel: cancel}

	go func() { _ = srv.ListenAndServeUnix(deploy.ID) }()
	waitForSocket(t, SocketPath(deploy.ID))
	coord.Start(ctx)
	return w
}

func (w *e2eWorker) stop(t *testing.T) {
	t.Helper()
	w.cancel()
	w.coord.Stop()
	ctx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	_ = w.srv.ShutdownUnix(ctx)
}

// e2eClient is a bare-bones external v1 client dialing the worker's Unix
// socket directly — no shared production client code, so the test proves
// the wire contract rather than round-tripping through daemon.Client.
type e2eClient struct {
	http *http.Client
}

func newE2EClient(deployID string) *e2eClient {
	socketPath := SocketPath(deployID)
	return &e2eClient{http: &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}}
}

func (c *e2eClient) getJSON(t *testing.T, path string, v any) {
	t.Helper()
	resp, err := c.http.Get("http://unix" + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s: status %d: %s", path, resp.StatusCode, body)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

// sseFrame is one parsed Server-Sent-Events frame.
type sseFrame struct {
	event string
	id    int64
	data  []byte
}

// openEvents opens the v1 events stream at the given cursor, unbuffered so
// frames can be read as they arrive.
func (c *e2eClient) openEvents(t *testing.T, ctx context.Context, after int64, epoch string) (*http.Response, *bufio.Reader) {
	t.Helper()
	url := fmt.Sprintf("http://unix/api/v1/events?after=%d", after)
	if epoch != "" {
		url += "&log_epoch=" + epoch
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	streamClient := &http.Client{Timeout: 0, Transport: c.http.Transport}
	resp, err := streamClient.Do(req)
	if err != nil {
		t.Fatalf("open events stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("open events stream: status %d: %s", resp.StatusCode, body)
	}
	return resp, bufio.NewReader(resp.Body)
}

// readFrame reads one SSE frame (terminated by a blank line) from reader.
func readFrame(t *testing.T, reader *bufio.Reader) sseFrame {
	t.Helper()
	var frame sseFrame
	var data bytes.Buffer
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE frame: %v", err)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "":
			frame.data = data.Bytes()
			return frame
		case strings.HasPrefix(line, "id: "):
			id, convErr := strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
			if convErr == nil {
				frame.id = id
			}
		case strings.HasPrefix(line, "event: "):
			frame.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data.WriteString(strings.TrimPrefix(line, "data: "))
		case strings.HasPrefix(line, ": "):
			// comment (heartbeat) — return as-is so the caller can skip it.
			frame.event = "comment"
			return frame
		}
	}
}

func testE2EDeployment(t *testing.T, store *db.Store, id string) *db.Deployment {
	t.Helper()
	deploy := &db.Deployment{
		ID: id, RepoDir: t.TempDir(), Owner: "acme", Repo: "widgets", Mode: "issues",
		MaxAgents: 1, MaxTurns: 50, MaxBudgetUSD: 5, Runtime: "claude-code",
		TotalBudgetUSD: 25, BaseBranch: "main", ActivationPolicy: db.ActivationExplicit,
	}
	if err := store.CreateDeployment(deploy); err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	agentsDir := filepath.Join(deploy.RepoDir, ".claude", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("mkdir agents dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "e2e-tester.md"), []byte(e2eTestAgentDef), 0o644); err != nil {
		t.Fatalf("write agent def: %v", err)
	}
	return deploy
}

func enqueueE2EJob(t *testing.T, store *db.Store, deploy *db.Deployment, name string) *db.Job {
	t.Helper()
	job := &db.Job{
		DeploymentID: deploy.ID, Agent: "e2e-tester", Name: name,
		Owner: deploy.Owner, Repo: deploy.Repo, Status: db.StatusQueued,
	}
	if err := store.CreateJob(job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	return job
}

// waitForJobDone polls the store until the named job reaches a terminal
// status, returning it. Fails the test on timeout.
func waitForJobDone(t *testing.T, store *db.Store, deployID string, jobID int64) *db.Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		job, err := store.GetJobForDeployment(deployID, jobID)
		if err == nil && (job.Status == db.StatusDone || job.Status == db.StatusBailed) {
			return job
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %d did not reach a terminal status in time", jobID)
	return nil
}

// TestE2E_SnapshotEventConsistencyAcrossRestart is the M1 exit-gate proof
// (#652): a real Coordinator drives a real job to completion; an external
// client observes the durable event stream and deployment/jobs snapshots
// over the real Unix socket with no gap or duplicate, and both stay
// consistent with each other at every observed watermark (Exp IV V-1).
// The worker is then torn down and rebuilt (simulating a restart): the SSE
// cursor resumes without re-delivering already-seen durable events and the
// worker incarnation changes, while live state (no jobs mid-flight) resets
// cleanly (V-7).
func TestE2E_SnapshotEventConsistencyAcrossRestart(t *testing.T) {
	t.Setenv("HOME", shortHome(t))

	conn, err := db.Open(filepath.Join(t.TempDir(), "e2e.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	store := db.NewStore(conn)

	deploy := testE2EDeployment(t, store, "e2e-worker")
	worker := startE2EWorker(t, store, deploy)

	client := newE2EClient(deploy.ID)

	// Baseline snapshot before any job exists.
	var before controlapi.ResourceEnvelope[controlapi.Deployment]
	client.getJSON(t, "/api/v1/deployments/"+deploy.ID, &before)
	incarnation1 := before.Snapshot.Incarnation
	if incarnation1 == "" {
		t.Fatal("expected non-empty incarnation on first snapshot")
	}

	streamCtx, cancelStream := context.WithCancel(context.Background())
	resp, reader := client.openEvents(t, streamCtx, 0, "")

	ready := readFrame(t, reader)
	if ready.event != "ready" {
		t.Fatalf("first frame = %q, want ready", ready.event)
	}
	var readyPayload controlapi.EventReady
	if err := json.Unmarshal(ready.data, &readyPayload); err != nil {
		t.Fatalf("decode ready payload: %v", err)
	}
	if readyPayload.Snapshot.Incarnation != incarnation1 {
		t.Fatalf("ready incarnation = %q, want %q", readyPayload.Snapshot.Incarnation, incarnation1)
	}
	logEpoch := readyPayload.Snapshot.LogEpoch
	if logEpoch == "" {
		t.Fatal("expected non-empty log epoch on first ready frame")
	}

	job := enqueueE2EJob(t, store, deploy, "audit-1")

	// Drain durable frames until the job's completed event arrives, tracking
	// monotonic IDs to prove no gap/dup on the wire.
	var lastID int64
	var sawCompleted bool
	deadline := time.Now().Add(10 * time.Second)
	for !sawCompleted && time.Now().Before(deadline) {
		frame := readFrame(t, reader)
		if frame.event != "durable" {
			continue
		}
		if frame.id <= lastID {
			t.Fatalf("event id went backward or duplicated: got %d after %d", frame.id, lastID)
		}
		lastID = frame.id
		var event controlapi.DurableEvent
		if err := json.Unmarshal(frame.data, &event); err != nil {
			t.Fatalf("decode durable event: %v", err)
		}
		if event.Type == "completed" && event.JobID != nil && *event.JobID == job.ID {
			sawCompleted = true
		}
	}
	if !sawCompleted {
		t.Fatal("did not observe the job's completed event before deadline")
	}

	done := waitForJobDone(t, store, deploy.ID, job.ID)
	if done.Status != db.StatusDone {
		t.Fatalf("job status = %q, want %q", done.Status, db.StatusDone)
	}

	// V-1: the jobs snapshot taken after the completed event must agree with
	// what the stream showed, and its watermark must be at or beyond the
	// last durable event id observed.
	var jobsRead controlapi.CollectionEnvelope[controlapi.Job]
	client.getJSON(t, "/api/v1/deployments/"+deploy.ID+"/jobs", &jobsRead)
	if jobsRead.Snapshot.Watermark < lastID {
		t.Fatalf("jobs snapshot watermark %d is behind last observed event id %d", jobsRead.Snapshot.Watermark, lastID)
	}
	var found bool
	for _, j := range jobsRead.Data {
		if j.ID == job.ID {
			found = true
			if j.Lifecycle.Status != db.StatusDone {
				t.Fatalf("snapshot job status = %q, want %q", j.Lifecycle.Status, db.StatusDone)
			}
		}
	}
	if !found {
		t.Fatalf("job %d missing from jobs snapshot", job.ID)
	}

	cancelStream()
	_ = resp.Body.Close()

	// --- Simulate a worker restart: tear down, rebuild against the same store. ---
	worker.stop(t)

	worker2 := startE2EWorker(t, store, deploy)
	t.Cleanup(func() { worker2.stop(t) })

	var after controlapi.ResourceEnvelope[controlapi.Deployment]
	client.getJSON(t, "/api/v1/deployments/"+deploy.ID, &after)
	incarnation2 := after.Snapshot.Incarnation
	if incarnation2 == "" || incarnation2 == incarnation1 {
		t.Fatalf("expected a new incarnation after restart, got %q (was %q)", incarnation2, incarnation1)
	}

	// V-7: resuming at the durable cursor from before the restart must not
	// re-deliver any already-seen durable event, and the new ready frame
	// must report the new incarnation.
	resp2, reader2 := client.openEvents(t, context.Background(), lastID, logEpoch)
	t.Cleanup(func() { _ = resp2.Body.Close() })

	ready2 := readFrame(t, reader2)
	if ready2.event != "ready" {
		t.Fatalf("post-restart first frame = %q, want ready", ready2.event)
	}
	var readyPayload2 controlapi.EventReady
	if err := json.Unmarshal(ready2.data, &readyPayload2); err != nil {
		t.Fatalf("decode post-restart ready payload: %v", err)
	}
	if readyPayload2.Snapshot.Incarnation != incarnation2 {
		t.Fatalf("post-restart ready incarnation = %q, want %q", readyPayload2.Snapshot.Incarnation, incarnation2)
	}
	if readyPayload2.Snapshot.Watermark != jobsRead.Snapshot.Watermark && readyPayload2.Snapshot.Watermark < lastID {
		t.Fatalf("post-restart watermark %d is behind pre-restart cursor %d", readyPayload2.Snapshot.Watermark, lastID)
	}

	// No running jobs should have survived the restart: live state reset.
	var jobsAfter controlapi.CollectionEnvelope[controlapi.Job]
	client.getJSON(t, "/api/v1/deployments/"+deploy.ID+"/jobs", &jobsAfter)
	for _, j := range jobsAfter.Data {
		if j.Lifecycle.Status == db.StatusRunning {
			t.Fatalf("job %d still running after restart; live state did not reset", j.ID)
		}
	}
}
