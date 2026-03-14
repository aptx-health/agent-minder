#!/usr/bin/env bash
# Sets up an isolated test environment for a worktree.
# Usage:
#   source scripts/test-env.sh [project-name]
#   go run . start "$MINDER_PROJECT"
#
# Creates a copy of the production DB and sets env vars so
# the worktree runs against its own DB and log file.

set -euo pipefail

PROJECT="${1:-minder-test-1}"
BRANCH=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")
SLUG=$(echo "$BRANCH" | tr '/' '-')

export MINDER_DB=~/.agent-minder/minder-${SLUG}.db
export MINDER_LOG=~/.agent-minder/debug-${SLUG}.log
export MINDER_DEBUG=1
export MINDER_PROJECT="$PROJECT"

# Copy production DB if test DB doesn't exist yet.
PROD_DB=~/.agent-minder/minder.db
if [ ! -f "$MINDER_DB" ] && [ -f "$PROD_DB" ]; then
    cp "$PROD_DB" "$MINDER_DB"
    echo "Copied $PROD_DB -> $MINDER_DB"
fi

echo "Test environment ready:"
echo "  MINDER_DB=$MINDER_DB"
echo "  MINDER_LOG=$MINDER_LOG"
echo "  MINDER_DEBUG=$MINDER_DEBUG"
echo "  MINDER_PROJECT=$MINDER_PROJECT"
echo ""
echo "Run: go run . start \"\$MINDER_PROJECT\""
