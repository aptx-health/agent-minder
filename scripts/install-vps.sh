#!/usr/bin/env bash
#
# agent-minder VPS install script
#
# Installs agent-minder and its dependencies on Ubuntu 22.04+.
# Run as root or with sudo.
#
# Usage:
#   curl -sL https://raw.githubusercontent.com/aptx-health/agent-minder/main/scripts/install-vps.sh | sudo bash
#
# Or clone and run locally:
#   sudo ./scripts/install-vps.sh
#
set -euo pipefail

# --- Configuration ---
MINDER_USER="${MINDER_USER:-minder}"
MINDER_HOME="/home/${MINDER_USER}"
DEPLOY_ASSETS="/opt/agent-minder/deploy"
GO_VERSION="${GO_VERSION:-1.25.0}"
NODE_VERSION="${NODE_VERSION:-22}"

# --- Colors ---
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC}  $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*" >&2; }

# --- Checks ---
if [[ $EUID -ne 0 ]]; then
    error "This script must be run as root (or with sudo)."
    exit 1
fi

if ! grep -qi 'ubuntu' /etc/os-release 2>/dev/null; then
    warn "This script is designed for Ubuntu 22.04+. Proceed with caution on other distros."
fi

info "=== agent-minder VPS installer ==="

# --- Step 1: System packages ---
info "Installing system dependencies..."
apt-get update -qq
apt-get install -y -qq git curl wget build-essential sqlite3 jq ufw

# --- Step 2: Create service user ---
if id "${MINDER_USER}" &>/dev/null; then
    info "User '${MINDER_USER}' already exists."
else
    info "Creating user '${MINDER_USER}'..."
    useradd -m -s /bin/bash "${MINDER_USER}"
fi

# --- Step 3: Install Go ---
if command -v go &>/dev/null && go version | grep -q "go${GO_VERSION}"; then
    info "Go ${GO_VERSION} already installed."
else
    info "Installing Go ${GO_VERSION}..."
    ARCH=$(dpkg --print-architecture)
    wget -q "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz" -O /tmp/go.tar.gz
    rm -rf /usr/local/go
    tar -C /usr/local -xzf /tmp/go.tar.gz
    rm /tmp/go.tar.gz

    # Add to system path
    cat > /etc/profile.d/go.sh << 'GOEOF'
export PATH=$PATH:/usr/local/go/bin
export GOPATH=$HOME/go
export PATH=$PATH:$GOPATH/bin
GOEOF
    source /etc/profile.d/go.sh
fi
export PATH=$PATH:/usr/local/go/bin

# --- Step 4: Install Node.js (for Claude Code CLI) ---
if command -v node &>/dev/null; then
    info "Node.js already installed: $(node --version)"
else
    info "Installing Node.js ${NODE_VERSION}.x..."
    curl -fsSL "https://deb.nodesource.com/setup_${NODE_VERSION}.x" | bash -
    apt-get install -y -qq nodejs
fi

# --- Step 5: Install Claude Code CLI ---
if command -v claude &>/dev/null; then
    info "Claude Code CLI already installed."
else
    info "Installing Claude Code CLI..."
    npm install -g @anthropic-ai/claude-code
fi

# --- Step 6: Clone and build agent-minder ---
info "Building agent-minder..."
BUILD_DIR=$(mktemp -d)
trap "rm -rf ${BUILD_DIR}" EXIT
git clone --depth 1 https://github.com/aptx-health/agent-minder.git "${BUILD_DIR}"
export PATH=$PATH:/usr/local/go/bin
cd "${BUILD_DIR}"
go build -o /usr/local/bin/agent-minder .
chmod +x /usr/local/bin/agent-minder

# Copy deploy assets for later steps
mkdir -p /opt/agent-minder
cp -r "${BUILD_DIR}/deploy" "${DEPLOY_ASSETS}"

# --- Step 7: Set up directories ---
info "Setting up directories..."
sudo -u "${MINDER_USER}" mkdir -p "${MINDER_HOME}/.agent-minder/worktrees"
sudo -u "${MINDER_USER}" mkdir -p "${MINDER_HOME}/.agent-minder/agents"

# --- Step 8: Install configuration ---
info "Setting up configuration..."
mkdir -p /etc/agent-minder
if [[ ! -f /etc/agent-minder/agent-minder.env ]]; then
    cp "${DEPLOY_ASSETS}/agent-minder.env.example" /etc/agent-minder/agent-minder.env
    chmod 0600 /etc/agent-minder/agent-minder.env
    chown root:"${MINDER_USER}" /etc/agent-minder/agent-minder.env
    warn "Created /etc/agent-minder/agent-minder.env — edit this file with your secrets!"
else
    info "Environment file already exists, skipping."
fi

# --- Step 9: Install systemd units ---
info "Installing systemd service units..."
cp "${DEPLOY_ASSETS}/agent-minder-daemon.service" /etc/systemd/system/
cp "${DEPLOY_ASSETS}/agent-minder-discord.service" /etc/systemd/system/
systemctl daemon-reload

# --- Step 10: Install logrotate config ---
info "Installing logrotate config..."
cp "${DEPLOY_ASSETS}/logrotate.d/agent-minder" /etc/logrotate.d/agent-minder

# --- Step 11: Firewall ---
info "Configuring firewall..."
if ufw status | grep -q "inactive"; then
    warn "UFW is inactive. Enable it with: ufw enable"
fi
# Don't open the API port by default — user should configure access
info "API port (7749) is NOT opened by default."
info "To allow access from specific IPs:  ufw allow from <IP> to any port 7749"
info "Or use Tailscale for secure access without opening ports."

# --- Done ---
echo ""
info "=== Installation complete ==="
echo ""
echo "Next steps:"
echo "  1. Edit /etc/agent-minder/agent-minder.env with your secrets"
echo "  2. Authenticate Claude Code (one-time, as the minder user):"
echo "       sudo -u ${MINDER_USER} claude auth login"
echo "  3. Clone your target repo into /opt/agent-minder/repo (or update WorkingDirectory)"
echo "  4. Start the daemon:"
echo "       systemctl enable --now agent-minder-daemon"
echo "  5. Check logs:"
echo "       journalctl -u agent-minder-daemon -f"
echo ""
echo "Optional: Start the Discord bot:"
echo "  systemctl enable --now agent-minder-discord"
echo ""
echo "See docs/vps-deployment.md for the full guide."
