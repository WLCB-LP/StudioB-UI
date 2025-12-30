#!/usr/bin/env bash
set -euo pipefail

APP_USER="${APP_USER:-wlcb}"
APP_GROUP="${APP_GROUP:-wlcb}"
REPO_DIR="${REPO_DIR:-/home/wlcb/devel/StudioB-UI}"
RUNTIME_BASE="${RUNTIME_BASE:-/opt/studiob-ui}"
CURRENT_DIR="${CURRENT_DIR:-${RUNTIME_BASE}/current}"
RELEASES_DIR="${RELEASES_DIR:-${RUNTIME_BASE}/releases}"
APP_DIR="${APP_DIR:-${CURRENT_DIR}}"
ENGINE_BIN="${ENGINE_BIN:-stub-engine}"
CONFIG_FILE="${CONFIG_FILE:-/etc/studiob-ui/config.yml}"
NGINX_SITE="${NGINX_SITE:-/etc/nginx/sites-available/stub-mixer}"
NGINX_LINK="${NGINX_LINK:-/etc/nginx/sites-enabled/stub-mixer}"
SYSTEMD_UNIT="${SYSTEMD_UNIT:-/etc/systemd/system/stub-engine.service}"

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
    golang-go
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
  mkdir -p "${REPO_DIR}"
  mkdir -p "${RELEASES_DIR}"
  mkdir -p /var/log/studiob-ui
  mkdir -p /var/lib/studiob-ui
  mkdir -p /etc/studiob-ui
  chown -R "${APP_USER}:${APP_GROUP}" "${REPO_DIR}"
  chown -R "${APP_USER}:${APP_GROUP}" "${RELEASES_DIR}"
  chown -R "${APP_USER}:${APP_GROUP}" /var/log/studiob-ui /var/lib/studiob-ui
  chmod 750 /etc/studiob-ui
}


ensure_git_origin() {
  # Ensure the repo has an SSH origin set correctly (required for update/rollback).
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
    log "NOTE: ${REPO_DIR} is not a git repo yet; update/rollback will require cloning git@github.com:WLCB-LP/StudioB-UI.git"
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
  host: "192.168.0.10"   # TODO: set to Studio B Radius IP
  port: 48631            # TODO: set to Symetrix control port (if needed)
  mode: "mock"           # "mock" or "symetrix" (future)
ui:
  http_listen: "127.0.0.1:8787"
  public_base_url: "http://localhost"
admin:
  pin: "CHANGE_ME"
meters:
  publish_hz: 20
  deadband: 0.01
rc_allowlist:
  # Input mutes
  - 121
  - 122
  - 123
  - 124
  # Program
  - 111
  - 131
  # Program meters
  - 411
  - 412
  # Speakers
  - 160
  - 161
  - 460
  - 461
  - 560
  # Remote Studio Return meters
  - 462
  - 463
YAML
  chown "${APP_USER}:${APP_GROUP}" "${CONFIG_FILE}"
  chmod 640 "${CONFIG_FILE}"
}

validate_and_repair_config() {
  log "Validating config…"
  # Basic checks; repair common issues.
  if ! grep -q "admin:" "${CONFIG_FILE}"; then
    log "Config missing admin section; repairing."
    echo -e "\nadmin:\n  pin: \"CHANGE_ME\"\n" >> "${CONFIG_FILE}"
  fi
  if grep -q 'pin: "CHANGE_ME"' "${CONFIG_FILE}"; then
    log "WARNING: admin.pin is CHANGE_ME. Set it before exposing Engineering page."
  fi
}

make_release_dir() {
  local sha stamp tag rel
  sha="$(git -C "${REPO_DIR}" rev-parse --short HEAD 2>/dev/null || echo unknown)"
  tag="$(git -C "${REPO_DIR}" describe --tags --always 2>/dev/null || echo ${sha})"
  stamp="$(date +%Y%m%d-%H%M%S)"
  rel="${RELEASES_DIR}/${stamp}-${tag}"
  mkdir -p "${rel}/web" "${rel}/scripts"
  echo "${rel}"
}

deploy_release() {
  log "Building and staging release…"
  local rel
  rel="$(make_release_dir)"

  log "Building engine -> ${rel}/stub-engine"
  pushd "${REPO_DIR}/engine" >/dev/null
  sudo -u "${APP_USER}" env -i HOME="/home/${APP_USER}" PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" \
    /usr/bin/go build -o "${rel}/stub-engine" ./cmd/stub-engine
  popd >/dev/null
  chmod 755 "${rel}/stub-engine"
  chown -R "${APP_USER}:${APP_GROUP}" "${rel}"

  log "Installing UI assets -> ${rel}/web"
  rsync -a --delete "${REPO_DIR}/ui/" "${rel}/web/"

  log "Installing runtime scripts -> ${rel}/scripts"
  rsync -a --delete "${REPO_DIR}/scripts/" "${rel}/scripts/"
  chmod -R a+rx "${rel}/scripts" || true

  log "Switching current symlink -> ${rel}"
  ln -sfn "${rel}" "${CURRENT_DIR}"

  log "Recording releases"
  echo "${rel}" > /var/lib/studiob-ui/last_release.txt
}/web"
  rsync -a --delete "${APP_DIR}/ui/" "${APP_DIR}/web/"
  chown -R "${APP_USER}:${APP_GROUP}" "${APP_DIR}/web"
}

configure_nginx() {
  log "Configuring nginx…"
  cat > "${NGINX_SITE}" <<'NGINX'
server {
  listen 80;
  server_name _;

  root /opt/studiob-ui/current/web;
  index index.html;

  # Static UI
  location / {
    try_files $uri $uri/ /index.html;
  }

  # API -> engine
  location /api/ {
    proxy_pass http://127.0.0.1:8787/api/;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
  }

  # WebSocket -> engine
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
ExecStart=${CURRENT_DIR}/${ENGINE_BIN} --config ${CONFIG_FILE}
Restart=always
RestartSec=2
Environment=GOMAXPROCS=2

# Hardening
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=false
ReadWritePaths=${CURRENT_DIR} /etc/studiob-ui /var/log/studiob-ui /var/lib/studiob-ui
AmbientCapabilities=
CapabilityBoundingSet=

[Install]
WantedBy=multi-user.target
SYSTEMD

  systemctl daemon-reload
  systemctl enable stub-engine
  systemctl restart stub-engine
}

health_check() {
  log "Running health checks…"
  if ! systemctl is-active --quiet stub-engine; then
    echo "stub-engine is not running."
    journalctl -u stub-engine --no-pager -n 50
    exit 1
  fi
  curl -fsS http://127.0.0.1/api/health >/dev/null
  log "OK: engine responds to /api/health"
}

main() {
  require_root
  apt_install
  ensure_user
  ensure_dirs
  ensure_git_origin
  write_default_config_if_missing
  validate_and_repair_config
  deploy_release
  configure_nginx
  configure_systemd
  health_check
  log "Install complete. Open: http://<vm-ip>/"
  log "IMPORTANT: set admin.pin in ${CONFIG_FILE}"
}

main "$@"
