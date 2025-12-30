\
#!/usr/bin/env bash
set -euo pipefail

# Polling-based deploy watcher (NAS-safe)
# Watches TMP_DIR for *.zip and deploys the newest zip when its content changes.
# Uses a SHA256 signature to allow re-deploying the "same filename" if rebuilt.

TMP_DIR="/mnt/NAS/Engineering/Audio Network/Studio B/UI/tmp"
ARCHIVE_DIR="/mnt/NAS/Engineering/Audio Network/Studio B/UI"
STATE_DIR="/home/wlcb/.StudioB-UI/state"
STATE_FILE="${STATE_DIR}/last_zip.sig"
SLEEP=5

REPO_DIR="/home/wlcb/devel/StudioB-UI"
DEPLOY_TMP="/tmp/stub-ui-deploy"

log() { echo "[watcher] $*"; }

mkdir -p "${STATE_DIR}"
touch "${STATE_FILE}"

shopt -s nullglob

zip_sig() {
  # signature: sha256 + size, to reduce very rare edge cases
  local zip="$1"
  sha256sum "$zip" | awk '{print $1}'
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
