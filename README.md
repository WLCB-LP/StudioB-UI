# STUB Mixer UI (Studio B) — Release 0.3.36

This release contains:
- A minimal v1 web UI (Studio + Engineering pages)
- A Go "engine" scaffold with:
  - health endpoint
  - websocket state stream (mock data by default)
  - admin endpoints for update/rollback (script-backed)
- Install + service setup scripts
- Git publish helper script

## Quick install (one-line)
Run:
  ./install.sh

## Notes

- v0.3.36: UI-only: Layout polish — tightened inter-group gaps and added a small PC↔Zoom separation while preserving **no scrolling** and **no vertical stacking**.
- v0.3.34: UI-only: Mixer fader bank regrouped with **elastic gaps** so cards stay in a single row with **no scrolling** and **no vertical stacking**:
  `Mic×4  | gap | CD×2 | gap | AUX | gap | BT | gap | PC+Zoom`.

- v0.3.33: UI-only (superseded by v0.3.34): initial attempt to spread groups; could cause stereo cards to stack vertically.

- v0.3.32: UI-only: stereo sources now show **dual VU placeholders** (L+R) behind the fader lane; UI build version is kept in sync with `VERSION`.

- v0.3.14: Studio mixer faders updated with a **glass handle** (meters visible through) and added **level markers** on the per-channel VU lanes.
- v0.3.15: Studio mixer visual **blue neon skin** pass (dark, flashy, high-tech) — scoped to mixer only; no behavior changes.
- v0.3.22: Installer health checks are **retry-based** (tolerant of slow restarts) and print `systemctl/journalctl` diagnostics on failure.
- v0.3.23: rc_allowlist repair is **mawk-safe** (POSIX awk) to avoid install-time awk syntax errors.
- v0.3.26: Studio mixer (historical): fader positions were **persisted in the browser** (localStorage) to survive reloads. (Superseded by v0.3.30 DSP/engine-authoritative hydration.)
- v0.3.27: Studio mixer: add source channel faders (CD1/CD2 grouped, AUX/Bluetooth/PC/Zoom individual cards).
- v0.3.20: Installer/update **self-repairs rc_allowlist** to include fader RCs **101–110** so gain writes are not blocked.
- v0.3.19: Studio mixer: **Host fader now writes gain** to **RC 101** (phased rollout; others remain visual-only).
- v0.3.18: Studio mixer polish: stronger **muted neon red border** + slightly brighter **live green fill** (visual-only).
- v0.3.16: Studio mixer **tactile polish**: grabby fader feedback + neon MUTE states (green/live, red/muted) + clearer 0/-12 reference marks.
- v0.3.13: Studio page now includes a **touch-first mixer strip prototype** (4 channels) with multitouch-style fader lanes and **RC-backed MUTE buttons** (operator intent only; faders are visual-only for now).
- v0.3.12: Engineering now includes a **Recent Runtime Events** UI-only timeline to provide "When did this change?" context (engine connect/disconnect, engine version/mode changes, DSP health transitions, persisted config loads/saves, and runtime override changes).

- v0.3.11: Fix: Header **Update** pill tooltip could still claim "Update available" when the system was up to date. The pill is now always visible as a shortcut to Engineering, and its tooltip/styling are driven only by normalized UI version comparisons (ignoring older engine booleans).

