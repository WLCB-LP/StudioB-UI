#!/usr/bin/env bash
set -euo pipefail

# If invoked via sudo, SUDO_USER is the original user.
# We treat that as the "application" user so git operations don't create
# root-owned files in the working tree.
APP_USER="${SUDO_USER:-$(id -un)}"

# StudioB-UI admin update script
# Called by stub-engine (unprivileged). This script performs a fast-forward
# update of the repo and then runs the full installer as root via sudo.
#
# NOTE: For this to work non-interactively, install_full.sh writes a sudoers
# rule allowing ${APP_USER} to run install_full.sh without a password prompt.

###############################################
# Repo discovery
#
# This script can be executed from:
#   * The git working tree (e.g. /home/wlcb/devel/StudioB-UI)
#   * A *runtime* copy (e.g. /home/wlcb/.StudioB-UI/runtime/current/scripts)
#
# The update mechanism needs a *git working tree* to fetch tags and check out
# the target version.
###############################################

THIS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# If invoked via sudo, the "real" user is in SUDO_USER.
APP_USER="${SUDO_USER:-$(id -un)}"

discover_repo_dir() {
  local candidate

  # 1) If the script is running from the repo, the parent dir is the repo.
  candidate="$(cd "${THIS_DIR}/.." && pwd)"
  if [ -d "${candidate}/.git" ]; then
    echo "${candidate}"
    return 0
  fi

  # 2) If a git-sync env file exists, use its GIT_SYNC_DIR.
  if [ -f /etc/stub-ui-watch.env ]; then
    # shellcheck disable=SC1091
    . /etc/stub-ui-watch.env
    if [ -n "${GIT_SYNC_DIR:-}" ] && [ -d "${GIT_SYNC_DIR}/.git" ]; then
      echo "${GIT_SYNC_DIR}"
      return 0
    fi
  fi

  # 3) Common fallback paths.
  for candidate in \
    "/home/${APP_USER}/devel/StudioB-UI" \
    "/home/${APP_USER}/.StudioB-UI/git-sync" \
    "/home/wlcb/devel/StudioB-UI" \
    "/home/wlcb/.StudioB-UI/git-sync"; do
    if [ -d "${candidate}/.git" ]; then
      echo "${candidate}"
      return 0
    fi
  done

  return 1
}

if ! REPO_DIR="$(discover_repo_dir)"; then
  echo "[admin-update][ERROR] Could not locate a git repo working tree." >&2
  echo "[admin-update][ERROR] This script must run from a git checkout (not just runtime/current)." >&2
  exit 1
fi

SCRIPT_DIR="${REPO_DIR}/scripts"

log() { echo "[admin-update] $*"; }

log "Starting admin update in ${REPO_DIR} as ${APP_USER}"

git_cmd() {
  if [[ "$(id -u)" -eq 0 ]]; then
    sudo -u "${APP_USER}" -H git -C "${REPO_DIR}" "$@"
  else
    git -C "${REPO_DIR}" "$@"
  fi
}

run_root() {
  if [[ "$(id -u)" -eq 0 ]]; then
    bash "$@"
  else
    sudo -n bash "$@"
  fi
}

# Ensure sudo is available non-interactively (if we are not already root).
if [[ "$(id -u)" -ne 0 ]]; then
  if ! sudo -n true 2>/dev/null; then
    echo "[admin-update][ERROR] sudo requires a password (NOPASSWD not configured). Re-run install.sh or check /etc/sudoers.d/studiob-ui" >&2
    exit 1
  fi
fi

# Ensure we can see the latest tags/commits.
log "Fetching origin..."
git_cmd fetch origin --prune --tags

log "Fast-forwarding main..."
git_cmd checkout -q main
git_cmd pull --ff-only origin main



# If this script is executed from inside the stub-engine systemd unit, systemd
# sandboxing (ProtectSystem=full) can make /etc read-only for *this entire
# process tree*, even when running as root via sudo.
#
# The full installer needs to update files under /etc (env files, sudoers, nginx,
# systemd units). If /etc is read-only, we re-run the installer in a transient
# systemd unit (systemd-run), which is not constrained by the stub-engine unit.
needs_unconfined_install() {
  local testfile
  testfile="/etc/.studiob-ui-write-test.$$"
  if touch "${testfile}" 2>/dev/null; then
    rm -f "${testfile}" >/dev/null 2>&1 || true
    return 1  # no, we do NOT need unconfined
  fi
  return 0    # yes, we need unconfined
}

run_install_full() {
  if needs_unconfined_install; then
    log "Detected read-only /etc (likely systemd sandbox). Running install_full.sh via systemd-run..."
    if command -v systemd-run >/dev/null 2>&1; then
      systemd-run --quiet --collect --wait --pipe         --unit=studiob-ui-install-full         /bin/bash "${SCRIPT_DIR}/install_full.sh"
      return $?
    fi
    echo "[admin-update][ERROR] /etc is read-only and systemd-run is not available." >&2
    return 1
  fi

  log "Running full installer (root)..."
  run_root "${SCRIPT_DIR}/install_full.sh"
}


run_install_full

log "Admin update complete."
