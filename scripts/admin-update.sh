#!/usr/bin/env bash
set -euo pipefail

# StudioB-UI admin update script
# Called by stub-engine (unprivileged). This script performs a fast-forward
# update of the repo and then runs the full installer as root via sudo.
#
# NOTE: For this to work non-interactively, install_full.sh writes a sudoers
# rule allowing ${APP_USER} to run install_full.sh without a password prompt.

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT_DIR="${REPO_DIR}/scripts"
APP_USER="$(id -un)"

log() { echo "[admin-update] $*"; }

log "Starting admin update in ${REPO_DIR} as ${APP_USER}"

# Ensure sudo is available non-interactively.
if ! sudo -n true 2>/dev/null; then
  echo "[admin-update][ERROR] sudo requires a password (NOPASSWD not configured). Re-run install.sh or check /etc/sudoers.d/studiob-ui" >&2
  exit 1
fi

# Ensure we can see the latest tags/commits.
log "Fetching origin..."
git -C "${REPO_DIR}" fetch origin --prune --tags

log "Fast-forwarding main..."
git -C "${REPO_DIR}" checkout -q main
git -C "${REPO_DIR}" pull --ff-only origin main

log "Running full installer (root)..."
sudo -n bash "${SCRIPT_DIR}/install_full.sh"

log "Admin update complete."
