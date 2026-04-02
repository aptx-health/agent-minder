#!/usr/bin/env bash
#
# Wrapper script for the agent-minder v2 systemd unit.
# Reads environment variables and builds the correct CLI flags.
#
set -euo pipefail

if [[ -z "${WATCH_FILTER:-}" ]]; then
    echo "ERROR: Set WATCH_FILTER in /etc/agent-minder/agent-minder.env" >&2
    echo "  e.g., WATCH_FILTER=label:agent-ready" >&2
    exit 1
fi

if [[ -z "${MINDER_API_KEY:-}" ]]; then
    echo "ERROR: Set MINDER_API_KEY in /etc/agent-minder/agent-minder.env" >&2
    exit 1
fi

# Find repo dir — use the first directory under /opt/agent-minder/repos/
REPO_DIR="${REPO_DIR:-}"
if [[ -z "$REPO_DIR" ]]; then
    for d in /opt/agent-minder/repos/*/; do
        if [[ -d "$d/.git" ]]; then
            REPO_DIR="$d"
            break
        fi
    done
fi

if [[ -z "$REPO_DIR" ]]; then
    echo "ERROR: No git repos found in /opt/agent-minder/repos/" >&2
    exit 1
fi

ARGS=(
    deploy
    --watch "${WATCH_FILTER}"
    --repo "${REPO_DIR}"
    --serve "${SERVE_ADDR:-:7749}"
    --api-key "${MINDER_API_KEY}"
    --foreground
    --max-agents "${MAX_AGENTS:-3}"
    --max-turns "${MAX_TURNS:-50}"
    --budget "${BUDGET:-5.00}"
    --total-budget "${TOTAL_BUDGET:-25.00}"
)

if [[ "${AUTO_MERGE:-false}" == "true" ]]; then
    ARGS+=(--auto-merge)
fi

if [[ -n "${BASE_BRANCH:-}" ]]; then
    ARGS+=(--base-branch "${BASE_BRANCH}")
fi

echo "Starting minder: repo=${REPO_DIR} watch=${WATCH_FILTER}"
exec /usr/local/bin/minder "${ARGS[@]}"
