## v0.3.83
- Engine (Symetrix): Fix "DSP truth on page load" by refreshing control RCs from the DSP right before sending the WebSocket rc_state snapshot.
  - This ensures faders/mutes/speakers/automute reflect DSP state even if the engine started earlier.


## v0.3.82
- Engine (Symetrix): Publish **all** meter RCs over `/ws` (not just 462/463).
  - Meters published: 401–410 (bottom row VU), 411–412 (program), 460–461 (speakers), 462–463 (PlayIt Live).
- Engine (Symetrix): Seed control RC cache from DSP on startup (truth on page load).
  - Controllers synced once at boot: 101–110 (faders), 121–130 (mutes), 160 (speakers fader), 560 (automute indicator).
- UI: VU display scaling option to visually match meter travel to the fader's -72..+12dB span.
- UI: Speaker automute alert — when RC 560 is true, Speakers card/fader glows red.

## v0.3.81
- Engine (Symetrix): Fix fader/mute writes failing with "connection reset by peer".
  - Switch CS controller writes to UDP (same rationale as meter polling).
  - Add GS2 UDP readback verification so writes are only acknowledged when the DSP reflects the requested value (DSP remains source of truth).

## v0.3.80
- HOTFIX (UI boot): Fix a JavaScript syntax error that prevented the Studio page from loading ("expected property name, got '/'").
  - Restores a valid `state.meters` object for the smoothing loop.
  - Moves bottom-row VU meter helpers (RC 401–410) out of the `state` literal into normal functions.
  - Bumps the UI build version constant to match `VERSION`.

## v0.3.79
- HOTFIX (build): Fix a stray newline that produced a Go parser error ("newline in string") in `engine/internal/engine.go`.
  - No behavior changes vs v0.3.78; this release restores a clean build.

## v0.3.78
- Engine: Symetrix LIVE write-through for bottom-row faders (RC 101–110) and mutes (RC 121–130) via CS.
- Engine: Poll bottom-row VU meters (RC 401–410) via UDP GS2 and publish over /ws.
- UI: Render bottom-row VU meters from RC cache (401–410).

## v0.3.77
- HOTFIX (build): Fix Go build failure caused by a duplicate `ecpGetCGUDP` method definition.
  - No behavior changes from v0.3.76.

## v0.3.76
- FIX (meters / Symetrix): Use **UDP** for Symetrix meter polling to avoid TCP resets from the DSP ("connection reset by peer").
  - Adds a dedicated UDP polling helper and switches the meter poll loop to use it.
  - Keeps TCP for control writes (ACK/NAK semantics) but meters are now resilient.

## v0.3.75
- Fix: Symetrix meter polling now accepts float or dB-style values and normalizes robustly.
- Debug: periodic log of raw+normalized meter values in LIVE mode.

# Changelog




## v0.3.74 (2026-01-11)
- FIX (DSP type): Engine DSP control is now implemented for **Symetrix** (SymNet Composer control protocol) instead of Q-SYS.
  - Controller Set uses `CS <id> <0..65535><CR>` (used for speaker mute writes).
  - Controller Get uses `GS2 <id><CR>` (used for meter reads).
  - Sets Quiet Mode ON (`SQ 1`) and Echo OFF (`EH 0`) per TCP session for reliable parsing.
- FIX (meters): PlayIt Live meters now poll **controller IDs 462/463** using Symetrix `GS2` reads and normalize by `value/65535` to produce UI-friendly 0.0–1.0 meter values.
  - On poll failure, forces both meters to 0 so the UI meters go dead (truthful).

## v0.3.73 (2026-01-11)
- FIX (meters stuck at 0): Q-SYS meter controls may return values in **dBFS** (negative numbers).
  - Removed premature clamping in the ECP `cg` read helper (negative values were being collapsed to 0.0).
  - Added normalization heuristics in the DSP meter poll loop:
    - 0..1 = already normalized
    - 0..100 = percent (÷100)
    - otherwise treated as dBFS and mapped `[meters.db_floor..0]` → `[0..1]` (default db_floor = -60).

## v0.3.72 (2026-01-11)
- FIX (truthful meters in live mode): the engine now polls the DSP for PlayIt Live meter controls and feeds them into the WS meters stream.
  - Uses Q-SYS ECP `cg` reads for `STUB_RSR_L` and `STUB_RSR_R`.
  - Updates RC 462/463 in-memory (normalized 0.0–1.0), then publishes via `/ws` as `{type:"meters", data:{"462":...,"463":...}}`.
  - Poll rate configurable via `meters.dsp_poll_hz` (default 10Hz, capped at 50Hz).
  - On poll failure, forces both meters to 0 so the UI meters go dead (visible and truthful).
