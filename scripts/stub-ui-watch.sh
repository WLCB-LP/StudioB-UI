#!/usr/bin/env bash
set -euo pipefail



: "${ALLOW_ROLLBACK:=0}"
# Load watcher environment (systemd EnvironmentFile is best-effort)
if [ -f /etc/stub-ui-watch.env ]; then
  set +u
  . /etc/stub-ui-watch.env
  set -u
fi

MODE="${MODE:-zip}"
SLEEP="${SLEEP:-5}"

TMP_DIR="${TMP_DIR:-/mnt/NAS/Engineering/Audio Network/Studio B/UI/tmp}"
ARCHIVE_DIR="${ARCHIVE_DIR:-/mnt/NAS/Engineering/Audio Network/Studio B/UI}"
STATE_DIR="${STATE_DIR:-/home/wlcb/.StudioB-UI/state}"
STATE_FILE="${STATE_DIR}/last_zip.sig"

REPO_DIR="${REPO_DIR:-/home/wlcb/devel/StudioB-UI}"
APP_USER="${APP_USER:-wlcb}"
APP_GROUP="${APP_GROUP:-wlcb}"

DEPLOY_TMP="${DEPLOY_TMP:-/tmp/stub-ui-deploy}"

GIT_SYNC_REMOTE="${GIT_SYNC_REMOTE:-}"
GIT_SYNC_BRANCH="${GIT_SYNC_BRANCH:-main}"
GIT_SYNC_AUTHOR_NAME="${GIT_SYNC_AUTHOR_NAME:-StudioB Watcher}"
GIT_SYNC_AUTHOR_EMAIL="${GIT_SYNC_AUTHOR_EMAIL:-watcher@localhost}"

log(){ echo "[watcher] $*"; }

mkdir -p "${STATE_DIR}"
touch "${STATE_FILE}"
shopt -s nullglob

zip_version() {
  # Try to read VERSION inside the zip without extracting the whole thing.
  # Returns empty string if missing.
  local zip="$1"
  unzip -p "$zip" VERSION 2>/dev/null | tr -d '\r\n[:space:]' || true
}

repo_version() {
  # Current dev repo VERSION (source of truth for "what we have now")
  if [[ -f "${REPO_DIR}/VERSION" ]]; then
    tr -d '\r\n[:space:]' < "${REPO_DIR}/VERSION"
  else
    echo ""
  fi
}

ver_to_sortkey() {
  # Converts versions like 0.1.11f into a sortable key:
  # major.minor.patch + optional suffix letters.
  # Output format: "0000000000.0000000000.0000000000|suffix"
  # so we can compare with string sort.
  local v="$1"
  local major=0 minor=0 patch=0 suffix=""
  # split numeric part and optional trailing letters
  if [[ "$v" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)([a-zA-Z]*)$ ]]; then
    major="${BASH_REMATCH[1]}"
    minor="${BASH_REMATCH[2]}"
    patch="${BASH_REMATCH[3]}"
    suffix="${BASH_REMATCH[4]}"
  elif [[ "$v" =~ ^([0-9]+)\.([0-9]+)([a-zA-Z]*)$ ]]; then
    major="${BASH_REMATCH[1]}"
    minor="${BASH_REMATCH[2]}"
    patch="0"
    suffix="${BASH_REMATCH[3]}"
  else
    # Unknown format: treat as 0.0.0 and keep suffix for determinism
    suffix="$v"
  fi
  printf "%010d.%010d.%010d|%s" "$major" "$minor" "$patch" "${suffix,,}"
}

ver_lt() {
  # returns 0 if $1 < $2
  local a b
  a="$(ver_to_sortkey "$1")"
  b="$(ver_to_sortkey "$2")"
  [[ "$a" < "$b" ]]
}


zip_sig() { sha256sum "$1" | awk '{print $1}'; }

# Find a plausible repo root in extracted content (handles zipball top folder)
find_repo_root() {
  local base="$1"
  # prefer a directory containing install.sh + engine/ + ui/ + scripts/
  local d
  while IFS= read -r -d '' d; do
    if [[ -f "$d/install.sh" && -d "$d/engine" && -d "$d/ui" && -d "$d/scripts" ]]; then
      echo "$d"
      return 0
    fi
  done < <(find "$base" -maxdepth 3 -type d -print0)
  # fallback: if base itself matches
  if [[ -f "$base/install.sh" && -d "$base/engine" && -d "$base/ui" && -d "$base/scripts" ]]; then
    echo "$base"
    return 0
  fi
  return 1
}

