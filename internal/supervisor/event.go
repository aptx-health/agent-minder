package supervisor

import (
	"encoding/json"
	"fmt"
	"time"
)

// EventType is the closed vocabulary of supervisor event types (Expedition I
// DM-2). Emit sites must use these constants — a raw string literal at an
// emitEvent/EmitEvent call site fails TestEventEmitSitesUseRegisteredTypes,
// and a constant added here without a severity registration fails
// TestEventTypeEnumIsClosed.
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

// Severity classifies an event for filtering, independent of its type.
type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// eventSeverity registers every EventType and fixes its severity. This map is
// the closed enum: membership here is what makes a type registered.
var eventSeverity = map[EventType]Severity{
	EventInfo:      SeverityInfo,
	EventWarning:   SeverityWarning,
	EventError:     SeverityError,
	EventStarted:   SeverityInfo,
	EventCompleted: SeverityInfo,
	EventFinished:  SeverityInfo,
	EventBailed:    SeverityWarning,
	EventStopped:   SeverityInfo,
	EventWaiting:   SeverityInfo,
	EventManual:    SeverityInfo,
	EventSpared:    SeverityInfo,
	EventReaped:    SeverityInfo,
}

// Known reports whether t is a registered event type.
func (t EventType) Known() bool {
	_, ok := eventSeverity[t]
	return ok
}

// EventSeverity returns the registered severity for t, defaulting to
// SeverityInfo for unregistered types (which the enum tests prevent).
func (t EventType) EventSeverity() Severity {
	if severity, ok := eventSeverity[t]; ok {
		return severity
	}
	return SeverityInfo
}

// AllEventTypes returns every registered event type, in no particular order.
func AllEventTypes() []EventType {
	types := make([]EventType, 0, len(eventSeverity))
	for t := range eventSeverity {
		types = append(types, t)
	}
	return types
}

// Envelope is the typed supervisor event record (Expedition I DM-2). The
// in-memory bus wrapper (eventbus.Event) carries its cursor; per Expedition IV
// R-3 that cursor never crosses a process or API boundary — durable ids arrive
// with M1-18. This issue adds no persistence and no wire exposure.
type Envelope struct {
	Time         time.Time
	DeploymentID string
	JobID        int64 // 0 when the event is not attributable to a job
	RunID        int64 // 0 until per-run attribution is wired at emit sites
	Type         EventType
	Severity     Severity
	Summary      string
	Data         json.RawMessage // structured payload; nil until M1-18 defines shapes
}

// Event is the legacy rendering of an Envelope, kept for test harnesses
// (DrainEvents) and API backward compat. New consumers should use Envelope.
type Event struct {
	Time    time.Time
	Type    string
	Summary string
	TaskID  int64 // kept as TaskID for API backward compat
}

// Legacy renders the envelope into the pre-envelope Event shape. TaskID dies
// at this boundary: it exists only in the legacy rendering, never on the wire.
func (e Envelope) Legacy() Event {
	return Event{Time: e.Time, Type: string(e.Type), Summary: e.Summary, TaskID: e.JobID}
}

// ForegroundLine renders the envelope into the foreground stdout line. The
// output is byte-identical to the pre-envelope rendering (compat C-1); the
// golden test in event_test.go is the enforcement.
func (e Envelope) ForegroundLine() string {
	return fmt.Sprintf("[%s] %s: %s", e.Time.Format("15:04:05"), e.Type, e.Summary)
}
