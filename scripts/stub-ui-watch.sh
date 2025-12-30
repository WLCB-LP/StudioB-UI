#!/usr/bin/env bash
set -euo pipefail

WATCH_DIR="/mnt/NAS/Engineering/Audio Network/Studio B/UI/tmp"
ARCHIVE_DIR="/mnt/NAS/Engineering/Audio Network/Studio B/UI"
DEST="/home/wlcb/devel/StudioB-UI"
OWNER="wlcb:wlcb"

WORK="/tmp/stub-ui-deploy"
LOCK="/tmp/stub-ui-watch.lock"

RSYNC_EXCLUDES=(
  "--exclude=.git/"
  "--exclude=.gitignore"
  "--exclude=.github/"
  "--exclude=.releases/"
  "--exclude=config.yml"
  "--exclude=logs/"
)

log() { echo "[stub-ui-watch] $*"; }

need_tools() {
  command -v inotifywait >/dev/null 2>&1 || { echo "Missing inotifywait (install inotify-tools)"; exit 1; }
  command -v rsync >/dev/null 2>&1 || { echo "Missing rsync"; exit 1; }
  command -v unzip >/dev/null 2>&1 || { echo "Missing unzip"; exit 1; }
  command -v flock >/dev/null 2>&1 || { echo "Missing flock (util-linux)"; exit 1; }
}

deploy_zip() {
  local zip="$1"
  local base
  base="$(basename "$zip")"

  log "Processing: $base"

  rm -rf "$WORK"
  mkdir -p "$WORK/src" "$ARCHIVE_DIR" "$DEST"

  unzip -q "$zip" -d "$WORK/src"

  rsync -a --delete     "${RSYNC_EXCLUDES[@]}"     "$WORK/src/" "$DEST/"

  log "Rsync completed to $DEST"
  log "Fixing ownership: $OWNER"
  chown -R "$OWNER" "$DEST"

  mkdir -p "$DEST/logs"
  chown -R "$OWNER" "$DEST/logs"
  echo "$(date -Is) deployed $base" >> "$DEST/logs/deploy.log"

  mv -f "$zip" "$ARCHIVE_DIR/$base"
  log "Moved $base to archive directory"
}

main() {
  need_tools

  exec 9>"$LOCK"
  if ! flock -n 9; then
    log "Watcher already running, exiting."
    exit 0
  fi

  log "Watching: $WATCH_DIR"

  shopt -s nullglob
  for z in "$WATCH_DIR"/*.zip; do
    deploy_zip "$z"
  done

  inotifywait -m -e close_write,moved_to --format "%w%f" "$WATCH_DIR" | while IFS= read -r file; do
    case "$file" in
      *.zip) deploy_zip "$file" ;;
      *) : ;;
    esac
  done
}

main "$@"
