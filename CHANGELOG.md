## 0.3.15

- UI: Studio mixer reskinned with a **dark blue neon** look (scoped to the mixer card only) to match the latest "high-tech" mockups — **no behavior changes**.
- UI: Fixed an HTML markup issue inside mixer strips that could duplicate VU marker elements in the DOM (rendering remained mostly OK, but the DOM was messy and harder to debug).

## 0.3.14

- UI: Studio mixer faders updated with a **glass handle** so the VU lane remains visible through the fader cap.
- UI: Added simple **level markers** on the per-channel VU lanes (0, -3, -6, -12, -20, -30, -∞) for faster at-a-glance reads.

## 0.3.13

- UI: Studio page now includes a **touch-first mixer strip prototype** (4 channels) with multitouch-style fader lanes and **WideOrbit-style MUTE buttons**.
  - **Operator intent only**: mic channels expose **only MUTE** (no automation modes).
  - Faders are **visual-only** (draggable for look/feel evaluation; no RC writes yet).
  - MUTE buttons are RC-backed: Host (RC 121), Guest 1 (RC 122), Guest 2 (RC 123), Guest 3 (RC 124).

## 0.3.12

- UI: Engineering now includes a **Recent Runtime Events** timeline (UI-only, in-memory, bounded) to answer "When did this change?" during troubleshooting.
  It logs key transitions such as engine connect/disconnect, engine version/mode changes, DSP health state changes, persisted config loads/saves, and runtime override activation/clear.

## 0.3.11

- Fix: Header **Update** pill could still show the tooltip "Update available" even when the system was up to date.
  Root cause: the pill's default HTML tooltip text could remain visible if older engines returned incomplete fields (e.g. missing `latestVersion`) while still returning a stale `updateAvailable` boolean.
  The pill is now always visible as a shortcut to Engineering, but its tooltip/styling are driven only by normalized UI version comparisons; the UI no longer trusts older engine booleans for availability.

## 0.3.10

- Fix: The **Update** pill tooltip/banner no longer sticks on "Update available" when the backend returns
  `currentVersion`/`latestVersion` with different formatting (e.g. `v0.3.09` vs `0.3.09`) or when an older
  engine incorrectly sets `updateAvailable=true`. The UI now prefers a normalized **version compare** whenever
  both versions are present, and only falls back to the engine boolean when versions are missing.

## 0.3.09

- Fix: The header **Update** pill no longer claims "Update available" when the UI is already up to date.
  Update availability is now computed strictly from `/api/update/check` (UI current vs UI latest),
  and does not accidentally compare against the pinned engine version.

## 0.3.08

- UI: When **Runtime override active** is shown, the badge now includes a tooltip explaining what it means and the most likely source (watchdog vs engine/runtime), without changing any behavior.

## 0.3.07

- UI: Engineering → Configuration now explicitly labels the form as **Persisted config (applies on restart)**.
- UI: Engineering shows **Persisted vs Runtime** modes side-by-side and displays a **Runtime override active** badge when they differ.

## 0.3.06

- UI: Header now shows **both** UI version and engine version (engine version comes from `/api/studio/status`, so you can immediately see if the engine failed to restart after an update).
- Fix: Engineering config form now reliably loads the persisted config after refresh (correct DSP IP field wiring + a safe fallback to engine status mode if the config payload is incomplete).

## 0.3.05

- Release bump only to trigger updater/import tooling. No functional changes from v0.3.04.

## 0.3.04

- Fix: DSP write mode now persists across refreshes/restarts even if a prior release wrote it to the deprecated top-level `mode` field (we migrate `mode` -> `dsp.mode` defensively).

## 0.3.03

- Fix: build failure during install — /api/health handler no longer does an invalid nil check on a non-pointer config copy, and uses the correct `DSPModeStatus` field name (`DesiredMode`).

## 0.3.02

- Fix: build failure during install — remove unused variable in config load warning path.

## 0.3.01

- Fix: Engineering config “Mode” display now always reflects the engine’s **desired** DSP write mode (so a browser refresh can’t *appear* to revert to `mock (default)` while the engine is still configured for LIVE).

## 0.3.00

- Fix: build failure in config editor after Config struct cleanup — reintroduced legacy top-level `mode` field (kept for backward compatibility; dsp.mode remains authoritative).

## 0.2.99

- Fix: build/installer regression — restore missing `Engine.DSPHealthSnapshot()` method used by debug snapshot code.
- Fix: when saving Engineering config, keep deprecated top-level `mode` in sync with `dsp.mode` to prevent any older tooling (or stale overrides) from appearing to “revert” after refresh.
- UI: bump UI build version to match release.

## 0.2.98

