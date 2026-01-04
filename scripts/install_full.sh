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
CONFIG_FILE="${CONFIG_FILE:-${BASE_DIR}/config/config.v1}"

# Backwards compatibility: older releases used ~/.StudioB-UI/config/config.yml.
# We now use config.v1 as the canonical operator config path.
#
# We migrate once during install so:
# - Engineering UI edits affect the running engine
# - systemd always starts with the same file the UI edits
migrate_legacy_config() {
  local legacy="${BASE_DIR}/config/config.yml"
  local new="${BASE_DIR}/config/config.v1"
  if [[ ! -f "${new}" && -f "${legacy}" ]]; then
    log "Migrating legacy config: ${legacy} -> ${new}"
    mkdir -p "$(dirname "${new}")"
    cp -a "${legacy}" "${new}"
    # Keep a marker for auditability.
    echo "# migrated from config.yml on $(date -u +%Y-%m-%dT%H:%M:%SZ)" >> "${new}" || true
  fi
}

NGINX_SITE="${NGINX_SITE:-/etc/nginx/sites-available/studiob-ui}"
NGINX_LINK="${NGINX_LINK:-/etc/nginx/sites-enabled/studiob-ui}"
SYSTEMD_UNIT="${SYSTEMD_UNIT:-/etc/systemd/system/stub-engine.service}"
ENGINE_ENV_FILE="${ENGINE_ENV_FILE:-/etc/stub-engine.env}"
WATCH_ENV_FILE="${WATCH_ENV_FILE:-/etc/stub-ui-watch.env}"

log() {
  echo "[install] $*"
  # Also write to journald so UI-triggered updates can be debugged via `journalctl`.
  logger -t stub-ui-install -- "[install] $*" || true
}

# Validate the staged static UI assets inside a newly created release directory.
#
# Why this exists:
# - If app.js contains a syntax error, the browser will load the page (HTTP 200)
#   but *nothing* will work (no click handlers, no navigation, etc.).
# - nginx logs will look totally fine, so without this check it's easy to publish
#   a broken UI by accident.
#
# This function is intentionally conservative: it only checks things that should
# always be true for our UI, and it fails the install *before* switching the
# /runtime/current symlink.
validate_ui_assets() {
  local rel="$1"
  local webdir="${rel}/web"
  local ver

  # Read the version from the repo VERSION file (source of truth for releases).
  # (We avoid relying on any global variables here so this function is safe to call
  # from anywhere in the installer.)
  ver="$(cat "${REPO_DIR}/VERSION" 2>/dev/null || true)"
  ver="${ver#v}"
  if [[ -z "${ver}" ]]; then
    log "ERROR: could not read version from ${REPO_DIR}/VERSION"
    return 1
  fi

  # Required files
  for f in "${webdir}/index.html" "${webdir}/app.js" "${webdir}/styles.css"; do
    if [ ! -s "${f}" ]; then
      log "ERROR: UI asset missing or empty: ${f}"
      return 1
    fi
  done

  # index.html must reference cache-busted assets for the *current* version
  # (so browsers quickly converge on the right JS/CSS after an update).
  if ! grep -q "/app.js?v=${ver}" "${webdir}/index.html"; then
    log "ERROR: index.html does not reference /app.js?v=${ver}"
    return 1
  fi
  if ! grep -q "/styles.css?v=${ver}" "${webdir}/index.html"; then
    log "ERROR: index.html does not reference /styles.css?v=${ver}"
    return 1
  fi

  # Optional JS syntax check (best-effort). We don't *require* node to be installed,
  # but if it is available, this catches fatal syntax mistakes immediately.
  if command -v node >/dev/null 2>&1; then
    if ! node --check "${webdir}/app.js" >/dev/null 2>&1; then
      log "ERROR: node --check failed for ${webdir}/app.js"
      return 1
    fi
  else
    # Fallback: extremely cheap heuristic check that the compiled bundle contains
    # at least one event listener hook (our UI always does).
    if ! grep -Eq "addEventListener\(" "${webdir}/app.js"; then
      log "ERROR: app.js sanity check failed (no addEventListener() found)"
      return 1
    fi
  fi

  return 0
}

