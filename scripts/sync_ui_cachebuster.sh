#!/usr/bin/env bash
set -euo pipefail

# sync_ui_cachebuster.sh
#
# PURPOSE
#   Keep ui/index.html cache-buster query strings in sync with VERSION.
#
# WHY
#   Browsers can cache app.js/styles.css aggressively. We use a query string
#   (e.g. /app.js?v=0.2.3) to ensure a new release pulls new assets.
#
#   This script is designed to be safe to run repeatedly.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION_FILE="${ROOT_DIR}/VERSION"
INDEX_HTML="${ROOT_DIR}/ui/index.html"
APP_JS="${ROOT_DIR}/ui/app.js"

if [[ ! -f "${VERSION_FILE}" ]]; then
  echo "[sync_ui_cachebuster] ERROR: missing VERSION at ${VERSION_FILE}" >&2
  exit 1
fi
if [[ ! -f "${INDEX_HTML}" ]]; then
  echo "[sync_ui_cachebuster] ERROR: missing ui/index.html at ${INDEX_HTML}" >&2
  exit 1
fi
if [[ ! -f "${APP_JS}" ]]; then
  echo "[sync_ui_cachebuster] ERROR: missing ui/app.js at ${APP_JS}" >&2
  exit 1
fi

VER="$(tr -d '[:space:]' < "${VERSION_FILE}")"
if [[ -z "${VER}" ]]; then
  echo "[sync_ui_cachebuster] ERROR: VERSION is empty" >&2
  exit 1
fi

tmp="${INDEX_HTML}.tmp"

# Update BOTH app.js and styles.css query strings.
# NOTE: In sed's default (basic) regex, '?' is literal unless escaped.
# The previous pattern used '\\?' which GNU sed interprets as a regex
# quantifier, so the replacement silently failed. We keep '?' UNESCAPED.
sed \
  -e "s|/app\\.js?v=[0-9.]*|/app.js?v=${VER}|g" \
  -e "s|/styles\\.css?v=[0-9.]*|/styles.css?v=${VER}|g" \
  "${INDEX_HTML}" > "${tmp}"

mv "${tmp}" "${INDEX_HTML}"

# Also keep the UI's embedded build version in sync.
# This is important because we use UI_BUILD_VERSION to detect "new engine / old UI"
# mismatches and trigger a one-time hard reload.
tmp_js="${APP_JS}.tmp"
sed \
  -e "s|^const UI_BUILD_VERSION = \"[0-9.]*\";|const UI_BUILD_VERSION = \"${VER}\";|" \
  "${APP_JS}" > "${tmp_js}"
mv "${tmp_js}" "${APP_JS}"

echo "[sync_ui_cachebuster] Synced UI asset versions to v${VER}"
