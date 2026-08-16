// Package controlapi owns the stable, transport-neutral v1 read contract.
// Database and Coordinator structs are intentionally not JSON-encoded here;
// every public wire shape is an explicit DTO.
package controlapi

import (
	"encoding/json"
	"sort"
	"time"
)

const APIVersion = "v1"
const WorkerMode = "worker"

// Capability is a closed vocabulary advertised by /api/v1/meta. Later read
// and event routes add their capability only when they are implemented.
type Capability string

const (
	CapabilityMeta       Capability = "meta"
	CapabilitySnapshot   Capability = "snapshot"
	CapabilityPagination Capability = "pagination"
)

// ImplementedCapabilities returns a new, sorted slice so callers cannot
// mutate package state and wire output is deterministic.
func ImplementedCapabilities() []Capability {
	capabilities := []Capability{CapabilityMeta, CapabilitySnapshot, CapabilityPagination}
	sort.Slice(capabilities, func(i, j int) bool { return capabilities[i] < capabilities[j] })
	return capabilities
}

// Timestamp is always encoded as an RFC3339Nano UTC string.
type Timestamp string

func NewTimestamp(value time.Time) Timestamp {
	return Timestamp(value.UTC().Format(time.RFC3339Nano))
}

// Snapshot is present in every successful v1 envelope. Watermark and epoch
// describe durable SQLite state; Incarnation scopes advisory live state.
type Snapshot struct {
	Watermark   int64  `json:"watermark"`
	LogEpoch    string `json:"log_epoch"`
	Incarnation string `json:"incarnation"`
}

