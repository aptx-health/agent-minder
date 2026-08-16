package controlapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aptx-health/agent-minder/internal/coordinator"
	"github.com/aptx-health/agent-minder/internal/db"
)

var ErrProviderUnavailable = errors.New("control API state provider unavailable")

// Service maps the Coordinator-owned provider boundary into frozen v1 DTOs.
// It never accepts a database Store or reaches into execution-plane internals.
type Service struct {
	Provider     coordinator.StateProvider
	BuildVersion string
}

func (s Service) Meta() (ResourceEnvelope[Meta], error) {
	if s.Provider == nil {
		return ResourceEnvelope[Meta]{}, ErrProviderUnavailable
	}
	marker, err := s.Provider.SnapshotMarker()
	if err != nil {
		return ResourceEnvelope[Meta]{}, err
	}
	return NewResourceEnvelope(Meta{
		APIVersion: APIVersion, BuildVersion: s.BuildVersion,
		Mode: WorkerMode, Capabilities: ImplementedCapabilities(),
		PendingCapabilities: PendingCapabilities(),
	}, s.snapshot(marker)), nil
}

func (s Service) Deployments(params Pagination) (CollectionEnvelope[Deployment], error) {
	if s.Provider == nil {
		return CollectionEnvelope[Deployment]{}, ErrProviderUnavailable
	}
	read, err := s.Provider.ReadDeploymentSnapshot()
	if err != nil {
		return CollectionEnvelope[Deployment]{}, err
	}
	items, page, err := PaginateDeployments([]Deployment{s.deploymentDTO(read.Deployment)}, params)
	if err != nil {
		return CollectionEnvelope[Deployment]{}, err
	}
	return NewCollectionEnvelope(items, s.snapshot(read.Marker), page), nil
}

func (s Service) Deployment(deploymentID string) (ResourceEnvelope[Deployment], error) {
	if err := s.requireDeployment(deploymentID); err != nil {
		return ResourceEnvelope[Deployment]{}, err
	}
	read, err := s.Provider.ReadDeploymentSnapshot()
	if err != nil {
		return ResourceEnvelope[Deployment]{}, err
	}
	return NewResourceEnvelope(s.deploymentDTO(read.Deployment), s.snapshot(read.Marker)), nil
}

func (s Service) Automations(deploymentID string, params Pagination) (CollectionEnvelope[Automation], error) {
	if err := s.requireDeployment(deploymentID); err != nil {
		return CollectionEnvelope[Automation]{}, err
	}
	read, err := s.Provider.ReadAutomationsSnapshot()
	if err != nil {
		return CollectionEnvelope[Automation]{}, err
	}
	items := make([]Automation, 0, len(read.Automations))
	for _, automation := range read.Automations {
		items = append(items, s.automationDTO(automation))
	}
	items, page, err := PaginateAutomations(items, params)
	if err != nil {
		return CollectionEnvelope[Automation]{}, err
	}
	return NewCollectionEnvelope(items, s.snapshot(read.Marker), page), nil
}

func (s Service) Jobs(deploymentID string, params Pagination) (CollectionEnvelope[Job], error) {
	if err := s.requireDeployment(deploymentID); err != nil {
		return CollectionEnvelope[Job]{}, err
	}
	read, err := s.Provider.ReadJobsSnapshot()
	if err != nil {
		return CollectionEnvelope[Job]{}, err
	}
	items := make([]Job, 0, len(read.Jobs))
	for _, job := range read.Jobs {
		items = append(items, jobDTO(job, read.Deployment))
	}
	items, page, err := PaginateJobs(items, params)
	if err != nil {
		return CollectionEnvelope[Job]{}, err
	}
	return NewCollectionEnvelope(items, s.snapshot(read.Marker), page), nil
}

func (s Service) Job(deploymentID string, jobID int64) (ResourceEnvelope[Job], error) {
	if err := s.requireDeployment(deploymentID); err != nil {
		return ResourceEnvelope[Job]{}, err
	}
	read, err := s.Provider.ReadJobSnapshot(jobID)
	if err != nil {
		return ResourceEnvelope[Job]{}, err
	}
	return NewResourceEnvelope(jobDTO(read.Job, read.Deployment), s.snapshot(read.Marker)), nil
}

func (s Service) Runs(deploymentID string, jobID int64, params Pagination) (CollectionEnvelope[AgentRun], error) {
	if err := s.requireDeployment(deploymentID); err != nil {
		return CollectionEnvelope[AgentRun]{}, err
	}
	read, err := s.Provider.ReadAgentRunsSnapshot(jobID)
	if err != nil {
		return CollectionEnvelope[AgentRun]{}, err
	}
	items := make([]AgentRun, 0, len(read.Runs))
	for _, run := range read.Runs {
		items = append(items, runDTO(run, deploymentID))
	}
	items, page, err := PaginateRuns(items, params)
	if err != nil {
		return CollectionEnvelope[AgentRun]{}, err
	}
	return NewCollectionEnvelope(items, s.snapshot(read.Marker), page), nil
}

