package github

import (
	"fmt"
	"strconv"
	"strings"
)

// ItemRef holds the parsed components of a GitHub item reference.
type ItemRef struct {
	Owner  string
	Repo   string
	Number int
}

// ParseItemRef parses a GitHub item reference string.
// Supported formats:
//   - "#42" (requires defaultOwner and defaultRepo)
//   - "owner/repo#42"
//
// Returns an error if the format is invalid or if owner/repo are needed but not provided.
func ParseItemRef(input, defaultOwner, defaultRepo string) (*ItemRef, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}

	// Format: owner/repo#number
	if idx := strings.Index(input, "#"); idx > 0 {
		prefix := input[:idx]
		numStr := input[idx+1:]

		num, err := strconv.Atoi(numStr)
		if err != nil || num <= 0 {
			return nil, fmt.Errorf("invalid issue/PR number: %q", numStr)
		}

		parts := strings.SplitN(prefix, "/", 2)
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			return &ItemRef{Owner: parts[0], Repo: parts[1], Number: num}, nil
		}
		return nil, fmt.Errorf("invalid owner/repo prefix: %q", prefix)
	}

	// Format: #number (with defaults)
	if strings.HasPrefix(input, "#") {
		numStr := input[1:]
		num, err := strconv.Atoi(numStr)
		if err != nil || num <= 0 {
			return nil, fmt.Errorf("invalid issue/PR number: %q", numStr)
		}
		if defaultOwner == "" || defaultRepo == "" {
			return nil, fmt.Errorf("short ref %q requires a default owner/repo (enroll a GitHub-hosted repo first)", input)
		}
		return &ItemRef{Owner: defaultOwner, Repo: defaultRepo, Number: num}, nil
	}

	// Try as plain number.
	if num, err := strconv.Atoi(input); err == nil && num > 0 {
		if defaultOwner == "" || defaultRepo == "" {
			return nil, fmt.Errorf("plain number %q requires a default owner/repo (enroll a GitHub-hosted repo first)", input)
		}
		return &ItemRef{Owner: defaultOwner, Repo: defaultRepo, Number: num}, nil
	}

	return nil, fmt.Errorf("unrecognized format: %q (use #42 or owner/repo#42)", input)
}
