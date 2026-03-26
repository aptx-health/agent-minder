# macOS LaunchAgent Setup

Run agent-minder deploy watch as a background service on macOS, managed by launchd.

## Prerequisites

- **macOS 13+ (Ventura)** — older versions work but won't show the Background Items notification
- **Homebrew** — for installing Go, Node.js, GitHub CLI
- **Go 1.25+** — `brew install go`
- **Node.js 22+** — `brew install node`
- **Claude Code CLI** — `npm install -g @anthropic-ai/claude-code` (must be authenticated)
- **GitHub CLI** — `brew install gh`
- **GitHub personal access token** with `repo` scope

## Quick Install

```bash
git clone https://github.com/aptx-health/agent-minder.git
cd agent-minder
./scripts/install-macos.sh
```

Then edit your env file and load the agent (see [Configuration](#configuration)).

## Manual Setup

### 1. Build and Install

```bash
cd /path/to/agent-minder
go build -o ~/go/bin/agent-minder .
```

### 2. Create Directories

```bash
mkdir -p ~/.agent-minder/deploy
mkdir -p ~/.agent-minder/worktrees
mkdir -p ~/.agent-minder/agents
```

### 3. Configure Environment

```bash
cp deploy/agent-minder.env.macos.example ~/.agent-minder/deploy/agent-minder.env
chmod 0600 ~/.agent-minder/deploy/agent-minder.env
$EDITOR ~/.agent-minder/deploy/agent-minder.env
```

**Required settings:**

| Variable | Description |
|----------|-------------|
| `GITHUB_TOKEN` | Personal access token with `repo` scope |
| `AGENT_MINDER_REPO` | Absolute path to the git repo agents will work on |
| `WATCH_MILESTONE` or `WATCH_LABEL` | GitHub filter for issue discovery |

See `deploy/agent-minder.env.macos.example` for the full list of options.

### 4. Install Wrapper Scripts

```bash
cp deploy/start-daemon-macos.sh ~/.agent-minder/deploy/
chmod +x ~/.agent-minder/deploy/start-daemon-macos.sh
```

### 5. Install LaunchAgent Plist

The plist template uses `__HOME__` as a placeholder (launchd can't expand `$HOME`). Replace it with your actual home directory:

```bash
sed "s|__HOME__|${HOME}|g" deploy/com.dustinlange.agent-minder.plist.template \
    > ~/Library/LaunchAgents/com.dustinlange.agent-minder.plist
```

### 6. Load the Agent

```bash
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.dustinlange.agent-minder.plist
```

> **Note:** macOS will show a "agent-minder wants to run in the background" notification. Allow it in **System Settings > General > Login Items & Extensions**.

## Managing the Service

Modern `launchctl` commands (the `load`/`unload` forms are deprecated):

```bash
# Check status
launchctl print gui/$(id -u)/com.dustinlange.agent-minder

# Force restart (kill + auto-restart via KeepAlive)
launchctl kickstart -k gui/$(id -u)/com.dustinlange.agent-minder

# Stop and unload
launchctl bootout gui/$(id -u)/com.dustinlange.agent-minder

# Reload after plist changes
launchctl bootout gui/$(id -u)/com.dustinlange.agent-minder 2>/dev/null
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.dustinlange.agent-minder.plist
```

### Monitoring

```bash
# LaunchAgent logs (startup, crashes)
tail -f ~/.agent-minder/launchd-daemon.log

# Deploy status via CLI
agent-minder deploy list
agent-minder deploy status <deploy-id>

# If --serve is enabled, HTTP API
curl -H "Authorization: Bearer $MINDER_API_KEY" http://localhost:7749/status
```

### Updating

```bash
# Stop the service
launchctl bootout gui/$(id -u)/com.dustinlange.agent-minder

# Rebuild
cd /path/to/agent-minder
git pull
go build -o ~/go/bin/agent-minder .

# Reload
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.dustinlange.agent-minder.plist
```

## Discord Bot (Optional)

The Discord bot runs embedded in the daemon process — no separate service needed. To enable it:

1. Set `SERVE_ADDR` in your env file (e.g., `SERVE_ADDR=:7749`)
2. Configure Discord credentials via `agent-minder setup` (stored in keychain) or set `DISCORD_BOT_TOKEN` and `DISCORD_CHANNEL_ID` in the env file
3. Restart the service — the bot starts automatically when credentials are detected

The standalone `agent-minder discord` command is still available for connecting to a remote daemon.

## How It Works

The LaunchAgent runs a wrapper script (`start-daemon-macos.sh`) that:

1. **Sets up PATH** — launchd provides only `/usr/bin:/bin:/usr/sbin:/sbin`, so the wrapper adds Homebrew, Go, Node, and user bin directories
2. **Sources the env file** — `~/.agent-minder/deploy/agent-minder.env`
3. **Handles restart recovery** — on first run, uses `--foreground` flag which creates the project in SQLite and runs the daemon inline. The deploy-id is saved to `~/.agent-minder/deploys/launchd-watch-id`. On subsequent restarts (crash recovery), uses `--daemon --deploy-id <saved-id>` to skip duplicate project creation
4. **Runs the daemon in foreground** — launchd owns the process lifecycle (no double-forking)

The plist uses:
- `KeepAlive` with `SuccessfulExit: false` — restarts on crash, stays dead on clean `deploy stop`
- `ProcessType: Background` — tells macOS to deprioritize CPU/IO scheduling
- `Nice: 10` — lower process priority (good macOS citizen)

## Troubleshooting

### Service won't start

```bash
# Check launchd status
launchctl print gui/$(id -u)/com.dustinlange.agent-minder

# Check logs
cat ~/.agent-minder/launchd-daemon.log

# Common causes:
# - Missing env file
# - GITHUB_TOKEN not set
# - AGENT_MINDER_REPO path doesn't exist
# - agent-minder binary not on PATH
```

### "agent-minder wants to run in the background" blocked

Go to **System Settings > General > Login Items & Extensions > Allow in the Background** and enable agent-minder.

### PATH issues (command not found)

launchd doesn't source your shell profile. If `claude`, `gh`, or `git` aren't found:

1. Check where they're installed: `which claude`
2. Add the directory to `PATH_EXTRA` in your env file:
   ```bash
   PATH_EXTRA=/some/custom/bin
   ```

### Claude Code auth expired

```bash
# Re-authenticate
claude auth login

# Restart the service
launchctl kickstart -k gui/$(id -u)/com.dustinlange.agent-minder
```

### Stale deploy-id after DB reset

If you've reset your database, the saved deploy-id will be invalid. The wrapper auto-detects this and starts fresh, but you can also clear it manually:

```bash
rm ~/.agent-minder/deploys/launchd-watch-id
launchctl kickstart -k gui/$(id -u)/com.dustinlange.agent-minder
```

### Fresh start (reset everything)

```bash
launchctl bootout gui/$(id -u)/com.dustinlange.agent-minder
rm -f ~/.agent-minder/deploys/launchd-watch-id
rm -f ~/.agent-minder/launchd-daemon.log
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.dustinlange.agent-minder.plist
```

## Menu Bar Status Indicator (xbar)

[xbar](https://xbarapp.com/) (formerly BitBar) can show a live agent-minder status indicator in your macOS menu bar.

### Install

```bash
brew install --cask xbar
```

### Set Up the Plugin

Symlink the plugin into xbar's plugins directory:

```bash
mkdir -p ~/Library/Application\ Support/xbar/plugins
ln -s /path/to/agent-minder/scripts/agent-minder.30s.sh \
      ~/Library/Application\ Support/xbar/plugins/agent-minder.30s.sh
```

Open xbar and the plugin will auto-detect. The `30s` in the filename controls the refresh interval.

### What It Shows

**Menu bar:** `🤖 2↑ 3✓ 1✗` — running agents, completed, errored

**Dropdown:**
- Per-deploy task list with status icons and costs
- Quick actions: view status, stop deploy, open logs
- Works across all active deployments
