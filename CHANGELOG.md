## v0.2.37 (2026-01-03)

- UI: harden Update/Rollback operator flow:
  - Disable Update while running; prevent double-submit.
  - Clear success vs failure messaging using styled status banners.
  - When the engine returns on a new version, show explicit **Refresh Now** button (no silent auto-refresh).
  - Add Clear button to dismiss sticky admin messages.

## v0.2.36 (2026-01-03)

- UI: fix Engineering "Update" button error in Firefox ("Response.text: Body has already been consumed") by reading fetch response body only once.

## v0.2.35 (2026-01-03)

- Installer: add strict UI asset validation before switching the `runtime/current` symlink.
  This catches broken/partial UI deploys (missing files, wrong cachebuster, and JavaScript syntax errors
  when `node` is installed) so we never ship a "buttons don't work" UI live.
- Installer: ensure staged release assets are owned by the application user (prevents root-owned UI assets
  in runtime releases when install runs under sudo).

## v0.2.34 (2026-01-03)

- Fix: UI buttons/navigation were broken due to a JavaScript syntax error (an accidental multi-line quoted string) which prevented the UI from loading its event handlers.


## v0.2.33 (2026-01-03)

- chore: version bump for folder watcher ingest test


## v0.2.32 (2026-01-02)

- Fix: UI/CLI update endpoint (`/api/updates/apply`) now runs the correct admin *action* ("update") instead of sending the script filename ("admin-update.sh"), which previously produced `unknown admin action: admin-update.sh` and caused updates to fail.

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
## v0.2.38 (2026-01-03)
- Added Watchdog Visibility UI (health summary, last action, reason)
- Exposed recent watchdog events in UI (read-only)
- Defensive validation of watchdog log presence

## v0.2.39 (2026-01-03)
- Exposed watchdog restart reasons inline with systemd status
- Watchdog now records last restart reason in a dedicated state file
- UI displays restart reason alongside service status (visibility only)

## v0.2.40 (2026-01-03)
- Show systemd Active and SubState strings verbatim in the UI (watchdog)

## v0.2.41 (2026-01-03)
- Fixed UI navigation regression (JavaScript unescaped newline) that prevented page routing.

## v0.2.42 (2026-01-03)
- Added DSP connection validation (visibility-only)
- Detects stale or disconnected DSP links and warns operator
- Exposes last successful DSP contact time and last error
- No automatic reconnect behavior added

## v0.2.43 (2026-01-03)
- Added DSP health history timeline (operator-visible)
- UI shows recent DSP health transitions with timestamps
- Timeline is append-only and bounded for safety

## v0.2.44 (2026-01-03)
- Added manual 'Test DSP Now' action (single-shot)
- Performs one bounded DSP round-trip on operator request
- Updates DSP health state and timeline based on result
- No background polling or automatic retries

## v0.2.45 (2026-01-03)
- Warn when operator attempts DSP control while DSP is Disconnected
- Control actions are blocked with an explicit warning (operator safety)
- Provides a one-click path to run 'Test DSP Now'
- No automatic reconnect or retries added

## v0.2.46 (2026-01-03)
- Server-side DSP control guard (defense-in-depth)
- Engine blocks /api/rc control attempts when DSP is Disconnected (live mode)
- Added /api/dsp/health (read-only) and /api/dsp/test (manual, single-shot)
- No background polling or automatic reconnects added

## v0.2.47 (2026-01-03)
- Fix: engine build failure in v0.2.46 (missing DSP guard fields on Engine)
- DSP server-side guard now compiles correctly; mock/simulate modes bypass guard
