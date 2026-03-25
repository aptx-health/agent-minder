#!/usr/bin/env bash
#
# macOS LaunchAgent wrapper for agent-minder deploy watch.
#
# This script bridges the launchd environment (minimal PATH, no shell profile)
# to the deploy watch daemon. It handles:
#   - PATH setup for Homebrew, Go, Node, Claude Code
#   - Environment sourcing from ~/.agent-minder/deploy/agent-minder.env
#   - Deploy-id persistence across restarts (first run vs crash recovery)
#   - Conditional --milestone / --label flag
#
# On first run:  uses --foreground to do full setup + run daemon inline
# On restart:    uses --daemon --deploy-id <saved-id> to skip duplicate setup
#
set -euo pipefail

# --- PATH setup ---
# launchd only provides /usr/bin:/bin:/usr/sbin:/sbin.
# We need Homebrew, Go, Node, and user-installed binaries.
BREW_PREFIX="$(/opt/homebrew/bin/brew --prefix 2>/dev/null || /usr/local/bin/brew --prefix 2>/dev/null || echo /opt/homebrew)"
export PATH="${BREW_PREFIX}/bin:${BREW_PREFIX}/sbin:/usr/local/go/bin:${HOME}/go/bin:${HOME}/.local/bin:${HOME}/.npm-global/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

# --- Source environment ---
ENV_FILE="${HOME}/.agent-minder/deploy/agent-minder.env"
if [[ ! -f "$ENV_FILE" ]]; then
    echo "ERROR: Missing ${ENV_FILE}" >&2
    echo "Copy deploy/agent-minder.env.macos.example and fill in your values." >&2
    exit 1
fi
set -a
# shellcheck source=/dev/null
source "$ENV_FILE"
set +a

# Allow PATH_EXTRA for custom additions.
if [[ -n "${PATH_EXTRA:-}" ]]; then
    export PATH="${PATH_EXTRA}:${PATH}"
fi

# --- Validate required vars ---
: "${GITHUB_TOKEN:?Set GITHUB_TOKEN in ${ENV_FILE}}"
: "${AGENT_MINDER_REPO:?Set AGENT_MINDER_REPO in ${ENV_FILE}}"

# --- Deploy-id persistence ---
# On first run, --foreground creates the project and runs the daemon inline.
# We save the deploy-id so restarts can skip project creation.
DEPLOY_ID_FILE="${HOME}/.agent-minder/deploys/launchd-watch-id"

if [[ -f "$DEPLOY_ID_FILE" ]]; then
    SAVED_ID=$(cat "$DEPLOY_ID_FILE")
    # Verify the project still exists in the DB.
    if agent-minder deploy status "$SAVED_ID" >/dev/null 2>&1; then
        echo "Resuming existing watch deployment: ${SAVED_ID}"
        cd "${AGENT_MINDER_REPO}"
        exec agent-minder deploy watch --daemon --deploy-id "$SAVED_ID"
    else
        echo "Saved deploy-id ${SAVED_ID} no longer valid, starting fresh."
        rm -f "$DEPLOY_ID_FILE"
    fi
fi

# --- First run: build args and use --foreground ---
ARGS=(deploy watch --foreground
    --max-agents "${MAX_AGENTS:-5}"
    --max-turns "${MAX_TURNS:-150}"
    --max-budget "${MAX_BUDGET:-10.00}"
    --total-budget "${TOTAL_BUDGET:-50.00}"
    --poll-interval "${POLL_INTERVAL:-300}"
)

if [[ -n "${SERVE_ADDR:-}" ]]; then
    ARGS+=(--serve "${SERVE_ADDR}")
fi
if [[ -n "${MINDER_API_KEY:-}" ]]; then
    ARGS+=(--api-key "${MINDER_API_KEY}")
fi
if [[ -n "${WATCH_PROJECT:-}" ]]; then
    ARGS+=(--project "${WATCH_PROJECT}")
fi

if [[ -n "${WATCH_MILESTONE:-}" ]]; then
    ARGS+=(--milestone "${WATCH_MILESTONE}")
elif [[ -n "${WATCH_LABEL:-}" ]]; then
    ARGS+=(--label "${WATCH_LABEL}")
else
    echo "ERROR: Set either WATCH_MILESTONE or WATCH_LABEL in ${ENV_FILE}" >&2
    exit 1
fi

cd "${AGENT_MINDER_REPO}"
exec agent-minder "${ARGS[@]}"