- v0.3.10: Fix sticky "Update available" tooltip/pill when backend versions are formatted differently (e.g. `v0.3.09` vs `0.3.09`) or an older engine sets `updateAvailable=true` incorrectly.
- v0.3.08: Runtime override badge now includes a tooltip explaining likely source (watchdog vs engine/runtime).
- v0.3.07: Engineering page clarifies persisted vs runtime mode (label + runtime override badge).
- v0.3.06: Engineering page config loads reliably after refresh; header shows both UI and engine versions.
- v0.3.04: Fixes a regression where DSP write mode could fail to persist across refresh/restart if a prior release wrote the mode to the deprecated top-level `mode` field (we now migrate it into `dsp.mode`).
- v0.3.03: Fixes an install-time build failure (`go test`) in the `/api/health` handler (invalid nil check + wrong DSP mode field name).
- v0.3.01: Fixes a UI refresh issue where Engineering could show `mock (default)` even when the engine's **desired** DSP write mode is `live`. `/api/config` now reports the engine's desired mode, avoiding confusing "flip back to mock" displays after refresh.
- v0.3.02: Fixes an install-time build failure (`go test`) caused by an unused variable in the config loader warning path.
- v0.2.99: Fixes a v0.2.98 build failure (missing `DSPHealthSnapshot()` compatibility shim) and keeps deprecated top-level `mode` in sync with `dsp.mode` when saving config to avoid apparent “reverts” after refresh.

- v0.2.98: Fixes unresponsive UI controls (JS syntax error), keeps “DSP Writes”/active mode accurate after refresh, and loads the effective config into the Engineering form without requiring a PIN.

- v0.2.96: Engineering UI hardening — auto-load the saved config into the Engineering → Configuration form after a browser refresh (avoids the misleading "mock (default)" placeholder state).
- v0.2.94: **Hardening change:** `/api/health` and `/api/version` now derive `desiredWriteMode`/`dspWriteMode` strictly from the loaded YAML config (no DSP-health locks). This prevents curl timeouts/"Empty reply" symptoms in LIVE mode and gives the watchdog a deterministic, fast endpoint.
- v0.2.92: Fixes a config precedence bug where a stale `~/.StudioB-UI/config.json` could override a newer `config.v1` and keep the engine in `mock` mode. YAML now wins when it is newer, and the engine syncs JSON to match.
- v0.2.91: Fixes installer build/test failure caused by a `yamlPath` variable typo in `engine/internal/config.go` (no behavior change).
- Update UI: When an in-app **Update** completes, the Engineering page now **auto-reloads the UI (cache-busted)**. The “Refresh now” button is still provided as a fallback.
- v0.2.88: Engineering surfaces **engine restart-required** state more clearly, and provides a
  one-click **Restart engine now** button (admin-only) so you don't have to manually refresh while
  testing mode changes.
- DSP control protocol is intentionally gated ("mock mode") until Engineering explicitly enables writes.
- v0.2.86: Speaker Mute is plumbed through the explicit **intent** path:
  UI → intent → engine → (DSP write gate).
  - Intents are append-logged to: `~/.StudioB-UI/state/intents.jsonl`
- v0.2.76: Speaker Mute can now perform a **real DSP write** when `dsp.mode=live`.
  - The engine uses Q-SYS External Control Protocol (ECP) over TCP and issues:
    `csv STUB_SPK_MUTE 0` or `csv STUB_SPK_MUTE 1`
  - This release is still strictly scoped to **Speaker Mute only**.
  - Every intent is logged, and every DSP write attempt/result is also logged (append-only JSONL).
- Update/Rollback are implemented as **local git operations** on the VM:
  - Update: fetch + fast-forward main (or latest tag if configured) then reinstall
  - Rollback: checkout a prior git tag and reinstall


## API
- `GET /api/health` — health + version
- `GET /api/state` — full RC snapshot (debug)
- `GET /api/studio/status` — stable Studio UI contract (speaker + meters)
- `POST /api/rc/<id>` — set allowlisted RC value (debug / interim)
- `POST /api/intent/speaker/mute` — Speaker Mute via intent (logs action + timestamp)
- `POST /api/reconnect` — operator-safe reconnect (stub)

## Runtime layout (LOCKED)
**Repo (source of truth):** `/home/wlcb/devel/StudioB-UI`

**Runtime / config / logs (Node-RED style):** `/home/wlcb/.StudioB-UI/`
- `config/config.yml`
- `runtime/releases/<timestamp-tag>/`
- `runtime/current -> runtime/releases/<timestamp-tag>`
- `logs/`
- `state/`


