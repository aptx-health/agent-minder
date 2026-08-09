package supervisor

import (
	"context"
	"sync"
	"testing"

	"github.com/aptx-health/agent-minder/internal/db"
)

// TestJobLifecycleSnapshotWatermarkConsistency is Expedition IV suite item 4
// (V-1): a reader loops snapshot-then-compare while a TestHooks-driven job
// runs through its lifecycle. At every observed watermark W, the snapshot
// state must reflect exactly the durable events with id <= W — a status the
// snapshot shows without its event at or below W, or an event at or below W
// whose status the snapshot doesn't show, is a store-first violation.
func TestJobLifecycleSnapshotWatermarkConsistency(t *testing.T) {
	h := newHarness(t)
	job := testJob(t, h.store, h.deploy, func(j *db.Job) { j.Status = db.StatusQueued })

	type observation struct {
		status    string
		watermark int64
	}
	var (
		mu           sync.Mutex
		observations []observation
	)
	done := make(chan struct{})
	readerStopped := make(chan struct{})
	go func() {
		defer close(readerStopped)
		for {
			select {
			case <-done:
				return
			default:
			}
			jobs, watermark, err := h.store.SnapshotJobs(h.deploy.ID)
			if err != nil {
				continue
			}
			for _, j := range jobs {
				if j.ID == job.ID {
					mu.Lock()
					observations = append(observations, observation{status: j.Status, watermark: watermark})
					mu.Unlock()
				}
			}
		}
	}()

	// Drive the lifecycle the way the supervisor does: the queued→running
	// transition and its started event commit atomically in markJobLaunched,
	// then the pipeline runs to completion.
	h.sup.mu.Lock()
	h.sup.markJobLaunched(job)
	h.sup.mu.Unlock()
	contract := &AgentContract{Name: "autopilot", Output: "none"}
	if err := h.run(context.Background(), job, contract); err != nil {
		t.Fatalf("pipeline run: %v", err)
	}
	close(done)
	<-readerStopped

	// The immutable event log, read once after the fact: events with id <= W
	// are exactly the events that existed when that snapshot was taken,
	// because ids are allocated at insert and never reused.
	events, err := h.store.EventsAfter(h.deploy.ID, 0, 0)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	eventID := func(typ EventType) int64 {
		for _, e := range events {
			if e.Type == string(typ) {
				return e.ID
			}
		}
		return 0
	}
	startedID := eventID(EventStarted)
	completedID := eventID(EventCompleted)
	if startedID == 0 || completedID == 0 {
		t.Fatalf("lifecycle did not persist started/completed events; log: %+v", events)
	}
	if startedID >= completedID {
		t.Fatalf("event order violated: started id %d >= completed id %d", startedID, completedID)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(observations) == 0 {
		t.Fatal("reader observed no snapshots")
	}
	for i, o := range observations {
		notQueued := o.status != db.StatusQueued
		startedVisible := startedID <= o.watermark
		if notQueued != startedVisible {
			t.Errorf("observation %d violates V-1: status %q at watermark %d, started event id %d",
				i, o.status, o.watermark, startedID)
		}
		isDone := o.status == db.StatusDone
		completedVisible := completedID <= o.watermark
		if isDone != completedVisible {
			t.Errorf("observation %d violates V-1: status %q at watermark %d, completed event id %d",
				i, o.status, o.watermark, completedID)
		}
	}

	// The lifecycle must end observable as done with its event at/below the
	// final watermark (checked via a fresh snapshot, not the racing reader).
	jobs, watermark, err := h.store.SnapshotJobs(h.deploy.ID)
	if err != nil {
		t.Fatalf("final SnapshotJobs: %v", err)
	}
	final := ""
	for _, j := range jobs {
		if j.ID == job.ID {
			final = j.Status
		}
	}
	if final != db.StatusDone {
		t.Fatalf("final status = %q, want %q", final, db.StatusDone)
	}
	if completedID > watermark {
		t.Fatalf("final watermark %d below completed event id %d", watermark, completedID)
	}
}

// TestDurableEnvelopesCarryDurableIDs: live envelopes for durable events carry
// their AUTOINCREMENT id (the only wire cursor, R-3) and mirror the persisted
// log exactly; ephemeral envelopes ride id-less and never touch the store
// (Expedition IV §5).
func TestDurableEnvelopesCarryDurableIDs(t *testing.T) {
	h := newHarness(t)
	job := testJob(t, h.store, h.deploy, func(j *db.Job) { j.Status = db.StatusQueued })

	h.sup.mu.Lock()
	h.sup.markJobLaunched(job)
	h.sup.mu.Unlock()
	contract := &AgentContract{Name: "autopilot", Output: "none"}
	if err := h.run(context.Background(), job, contract); err != nil {
		t.Fatalf("pipeline run: %v", err)
	}
	// One ephemeral event amid the durable ones.
	h.sup.emitEvent(EventInfo, "live-only chatter", job.ID)

	replayed, err := h.sup.eventBus().Replay(0)
	if err != nil {
		t.Fatalf("bus replay: %v", err)
	}
	var durable []Envelope
	for _, event := range replayed {
		if event.Value.Summary == "live-only chatter" {
			if event.Value.ID != 0 {
				t.Errorf("ephemeral envelope carries durable id %d, want 0", event.Value.ID)
			}
			continue
		}
		durable = append(durable, event.Value)
	}

	stored, err := h.store.EventsAfter(h.deploy.ID, 0, 0)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	if len(durable) != len(stored) {
		t.Fatalf("bus carried %d durable envelopes, store has %d rows", len(durable), len(stored))
	}
	lastID := int64(0)
	for i, envelope := range durable {
		row := stored[i]
		if envelope.ID != row.ID || string(envelope.Type) != row.Type || envelope.Summary != row.Summary {
			t.Errorf("envelope %d = (id %d, %s, %q); store row = (id %d, %s, %q)",
				i, envelope.ID, envelope.Type, envelope.Summary, row.ID, row.Type, row.Summary)
		}
		if envelope.ID <= lastID {
			t.Errorf("durable envelope ids not strictly increasing: %d after %d", envelope.ID, lastID)
		}
		lastID = envelope.ID
		if envelope.JobID != row.JobID {
			t.Errorf("envelope %d job attribution %d != stored %d", i, envelope.JobID, row.JobID)
		}
	}
}

// TestEphemeralEventsNeverPersisted: the classification boundary — emitEvent
// is live-only, so nothing it publishes may appear in the durable log.
func TestEphemeralEventsNeverPersisted(t *testing.T) {
	h := newHarness(t)
	h.sup.emitEvent(EventWarning, "Network offline (git fetch failed) — backing off, will retry quietly", 0)
	h.sup.emitEvent(EventInfo, "Waiting for PR(s) to be merged (checking every 30s, ctrl+c to exit)", 0)

	if w, err := h.store.EventWatermark(); err != nil || w != 0 {
		t.Fatalf("ephemeral events reached the durable log: watermark = (%d, %v), want (0, nil)", w, err)
	}
	if events := h.sup.DrainEvents(); len(events) != 2 {
		t.Fatalf("live stream carried %d events, want 2", len(events))
	}
}
