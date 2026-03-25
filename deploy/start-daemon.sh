#!/usr/bin/env bash
#
# Wrapper script for the agent-minder daemon systemd unit.
# Reads environment variables and builds the correct CLI flags.
#
# This exists because systemd ExecStart doesn't support conditional flags —
# we need to pass either --milestone or --label depending on which is set.
#
set -euo pipefail

ARGS=(
    deploy watch
    --max-agents "${MAX_AGENTS:-5}"
    --max-turns "${MAX_TURNS:-150}"
    --max-budget "${MAX_BUDGET:-10.00}"
    --total-budget "${TOTAL_BUDGET:-50.00}"
    --poll-interval "${POLL_INTERVAL:-300}"
    --serve "${SERVE_ADDR:-:7749}"
    --api-key "${MINDER_API_KEY}"
    --daemon
    --deploy-id "${DEPLOY_ID:-vps-daemon}"
)

if [[ -n "${WATCH_MILESTONE:-}" ]]; then
    ARGS+=(--milestone "${WATCH_MILESTONE}")
elif [[ -n "${WATCH_LABEL:-}" ]]; then
    ARGS+=(--label "${WATCH_LABEL}")
else
    echo "ERROR: Set either WATCH_MILESTONE or WATCH_LABEL in /etc/agent-minder/agent-minder.env" >&2
    exit 1
fi

exec /usr/local/bin/agent-minder "${ARGS[@]}"
