#!/bin/bash
# agent-minder SwiftBar/xbar plugin
# Shows agent status in the macOS menu bar.
#
# Install: symlink to your SwiftBar plugins directory
#   ln -s ~/repos/agent-minder/xbar/minder.5s.sh ~/Library/Application\ Support/SwiftBar/plugins/
#
# The filename "minder.5s.sh" means it refreshes every 5 seconds.
# Requires: curl, jq

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
  echo "🤖 offline | color=#999999"
  echo "---"
  echo "No minder daemon running on $ADDR | size=12"
  echo "Start: minder deploy --serve :7749 | size=11 font=Menlo color=#666666"
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

# Menu bar title — color-coded.
if [ "$PAUSED" = "true" ]; then
  echo "🤖 paused | color=#FF9500"
elif [ "$RUNNING" -gt 0 ]; then
  echo "🤖 ${RUNNING} active | color=#34C759"
elif [ "$REVIEW" -gt 0 ]; then
  echo "🤖 ${REVIEW} review | color=#5856D6"
elif [ "$QUEUED" -gt 0 ]; then
  echo "🤖 ${QUEUED} queued | color=#007AFF"
elif [ "$BAILED" -gt 0 ] && [ "$DONE" -eq 0 ]; then
  echo "🤖 idle | color=#FF3B30"
else
  echo "🤖 idle | color=#8E8E93"
fi

echo "---"

# Deploy header.
echo "Deploy $DEPLOY_ID  ·  ${UPTIME_FMT}  ·  \$$SPENT/\$$BUDGET | size=12 color=#8E8E93"
echo "---"

# Job list — color-coded by status.
echo "$TASKS" | jq -r '.[] |
  (if .status == "running" then { icon: "▶", color: "#34C759" }
   elif .status == "queued" then { icon: "◦", color: "#007AFF" }
   elif .status == "blocked" then { icon: "⊘", color: "#FF9500" }
   elif .status == "review" then { icon: "◎", color: "#5856D6" }
   elif .status == "reviewing" then { icon: "◉", color: "#5856D6" }
   elif .status == "reviewed" then { icon: "✓", color: "#34C759" }
   elif .status == "done" then { icon: "✓", color: "#8E8E93" }
   elif .status == "bailed" then { icon: "✗", color: "#FF3B30" }
   elif .status == "stopped" then { icon: "■", color: "#FF9500" }
   else { icon: "?", color: "#8E8E93" } end) as $style |
  (if .pr_number > 0 then "  →PR#\(.pr_number)" else "" end) as $pr |
  (if .cost_usd > 0 then "  $\(.cost_usd | tostring | .[0:5])" else "" end) as $cost |
  (if .issue_number > 0 then "#\(.issue_number) " else "" end) as $issue |
  (if .name != null and .issue_number == 0 then "\(.name[:30]) " else "" end) as $jobname |
  (if .agent != null and .agent != "autopilot" then "[\(.agent)] " else "" end) as $agent |
  "\($style.icon) \($agent)\($issue)\($jobname)\(.issue_title[:40])\($pr)\($cost) | font=Menlo size=12 color=\($style.color)"'

# Show empty state.
if [ "$(echo "$TASKS" | jq 'length')" -eq 0 ]; then
  echo "No active jobs | size=12 color=#8E8E93"
fi

echo "---"

# Actions.
if [ "$PAUSED" = "true" ]; then
  echo "▶ Resume Budget | bash=curl param1=-sX param2=POST param3=http://$ADDR/resume terminal=false refresh=true color=#34C759"
fi

echo "↻ Refresh | refresh=true"
echo "---"
echo "■ Stop Daemon | bash=curl param1=-sX param2=POST param3=http://$ADDR/stop terminal=false refresh=true color=#FF3B30"