# Installer traps
# - log fatal errors with line/command
# - always attempt to re-enable/start watchdog on exit (best-effort)
on_install_error() {
  local exit_code=$?
  local line_no="${BASH_LINENO[0]:-unknown}"
  local cmd="${BASH_COMMAND:-unknown}"
  log "FATAL: install_full.sh failed (exit=${exit_code}) at line ${line_no}: ${cmd}"
  # Dump a bit of context for systemd/service debugging
  log "FATAL: stub-engine status follows (best-effort)…"
  systemctl status stub-engine --no-pager -l 2>&1 | logger -t stub-ui-install || true
  log "FATAL: nginx -t follows (best-effort)…"
  nginx -t 2>&1 | logger -t stub-ui-install || true
  # attempt watchdog recovery even on failure
  ensure_watchdog_running_best_effort
  exit "${exit_code}"
}

ensure_watchdog_running_best_effort() {
  # If the unit exists, try to enable + start it. Do not fail the install on this.
  if [ -f /etc/systemd/system/stub-ui-watchdog.service ]; then
    systemctl daemon-reload || true
    systemctl enable stub-ui-watchdog >/dev/null 2>&1 || true
    systemctl start stub-ui-watchdog >/dev/null 2>&1 || true
  fi
}

on_install_exit() {
  local exit_code=$?
  if [ "${exit_code}" -eq 0 ]; then
    log "Install finished successfully (exit=0)."
  else
    # If set -e is disabled temporarily somewhere, we can still hit EXIT with non-zero.
    log "Install exiting with failure (exit=${exit_code})."
  fi
  ensure_watchdog_running_best_effort
}

trap on_install_error ERR
trap on_install_exit EXIT

# -----------------------------------------------------------------------------
# HARDENING: make install failures obvious in journald, and ensure we leave the
# system in the safest possible state (e.g., watchdog running if installed).
# -----------------------------------------------------------------------------

install_err_trap() {
  local ec="$?"
  # BASH_LINENO[0] is the line number in this script where the failing command
  # was executed. BASH_COMMAND is the command that failed.
  log "ERROR: install_full.sh failed (exit=${ec}) at line ${BASH_LINENO[0]}: ${BASH_COMMAND}"
  return "${ec}"
}

install_exit_trap() {
  local ec="$?"
  if [[ "${ec}" -eq 0 ]]; then
    log "Install finished successfully (exit=0)"
  else
    log "Install exiting with failure (exit=${ec})"
  fi

  # If the watchdog unit exists, we prefer to leave it running, even if the
  # installer failed part-way through. This prevents long downtime.
  if systemctl list-unit-files --no-pager 2>/dev/null | grep -q '^stub-ui-watchdog\.service'; then
    # Ensure enabled (idempotent).
    systemctl enable stub-ui-watchdog >/dev/null 2>&1 || true
    # Ensure running (idempotent).
    systemctl start stub-ui-watchdog >/dev/null 2>&1 || true
  fi

  return "${ec}"
}

trap install_err_trap ERR
trap install_exit_trap EXIT

assert_active() {
  local unit="$1"
  if ! systemctl is-active --quiet "${unit}"; then
    log "ERROR: systemd unit is not active: ${unit}"
    systemctl status "${unit}" --no-pager -l || true
    return 1
  fi
  return 0
}

assert_file() {
  local path="$1"
  if [[ ! -f "${path}" ]]; then
    log "ERROR: expected file missing: ${path}"
    return 1
  fi
  return 0
}



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
    nginx inotify-tools logrotate \
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

