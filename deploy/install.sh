#!/usr/bin/env bash
#
# Install agent-minder v2 on an Ubuntu VPS.
#
# Prerequisites:
#   - Go 1.25+ installed
#   - git, gh CLI installed
#   - Claude Code CLI installed and authenticated
#   - GITHUB_TOKEN available
#
# Usage:
#   curl -sL <raw-url>/deploy/install.sh | bash
#   # or
#   ./deploy/install.sh
#
set -euo pipefail

INSTALL_DIR="/opt/agent-minder"
CONFIG_DIR="/etc/agent-minder"
REPO_DIR="${INSTALL_DIR}/repos"
USER="minder"
GROUP="minder"

echo "=== agent-minder v2 installer ==="

# --- Check prerequisites ---
for cmd in go git gh claude; do
    if ! command -v "$cmd" &>/dev/null; then
        echo "ERROR: $cmd is required but not found in PATH" >&2
        exit 1
    fi
done

echo "Prerequisites OK: go, git, gh, claude"

# --- Create user ---
if ! id "$USER" &>/dev/null; then
    echo "Creating user: $USER"
    sudo useradd --system --create-home --shell /bin/bash "$USER"
else
    echo "User $USER already exists"
fi

# --- Create directories ---
echo "Creating directories..."
sudo mkdir -p "$INSTALL_DIR" "$REPO_DIR" "$CONFIG_DIR"
sudo mkdir -p "/home/$USER/.agent-minder"
sudo mkdir -p "/home/$USER/.claude"
sudo chown -R "$USER:$GROUP" "$INSTALL_DIR" "/home/$USER/.agent-minder" "/home/$USER/.claude"

# --- Build and install binary ---
echo "Building minder..."
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SOURCE_DIR="$(dirname "$SCRIPT_DIR")"

if [[ -f "$SOURCE_DIR/go.mod" ]]; then
    # Building from source checkout
    (cd "$SOURCE_DIR" && go build -o /tmp/minder ./cmd/minder)
    sudo mv /tmp/minder /usr/local/bin/minder
    sudo chmod 755 /usr/local/bin/minder
    echo "Installed binary: /usr/local/bin/minder"
else
    echo "ERROR: Run this script from the agent-minder repo" >&2
    exit 1
fi

# --- Install scripts and configs ---
echo "Installing service files..."
sudo cp "$SCRIPT_DIR/start-daemon.sh" "$INSTALL_DIR/start-daemon.sh"
sudo chmod 755 "$INSTALL_DIR/start-daemon.sh"

sudo cp "$SCRIPT_DIR/agent-minder-daemon.service" /etc/systemd/system/agent-minder.service

if [[ -f "$SCRIPT_DIR/logrotate.d/agent-minder" ]]; then
    sudo cp "$SCRIPT_DIR/logrotate.d/agent-minder" /etc/logrotate.d/agent-minder
fi

# --- Environment file ---
if [[ ! -f "$CONFIG_DIR/agent-minder.env" ]]; then
    sudo cp "$SCRIPT_DIR/agent-minder.env.example" "$CONFIG_DIR/agent-minder.env"
    sudo chmod 600 "$CONFIG_DIR/agent-minder.env"
    sudo chown root:"$GROUP" "$CONFIG_DIR/agent-minder.env"
    echo ""
    echo "IMPORTANT: Edit $CONFIG_DIR/agent-minder.env with your settings:"
    echo "  sudo nano $CONFIG_DIR/agent-minder.env"
    echo ""
    echo "Required:"
    echo "  - GITHUB_TOKEN"
    echo "  - MINDER_API_KEY (generate with: openssl rand -hex 32)"
    echo "  - WATCH_FILTER (e.g., label:agent-ready)"
else
    echo "Environment file already exists: $CONFIG_DIR/agent-minder.env"
fi

# --- Reload systemd ---
sudo systemctl daemon-reload

echo ""
echo "=== Installation complete ==="
echo ""
echo "Next steps:"
echo "  1. Clone your repo(s) into $REPO_DIR"
echo "     sudo -u $USER git clone <repo-url> $REPO_DIR/<name>"
echo ""
echo "  2. Run enrollment on each repo:"
echo "     sudo -u $USER minder enroll $REPO_DIR/<name>"
echo ""
echo "  3. Edit the environment file:"
echo "     sudo nano $CONFIG_DIR/agent-minder.env"
echo ""
echo "  4. Start the service:"
echo "     sudo systemctl enable agent-minder"
echo "     sudo systemctl start agent-minder"
echo ""
echo "  5. Check status:"
echo "     sudo systemctl status agent-minder"
echo "     journalctl -u agent-minder -f"
echo "     curl -H 'X-API-Key: <key>' http://localhost:7749/status"
