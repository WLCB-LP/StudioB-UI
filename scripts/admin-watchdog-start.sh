#!/usr/bin/env bash
set -euo pipefail

# Start the watchdog service (if installed).
# Called by stub-engine (unprivileged) via sudo NOPASSWD rule.

# Ensure sudo is available non-interactively (required when run from the UI).
if ! sudo -n true 2>/dev/null; then
  echo "[admin-watchdog-start][ERROR] sudo requires a password (NOPASSWD not configured)." >&2
  exit 1
fi

echo "[admin-watchdog-start] starting stub-ui-watchdog"
sudo -n systemctl start stub-ui-watchdog || true

echo "[admin-watchdog-start] status"
sudo -n systemctl --no-pager --full status stub-ui-watchdog || true

echo "[admin-watchdog-start] done"
