package autopilot

import "testing"

func TestTaskStatusString(t *testing.T) {
	tests := []struct {
		status TaskStatus
		want   string
	}{
		{TaskQueued, "Queued"},
		{TaskRunning, "Running"},
		{TaskReview, "In Review"},
		{TaskDone, "Done"},
		{TaskBailed, "Bailed"},
		{TaskStopped, "Stopped"},
		{TaskBlocked, "Blocked"},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := tt.status.String(); got != tt.want {
				t.Errorf("TaskStatus(%q).String() = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestTaskStatusStringUnknown(t *testing.T) {
	unknown := TaskStatus("unknown")
	if got := unknown.String(); got != "unknown" {
		t.Errorf("TaskStatus(%q).String() = %q, want %q", unknown, got, "unknown")
	}
}
