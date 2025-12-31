#!/usr/bin/env bash
set -euo pipefail

APP_USER="${APP_USER:-wlcb}"
APP_GROUP="${APP_GROUP:-wlcb}"

# Source repo (git working tree)
# Default to the directory containing this install.sh (repo root).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# scripts/ lives under the repo root; default REPO_DIR to the parent of SCRIPT_DIR.
DEFAULT_REPO_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
REPO_DIR="${REPO_DIR:-${DEFAULT_REPO_DIR}}"

# Node-RED style base (runtime/config/logs/state)
BASE_DIR="${BASE_DIR:-/home/wlcb/.StudioB-UI}"
RUNTIME_BASE="${RUNTIME_BASE:-${BASE_DIR}/runtime}"
CURRENT_DIR="${CURRENT_DIR:-${RUNTIME_BASE}/current}"
RELEASES_DIR="${RELEASES_DIR:-${RUNTIME_BASE}/releases}"

ENGINE_BIN="${ENGINE_BIN:-stub-engine}"
CONFIG_FILE="${CONFIG_FILE:-${BASE_DIR}/config/config.yml}"

NGINX_SITE="${NGINX_SITE:-/etc/nginx/sites-available/studiob-ui}"
NGINX_LINK="${NGINX_LINK:-/etc/nginx/sites-enabled/studiob-ui}"
SYSTEMD_UNIT="${SYSTEMD_UNIT:-/etc/systemd/system/stub-engine.service}"
ENGINE_ENV_FILE="${ENGINE_ENV_FILE:-/etc/stub-engine.env}"
WATCH_ENV_FILE="${WATCH_ENV_FILE:-/etc/stub-ui-watch.env}"

log() { echo "[install] $*"; }

require_root() {
  if [[ "$(id -u)" -ne 0 ]]; then
    echo "Please run as root (sudo)."
    exit 1
  fi
}

apt_install() {
  log "Installing OS dependencies…"
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -y
  apt-get install -y --no-install-recommends \
    ca-certificates curl git rsync unzip jq \
    nginx inotify-tools \
    golang-go util-linux
}

ensure_user() {
  if id -u "${APP_USER}" >/dev/null 2>&1; then
    log "User ${APP_USER} exists."
  else
    log "Creating user ${APP_USER}…"
    useradd -m -s /bin/bash "${APP_USER}"
  fi
}

ensure_dirs() {
  log "Ensuring directories…"
  mkdir -p "${BASE_DIR}"
  mkdir -p "${BASE_DIR}/config" "${BASE_DIR}/logs" "${BASE_DIR}/state"
  mkdir -p "${RELEASES_DIR}"
  chown -R "${APP_USER}:${APP_GROUP}" "${BASE_DIR}"
  chmod 750 "${BASE_DIR}/config"
}

ensure_git_origin() {
  if [[ -d "${REPO_DIR}/.git" ]]; then
    local want="git@github.com:WLCB-LP/StudioB-UI.git"
    if git -C "${REPO_DIR}" remote get-url origin >/dev/null 2>&1; then
      local cur
      cur="$(git -C "${REPO_DIR}" remote get-url origin)"
      if [[ "${cur}" != "${want}" ]]; then
        log "Setting git origin to SSH: ${want}"
        git -C "${REPO_DIR}" remote set-url origin "${want}"
      fi
    else
      log "Adding git origin: ${want}"
      git -C "${REPO_DIR}" remote add origin "${want}"
    fi
  else
    log "NOTE: ${REPO_DIR} is not a git repo yet."
  fi
}

write_default_config_if_missing() {
  if [[ -f "${CONFIG_FILE}" ]]; then
    log "Config exists: ${CONFIG_FILE}"
    return
  fi

  log "Writing default config: ${CONFIG_FILE}"
  cat > "${CONFIG_FILE}" <<'YAML'
# Studio B engine config
dsp:
  host: "192.168.0.10"
  port: 48631
  mode: "mock"
ui:
  http_listen: "127.0.0.1:8787"
  public_base_url: "http://localhost"

updates:
  mode: "git"
  github_repo: "WLCB-LP/StudioB-UI"
  asset_suffix: ".zip"
  watch_tmp_dir: "/mnt/NAS/Engineering/Audio Network/Studio B/UI/tmp"
  token_env: "GITHUB_TOKEN"

admin:
  pin: "CHANGE_ME"
meters:
  publish_hz: 20
  deadband: 0.01
rc_allowlist:
  - 121
  - 122
  - 123
  - 124
  - 111
  - 131
  - 411
  - 412
  - 160
  - 161
  - 460
  - 461
  - 560
  - 462
  - 463
YAML

  chown "${APP_USER}:${APP_GROUP}" "${CONFIG_FILE}"
  chmod 640 "${CONFIG_FILE}"
}

