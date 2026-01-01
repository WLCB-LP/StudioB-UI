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
