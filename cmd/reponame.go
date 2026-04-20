package cmd

import (
	"fmt"

	"github.com/aptx-health/agent-minder/internal/db"
)

// detectRepoRename checks if the current git remote name differs from what's
// stored in the DB (e.g., after a GitHub repo rename). If a mismatch is found,
// automatically migrates all DB references and prints a notice.
func detectRepoRename(store *db.Store, repoDir, owner, repo string) {
	oldOwner, oldRepo, err := store.FindRepoByDir(repoDir)
	if err != nil {
		return // no prior deployment for this dir
	}

	if oldOwner == owner && oldRepo == repo {
		return // no rename
	}

	n, err := store.RenameRepo(oldOwner, oldRepo, owner, repo)
	if err != nil {
		fmt.Printf("Warning: failed to migrate repo rename: %v\n", err)
		return
	}

	if n > 0 {
		fmt.Printf("Detected repo rename: %s/%s → %s/%s (updated %d records)\n",
			oldOwner, oldRepo, owner, repo, n)
	}
}
