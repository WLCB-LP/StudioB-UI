#!/usr/bin/env bash
set -euo pipefail

# Polling-based deploy watcher (NAS-safe)
# Watches TMP_DIR for *.zip and deploys the newest zip when its content changes.
# Uses a SHA256 signature to allow re-deploying the "same filename" if rebuilt.

TMP_DIR="${TMP_DIR:-/mnt/NAS/Engineering/Audio Network/Studio B/UI/tmp}"
ARCHIVE_DIR="${ARCHIVE_DIR:-/mnt/NAS/Engineering/Audio Network/Studio B/UI}"
STATE_DIR="${STATE_DIR:-/home/wlcb/.StudioB-UI/state}"
STATE_FILE="${STATE_DIR}/last_zip.sig"
SLEEP="${SLEEP:-5}"

REPO_DIR="${REPO_DIR:-/home/wlcb/devel/StudioB-UI}"
DEPLOY_TMP="${DEPLOY_TMP:-/tmp/stub-ui-deploy}"

log() { echo "[watcher] $*"; }

mkdir -p "${STATE_DIR}"
touch "${STATE_FILE}"

shopt -s nullglob

zip_sig() {
  # signature: sha256 + size, to reduce very rare edge cases
  local zip="$1"
  sha256sum "$zip" | awk '{print $1}'
}

git_sync() {
  # Optional: mirror ZIP contents into a git repo and push to remote.
  # Enable by setting GIT_SYNC_REMOTE (and optionally GIT_SYNC_BRANCH, GIT_SYNC_DIR, GIT_SYNC_TOKEN).
  local src="$1"   # extracted folder (DEPLOY_TMP)
  local zip="$2"
  local remote="${GIT_SYNC_REMOTE:-}"
  [[ -z "${remote}" ]] && return 0

  local dir="${GIT_SYNC_DIR:-/home/wlcb/.StudioB-UI/git-sync}"
  local branch="${GIT_SYNC_BRANCH:-main}"
  local token="${GIT_SYNC_TOKEN:-}"
  local author_name="${GIT_SYNC_AUTHOR_NAME:-StudioB Watcher}"
  local author_email="${GIT_SYNC_AUTHOR_EMAIL:-watcher@localhost}"

  if [[ ! -d "${dir}/.git" ]]; then
    log "git-sync: cloning ${remote} -> ${dir}"
    rm -rf "${dir}"
    if [[ -n "${token}" && "${remote}" =~ ^https:// ]]; then
      # inject token into https URL (kept out of process list as much as possible)
      remote_auth="$(echo "${remote}" | sed -E "s#^https://#https://${token}@#")"
      git clone --branch "${branch}" --depth 1 "${remote_auth}" "${dir}"
    else
      git clone --branch "${branch}" --depth 1 "${remote}" "${dir}"
    fi
  fi

  # Sync files from ZIP extract into repo working tree, excluding runtime/ and other host-only state.
  log "git-sync: syncing content into repo"
  if command -v rsync >/dev/null 2>&1; then
    rsync -a --delete \
      --exclude ".StudioB-UI/" \
      --exclude "runtime/" \
      --exclude "state/" \
      --exclude ".git/" \
      "${src}/" "${dir}/"
  else
    # crude fallback: wipe and copy
    find "${dir}" -mindepth 1 -maxdepth 1 ! -name ".git" -exec rm -rf {} +
    cp -a "${src}/." "${dir}/"
  fi

  ( cd "${dir}"
    git config user.name "${author_name}"
    git config user.email "${author_email}"

    if git diff --quiet && git diff --cached --quiet; then
      git add -A
    else
      git add -A
    fi

    if git diff --cached --quiet; then
      log "git-sync: no changes to commit"
      return 0
    fi

    local ver=""
    if [[ -f VERSION ]]; then ver="$(cat VERSION | tr -d '\r\n')"; fi
    local msg="Auto-import from ZIP: $(basename "${zip}")"
    if [[ -n "${ver}" ]]; then msg="${msg} (v${ver})"; fi

    git commit -m "${msg}"

    # Tag the version if it looks like a release and not already tagged.
    if [[ -n "${ver}" ]]; then
      if ! git rev-parse "v${ver}" >/dev/null 2>&1; then
        git tag "v${ver}" || true
      fi
    fi

    log "git-sync: pushing ${branch} (and tags)"
    git push origin "${branch}"
    git push --tags || true
  )
}


deploy() {
  local zip="$1"
  log "Deploying: ${zip}"

  rm -rf "${DEPLOY_TMP}"
  mkdir -p "${DEPLOY_TMP}"
  unzip -q "$zip" -d "${DEPLOY_TMP}"

  # Copy into repo working tree (preserve repo metadata and logs)
  rsync -a --delete \
    --exclude='.git/' \
    --exclude='.github/' \
    --exclude='logs/' \
    "${DEPLOY_TMP}/" "${REPO_DIR}/"

  chown -R wlcb:wlcb "${REPO_DIR}" || true

  # Move zip to archive (after successful copy)
  mv "$zip" "${ARCHIVE_DIR}/"

  # Run installer (self-healing; rebuilds engine & updates runtime symlink)
  sudo "${REPO_DIR}/install.sh"
}

while true; do
  zips=( "${TMP_DIR}"/*.zip )
  if (( ${#zips[@]} == 0 )); then
    sleep "${SLEEP}"
    continue
  fi

  # Pick newest by mtime
  newest="${zips[0]}"
  newest_mtime=$(stat -c %Y "${newest}" 2>/dev/null || echo 0)
  for z in "${zips[@]}"; do
    m=$(stat -c %Y "$z" 2>/dev/null || echo 0)
    if (( m > newest_mtime )); then
      newest="$z"
      newest_mtime=$m
    fi
  done

  sig="$(zip_sig "${newest}")"
  last_sig="$(cat "${STATE_FILE}" 2>/dev/null || true)"

  if [[ -n "${sig}" && "${sig}" != "${last_sig}" ]]; then
    echo "${sig}" > "${STATE_FILE}"
    deploy "${newest}"
  fi

  sleep "${SLEEP}"
done
