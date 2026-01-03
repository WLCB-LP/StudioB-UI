#!/usr/bin/env bash
set -euo pipefail

# StudioB-UI admin action: start (and enable) the watchdog.
#
# Why "enable --now"?
# - If an operator uses the CLI to "disable" the unit, a plain `systemctl start`
#   will run it *once* but the next reboot will leave it off.
# - The UI button is intended to mean "turn it on".
#
# IMPORTANT:
# - Do NOT swallow errors. The engine/UI needs real failures so it can surface
#   them to the operator.

UNIT="stub-ui-watchdog.service"

echo "[admin-watchdog-start] enabling + starting ${UNIT}"

# This script is invoked via sudo by stub-engine (through stub-ui-admin.sh).
# We're already root here, but we keep commands explicit for clarity.
systemctl daemon-reload
systemctl enable --now "${UNIT}"

echo "[admin-watchdog-start] ${UNIT} is-enabled=$(systemctl is-enabled "${UNIT}" 2>/dev/null || true) is-active=$(systemctl is-active "${UNIT}" 2>/dev/null || true)"

# v0.2.39
# When performing a restart, record reason to watchdog/LAST_RESTART_REASON
# This is used by the UI to display restart reasons inline with systemd status.
