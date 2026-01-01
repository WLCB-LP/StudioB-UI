#!/usr/bin/env bash
# Studio B UI Watchdog
#
# This watchdog is intentionally conservative.
#
# What it does:
#   - Checks that nginx, stub-engine, and stub-ui-watch are active.
#   - Runs `nginx -t` and (if OK) reloads nginx.
#   - Calls the stub-engine health endpoint.
#   - Repairs a broken runtime/current symlink by pointing it to the
#     newest release folder.
#   - If nginx config is broken, it logs loudly and optionally attempts
#     a minimal nginx reconfigure by calling install_full.sh with
#     --configure-nginx-only (added for the watchdog).
#
# What it *does not* do (on purpose):
#   - It does NOT run full installs automatically (too risky).
#   - It does NOT modify your repo checkout.
#   - It does NOT delete releases.
#
# Run context:
#   - Installed as a systemd service (stub-ui-watchdog.service).
#   - Runs as root because it may restart services and test nginx.

set -euo pipefail

RUNTIME_BASE="/home/wlcb/.StudioB-UI/runtime"
CURRENT_LINK="$RUNTIME_BASE/current"
RELEASES_DIR="$RUNTIME_BASE/releases"
LOG_FILE="/var/log/stub-ui-watchdog.log"
CHECK_INTERVAL_SECONDS=15

# How many consecutive stub-engine health check failures before we restart it.
ENGINE_HEALTH_FAIL_THRESHOLD=2
engine_fail_count=0

log() {
  local msg="$*"
  local ts
  ts="$(date -Is)"
  echo "[$ts] $msg"
  # Best-effort file logging (don't ever crash watchdog due to logging)
  {
    mkdir -p "$(dirname "$LOG_FILE")" || true
    touch "$LOG_FILE" || true
    echo "[$ts] $msg" >> "$LOG_FILE" || true
  } >/dev/null 2>&1 || true
}

is_active() {
  local svc="$1"
  systemctl is-active --quiet "$svc"
}

restart_service() {
  local svc="$1"
  log "Attempting restart: $svc"
  if systemctl restart "$svc"; then
    log "Restarted: $svc"
    return 0
  fi
  log "ERROR: Failed to restart: $svc"
  return 1
}

ensure_current_symlink() {
  # Ensure runtime/current is a valid symlink to an existing release.
  if [[ -L "$CURRENT_LINK" ]] && [[ -d "$CURRENT_LINK" ]]; then
    return 0
  fi

  log "WARN: $CURRENT_LINK is missing or broken. Attempting repair..."

  if [[ ! -d "$RELEASES_DIR" ]]; then
    log "ERROR: releases dir missing: $RELEASES_DIR"
    return 1
  fi

  # Pick newest release by mtime (safe + simple)
  local newest
  newest="$(ls -1dt "$RELEASES_DIR"/* 2>/dev/null | head -n 1 || true)"

  if [[ -z "$newest" ]] || [[ ! -d "$newest" ]]; then
    log "ERROR: No releases found to relink current ->"
    return 1
  fi

  ln -sfn "$newest" "$CURRENT_LINK"
  log "Repaired symlink: $CURRENT_LINK -> $newest"
  return 0
}

check_nginx_config() {
  if nginx -t >/dev/null 2>&1; then
    return 0
  fi

  log "ERROR: nginx -t failed. Nginx config is invalid."
  log "Attempting minimal nginx reconfigure using install_full.sh --configure-nginx-only"

  if [[ -x "$CURRENT_LINK/scripts/install_full.sh" ]]; then
    if bash "$CURRENT_LINK/scripts/install_full.sh" --configure-nginx-only; then
      log "nginx reconfigure succeeded."
      return 0
    fi
  fi

  log "ERROR: nginx remains invalid. Manual intervention may be required."
  return 1
}

check_engine_health() {
  # The engine listens on 127.0.0.1:8787.
  if curl -fsS --max-time 2 http://127.0.0.1:8787/api/health >/dev/null 2>&1; then
    engine_fail_count=0
    return 0
  fi

  engine_fail_count=$((engine_fail_count + 1))
  log "WARN: stub-engine /api/health failed ($engine_fail_count/$ENGINE_HEALTH_FAIL_THRESHOLD)"

  if (( engine_fail_count >= ENGINE_HEALTH_FAIL_THRESHOLD )); then
    engine_fail_count=0
    restart_service stub-engine || true
  fi

  return 1
}

main_loop() {
  log "stub-ui-watchdog starting (interval=${CHECK_INTERVAL_SECONDS}s)"

  # Ensure log file exists and is writable.
  touch "$LOG_FILE" || true

  while true; do
    # 1) Ensure runtime/current is sane (needed for everything else)
    ensure_current_symlink || true

    # 2) Ensure key services are up
    for svc in nginx stub-ui-watch stub-engine; do
      if is_active "$svc"; then
        :
      else
        log "WARN: Service not active: $svc"
        restart_service "$svc" || true
      fi
    done

    # 3) Validate and (if OK) reload nginx
    if check_nginx_config; then
      # Reload only if active, otherwise restart already attempted above.
      if is_active nginx; then
        systemctl reload nginx >/dev/null 2>&1 || true
      fi
    fi

    # 4) Engine health endpoint
    check_engine_health || true

    sleep "$CHECK_INTERVAL_SECONDS"
  done
}

# If called directly, run the loop.
main_loop
