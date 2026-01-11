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