- Fix: UI controls were unresponsive due to a JavaScript syntax error (duplicate variable declarations in ui/app.js).
- Fix: “DSP Writes”/active mode no longer falls back to MOCK after refresh or engine restarts. Active mode is now derived from desired mode + DSP connectivity (removes volatile in-memory arming from the decision).
- UX: Engineering config form now auto-loads the *effective* (running) config via /api/config without requiring an admin PIN, so the Mode/DSP fields remain accurate across refreshes. Loading from the persisted config file still requires the PIN.

## 0.2.96
- UI: Increase default API request timeout (WAN-friendly) to prevent the UI from getting stuck on "Connecting..." and disabling controls when accessed over slower links.
- UI: Use cache-busted asset URLs for v0.2.96.

# Changelog

## v0.2.95

### Fixed
- **Engineering UI clarity:** on the Engineering tab, the Configuration form now auto-loads the saved config on page open/refresh (unless the user has started editing). This prevents a browser refresh from *appearing* to revert to "mock (default)" when the engine is still running LIVE.

### Notes
- No DSP read/write behavior changed; this is a UI hardening/quality-of-life fix.

## v0.2.94

### Fixed
- **Harden `/api/health` and `/api/version` in LIVE mode:** both endpoints now derive `desiredWriteMode` and `dspWriteMode` strictly from the loaded YAML config (no DSP-health locks). This prevents curl hangs / "Empty reply from server" symptoms that could cascade into watchdog restarts.

### Notes
- This is intentionally a narrow change: it only affects how the *health/version endpoints report mode*, not how DSP polling or DSP writes work.

## v0.2.93

### Fixed
- **Stop watchdog restart loops when `/api/health` is flaky:** watchdog now treats `/api/config` as a fallback liveness probe. If `/api/health` fails but `/api/config` succeeds, the watchdog will **not** restart the engine.
- **Make `/api/health` + `/api/version` extra-low-risk:** both endpoints no longer call the richer `DSPModeStatus()` helper (which touches more state/locks). They now report active write mode using the simplest possible check (`desired == live` AND `DSPLiveActive()`), and still return explicit JSON.

### Notes
- This release is purely about stabilizing health/version signaling in LIVE mode. No DSP write behavior changed.

## v0.2.92

### Fixed
- **Health endpoint reliability:** `/api/health` no longer performs on-demand config disk reloads (which could stall or race with writes). It now returns fast, always-JSON health using the engine's in-memory config snapshot, with panic protection and explicit logging.
- **Version endpoint correctness:** `/api/version` now reports the engine's active DSP write mode instead of a hard-coded value.

## v0.2.91 (2026-01-04)
- **Build fix:** restore successful installs by correcting a typo in `engine/internal/config.go` (`yamlPath` -> `path`).

## v0.2.90 (2026-01-04)
- Fix (config precedence): If `config.v1` (YAML) is newer than `config.json`, treat YAML as authoritative so stale JSON cannot force the engine back into `mock`.
- Self-heal: When YAML is authoritative, sync `config.json` to match YAML values (mode + DSP host/port) and log a clear notice.

## v0.2.89 (2026-01-04)

- UX (restart/refresh signaling): When an in-app **Update** finishes, the Engineering page now **automatically performs a cache-busting reload** (after showing an explicit message), instead of relying on the operator to manually refresh the browser.
  - The **"Refresh now"** button is still shown as a fallback, but you should no longer need to hit F5.

## v0.2.88 (2026-01-04)

- Fix: engine build error in cmd/stub-engine (package alias typo: `internal` -> `app`).
- Add: installer now runs `go test ./...` before building and prints a clear PASS/FAIL result (logs saved in each release folder).

## v0.2.87 (2026-01-04)

- UX: Engineering shows clearer “restart required” status and provides an admin-only “Restart engine now” button.
- UX: Engineering auto-clears the restart-required message once the watchdog restart completes (no manual page refresh).
- Fix: UI build version shown in the header is now kept in sync with VERSION.

## v0.2.86 (2026-01-04)
- Fix: Engineering config save now prefers `dsp.mode` when present (some UI builds send both `mode` and `dsp.mode`, which previously could save the wrong mode).
- Add: PUT /api/admin/config/file returns `mode_input_top`, `mode_input_dsp`, and `mode_source` for debugging.
- Add: engine logs `[config] Saving dsp.mode=...` when writing the operator config file.

## v0.2.85
- Fix: accept Engineering config mode sent as either top-level `mode` or `dsp.mode` (prevents silent mock fallback when UI sends nested value).
- Improve: API `/api/admin/config/file` PUT response includes normalized mode and saved config path.

