#!/usr/bin/env bash
#
# xbar plugin for agent-minder deploy status
#
# Shows a menu bar indicator with task counts and a dropdown with details.
# Refreshes every 30 seconds (configured by filename: agent-minder.30s.sh).
#
# Install:
#   1. brew install --cask xbar
#   2. Symlink or copy to xbar plugins directory:
#      ln -s /path/to/agent-minder/scripts/agent-minder.30s.sh \
#            ~/Library/Application\ Support/xbar/plugins/agent-minder.30s.sh
#   3. Refresh xbar (or it auto-detects)
#
# Requires: agent-minder binary on PATH (or set AGENT_MINDER_BIN below)
#
# xbar metadata:
# <xbar.title>agent-minder</xbar.title>
# <xbar.desc>Deploy watch status indicator</xbar.desc>
# <xbar.author>Dustin Mays</xbar.author>
# <xbar.version>2.0</xbar.version>
# <xbar.dependencies>agent-minder</xbar.dependencies>

# --- Configuration ---
BREW_PREFIX="$(/opt/homebrew/bin/brew --prefix 2>/dev/null || /usr/local/bin/brew --prefix 2>/dev/null || echo /opt/homebrew)"
export PATH="${BREW_PREFIX}/bin:${HOME}/go/bin:${HOME}/.local/bin:/usr/local/bin:/usr/bin:/bin"

MINDER="${AGENT_MINDER_BIN:-agent-minder}"
LOGS_DIR="${HOME}/.agent-minder"
DEPLOYS_DIR="${LOGS_DIR}/deploys"
LAUNCHD_LOG="${LOGS_DIR}/launchd-daemon.log"

# --- Helpers ---
icon_for_status() {
    case "$1" in
        running)   echo "🔄" ;;
        queued)    echo "⏳" ;;
        blocked)   echo "🚫" ;;
        review)    echo "👀" ;;
        reviewing) echo "🔍" ;;
        reviewed)  echo "✅" ;;
        done)      echo "✅" ;;
        bailed)    echo "❌" ;;
        stopped)   echo "⏹" ;;
        *)         echo "·" ;;
    esac
}

