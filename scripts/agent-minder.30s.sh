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
# <xbar.version>1.0</xbar.version>
# <xbar.dependencies>agent-minder</xbar.dependencies>

# --- Configuration ---
BREW_PREFIX="$(/opt/homebrew/bin/brew --prefix 2>/dev/null || /usr/local/bin/brew --prefix 2>/dev/null || echo /opt/homebrew)"
export PATH="${BREW_PREFIX}/bin:${HOME}/go/bin:${HOME}/.local/bin:/usr/local/bin:/usr/bin:/bin"

MINDER="${AGENT_MINDER_BIN:-agent-minder}"
LOGS_DIR="${HOME}/.agent-minder"
LAUNCHD_LOG="${LOGS_DIR}/launchd-daemon.log"

# --- Helpers ---
icon_for_status() {
    case "$1" in
        running)  echo "🔄" ;;
        queued)   echo "⏳" ;;
        blocked)  echo "🚫" ;;
        pending)  echo "📋" ;;
        review)   echo "👀" ;;
        reviewing) echo "🔍" ;;
        done)     echo "✅" ;;
        bailed)   echo "❌" ;;
        failed)   echo "💥" ;;
        stopped)  echo "⏹" ;;
        manual)   echo "🔧" ;;
        skipped)  echo "⏭" ;;
        *)        echo "·" ;;
    esac
}

# --- Gather data ---
# Get deploy list output. If agent-minder isn't available, show offline.
if ! command -v "$MINDER" &>/dev/null; then
    echo "🤖 ?"
    echo "---"
    echo "agent-minder not found on PATH | color=red"
    echo "Expected: ${MINDER}"
    exit 0
fi

# Parse deploy projects from the database directly via deploy list.
# We read the DB to get structured data since deploy list is text-only.
DB="${MINDER_DB:-${HOME}/.agent-minder/minder.db}"

if [[ ! -f "$DB" ]]; then
    echo "🤖 —"
    echo "---"
    echo "No database found | color=gray"
    echo "${DB}"
    exit 0
fi

# Query deploy projects and their task counts using sqlite3.
# This avoids parsing CLI text output and is much more reliable.
DEPLOY_DATA=$(sqlite3 "$DB" "
    SELECT p.name, p.goal_description,
        COUNT(CASE WHEN t.status = 'running' THEN 1 END) as running,
        COUNT(CASE WHEN t.status = 'queued' THEN 1 END) as queued,
        COUNT(CASE WHEN t.status IN ('pending', 'blocked') THEN 1 END) as waiting,
        COUNT(CASE WHEN t.status = 'review' THEN 1 END) as review,
        COUNT(CASE WHEN t.status = 'done' THEN 1 END) as done,
        COUNT(CASE WHEN t.status IN ('bailed', 'failed', 'stopped') THEN 1 END) as errored,
        COUNT(*) as total,
        COALESCE(SUM(t.cost_usd), 0) as cost
    FROM projects p
    LEFT JOIN autopilot_tasks t ON t.project_id = p.id
    WHERE p.is_deploy = 1
    GROUP BY p.id
    ORDER BY p.created_at DESC;
" 2>/dev/null)

if [[ -z "$DEPLOY_DATA" ]]; then
    echo "🤖 —"
    echo "---"
    echo "No deployments | color=gray"
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

while IFS='|' read -r name desc running queued waiting review done errored total cost; do
    TOTAL_RUNNING=$((TOTAL_RUNNING + running))
    TOTAL_DONE=$((TOTAL_DONE + done))
    TOTAL_ERRORED=$((TOTAL_ERRORED + errored))
    TOTAL_ALL=$((TOTAL_ALL + total))
    if [[ $running -gt 0 || $queued -gt 0 || $waiting -gt 0 ]]; then
        HAS_ACTIVE=true
    fi
done <<< "$DEPLOY_DATA"

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
while IFS='|' read -r name desc running queued waiting review done errored total cost; do
    # Deploy header.
    cost_str=$(printf '$%.2f' "$cost")
    echo "${name} — ${running} running, ${done} done (${cost_str}) | font=MonospacedSystemFont size=13"
    echo "${desc} | color=gray size=11"

    # Per-task details: query individual tasks for this deploy.
    TASKS=$(sqlite3 "$DB" "
        SELECT t.issue_number, t.issue_title, t.status, t.pr_number, t.cost_usd
        FROM autopilot_tasks t
        JOIN projects p ON p.id = t.project_id
        WHERE p.name = '${name}'
        ORDER BY
            CASE t.status
                WHEN 'running' THEN 0
                WHEN 'queued' THEN 1
                WHEN 'pending' THEN 2
                WHEN 'blocked' THEN 3
                WHEN 'review' THEN 4
                WHEN 'reviewing' THEN 5
                WHEN 'done' THEN 6
                ELSE 7
            END,
            t.issue_number;
    " 2>/dev/null)

    while IFS='|' read -r num title status pr_num task_cost; do
        [[ -z "$num" ]] && continue
        icon=$(icon_for_status "$status")
        title_short="${title:0:40}"
        pr_info=""
        if [[ -n "$pr_num" && "$pr_num" != "0" ]]; then
            pr_info=" (PR #${pr_num})"
        fi
        cost_info=""
        if [[ -n "$task_cost" ]] && (( $(echo "$task_cost > 0" | bc -l 2>/dev/null || echo 0) )); then
            cost_info=$(printf ' $%.2f' "$task_cost")
        fi
        echo "--${icon} #${num} ${title_short} — ${status}${pr_info}${cost_info} | font=MonospacedSystemFont size=12"
    done <<< "$TASKS"

    echo "-----"
done <<< "$DEPLOY_DATA"

# --- Actions ---
echo "---"
echo "Deploy List | bash='${MINDER}' param1=deploy param2=list terminal=true"
echo "---"

# Per-deploy actions.
while IFS='|' read -r name desc running queued waiting review done errored total cost; do
    echo "${name}"
    echo "--Status | bash='${MINDER}' param1=deploy param2=status param3='${name}' terminal=true"
    echo "--Stop | bash='${MINDER}' param1=deploy param2=stop param3='${name}' terminal=true refresh=true"
done <<< "$DEPLOY_DATA"

echo "---"
echo "Open Logs | bash=open param1='${LOGS_DIR}' terminal=false"
if [[ -f "$LAUNCHD_LOG" ]]; then
    echo "Tail LaunchAgent Log | bash=tail param1=-f param2='${LAUNCHD_LOG}' terminal=true"
fi
echo "---"
echo "Refresh | refresh=true"