ensure_watchdog_logging() {
  # We want watchdog logging + rotation to exist even if a later install step fails.
  # This makes UI-triggered updates debuggable and prevents unbounded log growth.
  log "Ensuring watchdog log + logrotate policy…"

  local _log_group="adm"
  if ! getent group "${_log_group}" >/dev/null 2>&1; then
    _log_group="root"
  fi

  touch /var/log/stub-ui-watchdog.log
  chown root:"${_log_group}" /var/log/stub-ui-watchdog.log
  chmod 0640 /var/log/stub-ui-watchdog.log

  # Install logrotate policy so the log file cannot grow without bound.
  mkdir -p /etc/logrotate.d
  if [[ -f "${REPO_DIR}/scripts/stub-ui-watchdog.logrotate" ]]; then
    install -m 0644 "${REPO_DIR}/scripts/stub-ui-watchdog.logrotate" /etc/logrotate.d/stub-ui-watchdog
  else
    log "WARN: missing scripts/stub-ui-watchdog.logrotate (skipping logrotate policy)"
  fi
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
  # NOTE: We intentionally do NOT run `go mod tidy` during installs/updates.
  # It can mutate go.sum and may require network access.
  # We rely on checked-in go.mod/go.sum and build in readonly mode instead.
  # It can mutate go.sum and may require network access.
  # We rely on checked-in go.mod/go.sum and build in readonly mode.
  VER="$(tr -d '[:space:]' < "${REPO_DIR}/VERSION")"

  # Build in a minimal, deterministic environment.
  # - We pin GOPATH so the module cache is stable across root/systemd-run contexts.
  # - We build with -mod=readonly so Go will NOT attempt to edit go.sum or fetch new deps.
  #   If a dependency is missing, the build will fail loudly and we'll capture the error.
  TEST_OUT="${rel}/.go-test.log"
  : > "${TEST_OUT}"

  # Run unit tests before building so releases are self-validating and the installer output
  # clearly shows whether tests passed. (Per project requirement: show tests ran & succeeded.)
  if ! sudo -u "${APP_USER}" env -i \
    HOME="/home/${APP_USER}" \
    PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" \
    GOPATH="/home/${APP_USER}/go" \
    GOMODCACHE="/home/${APP_USER}/go/pkg/mod" \
    GOCACHE="/home/${APP_USER}/.cache/go-build" \
    /usr/bin/go test ./... >"${TEST_OUT}" 2>&1; then
    log "ERROR: go test failed. Last 120 lines:"
    tail -n 120 "${TEST_OUT}" | sed -e "s/^/[go-test] /" || true
    return 1
  fi
  log "Go tests: PASS (see ${TEST_OUT})"

  BUILD_OUT="${rel}/.go-build.log"
  : > "${BUILD_OUT}"

  if ! sudo -u "${APP_USER}" env -i \
    HOME="/home/${APP_USER}" \
    PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" \
    GOPATH="/home/${APP_USER}/go" \
    GOMODCACHE="/home/${APP_USER}/go/pkg/mod" \
    GOCACHE="/home/${APP_USER}/.cache/go-build" \
    VER="${VER}" \
    /usr/bin/go build -mod=readonly -ldflags "-X main.version=${VER}" \
    -o "${rel}/${ENGINE_BIN}" ./cmd/stub-engine >"${BUILD_OUT}" 2>&1; then
    log "ERROR: go build failed. Last 120 lines:"
    tail -n 120 "${BUILD_OUT}" | sed -e "s/^/[go-build] /" || true
    return 1
  fi
  popd >/dev/null

  chmod 755 "${rel}/${ENGINE_BIN}"
  chown -R "${APP_USER}:${APP_GROUP}" "${rel}"

  log "Installing UI assets -> ${rel}/web"
  # Ensure ui/index.html cache-buster query strings match VERSION for this release.
  # This prevents the operator from getting "stuck" on an older cached app.js/styles.css.
  bash "${REPO_DIR}/scripts/sync_ui_cachebuster.sh"
  rsync -a --delete "${REPO_DIR}/ui/" "${rel}/web/"

  # ---------------------------------------------------------------------------
  # UI sanity checks
  #
  # We serve the UI as static files (index.html + app.js + styles.css) behind nginx.
  # If app.js has a syntax error (or index.html points at a non-existent asset),
  # the operator will see "buttons don't work" because the JS never bootstraps.
  #
  # These checks keep installs/update rollouts safe:
  #   - We verify required files exist.
  #   - We verify index.html references the current VERSION cache-buster.
  #   - If Node.js is present, we run a syntax-only parse check of app.js.
  #
  # If any check fails, we abort BEFORE switching the runtime/current symlink.
  # ---------------------------------------------------------------------------
  validate_ui_assets "${rel}"

  log "Installing runtime scripts -> ${rel}/scripts"
  rsync -a --delete "${REPO_DIR}/scripts/" "${rel}/scripts/"
  chmod -R a+rx "${rel}/scripts" || true

  # rsync runs as root here (install.sh is invoked via sudo). Ensure the entire
  # release tree is owned by the app user so future tooling (watchdog, update
  # scripts) doesn't trip on permission edge cases.
  chown -R "${APP_USER}:${APP_GROUP}" "${rel}" || true

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

  # Prevent "stale UI" after updates. index.html should always be revalidated.
  location = /index.html {
    add_header Cache-Control "no-store" always;
  }

  location /api/ {
    proxy_pass http://127.0.0.1:8787/api/;
    proxy_http_version 1.1;
    proxy_set_header Host $host;

    # Admin actions (e.g., update/apply) can legitimately take longer than the
    # default Nginx proxy timeout.
    proxy_connect_timeout 10s;
    proxy_send_timeout 600s;
    proxy_read_timeout 600s;

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

  log "systemd: daemon-reload"
  systemctl daemon-reload
  log "systemd: enable --now stub-engine"
  systemctl enable --now stub-engine
  log "systemd: restart stub-engine"
  systemctl restart stub-engine
}

install_watcher() {
  log "Installing folder watcher service…"
  install -m 0755 "${REPO_DIR}/scripts/stub-ui-watch.sh" /usr/local/bin/stub-ui-watch.sh
  install -m 0644 "${REPO_DIR}/scripts/stub-ui-watch.service" /etc/systemd/system/stub-ui-watch.service

  systemctl daemon-reload
  systemctl enable --now stub-ui-watch
  systemctl restart stub-ui-watch

  # Hard requirement: if the watcher isn't running, the install MUST fail loudly.
  assert_active stub-ui-watch
}

install_watchdog() {
  log "Installing watchdog service…"

  # Preserve operator intent:
  # - On *fresh installs* (unit did not exist), enable + start the watchdog by default.
  # - On *updates*, preserve the prior enable/disable state so disabling the watchdog
  #   remains respected across updates.
  local existed_before=0
  systemctl cat stub-ui-watchdog.service >/dev/null 2>&1 && existed_before=1 || true

  local enabled_before="absent"
  if systemctl list-unit-files --no-legend stub-ui-watchdog.service >/dev/null 2>&1; then
    enabled_before="$(systemctl is-enabled stub-ui-watchdog 2>/dev/null || echo disabled)"
  fi

  # Install/update unit + script.
  install -m 0644 "${REPO_DIR}/scripts/stub-ui-watchdog.service" /etc/systemd/system/stub-ui-watchdog.service
  chmod +x "${REPO_DIR}/scripts/stub-ui-watchdog.sh" || true

  # Ensure a predictable, tail-able log file exists for operators.
  # The watchdog also logs to the systemd journal via stdout.
  local _log_group="adm"
  if ! getent group "${_log_group}" >/dev/null 2>&1; then
    _log_group="root"
  fi
  touch /var/log/stub-ui-watchdog.log
  chown root:"${_log_group}" /var/log/stub-ui-watchdog.log
  chmod 0640 /var/log/stub-ui-watchdog.log

  # Install logrotate policy so the log file cannot grow without bound.
  install -m 0644 "${REPO_DIR}/scripts/stub-ui-watchdog.logrotate" /etc/logrotate.d/stub-ui-watchdog

  # Always reload so systemd sees changes.
  log "systemd: daemon-reload"
  systemctl daemon-reload

  # Apply desired state.
  if [[ "${existed_before}" -eq 0 ]]; then
    # Fresh install: enable by default.
    log "systemd: enable --now stub-ui-watchdog (fresh install)"
    systemctl enable --now stub-ui-watchdog
    systemctl restart stub-ui-watchdog
  else
    case "${enabled_before}" in
      enabled)
        log "systemd: restart stub-ui-watchdog (was enabled)"
        systemctl enable --now stub-ui-watchdog
        systemctl restart stub-ui-watchdog
        ;;
      disabled|masked|static|indirect|generated|transient|""|absent)
        # Respect operator choice to keep it off.
        log "systemd: leave stub-ui-watchdog disabled (was ${enabled_before})"
        systemctl disable --now stub-ui-watchdog >/dev/null 2>&1 || true
        ;;
      *)
        # Unknown state: be conservative and do not start it.
        log "systemd: unknown stub-ui-watchdog state '${enabled_before}' — leaving disabled"
        systemctl disable --now stub-ui-watchdog >/dev/null 2>&1 || true
        ;;
    esac
  fi

  # If enabled, ensure it's actually running.
  if systemctl is-enabled --quiet stub-ui-watchdog 2>/dev/null; then
    systemctl is-active --quiet stub-ui-watchdog
  fi
}