func (s Service) Logs(deploymentID string, jobID int64, params Pagination) (CollectionEnvelope[LogDescriptor], error) {
	if err := s.requireDeployment(deploymentID); err != nil {
		return CollectionEnvelope[LogDescriptor]{}, err
	}
	read, err := s.Provider.ReadLogsSnapshot(jobID)
	if err != nil {
		return CollectionEnvelope[LogDescriptor]{}, err
	}
	items := make([]LogDescriptor, 0, len(read.Logs))
	for _, log := range read.Logs {
		items = append(items, LogDescriptor{
			ID: fmt.Sprintf("job-%d-run-%d", jobID, log.Run.ID), DeploymentID: deploymentID,
			JobID: jobID, RunID: log.Run.ID, Stage: log.Run.Stage, Attempt: log.Run.Attempt,
			Available: log.Available,
		})
	}
	items, page, err := PaginateLogs(items, params)
	if err != nil {
		return CollectionEnvelope[LogDescriptor]{}, err
	}
	return NewCollectionEnvelope(items, s.snapshot(read.Marker), page), nil
}

func (s Service) requireDeployment(deploymentID string) error {
	if s.Provider == nil {
		return ErrProviderUnavailable
	}
	if deploymentID == "" || deploymentID != s.Provider.DeploymentID() {
		return coordinator.ErrNotFound
	}
	return nil
}

func (s Service) snapshot(marker coordinator.SnapshotMarker) Snapshot {
	return Snapshot{Watermark: marker.Watermark, LogEpoch: marker.LogEpoch, Incarnation: s.Provider.WorkerIncarnation()}
}

// DurableEventDTO validates and freezes a database event into the v1 wire
// contract. Invalid structured data is treated as an internal failure instead
// of allowing json.Marshal to corrupt an SSE frame.
func DurableEventDTO(event *db.Event) (DurableEvent, error) {
	data, err := eventData(event.Data)
	if err != nil {
		return DurableEvent{}, err
	}
	return DurableEvent{
		ID: event.ID, Timestamp: NewTimestamp(event.Time), DeploymentID: event.DeploymentID,
		JobID: positiveInt64Ptr(event.JobID), RunID: positiveInt64Ptr(event.RunID),
		Type: EventType(event.Type), Severity: Severity(event.Severity),
		Summary: event.Summary, Data: data,
	}, nil
}

// EphemeralEventDTO renders a live-only envelope without exposing its internal
// bus cursor or assigning a durable SSE id.
func EphemeralEventDTO(envelope coordinator.Envelope, incarnation string) (EphemeralEvent, error) {
	data := json.RawMessage(nil)
	if len(envelope.Data) > 0 {
		if !json.Valid(envelope.Data) {
			return EphemeralEvent{}, fmt.Errorf("invalid ephemeral event data")
		}
		data = append(json.RawMessage(nil), envelope.Data...)
	}
	return EphemeralEvent{Event: LiveEvent{
		Timestamp: NewTimestamp(envelope.Time), DeploymentID: envelope.DeploymentID,
		JobID: positiveInt64Ptr(envelope.JobID), RunID: positiveInt64Ptr(envelope.RunID),
		Type: EventType(envelope.Type), Severity: Severity(envelope.Severity),
		Summary: envelope.Summary, Data: data,
	}, Incarnation: incarnation}, nil
}

func eventData(value sql.NullString) (json.RawMessage, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	data := json.RawMessage(value.String)
	if !json.Valid(data) {
		return nil, fmt.Errorf("invalid durable event data")
	}
	return append(json.RawMessage(nil), data...), nil
}

func positiveInt64Ptr(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}

func (s Service) deploymentDTO(deployment *db.Deployment) Deployment {
	config := s.Provider.ConfigRevision()
	configDTO := ConfigurationRevision{Available: config.HasConfig, Valid: config.ValidateErr == nil}
	if config.HasConfig {
		configDTO.Revision = stringPtr(config.SHA256)
		if !config.LoadedAt.IsZero() {
			loadedAt := NewTimestamp(config.LoadedAt)
			configDTO.LoadedAt = &loadedAt
		}
	}
	if config.ValidateErr != nil {
		configDTO.Error = stringPtr(config.ValidateErr.Error())
	}
	return Deployment{
		ID: deployment.ID, Owner: deployment.Owner, Repository: deployment.Repo,
		Mode: deployment.Mode, ActivationPolicy: string(s.Provider.ActivationPolicy()),
		StartedAt: NewTimestamp(deployment.StartedAt), ConfigRevision: configDTO,
	}
}