git_commit_push() {
  [[ -z "${GIT_SYNC_REMOTE}" ]] && { log "git-sync: disabled (GIT_SYNC_REMOTE empty)"; return 0; }

  # Ensure origin is correct (run as wlcb)
  sudo -u "${APP_USER}" git -C "${REPO_DIR}" remote get-url origin >/dev/null 2>&1 || \
    sudo -u "${APP_USER}" git -C "${REPO_DIR}" remote add origin "${GIT_SYNC_REMOTE}" || true

  local cur
  cur="$(sudo -u "${APP_USER}" git -C "${REPO_DIR}" remote get-url origin 2>/dev/null || true)"
  if [[ "${cur}" != "${GIT_SYNC_REMOTE}" ]]; then
    log "git-sync: setting origin -> ${GIT_SYNC_REMOTE}"
    sudo -u "${APP_USER}" git -C "${REPO_DIR}" remote set-url origin "${GIT_SYNC_REMOTE}"
  fi

  # Commit if changes
  sudo -u "${APP_USER}" git -C "${REPO_DIR}" add -A

  if sudo -u "${APP_USER}" git -C "${REPO_DIR}" diff --cached --quiet; then
    log "git-sync: no changes to commit"
    return 0
  fi

  local ver=""
  [[ -f "${REPO_DIR}/VERSION" ]] && ver="$(tr -d '\r\n[:space:]' < "${REPO_DIR}/VERSION" || true)"
  local msg="chore: import ZIP $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  [[ -n "${ver}" ]] && msg="chore: import ZIP (v${ver})"

  sudo -u "${APP_USER}" git -C "${REPO_DIR}" config user.name "${GIT_SYNC_AUTHOR_NAME}"
  sudo -u "${APP_USER}" git -C "${REPO_DIR}" config user.email "${GIT_SYNC_AUTHOR_EMAIL}"
  sudo -u "${APP_USER}" git -C "${REPO_DIR}" commit -m "${msg}"

  log "git-sync: pushing ${GIT_SYNC_BRANCH}"
  sudo -u "${APP_USER}" git -C "${REPO_DIR}" push origin "${GIT_SYNC_BRANCH}"
}

deploy_zip_to_dev() {
  local zip="$1"
  log "ZIP ingest: ${zip}"

  rm -rf "${DEPLOY_TMP}"
  mkdir -p "${DEPLOY_TMP}"
  unzip -q "$zip" -d "${DEPLOY_TMP}"

  # Safety: refuse downgrades unless explicitly allowed.
  local cur_ver zip_ver
  cur_ver="$(repo_version)"
  zip_ver="$(zip_version "$zip")"

  if [[ "${ALLOW_ROLLBACK}" != "1" && -n "${cur_ver}" && -n "${zip_ver}" ]]; then
    if ver_lt "${zip_ver}" "${cur_ver}"; then
      log "REFUSING ZIP rollback: zip=${zip_ver} < current=${cur_ver} (set ALLOW_ROLLBACK=1 to override)"
      return 0
    fi
  fi

  local src_root
  src_root="$(find_repo_root "${DEPLOY_TMP}")" || {
    log "ERROR: could not find repo root inside zip (needs install.sh + engine/ui/scripts)"
    return 2
  }

  # rsync into dev working tree (preserve .git and logs)
  mkdir -p "${REPO_DIR}"
  rsync -a --delete \
    --exclude='.git/' \
    --exclude='logs/' \
    "${src_root}/" "${REPO_DIR}/"

  # Normalize ownership for wlcb so git can operate
  chown -R "${APP_USER}:${APP_GROUP}" "${REPO_DIR}" || true

  # Commit/push to GitHub (as wlcb)
  git_commit_push

  # Archive ZIP after successful ingest + push attempt
  mkdir -p "${ARCHIVE_DIR}"
  mv -f "$zip" "${ARCHIVE_DIR}/"

  log "ZIP ingest complete"
}

pick_newest_zip() {
  local zips=( "${TMP_DIR}"/*.zip )
  (( ${#zips[@]} == 0 )) && return 1

  local newest="${zips[0]}"
  local newest_mtime
  newest_mtime="$(stat -c %Y "${newest}" 2>/dev/null || echo 0)"

  local z m
  for z in "${zips[@]}"; do
    m="$(stat -c %Y "$z" 2>/dev/null || echo 0)"
    if (( m > newest_mtime )); then
      newest="$z"
      newest_mtime="$m"
    fi
  done

  echo "${newest}"
}

while true; do
  if [[ "${MODE}" != "zip" ]]; then
    sleep "${SLEEP}"
    continue
  fi

  newest="$(pick_newest_zip || true)"
  if [[ -z "${newest:-}" ]]; then
    sleep "${SLEEP}"
    continue
  fi

  sig="$(zip_sig "${newest}")"
  last_sig="$(cat "${STATE_FILE}" 2>/dev/null || true)"

  if [[ -n "${sig}" && "${sig}" != "${last_sig}" ]]; then
    echo "${sig}" > "${STATE_FILE}"
    deploy_zip_to_dev "${newest}" || log "deploy failed (will retry on next change)"
  fi

  sleep "${SLEEP}"
done
