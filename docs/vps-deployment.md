# VPS Deployment Guide

Deploy agent-minder watch mode on an Ubuntu VPS to continuously monitor GitHub issues and launch autopilot agents.

## Prerequisites

- **Ubuntu 22.04+** (other Debian-based distros may work)
- **Git** 2.30+
- **Go 1.25+** (for building from source)
- **Node.js 22+** (Claude Code CLI dependency)
- **Claude Code CLI** (authenticated)
- **GitHub personal access token** with `repo` scope

## Quick Install

The install script handles all dependencies, builds the binary, and sets up systemd:

```bash
sudo ./scripts/install-vps.sh
```

Or run remotely:

```bash
curl -sL https://raw.githubusercontent.com/aptx-health/agent-minder/main/scripts/install-vps.sh | sudo bash
```

After install, continue to [Configuration](#configuration).

## Manual Setup

### 1. Install Dependencies

```bash
# System packages
sudo apt update && sudo apt install -y git curl wget build-essential sqlite3 jq ufw

# Go 1.25+
wget https://go.dev/dl/go1.25.0.linux-amd64.tar.gz
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.25.0.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin' >> ~/.bashrc
source ~/.bashrc

# Node.js 22 (for Claude Code CLI)
curl -fsSL https://deb.nodesource.com/setup_22.x | sudo -E bash -
sudo apt install -y nodejs

# Claude Code CLI
sudo npm install -g @anthropic-ai/claude-code
```

### 2. Create Service User

```bash
sudo useradd -m -s /bin/bash minder
```

### 3. Build and Install

```bash
git clone https://github.com/aptx-health/agent-minder.git /tmp/agent-minder-src
cd /tmp/agent-minder-src
go build -o /usr/local/bin/agent-minder .
```

### 4. Authenticate Claude Code

This is interactive and must be done once as the service user:

```bash
sudo -u minder claude auth login
```

Follow the browser-based OAuth flow. The token is stored in the user's home directory and persists across restarts.

### 5. Set Up Directories

```bash
sudo -u minder mkdir -p /home/minder/.agent-minder/{worktrees,agents}
```

### 6. Clone Target Repo

Clone the repository you want agents to work on:

```bash
sudo -u minder git clone https://github.com/<owner>/<repo>.git /opt/agent-minder/repo
```

> **Note:** The `WorkingDirectory` in the systemd unit must point to this clone. Update the unit file if you use a different path.

## Configuration

### Environment File

Copy the example and fill in your secrets:

```bash
sudo mkdir -p /etc/agent-minder
sudo cp deploy/agent-minder.env.example /etc/agent-minder/agent-minder.env
sudo chmod 0600 /etc/agent-minder/agent-minder.env
sudo chown root:minder /etc/agent-minder/agent-minder.env
sudo nano /etc/agent-minder/agent-minder.env
```

**Required settings:**

| Variable | Description |
|----------|-------------|
| `GITHUB_TOKEN` | Personal access token with `repo` scope |
| `MINDER_API_KEY` | API key for the HTTP API (generate: `openssl rand -hex 32`) |
| `WATCH_MILESTONE` or `WATCH_LABEL` | GitHub filter for issue discovery |

**Optional settings:**

| Variable | Default | Description |
|----------|---------|-------------|
| `MAX_AGENTS` | 5 | Concurrent agents |
| `MAX_TURNS` | 150 | Max turns per agent |
| `MAX_BUDGET` | 10.00 | Per-agent budget (USD) |
| `TOTAL_BUDGET` | 50.00 | Total spend ceiling (USD) |
| `POLL_INTERVAL` | 300 | GitHub poll interval (seconds) |
| `SERVE_ADDR` | :7749 | HTTP API bind address |

See `deploy/agent-minder.env.example` for the full list.

### Secrets Management with Doppler

For production deployments, consider using [Doppler](https://www.doppler.com/) instead of a flat env file:

```bash
# Install Doppler CLI
sudo apt-get install -y apt-transport-https
curl -sLf --retry 3 --tlsv1.2 --proto "=https" \
  'https://packages.doppler.com/public/cli/gpg.DE2A7741A397C129.key' | \
  sudo gpg --dearmor -o /usr/share/keyrings/doppler-archive-keyring.gpg
echo "deb [signed-by=/usr/share/keyrings/doppler-archive-keyring.gpg] https://packages.doppler.com/public/cli/deb/debian any-version main" | \
  sudo tee /etc/apt/sources.list.d/doppler-cli.list
sudo apt-get update && sudo apt-get install -y doppler

# Authenticate
doppler login

# Run with Doppler-injected secrets
doppler run -- agent-minder deploy watch --milestone "v1.0" --serve :7749
```

Update the systemd unit's `ExecStart` to use `doppler run --` as a prefix.

## systemd Services

### Install Service Units

```bash
sudo cp deploy/agent-minder-daemon.service /etc/systemd/system/
sudo cp deploy/agent-minder-discord.service /etc/systemd/system/
sudo systemctl daemon-reload
```

### Start the Daemon

```bash
# Enable and start
sudo systemctl enable --now agent-minder-daemon

# Check status
sudo systemctl status agent-minder-daemon

# View logs
journalctl -u agent-minder-daemon -f
```

### Start the Discord Bot (Optional)

Requires `DISCORD_BOT_TOKEN`, `DISCORD_CHANNEL_ID`, and `DISCORD_GUILD_ID` in the env file. See [docs/discord-setup.md](discord-setup.md) for full setup.

```bash
sudo systemctl enable --now agent-minder-discord
```

### Service Commands

```bash
# Restart after config changes
sudo systemctl restart agent-minder-daemon

# Stop gracefully
sudo systemctl stop agent-minder-daemon

# View recent logs
journalctl -u agent-minder-daemon --since "1 hour ago"

# View Discord bot logs
journalctl -u agent-minder-discord -f
```

## Firewall

### UFW Basics

```bash
# Enable firewall (SSH is usually already allowed)
sudo ufw allow OpenSSH
sudo ufw enable

# Allow API access from a specific IP
sudo ufw allow from 203.0.113.10 to any port 7749

# Allow API access from a subnet
sudo ufw allow from 10.0.0.0/8 to any port 7749

# Check rules
sudo ufw status numbered
```

### Tailscale (Recommended)

[Tailscale](https://tailscale.com/) provides secure access without opening ports publicly:

```bash
# Install Tailscale
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up

# The daemon is now accessible at your Tailscale IP
# No UFW rules needed — traffic goes over the Tailscale mesh
curl http://100.x.y.z:7749/status
```

Benefits:
- No ports exposed to the public internet
- Automatic TLS between nodes
- Access control via Tailscale ACLs
- Works behind NAT without port forwarding

### Bind to Localhost Only

If you only access the API via Tailscale or SSH tunneling, bind to localhost:

```bash
# In agent-minder.env:
SERVE_ADDR=127.0.0.1:7749
```

## Monitoring

### Health Check

The daemon exposes a `/status` endpoint:

```bash
curl -H "Authorization: Bearer $MINDER_API_KEY" http://localhost:7749/status
```

### Useful journalctl Queries

```bash
# Follow live logs
journalctl -u agent-minder-daemon -f

# Errors only
journalctl -u agent-minder-daemon -p err

# Logs from today
journalctl -u agent-minder-daemon --since today

# JSON output for parsing
journalctl -u agent-minder-daemon -o json --since "1 hour ago" | jq .
```

### Remote Status

From your local machine:

```bash
# Via CLI
agent-minder deploy list --remote vps:7749 --api-key "$MINDER_API_KEY"
agent-minder deploy status <deploy-id> --remote vps:7749 --api-key "$MINDER_API_KEY"

# Or set defaults
export MINDER_REMOTE=vps:7749
export MINDER_API_KEY=your-key
agent-minder deploy list
```

### Metrics Endpoint

```bash
curl -H "Authorization: Bearer $MINDER_API_KEY" http://localhost:7749/metrics
```

Returns task counts, costs, and budget utilization — useful for Prometheus scraping or custom dashboards.

## Log Rotation

journald handles its own rotation for service logs. For agent logs in `~/.agent-minder/agents/`, install the logrotate config:

```bash
sudo cp deploy/logrotate.d/agent-minder /etc/logrotate.d/
```

This rotates agent logs daily, keeping 14 days compressed.

## Updating

```bash
# Pull latest code
cd /opt/agent-minder/repo
git pull --ff-only

# Rebuild
go build -o /usr/local/bin/agent-minder .

# Restart services
sudo systemctl restart agent-minder-daemon
sudo systemctl restart agent-minder-discord  # if running
```

For zero-downtime updates, stop the daemon gracefully (it waits for running agents to finish):

```bash
sudo systemctl stop agent-minder-daemon
# Wait for agents to complete (check logs)
# Then rebuild and restart
```

## Troubleshooting

### Claude Code Auth Expired

**Symptom:** Agents fail immediately with authentication errors.

**Fix:**
```bash
sudo -u minder claude auth login
sudo systemctl restart agent-minder-daemon
```

### Disk Full

**Symptom:** SQLite errors, agent failures, git clone failures.

**Fix:**
```bash
# Check disk usage
df -h
du -sh /home/minder/.agent-minder/

# Clean up old agent logs
find /home/minder/.agent-minder/agents/ -name "*.log" -mtime +7 -delete

# Clean up stale worktrees
sudo -u minder git -C /opt/agent-minder/repo worktree prune
```

### Git Conflicts in Worktrees

**Symptom:** Agent bails with merge conflict errors.

**Fix:** The autopilot agents handle rebasing internally. If worktrees are left in a broken state:

```bash
# List worktrees
ls /home/minder/.agent-minder/worktrees/

# Remove a specific stale worktree
sudo -u minder git -C /opt/agent-minder/repo worktree remove \
    /home/minder/.agent-minder/worktrees/<project>/issue-<N> --force
```

### Daemon Won't Start (Stale PID)

**Symptom:** "deploy already running" error but no process exists.

**Fix:**
```bash
# Use the built-in respawn command
agent-minder deploy respawn <deploy-id>

# Or manually clean up
rm /home/minder/.agent-minder/deploy/<deploy-id>.pid
sudo systemctl start agent-minder-daemon
```

### Database Locked

**Symptom:** "database is locked" errors in logs.

**Fix:** SQLite WAL mode handles most concurrency. If persistent:

```bash
# Stop all services
sudo systemctl stop agent-minder-daemon agent-minder-discord

# Check for stale WAL files
ls -la /home/minder/.agent-minder/minder.db*

# agent-minder auto-recovers stale WAL on next start
sudo systemctl start agent-minder-daemon
```

### Connection Refused on API

**Symptom:** `curl: (7) Failed to connect to localhost port 7749`

**Check:**
```bash
# Is the service running?
sudo systemctl status agent-minder-daemon

# Is it listening?
ss -tlnp | grep 7749

# Check the bind address in env file
grep SERVE_ADDR /etc/agent-minder/agent-minder.env
```

### High Memory Usage

Agent processes (Claude Code CLI + Node.js) can be memory-intensive. For VPSes with limited RAM:

```bash
# Reduce concurrent agents
# In /etc/agent-minder/agent-minder.env:
MAX_AGENTS=2

# Add swap if needed (4GB example)
sudo fallocate -l 4G /swapfile
sudo chmod 600 /swapfile
sudo mkswap /swapfile
sudo swapon /swapfile
echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
```

## Recommended VPS Specs

| Workload | vCPUs | RAM | Disk | Notes |
|----------|-------|-----|------|-------|
| 1-2 agents | 2 | 4 GB | 40 GB | Minimum viable |
| 3-5 agents | 4 | 8 GB | 80 GB | Recommended |
| 5-10 agents | 8 | 16 GB | 160 GB | Heavy workloads |

Each agent runs a Claude Code CLI process (~300-500 MB RSS) plus git operations. Budget for ~1 GB per concurrent agent plus base system overhead.