## v0.2.84 (2026-01-04)

- Fix: Engineering config editor now resolves the operator config path deterministically under systemd.
  - Prefer `/home/wlcb/.StudioB-UI/config/config.v1` to avoid accidental drift to `/root/...` when HOME is unexpected.
  - Optional override: set `STUDIOB_UI_HOME` to force a different home (advanced/rare).
- Result: Saving Mode/DSP IP/Port updates the SAME config file the engine reads at startup, so restarts actually apply LIVE mode.

## v0.2.81

- Fix Engineering Mode save: accept UI label `live (reserved)` and normalize to `live` in config.v1.
- Make DSP write mode changes deterministic: saving config requests an engine restart (watchdog restarts stub-engine).
- /api/health now reports desired vs active write modes and whether a restart is pending (restartRequired).
- Engineering UI now tells the operator when a restart is required for changes to take effect.

## v0.2.80

- FIX: Engineering config editor now writes the *actual* config.v1 keys used by the engine (dsp.host / dsp.port / dsp.mode).
- FIX: /api/status and DSP mode reporting now reflect the running engine config (no more hard-coded "mock").
- UX: Mode "Saved and applied" now truly applies to the engine because reload updates the correct keys; LIVE can now be proven via Last write.

## v0.2.79 (2026-01-04)

- Fix build break in engine main.go (broken import block) that caused go build to fail during install.
- No behavior changes from the v0.2.77 intent (Mode hot-reload + Last write visibility); this is a compile/package fix only.

## v0.2.76 (2026-01-03)

- Phase 2 control plumbing (Speaker Mute becomes a REAL DSP write when `dsp.mode=live`):
  - Speaker Mute intent (`POST /api/intent/speaker/mute`) now attempts an External Control Protocol (ECP) write
    to the configured DSP host/port using `csv STUB_SPK_MUTE <0|1>`.
  - All writes are still *strictly scoped* to **Speaker Mute only** in this release.
  - The engine now appends a second, explicit audit record describing the DSP write attempt and result.
- Clarify LIVE gating:
  - `dsp.mode=live` enables writes immediately (there is no separate "arming" API/button in this project).
  - DISCONNECTED DSP still blocks control writes (defense-in-depth).

## v0.2.75 (2026-01-03)

- Phase 1 control plumbing (SAFE): Speaker Mute now uses an explicit intent endpoint.
  - UI calls `POST /api/intent/speaker/mute` instead of `POST /api/rc/...`.
  - Engine appends intents to `~/.StudioB-UI/state/intents.jsonl` (timestamped JSONL).
  - DSP writes remain **MOCKED** (no behavior change to live audio).

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


## v0.2.83

- Fix: Engine now always uses the canonical operator config path (~/.StudioB-UI/config/config.v1) so the Engineering UI and engine never drift (prevents "saved live but restarted back into mock" loops).
- Diagnostics: Log a loud warning if a non-canonical --config path is supplied, then force canonical for safety/clarity.

## v0.2.82
- Fix Go build error introduced in v0.2.81 (duplicate block in engine main.go).
- No behavior changes; restores successful install.
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

## v0.2.48 (2026-01-03)
- UI now shows systemd status verbatim plus DSP health snapshot
- Added manual 'Test DSP Now' wiring to engine (/api/dsp/test)
- Added DSP timeline endpoint (/api/dsp/timeline) and UI timeline display
- Added UI+server defense-in-depth: warn/block DSP control when DISCONNECTED
- No background DSP polling (only /api/dsp/test touches network)

## v0.2.49 (2026-01-03)
- Hotfix: fixed Go build failure introduced in v0.2.48 (escaped quotes in engine/internal/engine.go)
- No behavior change; restores successful compilation

## v0.2.50 (2026-01-03)
- Fix: 'Test DSP Now' no longer hangs in mock/simulate mode (returns immediately)
- UI: added hard client-side timeout + always-clears 'Testing…' state
- No DSP network traffic in mock/simulate mode

## v0.2.51 (2026-01-03)
- Fix: DSP timeline error due to missing getJSON helper in UI
- Timeline and DSP health now render correctly in mock and live modes
- No engine or DSP behavior changes

## v0.2.52 (2026-01-03)
- Added explicit mock → live DSP transition warning (visibility-only)
- UI warns when entering live mode with unvalidated DSP health
- Provides one-click 'Test DSP Now' and 'Acknowledge' actions
- No background DSP polling or automatic gating

## v0.2.53 (2026-01-03)
- Fix: Engineering Configuration panel error `loadConfig is not defined`
- Restored explicit config load/save helpers after refactors
- No engine or DSP behavior changes

