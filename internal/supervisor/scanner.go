package supervisor

// LiveStatus holds real-time agent status surfaced by the runtime's EventSink.
// The runtime owns stream parsing; this struct is the supervisor-side view used
// by minder status / the TUI to display what the agent is currently doing.
type LiveStatus struct {
	CurrentTool string // e.g. "Bash", "Read", "Edit"
	ToolInput   string // truncated summary of tool input
	StepCount   int    // incremented on each assistant message
}
