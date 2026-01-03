## v0.2.31 (2026-01-02)

- Engine: make admin-PIN failures return JSON (instead of plain text) so `/api/updates/apply` works cleanly with `curl | jq`.
- Engine: normalize API error responses to JSON for method/auth/validation failures.

## v0.2.30 (2026-01-02)

- Fix: stub-engine build break caused by calling `writeJSON` with missing HTTP status code.

## v0.2.29 (2026-01-02)

- Engine: fix build break in `/api/updates/apply` (admin PIN check + JSON response).
- Engine: use configured `admin.pin` field name consistently (`PIN`).

## v0.2.28 (2026-01-02)

### Fixed
- Engine: fix build break introduced in v0.2.27 (admin update status timestamps are RFC3339 strings).

## v0.2.27 (2026-01-02)

### Fixed
- Update-from-UI now reports real success/failure because `/api/updates/apply` waits for the installer and returns the combined output (instead of always replying OK while the update fails in the background).

## v0.2.25
- Fix: installer no longer runs 'go mod tidy' during updates; builds with -mod=readonly and stable caches to prevent update failures.

## v0.2.24 (2026-01-01)

### Fixed
- Watchdog start now enables+starts the systemd unit and reports real success/failure (no swallowed errors).
- Engineering UI polls watchdog status and updates immediately after a start request.

## v0.2.23 (2026-01-01)

- Watchdog: record "last known good" release after sustained health; rollback `runtime/current` automatically on sustained failures.
- Engine: add watchdog status endpoint (`/api/watchdog/status`) and admin start endpoint (`/api/admin/watchdog/start`).
- UI (Engineering): display watchdog status and allow starting the watchdog when it is enabled but not running.
- Installer: allow the controlled watchdog-start admin script via sudoers.

## v0.2.20 (2026-01-01)

## v0.2.22 (2026-01-01)

- Watchdog: create /var/log/stub-ui-watchdog.log and install logrotate policy during install.


- Installer: preserve existing stub-ui-watchdog enable/disable state during updates; enable watchdog by default on fresh installs.
- Installer: ensure watchdog stop/disable is respected when user has disabled it (no surprise restarts).

## v0.2.19
- Test-only release bump to validate updater + watchdog enable/disable behavior.

## v0.2.18
- Harden installer journald logging: add error/success traps and detailed failure output.
- Ensure watchdog is (re)enabled and started during installs, even if previously stopped/disabled.
- Add explicit "Install complete" and step boundary logs.

## v0.2.15

## v0.2.17

- Harden installer logging and ensure watchdog is left enabled/running even if an install fails mid-way.



## v0.2.16 - 2026-01-01

- Harden installer success checks for systemd units (watchdog + watcher).
- Installer now logs to journald via `logger` for post-mortem debugging.


- Add a **watchdog** service (`stub-ui-watchdog`) that continuously monitors:
  - `stub-engine`
  - `nginx`
  - `stub-ui-watch` (release folder watcher)
  
  It logs to journald and `/var/log/stub-ui-watchdog.log`, and performs safe auto-repair actions (restart services, reload nginx, repair a broken runtime `current` symlink). It can also repair nginx config by invoking `scripts/install_full.sh --configure-nginx-only`.

## v0.2.14
- Test-only release: bump version to validate update notification and pipeline.

## v0.2.13 - 2026-01-01

- UI: fix update-check rendering so the status message can never remain stuck on the startup “pending…” placeholder if a later non-critical step throws during polling. The UI now renders `/api/update/check` results immediately after parsing.

## v0.2.12 - 2026-01-01

- UI: fix update-status messaging getting stuck on “Update check failed” in environments where sessionStorage is disabled (auto-refresh no longer aborts update rendering).
- UI: show a neutral “Update check: pending…” message until the first check completes.

## v0.2.11 - 2026-01-01

### Fixed
- UI: finalize separation between **Admin action status** (update/rollback workflow) and **Update-check status** (GitHub/latest-version polling). During an in-progress update/rollback, transient update-check failures no longer overwrite the last known-good update-check message.
- UI: make `/api/update/check` parsing more defensive (treat non-JSON responses as transient errors and keep the last known-good status).


