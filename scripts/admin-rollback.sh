#!/usr/bin/env bash
set -euo pipefail

# Rollback by switching runtime "current" symlink to an existing release directory.
# Usage: admin-rollback.sh <release-dir-name>
#
# Called by stub-engine (unprivileged). Requires NOPASSWD sudoers rules.

if [[ $# -lt 1 ]]; then
  echo "usage: admin-rollback.sh <release>"
  echo "example: 20251230-153000-v0.1.2"
  exit 2
fi

# Ensure sudo is available non-interactively (required when run from the UI).
if ! sudo -n true 2>/dev/null; then
  echo "[admin-rollback][ERROR] sudo requires a password (NOPASSWD not configured). Re-run install.sh or check /etc/sudoers.d/studiob-ui" >&2
  exit 1
fi

REL="$1"
RUNTIME_BASE="${RUNTIME_BASE:-/home/wlcb/.StudioB-UI/runtime}"
CURRENT_DIR="${CURRENT_DIR:-${RUNTIME_BASE}/current}"
RELEASES_DIR="${RELEASES_DIR:-${RUNTIME_BASE}/releases}"

TARGET="${RELEASES_DIR}/${REL}"
if [[ ! -d "${TARGET}" ]]; then
  echo "release not found: ${TARGET}"
  exit 1
fi

echo "[admin-rollback] switching current -> ${TARGET}"
sudo -n ln -sfn "${TARGET}" "${CURRENT_DIR}"

# Reload nginx not required; restart engine
sudo -n systemctl restart stub-engine

echo "[admin-rollback] done"
