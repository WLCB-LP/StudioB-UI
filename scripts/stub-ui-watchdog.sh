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

BASE_DIR="/home/wlcb/.StudioB-UI"
RUNTIME_BASE="${BASE_DIR}/runtime"
CURRENT_LINK="$RUNTIME_BASE/current"
RELEASES_DIR="$RUNTIME_BASE/releases"
LOG_FILE="/var/log/stub-ui-watchdog.log"
CHECK_INTERVAL_SECONDS=15

# When the operator saves a config change that requires a deterministic restart,
# the engine records a restart request flag file here. The watchdog is responsible
# for noticing it and restarting stub-engine.
RESTART_FLAG="${BASE_DIR}/state/restart_required.json"


# How many consecutive stub-engine health check failures before we restart it.
#
# Why this is higher than 2 (v0.2.93):
# - When the DSP/network is having transient issues, the engine may still be
#   healthy, but HTTP requests can briefly fail.
# - A low threshold caused "restart loops" where the watchdog repeatedly
#   restarted a perfectly fine engine, creating the appearance of
#   "Empty reply from server" in curl.
#
# We keep restarts conservative: require a longer streak of failures before
# taking disruptive action.
ENGINE_HEALTH_FAIL_THRESHOLD=6
engine_fail_count=0

# "Last known good" marker written by the watchdog when the system has
# been healthy for a sustained period.
LAST_GOOD_FILE="$RUNTIME_BASE/last_good.json"

# How many consecutive fully-healthy loops are required before we record
# the current release as "last known good".
# 4 * 15s = ~60 seconds.
GOOD_STREAK_REQUIRED=4
good_streak=0

# Rollback control.
# If we keep failing health checks even after restarting stub-engine,
# we can rollback runtime/current to the last known good release.
ROLLBACK_FAIL_THRESHOLD=6
fail_streak=0
ROLLBACK_COOLDOWN_SECONDS=600
last_rollback_epoch=0

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


handle_restart_flag() {
  if [[ -f "$RESTART_FLAG" ]]; then
    log "INFO: Restart requested via $RESTART_FLAG; restarting stub-engine…"
    # Attempt restart; even if it fails, do NOT delete the flag (so we keep trying and it's visible).
    if restart_service stub-engine; then
      # If restart succeeded, remove the flag so we don't loop. The operator can re-request anytime.
      rm -f "$RESTART_FLAG" || true
      log "INFO: stub-engine restarted and restart flag cleared."
    else
      log "ERROR: stub-engine restart failed; leaving restart flag in place."
    fi
  fi
}

current_release_path() {
  # Resolve the current symlink to an absolute directory path.
  # Returns empty string if it cannot be resolved.
  if [[ -L "$CURRENT_LINK" ]]; then
    readlink -f "$CURRENT_LINK" 2>/dev/null || true
    return
  fi
  echo "" 
}

fetch_engine_version() {
  # Best-effort: ask the engine for its version.
  # Keep this extremely defensive; never break the watchdog if parsing fails.
  local v
  v="$(curl -fsS --max-time 2 http://127.0.0.1:8787/api/version 2>/dev/null | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]\+\)".*/\1/p' | head -n 1 || true)"
  echo "${v}"
}

write_last_good() {
  local path="$1"
  if [[ -z "$path" ]] || [[ ! -d "$path" ]]; then
    return 1
  fi
  local ts v
  ts="$(date -Is)"
  v="$(fetch_engine_version)"
  cat >"$LAST_GOOD_FILE" <<EOF
{"path":"$path","ts":"$ts","version":"$v"}
EOF
  log "Recorded last known good: $path (v${v:-unknown})"
  return 0
}

read_last_good_path() {
  if [[ ! -f "$LAST_GOOD_FILE" ]]; then
    echo ""
    return 0
  fi
  sed -n 's/.*"path"[[:space:]]*:[[:space:]]*"\([^"]\+\)".*/\1/p' "$LAST_GOOD_FILE" | head -n 1 || true
}

