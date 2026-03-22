package build

import "fmt"

// Version is the current version of agent-minder.
const Version = "0.1.0"

// VersionString returns a formatted version string.
func VersionString() string {
	return fmt.Sprintf("agent-minder v%s", Version)
}