health_check() {
  log "Running health checks…"
  systemctl is-active --quiet stub-engine
  if systemctl is-enabled --quiet stub-ui-watchdog 2>/dev/null; then
    systemctl is-active --quiet stub-ui-watchdog
  fi
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
  /bin/bash /home/${APP_USER}/.StudioB-UI/runtime/*/scripts/admin-rollback.sh, \
  /bin/bash /home/${APP_USER}/.StudioB-UI/runtime/*/scripts/admin-watchdog-start.sh
EOF

  chmod 0440 "${sudoers_file}"

  # Validate so we don't brick sudo.
  if ! visudo -cf "${sudoers_file}" >/dev/null 2>&1; then
    echo "[install][ERROR] sudoers validation failed; removing ${sudoers_file}" >&2
    rm -f "${sudoers_file}"
    exit 1
  fi
}

###############################################################################
# Optional "one-shot" modes
#
# The watchdog can invoke this installer in a narrow, safe mode to repair
# nginx config without doing a full rebuild/redeploy.
###############################################################################

MODE_FULL=1
MODE_NGINX_ONLY=0
if [[ "${1:-}" == "--configure-nginx-only" ]]; then
  MODE_FULL=0
  MODE_NGINX_ONLY=1
fi

main() {
  require_root

  # Narrow, safe mode used by the watchdog.
  if [[ "${MODE_NGINX_ONLY}" == "1" ]]; then
    log "Running in --configure-nginx-only mode"
    configure_nginx
    systemctl reload nginx || true
    nginx -t
    log "nginx-only configuration complete"
    return 0
  fi

  apt_install
  ensure_user
  ensure_dirs
  ensure_watchdog_logging
  write_env_files
  ensure_git_origin || true
  write_default_config_if_missing
  validate_and_repair_config
  deploy_release
  configure_nginx
  configure_systemd
  configure_sudoers
  install_watcher
  install_watchdog
  health_check
  log "Install complete. Open: http://<vm-ip>/"
  log "IMPORTANT: set admin.pin in ${CONFIG_FILE}"
}

main "$@"