## GitHub Releases automation

This repo is configured to auto-create GitHub Releases using Release Please.

- Merging to `main` updates (or opens) a Release PR.
- Merging the Release PR tags the repo (e.g. `v0.2.24`) and triggers an Actions workflow that builds and uploads `StudioB-UI_vX.Y.Z.zip` to the GitHub Release.

The StudioB-UI engine can check GitHub once per minute for `releases/latest` and queue the newest ZIP into the watched `tmp/` folder.


## Troubleshooting

### Watchdog logs

- Journal: `sudo journalctl -u stub-ui-watchdog -f --no-pager`
- File: `/var/log/stub-ui-watchdog.log` (rotated via logrotate)

### Release 0.2.38
Adds operator-visible Watchdog health and recent events. No automation behavior changes.

### Release 0.2.39
Adds inline visibility of watchdog restart reasons tied to systemd service status. No changes to restart or repair behavior.

### Release 0.2.40
Shows systemd "Active:" line and SubState for stub-ui-watchdog verbatim in the Engineering UI. Visibility-only.

### Release 0.2.41
Fixes a UI JavaScript syntax error that prevented navigation after v0.2.40.

### Release 0.2.42
Adds operator-visible DSP connection health detection. The system warns on stale or disconnected DSP links but does not perform automatic reconnects.

### Release 0.2.43
Adds a DSP health history timeline so operators can see recent DSP link state transitions (OK / Degraded / Disconnected) with timestamps. Visibility only; no automatic repair.

### Release 0.2.44
Adds an explicit operator-controlled 'Test DSP Now' action. This performs a single DSP connectivity test with a strict timeout and records the result in DSP health and history. No automatic polling or reconnect logic is introduced.

### Release 0.2.45
Adds an operator safety gate: if DSP health is Disconnected, DSP control actions are blocked and the UI warns the operator, offering a quick 'Test DSP Now' action. No automatic reconnect behavior is introduced.

### Release 0.2.46
Adds defense-in-depth: the engine refuses DSP control commands when DSP health is DISCONNECTED (in live mode). Also adds read-only DSP health and an explicit manual DSP connectivity test endpoint. No polling or auto-repair.

### Release 0.2.47
Hotfix for v0.2.46 build failure. Restores successful engine compilation and keeps DSP server-side guard behavior unchanged.

### Release 0.2.48
Wires the operator-controlled 'Test DSP Now' to the engine and displays DSP health + a recent timeline. Also adds defense-in-depth: UI and engine both block control commands when DSP is DISCONNECTED. DSP network traffic occurs only on explicit tests.

### Release 0.2.49
Hotfix for v0.2.48 build failure (invalid escaped quotes in Go source). No functional changes.

### Release 0.2.50
Hotfix for mock-mode workflows: manual 'Test DSP Now' now returns immediately without any network calls. UI also enforces a strict timeout so it never stays stuck on 'Testing…'.

### Release 0.2.51
Fixes a UI JavaScript error where DSP timeline rendering failed because a shared getJSON helper was not in scope. No backend or DSP behavior changes.

### Release 0.2.52
Adds explicit operator-visible handling for transitions from mock/simulate to live DSP mode. When live mode is entered without a validated DSP link, the UI shows a clear warning banner with manual actions. No automation or background DSP traffic is introduced.

### Release 0.2.53
Hotfix restoring the Engineering Configuration load/save helpers. Fixes a UI regression where the config panel could not load or save.

### Release 0.2.54
Hotfix for Engineering Configuration: fixes Save regression caused by a missing post-save helper (loadConfigFill/loadConfigPill). Save now completes and the mode pill updates immediately.

### Release 0.2.55
Improves LIVE transition visibility by showing the configured DSP endpoint (IP:Port), how long ago the DSP link was last validated, and a warning if DSP config changed since the last LIVE validation. No automation is introduced.

