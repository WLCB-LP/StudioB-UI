## v0.4.21 (2026-02-03)

### Engine
- Meters: fix Symetrix GS/GS2 parsing to accept UDP response shape `#<id>=<value>` (e.g. `#00410=40491`). Previously this caused frequent meter poll failures and forced meters to zero/hold.

## v0.4.20 (2026-02-03)

### Engine
- Meters: expand the high-rate meter publish set to include *all* Studio B stereo source VUs (CD1/CD2/AUX/BT/PC/Zoom L+R). This fixes missing right-channel meters.

### UI
- Meters: consume the engine's high-rate `{type:"meters"}` WebSocket frames using a meter-only fast path (no full fader/mute reconciliation). This makes the fader-row meters update as quickly as the DSP/engine publishes.
- Meters: render bottom-row strip meters as true stereo L/R fills (mono sources are mirrored) so stereo channels can be shown independently.

## v0.4.19 (2026-01-16)

### UI
- VU meters: fix segment threshold alignment so the blue->green transition occurs exactly at the printed -12 dB mark (and green spans -12..-6 as intended). The segmented fill now follows the same non-linear scale used by the on-lane meter markings.
- Header: replace the top-right pill stack with a local date/time readout.

## v0.4.18 (2026-01-16)

### UI
- VU meters: adjust the target-zone coloring so the entire -12 dB to -6 dB range is solid green.

## v0.4.17 (2026-01-16)

### UI
- VU meters: replace the single full-height gradient fill with threshold-based segmented fills (blue/green/yellow/red). This prevents low-level meters from showing traces of green/yellow/red before reaching the -12 dB mark.
- Peak attention: on meters that have faders (bottom row strips + Speakers), briefly flash a bright yellow or red glow when levels cross into the yellow/red ranges.

## v0.4.16 (2026-01-16)

### UI
- Recording: prevent the 5s `GET /api/wlcb/status` poll from overwriting the fast (500ms) Recording row. This fixes brief timecode back-jumps caused by mixing slow + fast update sources.

## v0.4.15 (2026-01-16)

### Engine
- Recording UDP: smooth timecode to prevent tiny backwards jumps (e.g. 11:20 -> 11:19 -> 11:21) caused by slight UDP packet reordering. We still accept large backwards jumps as a real reset.

## v0.4.14 (2026-01-16)

### Engine + UI
- RDS/Now Playing: decode HTML entities in `playingnow.txt` (e.g. "Brick&#8217;s" -> "Brick's") so operators never see entity codes.
- WLCB Status: make status indicators (dots) significantly brighter for better across-the-room visibility.
- Recording: pulse the green dot while actively recording (row background stays neutral).

## v0.4.13 (2026-01-16)

### Engine + UI
- WLCB Recording: add lightweight endpoint `GET /api/wlcb/recording` (no external I/O) so the UI can poll the recording timecode smoothly.
- UI: poll recording status every 500ms without changing the 5s cadence for the rest of WLCB Status.
- WLCB Status API: add optional `key` field per row (stable identifiers) so the UI can update the Recording row reliably even though its label changes.

## v0.4.12 (2026-01-16)

### Engine + UI
- Recording (UDP -> engine): accept both payload formats:
  - "<FILENAME>,<TIME>" (spec)
  - "<FILENAME>(<TIME>)" (common Node-RED concatenation)
- Recording: tolerate missing/"undefined" filename by substituting "Recording".
- WLCB Status ordering: move Recording row to the bottom of the stack.
- WLCB Status alarm behavior: Recording row never blinks red (dot still reflects state).

## v0.4.11 (2026-01-16)

### Engine + UI
- WLCB Status: add a Recording indicator sourced from UDP telemetry (Node-RED -> engine). Engine listens on UDP port 55123 by default (override with WLCB_RECORDING_UDP_PORT). If no packets arrive for 5 seconds, WLCB Status shows "Not Recording" (red); while receiving packets it shows "<FILENAME> (<TIME>)" (green).

## v0.4.10 (2026-01-16)

### UI
- Studio: widen the PlayIt Live and WLCB Status title pills (override the normal fader-width nameplate constraint) so they have the same visual presence as other top-row cards.

## v0.4.09 (2026-01-15)

### UI
- Studio: re-center the PlayIt Live and WLCB Status title pills and style them to match Speakers/Program (full-width, centered, no-wrap).

## v0.4.08 (2026-01-15)

### UI
- Studio: revert the bottom-row spacing change (restore the prior layout behavior) so bottom-row mute buttons are not clipped when the mixer is not fullscreen.

## v0.4.07 (2026-01-15)

