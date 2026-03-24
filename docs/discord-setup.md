# Discord Bot Setup

This guide walks through setting up the agent-minder Discord bot for monitoring deployments.

## Prerequisites

- A running deploy daemon with `--serve` enabled
- A Discord server where you have "Manage Server" permissions

## 1. Create a Discord Application

1. Go to the [Discord Developer Portal](https://discord.com/developers/applications)
2. Click **New Application**, give it a name (e.g., "agent-minder")
3. Go to the **Bot** tab
4. Click **Reset Token** and copy the token — this is your `DISCORD_BOT_TOKEN`
5. Under **Privileged Gateway Intents**, no special intents are needed

## 2. Set Bot Permissions

Go to **OAuth2 → URL Generator**:

1. Under **Scopes**, select:
   - `bot`
   - `applications.commands`

2. Under **Bot Permissions**, select:
   - Send Messages
   - Embed Links
   - Use Slash Commands

3. Copy the generated URL

## 3. Invite the Bot

Open the generated URL in your browser and select the server to add the bot to.

## 4. Get Channel and Guild IDs

1. In Discord, go to **User Settings → Advanced → Developer Mode** (enable it)
2. Right-click the channel you want notifications in → **Copy Channel ID**
3. Right-click the server name → **Copy Server ID** (this is the Guild ID)

## 5. Configure and Run

### Environment variables

```bash
export DISCORD_BOT_TOKEN="your-bot-token"
export DISCORD_CHANNEL_ID="123456789012345678"
export DISCORD_GUILD_ID="987654321098765432"   # optional, for dev (faster command registration)
export MINDER_REMOTE="your-vps:7749"
export MINDER_API_KEY="your-api-key"           # if the daemon uses --api-key
```

### Run the bot

```bash
agent-minder discord
```

Or with explicit flags:

```bash
agent-minder discord \
  --remote vps:7749 \
  --token "$DISCORD_BOT_TOKEN" \
  --channel "$DISCORD_CHANNEL_ID" \
  --guild "$DISCORD_GUILD_ID" \
  --api-key "$MINDER_API_KEY"
```

The bot registers four slash commands: `/analysis`, `/status`, `/settings`, `/cost`.

> **Note:** Guild-scoped commands (with `--guild`) register instantly. Global commands (without `--guild`) can take up to an hour to propagate across Discord.

## 6. Push Notifications (Webhook)

For push notifications (task failures, bails, PRs), configure the deploy daemon's webhook to use a Discord webhook URL with the `discord` format.

### Create a Discord webhook

1. In your Discord server, go to **Server Settings → Integrations → Webhooks**
2. Click **New Webhook**
3. Select the notification channel
4. Copy the webhook URL

### Configure the project

Set the webhook on the project that the deploy daemon uses. The webhook fields in the `projects` table are:

- `webhook_url` — the Discord webhook URL
- `webhook_format` — set to `"discord"`
- `webhook_events` — comma-separated event types (empty = all)

Available event types:
- `task.started` — agent begins work
- `task.completed` — task done / PR merged
- `task.bailed` — agent gave up
- `task.failed` — agent hit limits or errors
- `task.stopped` — agent manually stopped
- `budget.limit` — budget ceiling warning
- `agent.error` — agent crash or LLM failure
- `task.discovered` — new task found
- `autopilot.finished` — all tasks complete

### Example: only notify on failures and PRs

```
webhook_events: task.bailed,task.failed,task.completed,budget.limit
```

## Slash Command Reference

| Command | Description |
|---------|-------------|
| `/analysis` | Trigger a live LLM analysis — shows a "thinking..." indicator, then updates with the result (up to 3 min) |
| `/status` | Show all tasks grouped by status (running, review, queued, done, etc.) with issue numbers and PR links |
| `/settings` | Show autopilot configuration (max agents, turns, budget, analyzer model, etc.) — ephemeral (only you see it) |
| `/cost` | Show daily, weekly, and total spend with budget utilization bar and success rate |