// Page is present on every collection envelope. NextCursor is JSON null on
// the final page.
type Page struct {
	NextCursor *string `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}

type ResourceEnvelope[T any] struct {
	Data     T        `json:"data"`
	Snapshot Snapshot `json:"snapshot"`
}

type CollectionEnvelope[T any] struct {
	Data     []T      `json:"data"`
	Snapshot Snapshot `json:"snapshot"`
	Page     Page     `json:"page"`
}

func NewResourceEnvelope[T any](data T, snapshot Snapshot) ResourceEnvelope[T] {
	return ResourceEnvelope[T]{Data: data, Snapshot: snapshot}
}

func NewCollectionEnvelope[T any](data []T, snapshot Snapshot, page Page) CollectionEnvelope[T] {
	if data == nil {
		data = make([]T, 0)
	}
	return CollectionEnvelope[T]{Data: data, Snapshot: snapshot, Page: page}
}

type Meta struct {
	APIVersion   string       `json:"api_version"`
	BuildVersion string       `json:"build_version"`
	Mode         string       `json:"mode"`
	Capabilities []Capability `json:"capabilities"`
}

type ConfigurationRevision struct {
	Revision  *string    `json:"revision"`
	LoadedAt  *Timestamp `json:"loaded_at"`
	Available bool       `json:"available"`
	Valid     bool       `json:"valid"`
	Error     *string    `json:"error"`
}

type Deployment struct {
	ID               string                `json:"id"`
	Owner            string                `json:"owner"`
	Repository       string                `json:"repository"`
	Mode             string                `json:"mode"`
	ActivationPolicy string                `json:"activation_policy"`
	StartedAt        Timestamp             `json:"started_at"`
	ConfigRevision   ConfigurationRevision `json:"config_revision"`
}

type Execution struct {
	Agent        string  `json:"agent"`
	Runtime      string  `json:"runtime"`
	Model        *string `json:"model"`
	MaxTurns     int     `json:"max_turns"`
	MaxBudgetUSD float64 `json:"max_budget_usd"`
}

type Automation struct {
	ID               string     `json:"id"`
	DeploymentID     string     `json:"deployment_id"`
	Kind             string     `json:"kind"`
	Name             string     `json:"name"`
	Expression       string     `json:"expression"`
	Labels           []string   `json:"labels"`
	Effective        Execution  `json:"effective"`
	Enabled          bool       `json:"enabled"`
	Error            *string    `json:"error"`
	Revision         *string    `json:"revision"`
	LastActivationAt *Timestamp `json:"last_activation_at"`
	LastActivationID *int64     `json:"last_activation_job_id"`
	NextRunAt        *Timestamp `json:"next_run_at"`
}

type Provenance struct {
	Type      *string `json:"type"`
	Name      *string `json:"name"`
	Reference *string `json:"reference"`
}

type JobLifecycle struct {
	Status       string  `json:"status"`
	CurrentStage *string `json:"current_stage"`
}

type Failure struct {
	Reason *string `json:"reason"`
	Detail *string `json:"detail"`
}

type Review struct {
	Risk      *string `json:"risk"`
	CommentID *int64  `json:"comment_id"`
}

type JobTimestamps struct {
	QueuedAt    *Timestamp `json:"queued_at"`
	StartedAt   *Timestamp `json:"started_at"`
	CompletedAt *Timestamp `json:"completed_at"`
}

type Job struct {
	ID           int64         `json:"id"`
	DeploymentID string        `json:"deployment_id"`
	Name         string        `json:"name"`
	IssueNumber  *int          `json:"issue_number"`
	IssueTitle   *string       `json:"issue_title"`
	Owner        string        `json:"owner"`
	Repository   string        `json:"repository"`
	Provenance   Provenance    `json:"provenance"`
	Lifecycle    JobLifecycle  `json:"lifecycle"`
	Effective    Execution     `json:"effective"`
	CostUSD      float64       `json:"cost_usd"`
	Branch       *string       `json:"branch"`
	PullRequest  *int64        `json:"pull_request"`
	Failure      Failure       `json:"failure"`
	Review       Review        `json:"review"`
	Timestamps   JobTimestamps `json:"timestamps"`
}

type RunActivity struct {
	StepCount      int        `json:"step_count"`
	CurrentTool    *string    `json:"current_tool"`
	ToolInput      *string    `json:"tool_input"`
	LastActivityAt *Timestamp `json:"last_activity_at"`
}

type FinalUsage struct {
	Turns     int     `json:"turns"`
	CostUSD   float64 `json:"cost_usd"`
	FinalText *string `json:"final_text"`
}

type RunLimits struct {
	MaxTurns     *int     `json:"max_turns"`
	MaxBudgetUSD *float64 `json:"max_budget_usd"`
}

type RunOutcome struct {
	Status        string  `json:"status"`
	StopReason    *string `json:"stop_reason"`
	FailureDetail *string `json:"failure_detail"`
}

type RunTimestamps struct {
	StartedAt   *Timestamp `json:"started_at"`
	CompletedAt *Timestamp `json:"completed_at"`
}

type AgentRun struct {
	ID           int64         `json:"id"`
	DeploymentID string        `json:"deployment_id"`
	JobID        int64         `json:"job_id"`
	Stage        string        `json:"stage"`
	Attempt      int           `json:"attempt"`
	Agent        string        `json:"agent"`
	Runtime      *string       `json:"runtime"`
	Model        *string       `json:"model"`
	SessionID    *string       `json:"session_id"`
	Activity     RunActivity   `json:"activity"`
	FinalUsage   FinalUsage    `json:"final_usage"`
	Limits       RunLimits     `json:"limits"`
	Outcome      RunOutcome    `json:"outcome"`
	Timestamps   RunTimestamps `json:"timestamps"`
}

// DeliverableKind is the closed v1 deliverable vocabulary.
type DeliverableKind string

const (
	DeliverablePullRequest  DeliverableKind = "pull_request"
	DeliverableIssue        DeliverableKind = "issue"
	DeliverableComment      DeliverableKind = "comment"
	DeliverableReport       DeliverableKind = "report"
	DeliverableReview       DeliverableKind = "review"
	DeliverableBranch       DeliverableKind = "branch"
	DeliverableWorktree     DeliverableKind = "worktree"
	DeliverableFile         DeliverableKind = "file"
	DeliverableFinalSummary DeliverableKind = "final_summary"
)

type Deliverable struct {
	ID            int64           `json:"id"`
	DeploymentID  string          `json:"deployment_id"`
	JobID         int64           `json:"job_id"`
	RunID         *int64          `json:"run_id"`
	Kind          DeliverableKind `json:"kind"`
	Label         *string         `json:"label"`
	Status        *string         `json:"status"`
	ExternalRef   *string         `json:"external_reference"`
	URL           *string         `json:"url"`
	Path          *string         `json:"path"`
	PathAvailable bool            `json:"path_available"`
	Metadata      json.RawMessage `json:"metadata"`
	CreatedAt     Timestamp       `json:"created_at"`
}

// LogDescriptor addresses a log without disclosing its server-local path.
type LogDescriptor struct {
	ID           string `json:"id"`
	DeploymentID string `json:"deployment_id"`
	JobID        int64  `json:"job_id"`
	RunID        int64  `json:"run_id"`
	Stage        string `json:"stage"`
	Attempt      int    `json:"attempt"`
	Available    bool   `json:"available"`
}

type EventType string

const (
	EventInfo      EventType = "info"
	EventWarning   EventType = "warning"
	EventError     EventType = "error"
	EventStarted   EventType = "started"
	EventCompleted EventType = "completed"
	EventFinished  EventType = "finished"
	EventBailed    EventType = "bailed"
	EventStopped   EventType = "stopped"
	EventWaiting   EventType = "waiting"
	EventManual    EventType = "manual"
	EventSpared    EventType = "spared"
	EventReaped    EventType = "reaped"
)

type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

type DurableEvent struct {
	ID           int64           `json:"id"`
	Timestamp    Timestamp       `json:"timestamp"`
	DeploymentID string          `json:"deployment_id"`
	JobID        *int64          `json:"job_id"`
	RunID        *int64          `json:"run_id"`
	Type         EventType       `json:"type"`
	Severity     Severity        `json:"severity"`
	Summary      string          `json:"summary"`
	Data         json.RawMessage `json:"data"`
}

// EventCursor is the durable integer event ID used by replay/SSE. It is
// intentionally unrelated to opaque collection cursors.
type EventCursor int64
