#!/usr/bin/env bash
#
# macOS LaunchAgent wrapper for agent-minder Discord bot.
# Requires the daemon to be running with --serve enabled.
#
set -euo pipefail

# --- PATH setup ---
BREW_PREFIX="$(/opt/homebrew/bin/brew --prefix 2>/dev/null || /usr/local/bin/brew --prefix 2>/dev/null || echo /opt/homebrew)"
export PATH="${BREW_PREFIX}/bin:${BREW_PREFIX}/sbin:/usr/local/go/bin:${HOME}/go/bin:${HOME}/.local/bin:${HOME}/.npm-global/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

# --- Source environment ---
ENV_FILE="${HOME}/.agent-minder/deploy/agent-minder.env"
if [[ ! -f "$ENV_FILE" ]]; then
    echo "ERROR: Missing ${ENV_FILE}" >&2
    exit 1
fi
set -a
# shellcheck source=/dev/null
source "$ENV_FILE"
set +a

if [[ -n "${PATH_EXTRA:-}" ]]; then
    export PATH="${PATH_EXTRA}:${PATH}"
fi

# --- Validate required vars ---
: "${DISCORD_BOT_TOKEN:?Set DISCORD_BOT_TOKEN in ${ENV_FILE}}"
: "${DISCORD_CHANNEL_ID:?Set DISCORD_CHANNEL_ID in ${ENV_FILE}}"

ARGS=(discord
    --remote "${SERVE_ADDR:-localhost:7749}"
    --token "${DISCORD_BOT_TOKEN}"
    --channel "${DISCORD_CHANNEL_ID}"
)

if [[ -n "${DISCORD_GUILD_ID:-}" ]]; then
    ARGS+=(--guild "${DISCORD_GUILD_ID}")
fi
if [[ -n "${MINDER_API_KEY:-}" ]]; then
    ARGS+=(--api-key "${MINDER_API_KEY}")
fi

exec agent-minder "${ARGS[@]}"