## v0.2.10 - 2026-01-01

### Fixed
- UI: "Update check" status message is no longer sticky. It now always reflects the latest `/api/update/check` poll result (a prior transient failure cannot leave the UI stuck on "Update check: failed").


## v0.2.9 - 2026-01-01

### Fixed
- UI: fix update-status correctness by ensuring the UI bundle's embedded build version is synced to `VERSION` (via `scripts/sync_ui_cachebuster.sh`). This prevents "new engine / old UI" false positives that could leave the update UI in a confusing state.


## v0.2.8 - 2026-01-01

### Fixed
- UI: update-check status no longer falsely reports failure if /api/health is temporarily unreachable; update status is always derived from /api/update/check, with /api/health treated as optional.
- Ops: restores safe upgrade path after the v0.2.7 /api/health duplicate-route panic by keeping only a single health handler in the engine.


## v0.2.6 - 2025-12-31

- Updates: make update-check robust by falling back to local git tags when remote checks fail (prevents false "Update check: failed" when the system already knows the latest tag locally).

## v0.2.5 - 2025-12-31

### Fixed
- Engineering page update-check status message now correctly distinguishes:
  - **Up to date** (latest == current)
  - **Update available**
  - **Update check disabled** (not configured)
  - **Update check failed** (real error)

## v0.2.4 - 2025-12-31

### Fixed
- Fix UI-triggered updates failing due to nginx config error: removed a duplicate `location = /index.html` block that caused `nginx -t` to fail.

## v0.2.3 - 2025-12-31

### Fixed
- Eliminated "stale UI after update" confusion:
  - `ui/index.html` now has explicit `no-store` hints and versioned cache-busters for **both** `app.js` and `styles.css`.
  - UI auto-detects "new engine / old UI" mismatch and triggers a one-time hard refresh.
- Engineering config editor now targets the canonical config file: `~/.StudioB-UI/config/config.yml`.

### Ops
- Installer syncs UI asset cache-busters to `VERSION` at install time.
- nginx config adds `Cache-Control: no-store` for `/index.html`.

## v0.2.2 - 2025-12-31

- Fix: engine build failure during update/install (Go package import aliasing) that prevented UI-triggered updates from completing.

## v0.2.1 - 2025-12-31

- Engineering: add config editor (mode + DSP IP/port) with validation, backups, and atomic writes.
- Updates: improve visibility into update-check failures (UI displays last check status/details).

## v0.2.0 - 2025-12-31

- Add mode plumbing (mock vs live) with env + config overrides and new `/api/config` endpoint.
- No behavior changes to mock mode; live mode is reserved for future DSP control.

# Changelog

## v0.1.32

- Release bump for update-path testing.


## [0.1.14](https://github.com/WLCB-LP/StudioB-UI/compare/v0.1.13...v0.1.14) (2025-12-31)


### Bug Fixes

* **engine:** require release ZIP asset and refuse zipball updates ([029e5af](https://github.com/WLCB-LP/StudioB-UI/commit/029e5afd5e73dd118b3bc748a9c8288a3cf442a0))

## [0.1.13](https://github.com/WLCB-LP/StudioB-UI/compare/v0.1.12...v0.1.13) (2025-12-31)


### Bug Fixes

* release pipeline verification ([1da277f](https://github.com/WLCB-LP/StudioB-UI/commit/1da277f03a6796250a0c85fbcbbea5595561f902))

## [0.1.12](https://github.com/WLCB-LP/StudioB-UI/compare/v0.1.11...v0.1.12) (2025-12-31)


### Bug Fixes

* **ui:** enable update check polling and indicator ([e2187a3](https://github.com/WLCB-LP/StudioB-UI/commit/e2187a35ead5c00bd74df47e9e9c4acf7b6b774e))

## 0.1.36
- Fix UI cache-busting version.
- Make admin update run install_full.sh via systemd-run when available (avoids systemd sandbox /etc RO issues).