func (s Service) automationDTO(automation coordinator.Automation) Automation {
	name := automation.Name
	idName := name
	if automation.Kind == coordinator.AutomationWatch {
		idName = "default"
	}
	labels := append([]string(nil), automation.Labels...)
	if labels == nil {
		labels = make([]string, 0)
	}
	result := Automation{
		ID: string(automation.Kind) + ":" + idName, DeploymentID: s.Provider.DeploymentID(),
		Kind: string(automation.Kind), Name: name, Expression: automation.Expression, Labels: labels,
		Effective: Execution{Agent: automation.Agent, Runtime: automation.EffectiveRuntime,
			Model: stringPtr(automation.EffectiveModel), MaxTurns: automation.EffectiveMaxTurns,
			MaxBudgetUSD: automation.EffectiveMaxBudget},
		Enabled: automation.Enabled,
	}
	config := s.Provider.ConfigRevision()
	if config.HasConfig && automation.Kind != coordinator.AutomationWatch {
		result.Revision = stringPtr(config.SHA256)
	}
	if automation.HasLastActivation {
		at := NewTimestamp(automation.LastActivationAt)
		result.LastActivationAt = &at
		result.LastActivationID = &automation.LastActivationJobID
	}
	if automation.HasNextRun {
		at := NewTimestamp(automation.NextRunAt)
		result.NextRunAt = &at
	}
	return result
}

func jobDTO(job *db.Job, deployment *db.Deployment) Job {
	return Job{
		ID: job.ID, DeploymentID: job.DeploymentID, Name: job.Name,
		IssueNumber: intPtr(job.IssueNumber), IssueTitle: nullStringPtr(job.IssueTitle),
		Owner: job.Owner, Repository: job.Repo,
		Provenance: Provenance{Type: nullStringPtr(job.SourceType), Name: nullStringPtr(job.SourceName), Reference: nullStringPtr(job.SourceRef)},
		Lifecycle:  JobLifecycle{Status: job.Status, CurrentStage: nullStringPtr(job.CurrentStage)},
		Effective: Execution{Agent: job.Agent, Runtime: job.EffectiveRuntime(deployment), Model: stringPtr(job.EffectiveModel()),
			MaxTurns: job.EffectiveMaxTurns(deployment), MaxBudgetUSD: job.EffectiveMaxBudget(deployment)},
		CostUSD: job.CostUSD, Branch: nullStringPtr(job.Branch), PullRequest: nullInt64Ptr(job.PRNumber),
		Failure:    Failure{Reason: nullStringPtr(job.FailureReason), Detail: nullStringPtr(job.FailureDetail)},
		Review:     Review{Risk: nullStringPtr(job.ReviewRisk), CommentID: nullInt64Ptr(job.ReviewCommentID)},
		Timestamps: JobTimestamps{QueuedAt: nullTimePtr(job.QueuedAt), StartedAt: nullTimePtr(job.StartedAt), CompletedAt: nullTimePtr(job.CompletedAt)},
	}
}

func runDTO(run *db.AgentRun, deploymentID string) AgentRun {
	return AgentRun{
		ID: run.ID, DeploymentID: deploymentID, JobID: run.JobID, Stage: run.Stage, Attempt: run.Attempt,
		Agent: run.Agent, Runtime: nullStringPtr(run.Runtime), Model: nullStringPtr(run.Model), SessionID: nullStringPtr(run.SessionID),
		Activity:   RunActivity{StepCount: run.StepCount, LastActivityAt: nullTimePtr(run.LastActivityAt)},
		FinalUsage: FinalUsage{Turns: run.FinalTurns, CostUSD: run.CostUSD, FinalText: nullStringPtr(run.FinalText)},
		Limits:     RunLimits{MaxTurns: nullIntPtr(run.MaxTurns), MaxBudgetUSD: nullFloat64Ptr(run.MaxBudgetUSD)},
		Outcome:    RunOutcome{Status: run.Status, StopReason: nullStringPtr(run.StopReason), FailureDetail: nullStringPtr(run.FailureDetail)},
		Timestamps: RunTimestamps{StartedAt: nullTimePtr(run.StartedAt), CompletedAt: nullTimePtr(run.CompletedAt)},
	}
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func intPtr(value int) *int {
	if value <= 0 {
		return nil
	}
	return &value
}

func nullStringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func nullInt64Ptr(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

func nullIntPtr(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	result := int(value.Int64)
	return &result
}

func nullFloat64Ptr(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	return &value.Float64
}

func nullTimePtr(value sql.NullTime) *Timestamp {
	if !value.Valid {
		return nil
	}
	result := NewTimestamp(value.Time)
	return &result
}