# Check if a deployment daemon is alive by inspecting its PID file.
is_deploy_alive() {
    local deploy_id="$1"
    local pid_file="${DEPLOYS_DIR}/${deploy_id}.pid"
    [[ -f "$pid_file" ]] || return 1
    local pid
    pid=$(cat "$pid_file" 2>/dev/null)
    [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null
}

# --- Gather data ---
# If agent-minder isn't available, show offline.
if ! command -v "$MINDER" &>/dev/null; then
    echo "🤖 ?"
    echo "---"
    echo "agent-minder not found on PATH | color=red"
    echo "Expected: ${MINDER}"
    exit 0
fi

# Use the v2 database path (matches internal/db.DefaultDBPath).
DB="${MINDER_DB:-${HOME}/.agent-minder/v2.db}"

if [[ ! -f "$DB" ]]; then
    echo "🤖 —"
    echo "---"
    echo "No database found | color=gray"
    echo "${DB}"
    exit 0
fi

# Query deployments and their job counts using sqlite3.
# Uses the v2 schema: deployments + jobs tables.
# Order by started_at DESC so the most recent deploy is first.
DEPLOY_DATA=$(sqlite3 "$DB" "
    SELECT d.id, d.owner || '/' || d.repo, d.mode,
        COUNT(CASE WHEN j.status = 'running' THEN 1 END) as running,
        COUNT(CASE WHEN j.status = 'queued' THEN 1 END) as queued,
        COUNT(CASE WHEN j.status = 'blocked' THEN 1 END) as waiting,
        COUNT(CASE WHEN j.status IN ('review', 'reviewing', 'reviewed') THEN 1 END) as review,
        COUNT(CASE WHEN j.status = 'done' THEN 1 END) as done,
        COUNT(CASE WHEN j.status IN ('bailed', 'stopped') THEN 1 END) as errored,
        COUNT(j.id) as total,
        COALESCE(SUM(j.cost_usd), 0) as cost,
        d.total_budget_usd
    FROM deployments d
    LEFT JOIN jobs j ON j.deployment_id = d.id
    GROUP BY d.id
    ORDER BY d.started_at DESC;
" 2>/dev/null)

if [[ -z "$DEPLOY_DATA" ]]; then
    echo "🤖 —"
    echo "---"
    echo "No deployments | color=gray"
    echo "---"
    echo "Refresh | refresh=true"
    exit 0
fi

# Filter to only alive deployments (daemon process still running).
ALIVE_DATA=""
while IFS='|' read -r deploy_id fullrepo mode running queued waiting review done errored total cost budget; do
    [[ -z "$deploy_id" ]] && continue
    if is_deploy_alive "$deploy_id"; then
        ALIVE_DATA+="${deploy_id}|${fullrepo}|${mode}|${running}|${queued}|${waiting}|${review}|${done}|${errored}|${total}|${cost}|${budget}"$'\n'
    fi
done <<< "$DEPLOY_DATA"

# Remove trailing newline.
ALIVE_DATA="${ALIVE_DATA%$'\n'}"

if [[ -z "$ALIVE_DATA" ]]; then
    echo "🤖 —"
    echo "---"
    echo "No active deployments | color=gray"
    echo "---"
    echo "Refresh | refresh=true"
    exit 0
fi

# --- Menu bar line ---
# Aggregate across all active deploys.
TOTAL_RUNNING=0
TOTAL_DONE=0
TOTAL_ERRORED=0
TOTAL_ALL=0
HAS_ACTIVE=false

while IFS='|' read -r deploy_id fullrepo mode running queued waiting review done errored total cost budget; do
    TOTAL_RUNNING=$((TOTAL_RUNNING + running))
    TOTAL_DONE=$((TOTAL_DONE + done))
    TOTAL_ERRORED=$((TOTAL_ERRORED + errored))
    TOTAL_ALL=$((TOTAL_ALL + total))
    if [[ $running -gt 0 || $queued -gt 0 || $waiting -gt 0 ]]; then
        HAS_ACTIVE=true
    fi
done <<< "$ALIVE_DATA"

# Build the menu bar string.
if [[ "$HAS_ACTIVE" == true ]]; then
    BAR="🤖 ${TOTAL_RUNNING}↑"
    [[ $TOTAL_DONE -gt 0 ]] && BAR="${BAR} ${TOTAL_DONE}✓"
    [[ $TOTAL_ERRORED -gt 0 ]] && BAR="${BAR} ${TOTAL_ERRORED}✗"
else
    BAR="🤖 ${TOTAL_DONE}✓"
    [[ $TOTAL_ERRORED -gt 0 ]] && BAR="${BAR} ${TOTAL_ERRORED}✗"
fi

echo "$BAR"
echo "---"

# --- Dropdown: per-deploy details ---
while IFS='|' read -r deploy_id fullrepo mode running queued waiting review done errored total cost budget; do
    # Deploy header.
    cost_str=$(printf '$%.2f' "$cost")
    budget_str=$(printf '$%.2f' "$budget")
    echo "${deploy_id} ${fullrepo} — ${running} running, ${done} done (${cost_str}/${budget_str}) | font=MonospacedSystemFont size=13"
    echo "${mode} mode | color=gray size=11"

    # Per-job details for this deployment.
    TASKS=$(sqlite3 "$DB" "
        SELECT j.issue_number, j.issue_title, j.name, j.status, j.pr_number, j.cost_usd
        FROM jobs j
        WHERE j.deployment_id = '${deploy_id}'
        ORDER BY
            CASE j.status
                WHEN 'running' THEN 0
                WHEN 'queued' THEN 1
                WHEN 'blocked' THEN 2
                WHEN 'review' THEN 3
                WHEN 'reviewing' THEN 4
                WHEN 'reviewed' THEN 5
                WHEN 'done' THEN 6
                ELSE 7
            END,
            j.id;
    " 2>/dev/null)

    while IFS='|' read -r num title name status pr_num task_cost; do
        [[ -z "$status" ]] && continue
        icon=$(icon_for_status "$status")
        # Use issue number if available, otherwise use job name.
        if [[ -n "$num" && "$num" != "0" ]]; then
            label="#${num} ${title:0:40}"
        else
            label="${name:0:40}"
        fi
        pr_info=""
        if [[ -n "$pr_num" && "$pr_num" != "0" ]]; then
            pr_info=" (PR #${pr_num})"
        fi
        cost_info=""
        if [[ -n "$task_cost" ]] && (( $(echo "$task_cost > 0" | bc -l 2>/dev/null || echo 0) )); then
            cost_info=$(printf ' $%.2f' "$task_cost")
        fi
        echo "--${icon} ${label} — ${status}${pr_info}${cost_info} | font=MonospacedSystemFont size=12"
    done <<< "$TASKS"

    echo "-----"
done <<< "$ALIVE_DATA"

# --- Actions ---
echo "---"

# Per-deploy actions.
while IFS='|' read -r deploy_id fullrepo mode running queued waiting review done errored total cost budget; do
    echo "${deploy_id} (${fullrepo})"
    echo "--Status | bash='${MINDER}' param1=status param2='${deploy_id}' terminal=true"
    echo "--Stop | bash='${MINDER}' param1=stop param2='${deploy_id}' terminal=true refresh=true"
done <<< "$ALIVE_DATA"

echo "---"
echo "Open Logs | bash=open param1='${LOGS_DIR}' terminal=false"
if [[ -f "$LAUNCHD_LOG" ]]; then
    echo "Tail LaunchAgent Log | bash=tail param1=-f param2='${LAUNCHD_LOG}' terminal=true"
fi
echo "---"
echo "Refresh | refresh=true"