validate_and_repair_config() {
  log "Validating config…"
  if ! grep -q "^admin:" "${CONFIG_FILE}"; then
    log "Config missing admin section; repairing."
    printf "\nadmin:\n  pin: \"CHANGE_ME\"\n" >> "${CONFIG_FILE}"
  fi
  if grep -q 'pin: "CHANGE_ME"' "${CONFIG_FILE}"; then
    log "WARNING: admin.pin is CHANGE_ME. Set it before exposing Engineering page."
  fi
}

make_release_dir() {
  local version ts rel
  version="$(cat "${REPO_DIR}/VERSION" 2>/dev/null | tr -d '[:space:]')"
  [ -n "${version}" ] || version="0.0.0"
  ts="$(date +%Y%m%d-%H%M%S)"
  rel="${RELEASES_DIR}/${ts}-v${version}"
  mkdir -p "${rel}"
  echo "${rel}"
}


deploy_release() {
  log "Building and staging release…"
  local rel
  rel="$(make_release_dir)"

  # Ensure release dir writable by app user (created while running as root)
  chown -R "${APP_USER}:${APP_GROUP}" "${rel}"

  log "Building engine -> ${rel}/${ENGINE_BIN}"
  pushd "${REPO_DIR}/engine" >/dev/null
  # Ensure module metadata is complete (creates/updates go.sum)
  sudo -u "${APP_USER}" bash -lc "cd \"${REPO_DIR}/engine\" && go mod tidy"
  VER="$(tr -d '[:space:]' < "${REPO_DIR}/VERSION")"

  sudo -u "${APP_USER}" env -i \
  HOME="/home/${APP_USER}" \
  PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" \
  VER="${VER}" \
  /usr/bin/go build -ldflags "-X main.version=${VER}" \
  -o "${rel}/${ENGINE_BIN}" ./cmd/stub-engine
  popd >/dev/null

  chmod 755 "${rel}/${ENGINE_BIN}"
  chown -R "${APP_USER}:${APP_GROUP}" "${rel}"

  log "Installing UI assets -> ${rel}/web"
  rsync -a --delete "${REPO_DIR}/ui/" "${rel}/web/"

  log "Installing runtime scripts -> ${rel}/scripts"
  rsync -a --delete "${REPO_DIR}/scripts/" "${rel}/scripts/"
  chmod -R a+rx "${rel}/scripts" || true

  log "Switching current symlink -> ${rel}"
  ln -sfn "${rel}" "${CURRENT_DIR}"

  echo "${rel}" > "${BASE_DIR}/state/last_release.txt"
  chown "${APP_USER}:${APP_GROUP}" "${BASE_DIR}/state/last_release.txt" || true
}

configure_nginx() {
  log "Configuring nginx…"
  cat > "${NGINX_SITE}" <<'NGINX'
server {
  listen 80;
  server_name _;

  root /home/wlcb/.StudioB-UI/runtime/current/web;
  index index.html;

  location / {
    try_files $uri $uri/ /index.html;
  }

  location /api/ {
    proxy_pass http://127.0.0.1:8787/api/;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
  }

  location /ws {
    proxy_pass http://127.0.0.1:8787/ws;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "Upgrade";
    proxy_set_header Host $host;
  }
}
NGINX

  ln -sf "${NGINX_SITE}" "${NGINX_LINK}"
  rm -f /etc/nginx/sites-enabled/default || true
  nginx -t
  systemctl enable nginx
  systemctl restart nginx
}

write_env_files() {
  log "Writing systemd environment files…"
  # Engine env (optional secrets like GITHUB_TOKEN)
  cat > "${ENGINE_ENV_FILE}" <<EOF
# Stub engine environment
# Optional: set a GitHub token to avoid rate limits when checking releases.
# GITHUB_TOKEN=...
EOF
  chmod 600 "${ENGINE_ENV_FILE}"

  # Watcher env (optional git sync after successful deploy)
  cat > "${WATCH_ENV_FILE}" <<EOF
# Stub UI watcher environment
# Enable git sync by setting remote and credentials.
GIT_SYNC_REMOTE="git@github.com:WLCB-LP/StudioB-UI.git"
GIT_SYNC_BRANCH=main
GIT_SYNC_DIR=/home/${APP_USER}/.StudioB-UI/git-sync
GIT_SYNC_AUTHOR_NAME="WLCB"
GIT_SYNC_AUTHOR_EMAIL="wlcb@wlcb.local"
# For HTTPS remotes, set token:
# GIT_SYNC_TOKEN=...
EOF
  chmod 600 "${WATCH_ENV_FILE}"
}

