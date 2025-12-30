#!/usr/bin/env bash
set -euo pipefail

# Rollback by switching /opt/studiob-ui/current to an existing release directory.
# Usage: admin-rollback.sh <release-dir-name>

if [[ $# -lt 1 ]]; then
  echo "usage: admin-rollback.sh <release>"
  echo "example: 20251230-153000-v0.1.2"
  exit 2
fi

REL="$1"
RUNTIME_BASE="${RUNTIME_BASE:-/opt/studiob-ui}"
CURRENT_DIR="${CURRENT_DIR:-${RUNTIME_BASE}/current}"
RELEASES_DIR="${RELEASES_DIR:-${RUNTIME_BASE}/releases}"

TARGET="${RELEASES_DIR}/${REL}"
if [[ ! -d "${TARGET}" ]]; then
  echo "release not found: ${TARGET}"
  exit 1
fi

echo "[admin-rollback] switching current -> ${TARGET}"
sudo ln -sfn "${TARGET}" "${CURRENT_DIR}"

# Reload nginx not required; restart engine
sudo systemctl restart stub-engine

echo "[admin-rollback] done"
