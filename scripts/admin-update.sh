#!/usr/bin/env bash
set -euo pipefail

REPO_DIR="${REPO_DIR:-/home/wlcb/devel/StudioB-UI}"
cd "${REPO_DIR}"

echo "[admin-update] repo=${REPO_DIR}"

git fetch --all --tags
BRANCH="$(git rev-parse --abbrev-ref HEAD)"
echo "[admin-update] branch=${BRANCH}"

git pull --ff-only

# Build + stage into /opt via installer
if command -v sudo >/dev/null 2>&1; then
  sudo bash scripts/install_full.sh
else
  bash scripts/install_full.sh
fi

# Restart service (installer usually does)
if command -v systemctl >/dev/null 2>&1; then
  sudo systemctl restart stub-engine || true
fi