rollback_to_last_good() {
  local now
  now="$(date +%s)"
  if (( now - last_rollback_epoch < ROLLBACK_COOLDOWN_SECONDS )); then
    log "WARN: rollback suppressed (cooldown active)"
    return 1
  fi

  local current target
  current="$(current_release_path)"
  target="$(read_last_good_path)"

  # If we don't have a marker yet, fall back to "previous release" (newest excluding current).
  if [[ -z "$target" ]] || [[ ! -d "$target" ]]; then
    target="$(ls -1dt "$RELEASES_DIR"/* 2>/dev/null | grep -v -F "${current}" | head -n 1 || true)"
  fi

  if [[ -z "$target" ]] || [[ ! -d "$target" ]]; then
    log "ERROR: rollback requested but no valid target found"
    return 1
  fi
  if [[ -n "$current" ]] && [[ "$target" == "$current" ]]; then
    log "ERROR: rollback target equals current; refusing"
    return 1
  fi

  log "ROLLBACK: switching current -> $target"
  ln -sfn "$target" "$CURRENT_LINK" || true

  # Restart services to pick up the release switch.
  restart_service stub-engine || true
  restart_service stub-ui-watch || true
  if nginx -t >/dev/null 2>&1; then
    systemctl reload nginx >/dev/null 2>&1 || restart_service nginx || true
  else
    restart_service nginx || true
  fi

  last_rollback_epoch="$now"
  fail_streak=0
  engine_fail_count=0
  good_streak=0

  log "ROLLBACK: complete"
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
  # NOTE: We keep this *very* defensive.
  # If /api/health is temporarily unhappy, but other endpoints respond,
  # we should NOT restart the engine and make the situation worse.
  if curl -fsS --max-time 2 http://127.0.0.1:8787/api/health >/dev/null 2>&1; then
    engine_fail_count=0
    return 0
  fi

  # Fallback probe (v0.2.93): if /api/config responds, the engine HTTP server is alive.
  # This avoids restart loops caused by a single endpoint regression.
  if curl -fsS --max-time 2 http://127.0.0.1:8787/api/config >/dev/null 2>&1; then
    log "WARN: /api/health failed but /api/config is OK. Treating engine as up (no restart)."
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

    # 2b) Apply any operator-requested restart (e.g., config mode change)
handle_restart_flag || true

# 3) Validate and (if OK) reload nginx
    local nginx_ok=0
    if check_nginx_config; then
      nginx_ok=1
      # Reload only if active, otherwise restart already attempted above.
      if is_active nginx; then
        systemctl reload nginx >/dev/null 2>&1 || true
      fi
    fi

    # 4) Engine health endpoint
    local engine_ok=0
    if check_engine_health; then
      engine_ok=1
    fi

    # 5) "Last known good" + rollback logic
    # Fully healthy means:
    #   - nginx config is valid
    #   - nginx, stub-engine, and stub-ui-watch are active
    #   - engine /api/health responds
    local services_ok=0
    if is_active nginx && is_active stub-ui-watch && is_active stub-engine; then
      services_ok=1
    fi

    if (( nginx_ok == 1 && engine_ok == 1 && services_ok == 1 )); then
      fail_streak=0
      good_streak=$((good_streak + 1))
      if (( good_streak >= GOOD_STREAK_REQUIRED )); then
        # Only write if it changed; keep log noise down.
        local cur
        cur="$(current_release_path)"
        local prev
        prev="$(read_last_good_path)"
        if [[ -n "$cur" ]] && [[ "$cur" != "$prev" ]]; then
          write_last_good "$cur" || true
        fi
        good_streak=$GOOD_STREAK_REQUIRED
      fi
    else
      good_streak=0
      fail_streak=$((fail_streak + 1))
      if (( fail_streak >= ROLLBACK_FAIL_THRESHOLD )); then
        log "ERROR: sustained failures detected (${fail_streak}/${ROLLBACK_FAIL_THRESHOLD}) — attempting rollback"
        rollback_to_last_good || true
      fi
    fi

    sleep "$CHECK_INTERVAL_SECONDS"
  done
}

# If called directly, run the loop.
main_loop

# v0.2.39
# When performing a restart, record reason to watchdog/LAST_RESTART_REASON
# This is used by the UI to display restart reasons inline with systemd status.
