package supervisor

import (
	"database/sql"
	"testing"
	"time"

	"github.com/aptx-health/agent-minder/internal/db"
)

// newTestSupervisor creates a Supervisor with pre-populated running jobs for testing.
func newTestSupervisor(t *testing.T, jobIDs []int64) *Supervisor {
	t.Helper()
	s := &Supervisor{
		running:   make(map[int64]*runState),
		maxAgents: 5,
	}
	for _, id := range jobIDs {
		s.running[id] = &runState{
			job: &db.Job{
				ID:          id,
				IssueNumber: int(id * 10),
				IssueTitle:  sql.NullString{String: "Issue " + string(rune('A'+id-1)), Valid: true},
				Branch:      sql.NullString{String: "branch-" + string(rune('a'+id-1)), Valid: true},
				Status:      db.StatusRunning,
			},
			startedAt: time.Now(),
		}
	}
	return s
}

func TestRunningJobsSortedByID(t *testing.T) {
	// Insert jobs in non-sorted order to test sorting.
	s := newTestSupervisor(t, []int64{30, 10, 20})

	infos := s.RunningJobs()
	if len(infos) != 3 {
		t.Fatalf("expected 3 running jobs, got %d", len(infos))
	}

	// Verify jobs are returned sorted by ID.
	if infos[0].JobID != 10 {
		t.Errorf("expected first job ID 10, got %d", infos[0].JobID)
	}
	if infos[1].JobID != 20 {
		t.Errorf("expected second job ID 20, got %d", infos[1].JobID)
	}
	if infos[2].JobID != 30 {
		t.Errorf("expected third job ID 30, got %d", infos[2].JobID)
	}
}

func TestSlotStatusSortedByID(t *testing.T) {
	s := newTestSupervisor(t, []int64{30, 10, 20})

	slots := s.SlotStatus()
	if len(slots) != 5 {
		t.Fatalf("expected 5 slots (3 running + 2 idle), got %d", len(slots))
	}

	// Running slots should be sorted by job ID with sequential slot numbers.
	if slots[0].IssueNumber != 100 || slots[0].SlotNum != 0 {
		t.Errorf("slot 0: expected issue 100 at slot 0, got issue %d at slot %d", slots[0].IssueNumber, slots[0].SlotNum)
	}
	if slots[1].IssueNumber != 200 || slots[1].SlotNum != 1 {
		t.Errorf("slot 1: expected issue 200 at slot 1, got issue %d at slot %d", slots[1].IssueNumber, slots[1].SlotNum)
	}
	if slots[2].IssueNumber != 300 || slots[2].SlotNum != 2 {
		t.Errorf("slot 2: expected issue 300 at slot 2, got issue %d at slot %d", slots[2].IssueNumber, slots[2].SlotNum)
	}

	// Remaining slots should be idle.
	for i := 3; i < 5; i++ {
		if slots[i].Status != "idle" {
			t.Errorf("slot %d: expected idle, got %s", i, slots[i].Status)
		}
		if slots[i].SlotNum != i {
			t.Errorf("slot %d: expected SlotNum %d, got %d", i, i, slots[i].SlotNum)
		}
	}
}

func TestStopAgentBySlotIndex(t *testing.T) {
	s := newTestSupervisor(t, []int64{30, 10, 20})

	// Slot 1 should correspond to job ID 20 (the second by sorted order).
	s.StopAgent(1)

	rs := s.running[20]
	if !rs.stoppedByUser {
		t.Error("expected job 20 (slot 1) to be stopped, but it was not")
	}

	// Other jobs should not be stopped.
	if s.running[10].stoppedByUser {
		t.Error("job 10 (slot 0) should not be stopped")
	}
	if s.running[30].stoppedByUser {
		t.Error("job 30 (slot 2) should not be stopped")
	}
}

func TestStopAgentOutOfRange(t *testing.T) {
	s := newTestSupervisor(t, []int64{10})

	// Should not panic on out-of-range index.
	s.StopAgent(5)
	s.StopAgent(-1)

	if s.running[10].stoppedByUser {
		t.Error("job should not be stopped by out-of-range index")
	}
}
