package db

import (
	"os"
	"path/filepath"
	"strings"
)

// ExpandHome expands a leading ~ to the user's home directory.
func ExpandHome(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}

// EnsureDir creates the parent directory for the given path if needed.
func EnsureDir(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0755)
}
