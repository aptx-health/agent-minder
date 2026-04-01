#!/bin/bash
# agent-minder xbar plugin
# Shows agent status in the macOS menu bar.
#
# Install: symlink or copy to your xbar plugins directory
#   ln -s ~/repos/agent-minder/xbar/minder.5s.sh ~/Library/Application\ Support/xbar/plugins/
#
# The filename "minder.5s.sh" means xbar refreshes every 5 seconds.
# Requires: curl, jq
#
# Configure the daemon address below, or set MINDER_ADDR env var.

ADDR="${MINDER_ADDR:-localhost:7749}"
API_KEY="${MINDER_API_KEY:-}"

# Build curl args.
CURL_OPTS="-s --connect-timeout 2"
if [ -n "$API_KEY" ]; then
  CURL_OPTS="$CURL_OPTS -H X-API-Key:$API_KEY"
fi

# Fetch status.
STATUS=$(curl $CURL_OPTS "http://$ADDR/status" 2>/dev/null)
if [ -z "$STATUS" ] || ! echo "$STATUS" | jq -e '.deploy_id' >/dev/null 2>&1; then
  echo "🤖 offline"
  echo "---"
  echo "No minder daemon running on $ADDR"
  echo "Start one: minder deploy <issues> --serve :7749 | font=Menlo size=11"
  exit 0
fi

# Fetch jobs.
TASKS=$(curl $CURL_OPTS "http://$ADDR/jobs" 2>/dev/null)
if [ -z "$TASKS" ] || ! echo "$TASKS" | jq -e 'type == "array"' >/dev/null 2>&1; then
  TASKS="[]"
fi

# Count by status.
RUNNING=$(echo "$TASKS" | jq '[.[] | select(.status=="running")] | length')
QUEUED=$(echo "$TASKS" | jq '[.[] | select(.status=="queued" or .status=="blocked")] | length')
REVIEW=$(echo "$TASKS" | jq '[.[] | select(.status=="review" or .status=="reviewing" or .status=="reviewed")] | length')
DONE=$(echo "$TASKS" | jq '[.[] | select(.status=="done")] | length')
BAILED=$(echo "$TASKS" | jq '[.[] | select(.status=="bailed" or .status=="stopped")] | length')
TOTAL=$(echo "$TASKS" | jq 'length')

# Budget info.
SPENT=$(echo "$STATUS" | jq -r '.total_spent // 0' | xargs printf '%.2f')
BUDGET=$(echo "$STATUS" | jq -r '.total_budget // 0' | xargs printf '%.2f')
PAUSED=$(echo "$STATUS" | jq -r '.budget_paused // false')
DEPLOY_ID=$(echo "$STATUS" | jq -r '.deploy_id')
UPTIME=$(echo "$STATUS" | jq -r '.uptime_sec // 0')

# Format uptime.
if [ "$UPTIME" -ge 3600 ]; then
  UPTIME_FMT="$((UPTIME / 3600))h$((UPTIME % 3600 / 60))m"
elif [ "$UPTIME" -ge 60 ]; then
  UPTIME_FMT="$((UPTIME / 60))m"
else
  UPTIME_FMT="${UPTIME}s"
fi

# Menu bar title.
if [ "$PAUSED" = "true" ]; then
  echo "🤖 ⏸ paused"
elif [ "$RUNNING" -gt 0 ]; then
  echo "🤖 ${RUNNING}⚡${REVIEW}👀${DONE}✓"
elif [ "$REVIEW" -gt 0 ]; then
  echo "🤖 ${REVIEW}👀 waiting"
elif [ "$QUEUED" -gt 0 ]; then
  echo "🤖 ${QUEUED} queued"
else
  echo "🤖 done"
fi

echo "---"

# Deploy info.
echo "Deploy $DEPLOY_ID ($UPTIME_FMT) | size=11 color=#888888"
echo "Cost: \$$SPENT / \$$BUDGET | size=11 color=#888888"
echo "---"

# Task list with status icons.
echo "$TASKS" | jq -r '.[] |
  (if .status == "running" then "⚡"
   elif .status == "queued" then "⏳"
   elif .status == "blocked" then "🔒"
   elif .status == "review" then "👀"
   elif .status == "reviewing" then "🔍"
   elif .status == "reviewed" then "✅"
   elif .status == "done" then "✓"
   elif .status == "bailed" then "✗"
   elif .status == "stopped" then "⏹"
   else "?" end) as $icon |
  (if .pr_number > 0 then " PR#\(.pr_number)" else "" end) as $pr |
  (if .cost_usd > 0 then " $\(.cost_usd | tostring | .[0:5])" else "" end) as $cost |
  "\($icon) #\(.issue_number) \(.issue_title[:45])\($pr)\($cost) | font=Menlo size=11"'

# Actions.
echo "---"

if [ "$PAUSED" = "true" ]; then
  echo "▶ Resume Budget | bash=curl param1=-sX param2=POST param3=http://$ADDR/resume terminal=false refresh=true"
fi

echo "Refresh | refresh=true"
echo "---"
echo "⏹ Stop Daemon | bash=curl param1=-sX param2=POST param3=http://$ADDR/stop terminal=false refresh=true color=red"
