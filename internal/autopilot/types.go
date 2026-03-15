package autopilot

// TaskStatus represents the lifecycle status of an autopilot task.
type TaskStatus string

const (
	TaskQueued  TaskStatus = "queued"
	TaskRunning TaskStatus = "running"
	TaskReview  TaskStatus = "review"
	TaskDone    TaskStatus = "done"
	TaskBailed  TaskStatus = "bailed"
	TaskStopped TaskStatus = "stopped"
	TaskBlocked TaskStatus = "blocked"
)

// String returns a human-readable string for the task status.
func (s TaskStatus) String() string {
	switch s {
	case TaskQueued:
		return "Queued"
	case TaskRunning:
		return "Running"
	case TaskReview:
		return "In Review"
	case TaskDone:
		return "Done"
	case TaskBailed:
		return "Bailed"
	case TaskStopped:
		return "Stopped"
	case TaskBlocked:
		return "Blocked"
	default:
		return string(s)
	}
}