## v0.2.54 (2026-01-03)
- Fix: Engineering Configuration Save no longer errors (`loadConfigFill/loadConfigPill` missing)
- Restored explicit post-save UI refresh helper and mode pill update
- No engine/DSP behavior changes

## v0.2.55 (2026-01-03)
- Transition banner polish: shows DSP IP:Port and validation age
- Warns when DSP config changed since last LIVE validation
- Visibility-only; no polling or automatic gating

## v0.2.56 (2026-01-03)
- Watchdog panel now includes DSP health summary (mode, state, last test, failures)
- Shows LIVE validation age and config-changed flag inline for operator clarity
- Visibility-only: no DSP polling, no watchdog behavior changes

## v0.2.57 (2026-01-03)
- Documentation: added Operating Procedures section
- Freeze checkpoint: no code or behavior changes

## v0.2.58 (2026-01-03)
- Fix: applying Engineering config now updates the running engine (no restart required)
- Live-mode banner now appears immediately after switching mode to live (until validated)
- Conservative: clears LIVE validation state when DSP-relevant config changes

## v0.2.59 (2026-01-03)
- Hotfix: v0.2.58 build failures (config apply + DSP constants + cfg scoping)
- Runtime ApplyConfig now uses *Config consistently and compiles
- No behavior changes beyond intended v0.2.58 fix (apply config without restart)

## v0.2.60 (2026-01-03)
- Hotfix: v0.2.59 build errors (remove unused cfg var; replace e.GetConfig -> GetConfigCopy)
- No behavior changes beyond intended runtime config apply

## v0.2.61 (2026-01-03)
- Engine: add always-on DSP connectivity monitor (read-only TCP connect) so UI reflects status continuously
- API: /api/health, /api/version, /api/config now report active in-memory config (after reload) instead of startup snapshot
- UI: DSP Health and Engineering DSP summary show Last Poll timestamp

## v0.2.62 (2026-01-03)
- Hotfix: v0.2.61 build failure (remove unused net import; add missing dspMonitorLoop)
- Always-on DSP monitor loop now compiles and updates /api/dsp/health continuously

## v0.2.63 (2026-01-03)
- Hotfix: v0.2.62 build failure (pass engine context into dspMonitorLoop)
- No behavior changes beyond enabling always-on DSP monitor to start correctly

## v0.2.64 (2026-01-03)
- Hotfix: v0.2.63 build failure (ctx undefined)
- DSP monitor loop now runs for engine lifetime (no context dependency)
- Maintains always-on, read-only DSP connectivity monitoring

## v0.2.65 (2026-01-03)
- Fix: UI now polls /api/dsp/health on an interval so DSP connectivity updates automatically
- Watchdog DSP summary now shows Last poll timestamp
- No changes to DSP monitor loop (engine already updates health every 2s)

## v0.2.66 (2026-01-03)
- Added explicit 'Enter LIVE Mode' button (gates DSP control writes)
- LIVE remains reserved until operator arms it (requires Admin PIN + DSP connected)
- /api/dsp/mode now reports desired vs active mode and armed state
- Always-on DSP monitoring remains read-only and continues updating status

## v0.2.67 (2026-01-03)
- Remove LIVE write gating (Option 1): DSP control writes follow config mode immediately
- Remove 'Enter LIVE Mode' UI/button and /api/dsp/enter_live usage
- Always-on DSP monitoring remains enabled and UI continues to auto-refresh status

## v0.2.68 (2026-01-03)
- Hotfix: restore UI functionality (remove orphaned JS block left from LIVE button removal)
- No behavior change: Option 1 remains (no LIVE gating; connect/monitor on startup)

## v0.2.69 (2026-01-03)
- UI clarity: separate pills for engine mode, DSP connectivity state, and DSP write mode
- Eliminates confusion where 'mode: mock' could be mistaken for DSP connectivity
- No behavior change: DSP monitoring remains always-on; writes follow configured mode (Option 1)

## v0.2.74 (2026-01-03)
- Fix: stop attempting to add a new engine Config.Mode field (it lives elsewhere); use DSP.Mode only
- Engineering: 'Mode' selector now explicitly controls DSP Writes (mock/live)
- API: /api/health 'mode' is engine simulation (mock); added dspWriteMode for clarity
- API: /api/config now includes dsp.mode; admin config writer persists it

## v0.2.77

- Fix: Engineering config Mode Save now reliably changes the *running engine* mode (mock/live) by reloading the same canonical config file the engine started with.
- Add: Explicit "Last DSP write" status in Engineering DSP summary (shows last write attempt, timestamp, and error if any).
- Safety: DSP writes still follow the existing gate; only Speaker Mute intent writes in LIVE.
