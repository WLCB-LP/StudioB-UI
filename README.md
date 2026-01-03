# STUB Mixer UI (Studio B) — Release 0.2.37

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

- Update UI: Update/Rollback now show an explicit “Refresh Now” button when the engine restarts or the version changes.
- DSP control protocol is intentionally stubbed ("mock mode") until we wire it to Symetrix control.
- Update/Rollback are implemented as **local git operations** on the VM:
  - Update: fetch + fast-forward main (or latest tag if configured) then reinstall
  - Rollback: checkout a prior git tag and reinstall


## API
- `GET /api/health` — health + version
- `GET /api/state` — full RC snapshot (debug)
- `GET /api/studio/status` — stable Studio UI contract (speaker + meters)
- `POST /api/rc/<id>` — set allowlisted RC value (debug / interim)
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