### UI
- Studio: make all top-row cards match the height of the tallest card (PlayIt Live) for a cleaner console line.
- Studio: tighten the gap between the top row and bottom fader row to ~40px (removes the large “wasted” band).
- PlayIt Live + WLCB Status: title pills are roomier and more clearly “fully enclosed” around the label.
- WLCB Status: increased row text size and slightly enlarged status dots for improved at-a-glance readability.

## v0.4.06 (2026-01-14)

### Fixes
- Donations: prevent already-known donations from re-flashing after upgrades by migrating legacy donation IDs (older builds didn't include the RFC3339 time field). Added safety net: only flash donations dated "today".
- WLCB Status: improve Icecast parsing by deriving the mount path from `listenurl` when the `mount` field is missing (fixes Transmitter (/STL) and Stream (/stream) detection on Icecast variants).

## v0.4.04 (2026-01-14)

### Fixes
- Engine build: remove unused import and fix WLCB status handler calling signature so `go test ./...` passes.

## v0.4.03 (2026-01-14)

### Changed
- WLCB Status card:
  - Removed uptime and all non-requested items.
  - Implemented operator-facing checks: Internet, Web Site, Transmitter, Stream.
  - Stream row includes a right-justified green pill: "X Listeners / Y Peak".
  - Peak listeners are tracked and persisted locally by the engine so they survive engine/server restarts.

## v0.4.02 (2026-01-14)

### Added
- WLCB Status card (first functional implementation):
  - Engine: new endpoint `GET /api/wlcb/status` returns a normalized list of checks (engine uptime, DSP link health, PlayIt Live reachability/automation, lakesradio.org reachability).
  - UI: renders the checks inside the existing “WLCB Status” top-row card and polls every 5 seconds.
  - Contract: UI performs **no browser-side probing** of external services for this card; it only renders engine-provided status.

## v0.4.01 (2026-01-14)

### Fixes
- Donations: fix GOAL parsing by consulting GiveWP's public form-grid endpoint (`/wp-json/give-api/v2/form-grid`) and extracting the `of $GOAL` value for the "Support WLCB" card.
  - This resolves the case where the donor-wall page does not contain the goal widget in server-rendered HTML (client-side rendering).

## v0.4.00 (2026-01-14)

### Fixes
- Donations: re-center the "Raised ..." meta line (CSS) after font/weight changes.
- Donations: improve GiveWP goal parsing robustness (scan goal stat list-items by label/value), and increase the HTML fetch cap to reduce truncation-related goal misses.
- Donations: log the HTML byte length on goal parse failure to make future debugging deterministic.

## v0.3.99 (2026-01-14)

- Donations (engine): fix GOAL parsing for GiveWP by matching the explicit `Goal` label/value DOM pair:
  - `givewp-layouts-goal_stats-panel_stat-label` == `Goal`
  - paired with `givewp-layouts-goal_stats-panel_stat-value` (supports both VALUE→LABEL and LABEL→VALUE ordering)
  - This makes `Raised $X of $Y for YEAR` reliably include the goal when the GiveWP widget is present.

## v0.3.97 (2026-01-14)

- Donations (UI): render progress in the same visual style as donation rows (matching font size/weight).
- Donations (UI): requested string format now includes the year: `Raised $X of $Y for YEAR` (falls back to `Raised $X for YEAR` if goal is unavailable).
- Donations (engine): add a conservative last-resort goal parser that selects the largest "$X" in raw HTML constrained by the computed raised total (helps recover the campaign goal when the widget markup changes).

## v0.3.96 (2026-01-14)

- Hotfix (engine): fix install-time `go test` failure (`parseMoney` helper was referenced before it existed).
  - Adds a small shared `parseMoney()` helper and removes accidental shadowed local lambdas.

## v0.3.95 (2026-01-14)

- Donations: fix "Raised" summary by computing current-year total and publishing it even when goal parsing fails.
- Donations: make goal parsing more robust by capturing "$X Goal" from raw HTML/text.
- Donations: only flash truly new donations (bootstrap existing list as already-seen).
## v0.3.94

- Latest Donations (UI): fix a runtime ReferenceError (`trackNewDonations is not defined`) introduced in v0.3.93.
  - The donations fetch loop no longer trips into STALE mode due to that error.
  - New-donation flashing continues to work via the existing `markDonationsSeen()` + `isDonationFlashing()` logic.

## v0.3.93

- Donations progress (engine): scrape GOAL from raw HTML (data-* / JSON / label patterns) and fall back to stripped text; fixes cases where goal was not present after tag stripping.
- Donations progress (UI): if GOAL is missing/0, still show "Raised $X" (instead of showing nothing).
- Donations UX (UI): highlight newly-seen donations by flashing the row background yellow for 10 minutes (persists across reloads via localStorage).

## v0.3.92

- Latest Donations: card title now reads "Latest Donations at lakesradio.org".
- Latest Donations: campaign progress now uses GOAL scraped from website, and RAISED computed as sum of current-year donations.

## v0.3.91
- Latest Donations: replace the "Updated" timestamp line with fundraiser progress.
  - Engine: scrape + return `summary.raised` and `summary.goal` (USD) from the same Support WLCB page.
  - UI: render `Raised $X of $Y` (and still append `STALE` + `error` when applicable).

## v0.3.90
- Latest Donations: fix scrape parser to match real HTML output of the WLCB Support page.
  - Engine: donation entries are now detected using the stable text anchor **"Amount Donated"** (instead of relying on markdown-like "###" headings, which do not exist in the raw HTML).
  - Engine: amount parsing is more flexible (supports "$50.00" on the next line **or** on the same line as the label).
  - UI: poll interval changed from **60s → 30s**.

## v0.3.89
- Latest Donations: populate the Studio page "Latest Donations" card from a server-side scrape.
  - Engine: add `GET /api/donations/latest?limit=5`.
    - Scrapes the public WLCB Support page and returns a stable JSON contract.
    - Includes defensive parsing + a 2MB download cap.
    - On scrape/parse failure, returns **last-known-good** results with `stale: true` and an `error` field.
  - UI: poll the engine endpoint every 60s and render:
    - `Name - Amount` on the first line
    - `Comment` on the second line (when present)

## v0.3.88
- Automute glow: eliminate "blink" while all mics are muted.
  - Engine: on transient DSP UDP poll failure, we now hard-zero **meters only**; we **do not** clobber logic/indicator RCs like **560**.
  - Engine: if a DSP response is partial (missing a controller), we now **hold last-known** value rather than forcing 0.
  - UI: add a small **visual-only debounce** for the Speakers automute glow so brief gaps do not flash red.

## v0.3.87
- Automute indicator reliability + visibility.
  - Engine: RC **560** (ALL_MICS_CLOSED) is now included in the DSP UDP poll loop, so the UI receives frequent truthful updates (no "refresh-only" behavior).
  - Engine: After mic mute writes (RC **121–124**) we now coalesce and perform a small burst of best-effort RC 560 reads at multiple delays to handle DSP logic settling.
  - UI: Make the Speakers automute glow **brighter and more pronounced** (applied to the fader lane/puck only).
- PlayIt Live: Fix AUTO/LIVE toggling against the documented Control API.
  - Engine: Add `/api/pil/playoutMode/toggleAutomation` proxy (POST) → `/api/control/liveAssist/playoutMode/toggleAutomation`.
  - UI: AUTO/LIVE button now prefers the toggleAutomation endpoint with body `{ "on": true|false }`, with legacy fallbacks.

## v0.3.86
- Engine/UI: Make Speaker automute glow update **live** (no refresh required).
  - Engine now performs a best-effort delayed DSP read of RC **560** after any mic mute write (RC **121–124**) so derived logic indicators publish real-time deltas.
- PlayIt Live controls: Fix AUTO toggle + START command plumbing.
  - Engine `/api/pil/playoutMode` now forwards POST as POST (some PIL builds reject PUT-only writes).
  - Engine `/api/pil/play` accepts POST (and GET for legacy) and forwards a POST play command.
  - UI START button now sends POST (matches engine contract).

## v0.3.85
- UI: Fix Speakers card glow + layout polish.
  - Prevent top-row card glow from being clipped (Studio layout no longer hides vertical overflow).
  - Automute visual alert now turns the **Speakers fader lane/puck** red (not the entire card).
  - Automute glow polarity updated for relabeled RC 560 (**ALL_MICS_CLOSED**): glow engages when RC 560 is **FALSE** (any mic open).

## v0.3.84
- UI: Wire **Speakers VU meters** on the Studio page to RC **460/461**.
  - Add the missing vertical meter fill DOM elements (`m_spkL`, `m_spkR`) so the existing meter animation loop can render movement.
  - Use the same display mapping as other vertical VU meters (VU travel scaled to match the fader's -72..+12 dB span).
- UI: Update cache-buster query strings in `index.html` to match VERSION (reduces "stale UI" after updates).

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





## v0.4.05
- WLCB Status: reorder rows (Transmitter above Web Site) and add RDS line from playingnow.txt (blue dot).
- WLCB Status: grey out dependent checks when Internet is down.
- WLCB Status: alarm rows blink bright red when in alarm.
- Icecast parsing: make mount matching case-insensitive; Stream pill shows listeners/peak when mount is active.

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
