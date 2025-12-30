# STUB Mixer UI (Studio B) — Release 0.1.2

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


## Runtime layout (Option B)
- Repo (source): /home/wlcb/devel/StudioB-UI
- Runtime: /opt/studiob-ui/current (symlink)
- Releases: /opt/studiob-ui/releases/<stamp>-<gitsha>
- Config: /etc/studiob-ui/config.yml
- Logs: /var/log/studiob-ui/
- State: /var/lib/studiob-ui/
