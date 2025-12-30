# STUB Mixer UI (Studio B) — Release 0.1.7

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
