#!/usr/bin/env bash
#
# agent-minder macOS LaunchAgent installer
#
# Sets up agent-minder deploy watch as a macOS background service.
# Does NOT start the service — you must configure and load it yourself.
#
# Usage:
#   ./scripts/install-macos.sh
#
set -euo pipefail

# --- Colors ---
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC}  $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*" >&2; }

# --- Checks ---
if [[ "$(uname -s)" != "Darwin" ]]; then
    error "This script is for macOS only."
    exit 1
fi

info "=== agent-minder macOS installer ==="

# --- Detect Homebrew prefix ---
if command -v brew &>/dev/null; then
    BREW_PREFIX="$(brew --prefix)"
    info "Homebrew detected at ${BREW_PREFIX}"
else
    warn "Homebrew not found. Some dependencies may need manual installation."
    BREW_PREFIX="/opt/homebrew"
fi

# --- Check prerequisites ---
MISSING=()
command -v git   &>/dev/null || MISSING+=("git")
command -v go    &>/dev/null || MISSING+=("go (1.25+)")
command -v node  &>/dev/null || MISSING+=("node (22+)")
command -v claude &>/dev/null || MISSING+=("claude (npm install -g @anthropic-ai/claude-code)")
command -v gh    &>/dev/null || MISSING+=("gh (GitHub CLI)")

if [[ ${#MISSING[@]} -gt 0 ]]; then
    warn "Missing prerequisites:"
    for dep in "${MISSING[@]}"; do
        echo "  - ${dep}"
    done
    echo ""
    warn "Install missing deps before loading the LaunchAgent."
fi

# --- Locate deploy assets ---
# The script may be run from the repo root or via a downloaded copy.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
DEPLOY_DIR="${REPO_ROOT}/deploy"

if [[ ! -f "${DEPLOY_DIR}/com.dustinlange.agent-minder.plist.template" ]]; then
    error "Cannot find deploy assets. Run this script from the agent-minder repo root."
    exit 1
fi

# --- Build agent-minder ---
if command -v agent-minder &>/dev/null; then
    info "agent-minder already installed: $(which agent-minder)"
    read -rp "Rebuild from source? [y/N] " rebuild
    if [[ "$rebuild" =~ ^[Yy] ]]; then
        info "Building agent-minder..."
        (cd "${REPO_ROOT}" && go build -o "${GOPATH:-$HOME/go}/bin/agent-minder" .)
        info "Installed to $(which agent-minder)"
    fi
else
    info "Building agent-minder..."
    (cd "${REPO_ROOT}" && go build -o "${GOPATH:-$HOME/go}/bin/agent-minder" .)
    info "Installed to ${GOPATH:-$HOME/go}/bin/agent-minder"
fi

# --- Create directories ---
info "Setting up directories..."
mkdir -p "${HOME}/.agent-minder/deploy"
mkdir -p "${HOME}/.agent-minder/worktrees"
mkdir -p "${HOME}/.agent-minder/agents"
mkdir -p "${HOME}/Library/LaunchAgents"

# --- Copy wrapper scripts ---
info "Installing wrapper scripts..."
cp "${DEPLOY_DIR}/start-daemon-macos.sh" "${HOME}/.agent-minder/deploy/"
cp "${DEPLOY_DIR}/start-discord-macos.sh" "${HOME}/.agent-minder/deploy/"
chmod +x "${HOME}/.agent-minder/deploy/start-daemon-macos.sh"
chmod +x "${HOME}/.agent-minder/deploy/start-discord-macos.sh"

# --- Copy env file (if not exists) ---
if [[ ! -f "${HOME}/.agent-minder/deploy/agent-minder.env" ]]; then
    cp "${DEPLOY_DIR}/agent-minder.env.macos.example" "${HOME}/.agent-minder/deploy/agent-minder.env"
    chmod 0600 "${HOME}/.agent-minder/deploy/agent-minder.env"
    warn "Created ~/.agent-minder/deploy/agent-minder.env — edit this file with your secrets!"
else
    info "Environment file already exists, skipping."
fi

# --- Generate plists (expand __HOME__) ---
info "Installing LaunchAgent plists..."
sed "s|__HOME__|${HOME}|g" "${DEPLOY_DIR}/com.dustinlange.agent-minder.plist.template" \
    > "${HOME}/Library/LaunchAgents/com.dustinlange.agent-minder.plist"

sed "s|__HOME__|${HOME}|g" "${DEPLOY_DIR}/com.dustinlange.agent-minder.discord.plist.template" \
    > "${HOME}/Library/LaunchAgents/com.dustinlange.agent-minder.discord.plist"

info "Plists installed to ~/Library/LaunchAgents/"

# --- Done ---
echo ""
info "=== Installation complete ==="
echo ""
echo "Next steps:"
echo ""
echo "  1. Edit your env file:"
echo "       \$EDITOR ~/.agent-minder/deploy/agent-minder.env"
echo "     Set: GITHUB_TOKEN, AGENT_MINDER_REPO, WATCH_MILESTONE (or WATCH_LABEL)"
echo ""
echo "  2. Verify Claude Code is authenticated:"
echo "       claude -p 'say hello' --output-format json"
echo ""
echo "  3. Load the LaunchAgent:"
echo "       launchctl bootstrap gui/\$(id -u) ~/Library/LaunchAgents/com.dustinlange.agent-minder.plist"
echo ""
echo "  4. Check status:"
echo "       launchctl print gui/\$(id -u)/com.dustinlange.agent-minder"
echo "       tail -f ~/.agent-minder/launchd-daemon.log"
echo "       agent-minder deploy list"
echo ""
echo "  To stop:    launchctl bootout gui/\$(id -u)/com.dustinlange.agent-minder"
echo "  To restart: launchctl kickstart -k gui/\$(id -u)/com.dustinlange.agent-minder"
echo ""
echo "  Optional — Discord bot (requires SERVE_ADDR + Discord tokens in env):"
echo "       launchctl bootstrap gui/\$(id -u) ~/Library/LaunchAgents/com.dustinlange.agent-minder.discord.plist"
echo ""
echo "See docs/macos-launchagent.md for the full guide."