### Release 0.2.56
Adds a DSP health summary inside the Engineering Watchdog card so operators can see DSP mode, connection state, recent test time, failures, and LIVE validation context in one place. Visibility-only; no watchdog automation added.


---
## Operating Procedures

### Modes: mock vs live

- **mock (default):**
  - No DSP network traffic
  - DSP state begins as UNKNOWN
  - Use *Test DSP Now* to exercise UI + engine paths safely

- **live:**
  - DSP network traffic is allowed **only** when explicitly triggered
  - Entering live mode does NOT auto-test the DSP
  - A visible banner warns until validation is performed

### Recommended LIVE transition workflow

1. Switch `dsp.mode` to `live` in Engineering → Configuration
2. Save configuration
3. Observe LIVE warning banner
4. Click **Test DSP Now** once
5. Confirm DSP Health = OK and banner clears

### Interpreting DSP Health

- **UNKNOWN:** no validation performed yet
- **OK:** last validation succeeded
- **Failures:** count of consecutive failures
- **Timeline:** recent validation history

### Watchdog + DSP Summary

- Watchdog panel always reflects service state
- DSP (summary) shows:
  - mode
  - last test
  - validation age (LIVE)
  - config-changed warning if applicable

No automatic repairs or polling are performed.
All actions remain explicit and operator-driven.

### Release 0.2.58
Fixes a mismatch where Engineering config could be saved but the running engine stayed in the old mode until restart. The engine now applies the validated config in-memory after a successful save, and LIVE transition warnings appear immediately when switching to live.

### Release 0.2.59
Hotfix for v0.2.58 compilation errors. Keeps the intended behavior: applying saved config to the running engine without restart.

### Release 0.2.60
Hotfix for v0.2.59 compilation errors. Keeps intended behavior: config Save applies immediately to the running engine.

### Release 0.2.61
- Adds an always-on, read-only DSP monitor loop so the UI continuously reflects DSP reachability.
- Saving config.yml hot-reloads the engine and API endpoints now reflect the active config immediately.

### Release 0.2.62
Hotfix for v0.2.61 build failure. Adds the missing always-on DSP monitor loop implementation and removes an unused import so the engine builds cleanly.

### Release 0.2.63
Hotfix for v0.2.62 compilation issue. The DSP monitor loop now receives the engine context when started.

### Release 0.2.64
Hotfix for v0.2.63 compilation error (undefined ctx). The DSP monitor loop no longer depends on a context and runs for the lifetime of the engine process under systemd.

### Release 0.2.65
Fixes a UI regression where DSP connectivity did not appear to update because the UI only refreshed DSP health on manual actions. The UI now polls /api/dsp/health on a short interval so the continuously updated engine-side DSP monitor status is visible.

### Release 0.2.66
Adds an explicit 'Enter LIVE Mode' action that enables DSP control writes only after an operator confirmation. DSP monitoring remains always-on and read-only; LIVE gating affects writes only.

### Release 0.2.67
Implements Option 1: the system connects/monitors DSP on startup and allows DSP control writes immediately when config dsp.mode is set to 'live' (no additional operator arming step).

### Release 0.2.68
Hotfix for v0.2.67: fixes a JavaScript syntax error that prevented the UI from initializing (buttons unresponsive). Option 1 behavior remains unchanged.

### Release 0.2.69
UI clarity update. The header now shows Engine mode separately from DSP connectivity state and DSP write mode, so operators can distinguish simulation state from real DSP connection/controls.

### Release 0.2.77
DSP-mode-only fix. StudioB-UI always monitors DSP connectivity on startup. The Engineering mode selector controls DSP write behavior via dsp.mode (mock/live). No new engine config fields were added.


### Operator config

The Engineering page edits the operator config file at `~/.StudioB-UI/config/config.v1` (persisted across updates). Mode changes are applied immediately by hot-reloading the running engine.
