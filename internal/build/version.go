package build

// Version is the current version of agent-minder.
const Version = "0.1.0"

// VersionString returns a formatted version string.
func VersionString() string {
	return "agent-minder v" + Version
}