configure_systemd() {
  log "Configuring systemd service…"
  cat > "${SYSTEMD_UNIT}" <<SYSTEMD
[Unit]
Description=Studio B UI Engine
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${APP_USER}
Group=${APP_GROUP}
WorkingDirectory=${CURRENT_DIR}
EnvironmentFile=-${ENGINE_ENV_FILE}
ExecStart=${CURRENT_DIR}/${ENGINE_BIN} --config ${CONFIG_FILE}
Restart=always
RestartSec=2
Environment=GOMAXPROCS=2

# NOTE: NoNewPrivileges breaks sudo (required for UI-driven Update/Rollback).
# Leaving this disabled so admin-update can invoke sudo -n.
NoNewPrivileges=false
PrivateTmp=true
ProtectSystem=full
# NOTE: Updates may need to write /etc (sudoers, env files) and /etc/nginx;
# ReadWritePaths above whitelists those paths even with ProtectSystem=full.
ProtectHome=false
ReadWritePaths=/home/wlcb/.StudioB-UI/runtime/current /home/wlcb/.StudioB-UI/runtime /home/wlcb/.StudioB-UI/state /etc /etc/systemd/system /etc/nginx /usr/local/bin /usr/local/sbin

[Install]
WantedBy=multi-user.target
SYSTEMD

  systemctl daemon-reload
  systemctl enable --now stub-engine
  systemctl restart stub-engine
}

install_watcher() {
  log "Installing folder watcher service…"
  install -m 0755 "${REPO_DIR}/scripts/stub-ui-watch.sh" /usr/local/bin/stub-ui-watch.sh
  install -m 0644 "${REPO_DIR}/scripts/stub-ui-watch.service" /etc/systemd/system/stub-ui-watch.service
  systemctl daemon-reload
  systemctl enable --now stub-ui-watch
  systemctl restart stub-ui-watch
}

health_check() {
  log "Running health checks…"
  systemctl is-active --quiet stub-engine
  curl -fsS http://127.0.0.1:8787/api/health >/dev/null
  curl -fsS http://127.0.0.1/api/health >/dev/null
  log "OK: engine responds and nginx proxy is healthy"
}

configure_sudoers() {
  # Allow the unprivileged engine (running as ${APP_USER}) to run specific privileged scripts
  # without prompting for a password. This is REQUIRED for "Update" and "Rollback" from the UI.
  #
  # We keep the allowed commands tightly scoped to the runtime scripts shipped with this app.
  local sudoers_file="/etc/sudoers.d/studiob-ui"

  log "Configuring sudoers..."

  cat > "${sudoers_file}" <<EOF
# Managed by StudioB-UI install_full.sh — DO NOT EDIT BY HAND.
# Allow StudioB UI engine (running as ${APP_USER}) to run controlled privileged actions.
${APP_USER} ALL=(root) NOPASSWD: \
  /bin/bash /home/${APP_USER}/.StudioB-UI/runtime/*/scripts/install_full.sh, \
  /bin/bash /home/${APP_USER}/.StudioB-UI/runtime/*/scripts/admin-update.sh, \
  /bin/bash /home/${APP_USER}/.StudioB-UI/runtime/*/scripts/admin-rollback.sh
EOF

  chmod 0440 "${sudoers_file}"

  # Validate so we don't brick sudo.
  if ! visudo -cf "${sudoers_file}" >/dev/null 2>&1; then
    echo "[install][ERROR] sudoers validation failed; removing ${sudoers_file}" >&2
    rm -f "${sudoers_file}"
    exit 1
  fi
}

main() {
  require_root
  apt_install
  ensure_user
  ensure_dirs
  write_env_files
  ensure_git_origin || true
  write_default_config_if_missing
  validate_and_repair_config
  deploy_release
  configure_nginx
  configure_systemd
  configure_sudoers
  install_watcher
  health_check
  log "Install complete. Open: http://<vm-ip>/"
  log "IMPORTANT: set admin.pin in ${CONFIG_FILE}"
}

main "$@"
