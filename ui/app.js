// StudioB-UI (Studio page) – stable contract polling + named RC control
const POLL_MS = 250;

// UI_BUILD_VERSION MUST match VERSION for this release.
// This is used for:
//   1) Display
//   2) Update check requests
//   3) Cache-bust UX
//
// NOTE: The UI and engine can update/restart independently, so the header shows
// BOTH the UI build version (this value) and the engine version (from /api/studio/status).
// NOTE: Keep in sync with ../VERSION (release packaging checks rely on this).
const UI_BUILD_VERSION = "0.3.92";

// ---------------------------------------------------------------------------
// Cache / stale-HTML self-repair (v0.3.64)
//
// Real-world studio deployments sometimes see "split-brain" updates:
//   - app.js loads (cache-busted via ?v=...)
//   - index.html remains cached by an intermediary (proxy / kiosk / etc.)
//
// When that happens, purely static text in index.html can appear "stuck" even
// though the rest of the UI is up to date.
//
// Contract: scripts should be self-verifying / self-repairing.
// This function patches known legacy labels at runtime, then logs the repair so
// operators can diagnose caching issues quickly.
function repairLegacyStaticLabels(){
  try {
    // Donations title (legacy builds rendered as "LATEST DONATIONS")
    const donationsTitle = document.querySelector('#donationsCard .mixerCard__title');
    if(donationsTitle){
      const t = (donationsTitle.textContent || '').trim();
      if(t === 'LATEST DONATIONS'){
        donationsTitle.textContent = 'Latest Donations at lakesradio.org';
        addRuntimeEvent('Repaired cached Donations label (LATEST DONATIONS → Latest Donations at lakesradio.org)');
      } else if(t === 'Latest Donations'){
        donationsTitle.textContent = 'Latest Donations at lakesradio.org';
        addRuntimeEvent('Repaired cached Donations label (Latest Donations → Latest Donations at lakesradio.org)');
      }
    }
  } catch (e){
    // Never allow a repair step to break UI boot.
    console.warn('[repairLegacyStaticLabels] failed', e);
  }
}

// One-time auto-refresh guard. We *try* to use sessionStorage so a refresh
// survives a reload, but we also keep an in-memory flag so browsers with
// disabled storage won't get stuck in a refresh loop.
let autoRefreshDone = false;

const state = {
  dspModeStatus: { mode:"", host:"", port:null, validated:false, validatedAt:"", configChanged:false },
  dspHealth: { state:"UNKNOWN", lastOk:"", failures:0, lastError:"", lastTestAt:"" },
  connected: false,
  lastOkAt: 0,
  version: "—",
  mode: "—",
  update: {
    ok:false,
    available:false,
    latest:"",
    checkedAt:"",
    // UI-only diagnostics (never sent to the engine)
    lastMsg:"",
    lastTitle:"",
    lastErr:"",
    // When an update completes, we auto-trigger a cache-busting reload.
    // This avoids the common "nothing happened until I hit refresh" confusion.
    autoReloadArmed:false,
  },

  // -----------------------------------------------------------------------
  // Latest Donations (UI v0.3.91)
  // -----------------------------------------------------------------------
  // This is populated by the engine endpoint:
  //   GET /api/donations/latest?limit=5
  //
  // Why the engine (not the browser) fetches:
  // - Avoids CORS/mixed-content issues
  // - Avoids exposing any future auth/token needs
  // - Provides a stable JSON contract even if the website HTML changes
  donations: {
    items: [],
    // Optional campaign progress numbers (Raised / Goal) from the engine.
    // When present, the UI shows: "Raised $X of $Y".
    summary: null,
    updatedAt: "",
    stale: false,
    lastErr: "",
    // Internal: map of donationId -> firstSeenEpochMs (for flash effect).
    seenMap: {},
  },
  // meter smoothing
  meters: {
    // PlayIt Live meters (UI v0.3.66)
    // Operator-provided RC assignments:
    //   462 = PlayIt Live meter Left
    //   463 = PlayIt Live meter Right
    //
    // NOTE: state.meters is ONLY used by the smoothing animation loop.
    // Each entry must be an object with {cur,tgt}.
    pilL: { cur: 0, tgt: 0 },
    pilR: { cur: 0, tgt: 0 },

    // Reserved for future taps (not yet rendered as DOM IDs on the Studio page)
    pgmL: { cur: 0, tgt: 0 },
    pgmR: { cur: 0, tgt: 0 },
    spkL: { cur: 0, tgt: 0 },
    spkR: { cur: 0, tgt: 0 },
    rsrL: { cur: 0, tgt: 0 },
    rsrR: { cur: 0, tgt: 0 },
  },
  speaker: { level: 0, mute: false, automute: false },

  // Persisted-vs-runtime clarity (UI v0.3.07)
  // - persistedMode: what is stored in ~/.StudioB-UI/config/config.v1
  // - runtimeMode:   what the running engine currently believes the mode is
  //   (may be overridden/promoted by watchdog without writing back to disk)
  cfgClarity: {
    persistedMode: "",
    runtimeMode: "",
    runtimeActiveMode: "",
    lastUpdatedAt: "",
  },

  // -----------------------------------------------------------------------
  // Recent Runtime Events (UI v0.3.12)
  // -----------------------------------------------------------------------
  // Purpose:
  // Operators commonly ask: "When did this change?" (especially when the
  // watchdog promotes runtime mode or the engine restarts).
  //
  // This is a small, UI-only, in-memory event list that records key state
  // transitions *since the current page load*.
  //
  // IMPORTANT SAFETY/CONTINUITY RULES:
  // - Read-only (does not change runtime state)
  // - In-memory only (does not write to disk)
  // - Bounded size (prevents unbounded growth)
  // - Best-effort: we only log what we can observe from existing endpoints.
  runtimeEvents: {
    max: 20,
    items: [], // { t: "HH:MM:SS", msg: string }
  },

  // -----------------------------------------------------------------------
  // Mixer UI (Studio page) – touch-first fader visuals (UI v0.3.13)
  // -----------------------------------------------------------------------
  // DESIGN CONTRACT (operator intent only):
  // - MUTE buttons are the ONLY operator action for mic channels right now.
  // - Faders are intentionally VISUAL-ONLY in v0.3.13.
  //   We still make them draggable so we can iterate on the look/feel and
  //   touchscreen ergonomics without risking any audio changes.
  //
  // When we later wire gains, we will:
  // - add explicit RC mappings (and comments)
  // - add safety guards (DSP connected, live vs mock, etc.)
  // - add snap points (e.g., -inf / 0 dB)
  mixer: {
    // 0..1 UI-only normalized positions
    faders: {

      host: 0.65,
      g1: 0.65,
      g2: 0.65,
      g3: 0.65,

      // Sources (UI v0.3.27)
      cd1: 0.65,
      cd2: 0.65,
      aux: 0.65,
      bt: 0.65,
      pc: 0.65,
      zoom: 0.65,

      // Studio monitors (Speakers) (UI v0.3.38)
      // RC assignment (operator-provided):
      //   160 = Speaker Level
      //   161 = Speaker Mute
      // NOTE: This fader is placed on the *top* row for quick access.
      spk: 0.65
    },
    // one-time init guard
    inited: false,
  },
};

// ---------------------------------------------------------------------------
// Debug exports (UI v0.3.67)
// These are intentionally *read-only* conveniences for field diagnostics.
// They let operators verify whether RC snapshot data is arriving without
// modifying application state from the console.
//
// NOTE: We do NOT rely on these exports for functionality. The UI should
// behave correctly even if a kiosk browser blocks access to window.*.
try{
  // Expose a reference so DevTools checks like `window.state.rc` work.
  // This is critical for diagnosing "meters moving but not matching DSP".
  window.state = state;
}catch(_e){ /* ignore */ }


// Keep prior observed values here so we can detect transitions cleanly.
// (We avoid sprinkling "prevX" properties across unrelated code paths.)
const _prev = {
  connected: null,
  engineVersion: null,
  engineMode: null,
  dspHealthState: null,
  persistedMode: null,
  runtimeMode: null,
  runtimeOverrideActive: null,
};

function _hhmmss(){
  const d = new Date();
  const hh = String(d.getHours()).padStart(2, "0");
  const mm = String(d.getMinutes()).padStart(2, "0");
  const ss = String(d.getSeconds()).padStart(2, "0");
  return `${hh}:${mm}:${ss}`;
}

function addRuntimeEvent(msg){
  try{
    if(!msg) return;
    const ev = state.runtimeEvents;
    if(!ev) return;

    // Avoid noisy duplicates: if the last message matches, don't add another.
    const last = ev.items.length ? ev.items[ev.items.length - 1] : null;
    if(last && last.msg === msg) return;

    ev.items.push({ t: _hhmmss(), msg: String(msg) });
    // Keep a strict bound.
    while(ev.items.length > ev.max) ev.items.shift();

    renderRuntimeEvents();
  }catch(_){
    // Never let UI-only diagnostics break the operator UI.
  }
}

function renderRuntimeEvents(){
  const el = document.querySelector("#runtimeEvents");
  if(!el) return;
  const items = state.runtimeEvents?.items || [];
  if(!items.length){
    el.textContent = "—";
    return;
  }
  el.textContent = items.map(e => `${e.t} – ${e.msg}`).join("\n");
}

// ---------------------------------------------------------------------------
// Mixer fader visuals (UI v0.3.13)
// ---------------------------------------------------------------------------
// These faders are intentionally VISUAL-ONLY for now.
//
// Touch/Mouse behavior goals:
// - The entire vertical lane is draggable (not just the small puck).
// - Pointer capture is used so dragging continues even if the pointer/finger
//   slips outside the lane.
// - We do not post RC writes in v0.3.13 (operator safety + phased rollout).
//
// Values:
// - We store a normalized 0..1 value in state.mixer.faders[<id>]
// - 0 means bottom (off), 1 means top (full)

// ---------------------------------------------------------------------------
// Mixer gain (fader) RC assignments
// ---------------------------------------------------------------------------
// We are phasing in "real" gain control carefully.
//
// IMPORTANT SAFETY CONTRACT (operator intent only):
// - Mute buttons remain direct operator intent.
// - Fader gains are being enabled ONE channel at a time so we can validate:
//     * touchscreen ergonomics
//     * RC semantics/range
//     * WAN/latency behavior
//     * DSP safety guards
//
// Studio B fader assignments (provided by operator):
//   Host Mic   -> RC 101
//   Guest 1    -> RC 102
//   Guest 2    -> RC 103
//   Guest 3    -> RC 104
//   CD1        -> RC 105
//   CD2        -> RC 106
//   AUX        -> RC 107
//   Bluetooth  -> RC 108
//   PC         -> RC 109
//   Zoom       -> RC 110
//
// v0.3.20 scope:
// - Installer/update now self-repairs rc_allowlist (RC 101–110) so fader writes aren't blocked.
// - Host fader remains the only LIVE gain write in the phased rollout.
//
// v0.3.31 scope:
//   All Studio B input faders are now LIVE RC controls (101–110).
//   NOTE: Engine v0.2.97 will accept and cache these RC values; DSP gain
//   write-through will be added later (engine v0.2.98).
//
// Why enable all faders now?
// - The UI must not allow the operator to "move" a fader that immediately
//   snaps back because the engine RC cache never changed.
// - The installer now self-repairs the rc_allowlist for 101–110 so these
//   writes are defense-in-depth protected.
const MIXER_FADER_RC = {
  host: "101",
  g1:   "102",
  g2:   "103",
  g3:   "104",
  cd1:  "105",
  cd2:  "106",
  aux:  "107",
  bt:   "108",
  pc:   "109",
  zoom: "110",

  // Top-row Speakers fader (UI v0.3.38)
  // Studio monitor speaker level (operator-provided): RC 160
  spk:  "160",
};

// Human-friendly labels for runtime event logging (keep short; operators read)
const MIXER_LABEL = {
  host: "Host",
  g1:   "Guest 1",
  g2:   "Guest 2",
  g3:   "Guest 3",
  cd1:  "CD1",
  cd2:  "CD2",
  aux:  "AUX",
  bt:   "Bluetooth",
  pc:   "PC",
  zoom: "Zoom",

  // Speakers (top row)
  spk:  "Speakers",
};

// ---------------------------------------------------------------------------
// Mixer hydration (DSP/engine-authoritative) (UI v0.3.30)
// ---------------------------------------------------------------------------
// IMPORTANT OPERATOR CONTRACT:
// - The DSP (via the engine) is the source-of-truth for control states.
// - The UI must NEVER invent initial positions, must NEVER apply "defaults",
//   and must NEVER write anything on load.
// - Controls stay hidden/locked until we receive an authoritative RC snapshot.
//
// Data path:
// - Primary: WebSocket /ws
//     * { type: "snapshot", data: { rc: {"101":0.5, ...} } }
//     * { type: "delta", rc: {"101":0.55, ...} }
// - Fallback: one-shot GET /api/state (same rc map)
//
// We keep a local copy of the last known RC map strictly for rendering.
// NOTE: Keys arrive as STRINGS in JSON.
state.rc = state.rc || {};

// Mixer is considered "hydrated" once we have received at least one snapshot.
state.mixerHydrated = false;

function showMixerWhenReady(){
  // We start the mixer hidden to avoid a misleading "flash" before we have
  // authoritative state.
  // UI v0.3.38: We now have TWO fader rows on the Studio page:
  //   - Top row (PIL / Headphones / Speakers / Program)
  //   - Bottom row (Mic + sources)
  // Both rows must remain hidden until we have an authoritative RC snapshot.
  ['#mixerRoot', '#topMixerRoot'].forEach(sel=>{
    const root = document.querySelector(sel);
    if(root) root.classList.remove('isHydrating');
  });
}

function hideMixerUntilHydrated(){
  ['#mixerRoot', '#topMixerRoot'].forEach(sel=>{
    const root = document.querySelector(sel);
    if(root) root.classList.add('isHydrating');
  });
}

// ---------------------------------------------------------------------------
// Mixer layout (UI v0.3.34)
// ---------------------------------------------------------------------------
// Operator requirement: NO scrolling and NO vertical stacking of fader cards.
// We achieve this with *pure markup + CSS*:
//   - .mixerGroup blocks for each logical group (Mic, CD1/CD2, AUX, BT, PC+Zoom)
//   - .mixerSpacer flex elements between groups to consume extra horizontal space
// No JS measurements or resize handlers are used for the layout.
// Studio B: fader readback RC assignments (authoritative render source)
const MIXER_FADER_RC_READ = {
  host: "101",
  g1:   "102",
  g2:   "103",
  g3:   "104",
  cd1:  "105",
  cd2:  "106",
  aux:  "107",
  bt:   "108",
  pc:   "109",
  zoom: "110",

  // Speakers (top row)
  spk:  "160",
};

// Studio B: mute RC assignments (authoritative render source)
const MIXER_MUTE_RC = {
  host: "121",
  g1:   "122",
  g2:   "123",
  g3:   "124",
  cd1:  "125",
  cd2:  "126",
  aux:  "127",
  bt:   "128",
  pc:   "129",
  zoom: "130",

  // Speakers (top row)
  spk:  "161",
  // Program/Speakers exist but are rendered elsewhere for now:
  // pgm: "131",
  // spk: "161",
};

function rcGet(id){
  try{
    const k = String(id);
    const v = state.rc ? state.rc[k] : undefined;
    return (typeof v === 'number' && Number.isFinite(v)) ? v : 0;
  }catch(_){
    return 0;
  }
}

function applyMixerFadersFromRC(){
  for(const id of Object.keys(state.mixer.faders || {})){
    const rc = MIXER_FADER_RC_READ[id];
    if(!rc) continue;
    setFaderUI(id, rcGet(rc));
  }
}

function applyMixerMutesFromRC(){
  // Any toggle button with a numeric RC is treated as a real control.
  // (Speaker mute uses STUB_SPK_MUTE and remains driven by /api/studio/status.)
  document.querySelectorAll('.btn.toggle[data-rc]').forEach(btn=>{
    const rc = btn.getAttribute('data-rc');
    if(!rc) return;
    if(rc === 'STUB_SPK_MUTE' || rc === 'STUB_SPK_AUTOMUTE') return;
    const on = rcGet(rc) >= 0.5;
    btn.classList.toggle('on', on);
    btn.setAttribute('aria-pressed', on ? 'true' : 'false');
  });
}

// ---------------------------------------------------------------------------
// PlayIt Live meters (UI v0.3.66)
//
// The operator asked for visible meter movement on the Studio page.
// The DSP/engine are the source of truth, and the operator-provided
// RC assignments for Remote Studio Return meters are:
//   462 = Remote Studio Return VU (Left)
//   463 = Remote Studio Return VU (Right)
//
// We treat these as normalized 0..1 values (engine contract).
// If the engine sends different semantics in the future, we only need to
// adjust this mapping (single source of truth).
// ---------------------------------------------------------------------------
function applyPILMetersFromRC(){
  // RC-only meters (UI v0.3.66)
  // 462 = Remote Studio Return VU (Left)
  // 463 = Remote Studio Return VU (Right)
  //
  // IMPORTANT: Do not blend in /api/studio/status meters here.
  // We want the UI to match Composer exactly for these meter taps.
  state.meters.pilL.tgt = clamp01(rcGet(462));
  state.meters.pilR.tgt = clamp01(rcGet(463));
}

// ---------------------------------------------------------------------------
// Bottom-row VU meters (Studio B) — authoritative RC render source
//
// Operator-provided RC assignments (DSP truth):
//   Host Mic VU     -> 401
//   Guest 1 VU      -> 402
//   Guest 2 VU      -> 403
//   Guest 3 VU      -> 404
//   CD1 VU          -> 405
//   CD2 VU          -> 406
//   AUX VU          -> 407
//   Bluetooth VU    -> 408
//   PC VU           -> 409
//   Zoom VU         -> 410
//
// Contract: engine publishes normalized 0.0–1.0 values over /ws as RC meters.
// The UI never synthesizes meter motion; if the DSP goes dead, meters go dead.
// ---------------------------------------------------------------------------
const STRIP_VU_RC = {
  host: "401",
  g1:   "402",
  g2:   "403",
  g3:   "404",
  cd1:  "405",
  cd2:  "406",
  aux:  "407",
  bt:   "408",
  pc:   "409",
  zoom: "410",
};

// ---------------------------------------------------------------------------
// VU display scaling
//
// DSP facts (Studio B):
//   - Fader range:  -72 dB .. +12 dB
//   - Meter range:  -72 dB .. 0 dB
//
// The engine publishes normalized 0..1 values for meter RCs.
// When those normalized values represent a -72..0 dB meter, the top of the
// meter corresponds to 0 dB, not the fader's +12 dB headroom.
//
// To make meter travel visually consistent with the fader travel, we map the
// -72..0 span into the -72..+12 span.
//
// This is DISPLAY-ONLY. It does not change truth; it only changes how tall the
// meter looks.
// ---------------------------------------------------------------------------
const VU_MATCH_FADER_RANGE = true;
const _VU_TO_FADER_SCALE = 72 / 84; // 0.857...

function vuDisplay(v){
  const x = clamp01(v);
  if(!VU_MATCH_FADER_RANGE) return x;
  return clamp01(x * _VU_TO_FADER_SCALE);
}

// Ensure each bottom-row strip has a .fader__meterFill we can drive.
// The markup renders an empty .fader__meter lane; we attach the inner fill
// element dynamically so we can keep HTML clean and stable.
function ensureBottomRowVUMeterFills(){
  try{
    document.querySelectorAll(".studioBottomFaders .strip[data-strip]").forEach(stripEl=>{
      const id = stripEl.getAttribute("data-strip");
      if(!id) return;

      const meterLane = stripEl.querySelector(".fader__meter");
      if(!meterLane) return;

      // One mono VU per strip in this design (we fill the single lane).
      if(meterLane.querySelector(".fader__meterFill")) return;

      const fill = document.createElement("div");
      fill.className = "fader__meterFill";
      fill.id = `m_vu_${id}`;
      meterLane.appendChild(fill);
    });
  }catch(e){
    // Never allow meter scaffolding to break UI boot.
    console.warn("[ensureBottomRowVUMeterFills] failed", e);
  }
}

function applyBottomRowVUMetersFromRC(){
  // Drive bottom-row VU lanes directly from the authoritative RC cache.
  // IMPORTANT: This is *not* part of the smoothing loop — meters should match
  // DSP motion as closely as possible. If we want smoothing, we add it later
  // purely as a visual post-process (no effect on truth).
  for(const strip of Object.keys(STRIP_VU_RC)) {
    const rc = STRIP_VU_RC[strip];
    const v = clamp01(rcGet(rc));
    setMeterFillV(`m_vu_${strip}`, vuDisplay(v));
  }
}




// Connect to the engine RC WebSocket and keep our local RC cache current.
//
// Why WebSocket?
// - It avoids extra HTTP polling.
// - It gives us an immediate authoritative snapshot on connect.
// - It streams deltas so faders/mutes stay correct if something else changes
//   state (watchdog restart, other UI, CLI, DSP, etc.).
let _rcWS = null;
let _rcWSBackoffMs = 500;
let _rcWSMsgSeen = 0;

function connectRCWebSocket(){
  // Avoid duplicate sockets.
  if(_rcWS && (_rcWS.readyState === WebSocket.OPEN || _rcWS.readyState === WebSocket.CONNECTING)){
    return;
  }

  try{
    const proto = (location.protocol === 'https:') ? 'wss:' : 'ws:';
    const url = `${proto}//${location.host}/ws`;
    const ws = new WebSocket(url);
    _rcWS = ws;

    // Debug handle (UI v0.3.67): allow quick inspection of WS status.
    try{ window.rcSocket = ws; }catch(_e){ /* ignore */ }

    ws.onopen = ()=>{
      _rcWSBackoffMs = 500; // reset backoff on success
      _rcWSMsgSeen = 0;
      // Keep mixer hidden until we receive the first snapshot.
      hideMixerUntilHydrated();

      // -------------------------------------------------------------------
      // /ws subscription handshake (UI v0.3.68)
      //
      // The engine uses a single /ws endpoint for ALL realtime updates.
      // In some deployments, the engine will not begin publishing RC/meter
      // messages until the client explicitly subscribes.
      //
      // We send a conservative, future-friendly subscribe message here.
      // If the engine ignores it (push-only model), this is harmless.
      // If the engine requires it (subscribe model), this unblocks meters.
      //
      // Expected (suggested) engine payloads after subscribe:
      //   {type:"rc_state", rc:{"462":0.1,...}}
      //   {type:"meter", rc:462, value:0.1}
      //   {type:"rc_delta", rc:{"462":0.2}}
      // -------------------------------------------------------------------
      try{
        const sub = { type: 'subscribe', topics: ['rc', 'meters'] };
        ws.send(JSON.stringify(sub));
        addRuntimeEvent('WS connected; subscribe sent (rc, meters)');
      }catch(_e){
        addRuntimeEvent('WS connected; subscribe send FAILED');
      }
    };

    ws.onmessage = (ev)=>{
      let msg = null;
      try{ msg = JSON.parse(ev.data); }catch(_e){ return; }

      // Lightweight field diagnostics: record the first few message types.
      // This helps distinguish "socket connected but no publishes" from
      // "publishes exist but we don't understand the shape".
      try{
        _rcWSMsgSeen++;
        if(_rcWSMsgSeen <= 5){
          addRuntimeEvent(`WS msg #${_rcWSMsgSeen}: type=${String(msg.type || 'unknown')}`);
        }
      }catch(_e){ /* ignore */ }

      // -------------------------------------------------------------------
      // RC payload compatibility layer (UI v0.3.67)
      //
      // The engine publishes ALL realtime updates over a single /ws endpoint.
      // We accept multiple message shapes so the UI can remain stable while
      // we converge on a standard payload format.
      //
      // Supported shapes (current + future-friendly):
      //   1) Snapshot:
      //      { type: "snapshot", data: { rc: { "462": 0.12, ... } } }
      //      { type: "rc_state", rc: { "462": 0.12, ... } }
      //
      //   2) Delta / partial update:
      //      { type: "delta", rc: { "462": 0.13 } }
      //      { type: "rc_delta", rc: { "462": 0.13 } }
      //
      //   3) Single-meter event:
      //      { type: "meter", rc: 462, value: 0.13 }
      //
      // NOTE:
      // - We keep ALL RC values in state.rc as string keys.
      // - We do not invent or smooth values at the RC layer.
      // - Meters are rendered from RC only; if RC is absent, meters freeze.
      // -------------------------------------------------------------------

      function applyRCObject(rcObj, isSnapshot){
        if(!rcObj || typeof rcObj !== 'object') return;
        if(isSnapshot || !state.rc) state.rc = {};
        for(const k of Object.keys(rcObj)){
          state.rc[String(k)] = rcObj[k];
        }
        state.mixerHydrated = true;

        // Update any UI surfaces that depend on RC.
        applyPILMetersFromRC();
        applyBottomRowVUMetersFromRC();
        applyMixerFadersFromRC();
        applyMixerMutesFromRC();
        applySpeakerAutoMuteGlowFromRC();
        showMixerWhenReady();
      }

      // Snapshot (preferred)
      if(msg && msg.type === 'snapshot' && msg.data && msg.data.rc){
        applyRCObject(msg.data.rc, true);
        return;
      }
      if(msg && msg.type === 'rc_state' && msg.rc){
        applyRCObject(msg.rc, true);
        return;
      }

      // Delta
      if(msg && msg.type === 'delta' && msg.rc){
        applyRCObject(msg.rc, false);
        return;
      }
      if(msg && msg.type === 'rc_delta' && msg.rc){
        applyRCObject(msg.rc, false);
        return;
      }

      // Single meter event
      if(msg && msg.type === 'meter' && (msg.rc !== undefined) && (msg.value !== undefined)){
        state.rc = state.rc || {};
        state.rc[String(msg.rc)] = msg.value;
        state.mixerHydrated = true;
        applyPILMetersFromRC();
        applyBottomRowVUMetersFromRC();
        return;
      }
    };

    ws.onclose = ()=>{
      // Reconnect with bounded backoff.
      _rcWS = null;
      state.mixerHydrated = state.mixerHydrated || false;
      try{ addRuntimeEvent('WS closed; scheduling reconnect'); }catch(_e){ }
      setTimeout(connectRCWebSocket, _rcWSBackoffMs);
      _rcWSBackoffMs = Math.min(8000, Math.floor(_rcWSBackoffMs * 1.6));
    };

    ws.onerror = ()=>{
      // Close will trigger reconnect.
      try{ ws.close(); }catch(_){ }
    };

  }catch(_e){
    // If WS construction fails (older browser / blocked), fallback fetch will kick in.
  }
}

// Fallback: if we haven't hydrated within a short window, do a one-shot HTTP snapshot.
async function hydrateMixerViaHTTPFallback(){
  if(state.mixerHydrated) return;
  try{
    const j = await fetchJSON('/api/state', { cache: 'no-store' }, 2500);
    if(j && j.rc){
      state.rc = j.rc || {};
      state.mixerHydrated = true;
      applyMixerFadersFromRC();
      applyMixerMutesFromRC();
      showMixerWhenReady();
    }
  }catch(_e){
    // Best-effort only.
  }
}

// NOTE: A global clamp01() already exists later in this file.
// We intentionally re-use that helper so we don't end up with subtly
// different clamping semantics in different parts of the UI.

function setFaderUI(id, v){
  const lane = document.querySelector(`.fader__lane[data-fader="${id}"]`);
  if(!lane) return;
  const puck = lane.querySelector('.fader__puck');
  if(!puck) return;

  const val = clamp01(v);
  state.mixer.faders[id] = val;

  // Position puck: 0 => bottom, 1 => top
  // We use CSS translateY to avoid layout thrash.
  const h = lane.clientHeight;
  const puckH = puck.clientHeight;
  const usable = Math.max(1, h - puckH);
  const y = usable - (usable * val);
  // Keep X centered while moving in Y.
  puck.style.transform = `translate(-50%, ${y}px)`;
  puck.setAttribute('aria-valuenow', val.toFixed(2));
}

function initMixerFaders(){
  // Guard so we don't attach multiple listeners (e.g., hot reload, future
  // refactors, or accidental double-calls).
  if(state.mixer.inited) return;
  state.mixer.inited = true;

  // Initialize positions from state.
  for(const id of Object.keys(state.mixer.faders || {})){
    setFaderUI(id, state.mixer.faders[id]);
  }

  // Keep mixer hidden until we receive an authoritative snapshot.
  hideMixerUntilHydrated();

  // Bind pointer handlers.
  document.querySelectorAll('.fader__lane').forEach(lane => {
    const id = lane.getAttribute('data-fader');
    if(!id) return;

    // If this fader has an RC mapping, it is "LIVE" (writes gain).
    // Otherwise it remains visual-only.
    const rc = MIXER_FADER_RC[id] || null;

    // Rate limit writes so we don't flood the engine/RC system during
    // a fast finger drag. We also only post when the value meaningfully
    // changes (avoid micro-jitter).
    let lastSentAt = 0;
    let lastSentVal = null;
    let pendingVal = null;
    let sendRAF = 0;
    let dragHadError = false;

    const POST_MIN_MS = 60;      // ~16 posts/sec max (safe for WAN)
    const EPS = 0.005;          // ignore tiny changes

    async function maybePostGain(v, opts={}){
      if(!rc) return; // visual-only channel

      const val = clamp01(v);
      const now = Date.now();

      // If this is a "commit" send (pointerup), always post.
      const force = !!opts.force;

      if(!force){
        if(lastSentVal !== null && Math.abs(val - lastSentVal) < EPS) return;
        if((now - lastSentAt) < POST_MIN_MS) return;
      }

      lastSentAt = now;
      lastSentVal = val;
      try{
        await postRC(rc, val);
        if(force){
          const nm = MIXER_LABEL[id] || id;
          addRuntimeEvent(`${nm} fader set: ${val.toFixed(2)} (RC ${rc})`);
        }
      }catch(e){
        // Don't spam errors during a drag; report once per drag session.
        if(!dragHadError){
          dragHadError = true;
          const nm = MIXER_LABEL[id] || id;
          addRuntimeEvent(`${nm} fader write failed (RC ${rc}): ${String(e?.message||e)}`);
        }
      }
    }

    function schedulePost(v){
      // We intentionally schedule to the next animation frame. This keeps the
      // UI responsive while still delivering frequent enough updates.
      pendingVal = clamp01(v);
      if(sendRAF) cancelAnimationFrame(sendRAF);
      sendRAF = requestAnimationFrame(async ()=>{
        const pv = pendingVal;
        pendingVal = null;
        sendRAF = 0;
        await maybePostGain(pv);
      });
    }

    const computeValFromClientY = (clientY) => {
      const r = lane.getBoundingClientRect();
      const y = clientY - r.top; // 0 at top
      const v = 1 - (y / Math.max(1, r.height));
      return clamp01(v);
    };

    lane.addEventListener('pointerdown', (e) => {
      // Only primary button for mouse; touch has no buttons.
      if(e.pointerType === 'mouse' && e.button !== 0) return;
      e.preventDefault();
      lane.setPointerCapture(e.pointerId);
      // v0.3.16: tactile feedback while dragging
      lane.classList.add('isDragging');
      dragHadError = false;
      const v = computeValFromClientY(e.clientY);
      setFaderUI(id, v);

      // First-touch write (if LIVE). We do a scheduled write rather than a
      // synchronous one to avoid input latency.
      schedulePost(v);
    });

    lane.addEventListener('pointermove', (e) => {
      if(!lane.hasPointerCapture(e.pointerId)) return;
      e.preventDefault();
      const v = computeValFromClientY(e.clientY);
      setFaderUI(id, v);
      schedulePost(v);
    });

    lane.addEventListener('pointerup', (e) => {
      if(!lane.hasPointerCapture(e.pointerId)) return;
      e.preventDefault();
      try{ lane.releasePointerCapture(e.pointerId); }catch(_){ }
      lane.classList.remove('isDragging');

      // Commit final value on pointer up (if LIVE).
      // We intentionally force this post even if the last move was recent so
      // the engine is guaranteed to end up with the last operator position.
      try{
        if(sendRAF){ cancelAnimationFrame(sendRAF); sendRAF = 0; }
      }catch(_){ }
      const finalVal = state.mixer.faders[id];
      maybePostGain(finalVal, { force: true });

      // v0.3.26: Persist the operator's last-known fader position so a
      // browser reload returns to where they left it (until DSP truth
      // readback exists).
    });

    lane.addEventListener('pointercancel', (e) => {
      if(!lane.hasPointerCapture(e.pointerId)) return;
      try{ lane.releasePointerCapture(e.pointerId); }catch(_){ }
      lane.classList.remove('isDragging');

      // Best-effort persistence even on cancel.
    });
  });

  // If the window resizes (touchscreen orientation changes), recompute
  // puck positions based on the normalized values.
  window.addEventListener('resize', () => {
    for(const id of Object.keys(state.mixer.faders || {})){
      setFaderUI(id, state.mixer.faders[id]);
    }
  });
}

// Engineering page config form state.
//
// IMPORTANT UX NOTE:
// Historically, the Engineering → Configuration form did NOT auto-load
// the saved config on page refresh; it showed default placeholders
// (e.g. "mock (default)") until the user clicked "Load".
//
// That behavior is correct but confusing: it *looks* like the system
// reverted to mock mode when, in reality, only the form reset.
//
// To reduce confusion we:
//   1) Auto-load the saved config into the form when the Engineering page opens.
//   2) Never overwrite user edits in-progress ("dirty" tracking).
// We now auto-load the config when the Engineering page is shown,
// *as long as the user hasn't started editing*.
let engCfgLoaded = false;
let engCfgDirty = false;
let engCfgAutoLoadInFlight = false;

function $(sel){ return document.querySelector(sel); }
// ---------------------------------------------------------------------------
// Shared JSON fetch helper (v0.2.51)
// Centralized here so DSP health/timeline and other UI features
// never depend on implicit scope or load order.
// ---------------------------------------------------------------------------
async function getJSON(url){
  const res = await fetch(url, { headers: { "Accept": "application/json" } });
  if(!res.ok){
    const t = await res.text();
    throw new Error(t || ("HTTP " + res.status));
  }
  return await res.json();
}

function $all(sel){ return Array.from(document.querySelectorAll(sel)); }

// ------------------------------
// Admin/status message helpers
// ------------------------------
// We keep messaging logic centralized so we don't end up with "half states"
// where the message says one thing but buttons show another.
//
// IMPORTANT PRODUCTION NOTE:
// - Updates intentionally do NOT auto-deploy from the folder watcher.
// - The ONLY thing that makes changes live is `sudo ./install.sh`.
// - Even after install completes, the browser may still be showing cached JS/CSS.
//   Therefore a *manual refresh* is an accepted and explicit operator step.
// ------------------------------
function setSvcStatus(kind, msg){
  const el = $("#svcMsg");
  if(!el) return;

  // Preserve the small typography while adding status styling.
  // kind: "ok" | "warn" | "bad" | "busy"
  const k = (kind === "ok") ? "ok" : (kind === "bad") ? "bad" : "warn";
  el.className = "small statusline " + k;
  el.textContent = msg || "";

  // Show/hide "Clear" based on whether there's any message to clear.
  const clr = $("#btnSvcClear");
  if(clr){
    if(msg){
      clr.classList.remove("hidden");
    }else{
      clr.classList.add("hidden");
    }
  }
}

function clearSvcStatus(){
  setSvcStatus("warn", "");
  const el = $("#svcMsg");
  if(el){
    // Return to the original class list so layout stays consistent.
    el.className = "small";
    el.textContent = "";
  }
  const r = $("#btnRefresh");
  if(r) r.classList.add("hidden");
  const clr = $("#btnSvcClear");
  if(clr) clr.classList.add("hidden");
}

// Show the explicit refresh button (we don't silently refresh in production).
function showRefreshButton(){
  const r = $("#btnRefresh");
  if(!r) return;
  r.classList.remove("hidden");
  r.disabled = false;
  r.textContent = "Refresh Now";
  r.onclick = () => hardReload();
}


// Force a refresh that is very likely to pull new JS/CSS after an update.
// Some browsers will happily keep serving cached assets on a plain reload,
// leaving the operator on a "new engine / old UI" mismatch until they
// manually refresh.
function hardReload(){
  try{
    const u = new URL(window.location.href);
    // Preserve existing query params; just bump a cache buster.
    u.searchParams.set("_r", String(Date.now()));
    window.location.replace(u.toString());
  }catch(_){
    // Fallback if URL parsing fails for any reason.
    window.location.reload();
  }
}

function clamp01(x){
  const v = Number(x);
  if(Number.isNaN(v)) return 0;
  return Math.max(0, Math.min(1, v));
}

function setConn(ok){
  const el = $("#connStatus");
  if(ok){
    el.textContent = "Connected";
    el.classList.remove("bad");
    el.classList.add("ok");
  }else{
    el.textContent = "Disconnected";
    el.classList.remove("ok");
    el.classList.add("bad");
  }
}

function setPills(){
  // Engine runtime identity
  // Show UI + engine versions separately so it's obvious what updated.
  const uiVerPill = $("#uiVerPill");
  if (uiVerPill) uiVerPill.textContent = `ui v${UI_BUILD_VERSION}`;
  $("#verPill").textContent = "engine v" + (state.version || "—");
  $("#modePill").textContent = "engine: " + (state.mode || "—");

  // DSP connectivity (status/monitoring) — always-on.
  const dspConn = $("#dspConnPill");
  if(dspConn){
    const s = (state.dspHealth && state.dspHealth.state) ? String(state.dspHealth.state).toUpperCase() : "—";
    dspConn.textContent = "dsp: " + s;
    dspConn.classList.remove("ok","bad");
    if(s === "OK"){
      dspConn.classList.add("ok");
    }else if(s === "DISCONNECTED"){
      dspConn.classList.add("bad");
    }
  }

  // DSP write behavior — derived from /api/dsp/mode (config intent).
  const dspW = $("#dspWritePill");
  if(dspW){
    const m = state.dspModeStatus || {};
    const desired = (m.mode || "").toLowerCase();
    const active = (m.activeMode || m.mode || "—").toLowerCase();

    // In Option 1, active should match desired; we still display both concepts plainly.
    const label = (active && active !== "—") ? active.toUpperCase() : "—";
    dspW.textContent = "dsp writes: " + label;

    dspW.classList.remove("pill--warn","ok","bad");
    if(active === "live"){
      // Attention without being alarming: LIVE means writes affect the real DSP.
      dspW.classList.add("pill--warn");
    }
  }
}

// ---------------------------------------------------------------------------
// Persisted vs runtime configuration clarity (UI v0.3.07)
// ---------------------------------------------------------------------------
// The Engineering → Configuration card edits the persisted on-disk file.
// The running engine may be in a different mode if the watchdog promoted or
// overrode runtime state without writing back to disk.
//
// This helper keeps the display explicit so operators don't have to guess.
function renderConfigClarity(){
  const pEl = $("#cfgPersistedMode");
  const rEl = $("#cfgRuntimeMode");
  const bEl = $("#cfgRuntimeBadge");
  if(!pEl || !rEl || !bEl) return;

  const persisted = (state.cfgClarity.persistedMode || "").toLowerCase();
  const runtime = (state.cfgClarity.runtimeMode || "").toLowerCase();
  const active = (state.cfgClarity.runtimeActiveMode || "").toLowerCase();

  pEl.textContent = persisted ? persisted.toUpperCase() : "—";
  pEl.title = "Persisted mode from ~/.StudioB-UI/config/config.v1 (applies on restart)";

  // Runtime display includes the "active write" mode if it differs.
  if(runtime){
    let rt = runtime.toUpperCase();
    if(active && active !== runtime){
      rt += ` (active: ${active.toUpperCase()})`;
    }
    rEl.textContent = rt;
  }else{
    rEl.textContent = "—";
  }
  rEl.title = "Runtime mode reported by the running engine (may differ from persisted config if overridden)";

  const mismatch = !!(persisted && runtime && persisted !== runtime);
  bEl.classList.toggle("hidden", !mismatch);

  // Tooltip (v0.3.08): explain *why* the badge is present.
  //
  // We intentionally do not auto-write runtime state back to disk.
  // The watchdog may temporarily promote/override runtime behavior
  // (for safety/continuity) while leaving the persisted config file
  // untouched.
  //
  // NOTE: We don't yet have an explicit {source} field from the engine.
  // Until that API exists, we show a best-effort hint based on watchdog
  // status so the operator has a plausible explanation.
  if(mismatch){
    const wd = window.__lastWatchdogStatus || null;
    const wdLikely = !!(wd && wd.ok && String(wd.enabled).toLowerCase() === "enabled" && String(wd.active).toLowerCase() === "active");
    const src = wdLikely ? "watchdog" : "engine/runtime";
    bEl.title = `Persisted mode (${persisted.toUpperCase()}) differs from runtime mode (${runtime.toUpperCase()}). Possible source: ${src}. Persisted config applies on restart.`;
  }else{
    bEl.title = "";
  }
}


// ---------------------------------------------------------------------------
// Engineering Config post-save helper (v0.2.54)
//
// This function exists for one job:
// After a successful config Save, update small UI bits immediately so the
// operator has instant feedback without needing a refresh.
//
// IMPORTANT:
// - This does NOT reload config from disk (that requires Admin PIN + API call).
// - It updates the mode pill to match the currently selected Mode dropdown.
// - It is safe, explicit, and local-only.
// ---------------------------------------------------------------------------
async function loadConfigPill(){
  try{
    // Keep the header pill aligned with the selected mode.
    state.mode = $("#cfgMode") ? $("#cfgMode").value : (state.mode || "—");
    setPills();
  }catch(e){
    // Best-effort only.
  }
}

// Backwards-compat alias: some older UI code referenced loadConfigFill().
// Keeping this avoids regressions when we touch config code.
async function loadConfigFill(){
  return await loadConfigPill();
}


function setMeterFill(id, v){
  const el = document.getElementById(id);
  if(!el) return;
  el.style.width = (clamp01(v) * 100).toFixed(1) + "%";
}

// Vertical meter fill helper (UI v0.3.65)
// The Studio page VU lanes are tall vertical columns. We drive the inner
// .fader__meterFill height from 0..100%.
function setMeterFillV(id, v){
  const el = document.getElementById(id);
  if(!el) return;
  el.style.height = (clamp01(v) * 100).toFixed(1) + "%";
}

function setLampAutoMute(on){
  const lamp = $("#lampAutoMute");
  if(!lamp) return;
  lamp.classList.toggle("on", !!on);
}

// Speaker automute visual alert (RC 560)
//
// v0.3.82 intent (original): RC 560 was "SPK AUTO-MUTED" (TRUE when muted).
// v0.3.85 update (operator relabel): RC 560 is now "ALL_MICS_CLOSED".
//   - RC 560 TRUE  = all mics closed  (speakers should be free)
//   - RC 560 FALSE = any mic open     (speakers are auto-muted)
//
// Therefore, the red "automute" glow should engage when RC 560 is FALSE.
//
// IMPORTANT: this is purely a visual indicator. The DSP remains the source of
// truth for the automute state.
//
// v0.3.88 reliability:
// RC 560 is DSP-derived logic and may occasionally arrive late or be missing
// from a partial update. Additionally, transient DSP poll failures can cause
// brief gaps in the RC stream. To prevent a distracting red "blink" when all
// mics are muted, we add a tiny debounce / hold-last-known layer *only for the
// visual glow*.
let _spkAutoMuteStableOn = false;        // what we are currently showing
let _spkAutoMuteCandidateOn = null;      // proposed new state (waiting)
let _spkAutoMuteCandidateSince = 0;      // ms timestamp when candidate began
function applySpeakerAutoMuteGlowFromRC(){
  const v = rcGet(560);
  // If RC 560 is absent/invalid, hold last-known visual state.
  if(typeof v !== 'number' || isNaN(v)){
    const card = document.getElementById('speakersCard');
    if(card) card.classList.toggle('automuteActive', _spkAutoMuteStableOn);
    return;
  }

  const allMicsClosed = v >= 0.5;
  const on = !allMicsClosed;

  // Debounce: require the new state to be stable for a short window
  // before we flip the UI. (Purely visual; DSP remains truth.)
  const now = Date.now();
  const msOn  = 180; // require ~180ms stable before showing red
  const msOff = 320; // require longer stability before clearing red
  if(on !== _spkAutoMuteStableOn){
    if(_spkAutoMuteCandidateOn !== on){
      _spkAutoMuteCandidateOn = on;
      _spkAutoMuteCandidateSince = now;
    }
    const age = now - _spkAutoMuteCandidateSince;
    const need = on ? msOn : msOff;
    if(age >= need){
      _spkAutoMuteStableOn = on;
      _spkAutoMuteCandidateOn = null;
    }
  } else {
    // If we're already showing the correct state, clear any candidate.
    _spkAutoMuteCandidateOn = null;
  }

  const card = document.getElementById('speakersCard');
  if(card) card.classList.toggle('automuteActive', _spkAutoMuteStableOn);
}

function updateSpeakerUI(){
  // UI v0.3.38: The Studio page no longer renders the speaker "panel".
  // Speakers are now controlled via the top-row fader (RC 160/161).
  // If the panel isn't present, this function becomes a no-op.
  const hasPanel = !!(
    document.querySelector('[data-val-for="STUB_SPK_LEVEL"]') ||
    document.querySelector('input.slider[data-rc="STUB_SPK_LEVEL"]') ||
    document.getElementById('lampAutoMute') ||
    document.getElementById('spkMuteNote') ||
    document.querySelector('.btn.toggle[data-rc="STUB_SPK_MUTE"]')
  );
  if(!hasPanel) return;

  const v = clamp01(state.speaker.level);
  $all('[data-val-for="STUB_SPK_LEVEL"]').forEach(el=> el.textContent = v.toFixed(2));

  const slider = document.querySelector('input.slider[data-rc="STUB_SPK_LEVEL"]');
  if(slider && !slider.matches(":active")){
    // Don't fight the operator while dragging
    slider.value = String(v);
  }

  const muteBtn = document.querySelector('.btn.toggle[data-rc="STUB_SPK_MUTE"]');
  if(muteBtn) muteBtn.classList.toggle("on", !!state.speaker.mute);

  setLampAutoMute(state.speaker.automute);

  const note = $("#spkMuteNote");
  if(note){
    if(state.speaker.automute) note.textContent = "Auto-mute active";
    else if(state.speaker.mute) note.textContent = "Muted";
    else note.textContent = "";
  }
}

function syncTogglesFromStatus(){
  // Mic buttons reflect their last commanded state (until real DSP feedback exists)
  // For now, keep their visual state based on local dataset cache.
  $all(".btn.toggle").forEach(btn=>{
    const k = btn.getAttribute("data-rc");
    if(k === "STUB_SPK_MUTE") return; // driven by status
    const on = btn.dataset.on === "1";
    btn.classList.toggle("on", on);
  });
}

// NOTE: This UI is frequently used over WAN links (home access, port-forwards,
// VPNs). A 500ms timeout can be too aggressive and can leave the UI stuck on
// "Connecting..." even though the backend is healthy.
//
// We still want to fail fast on real outages, so we pick a few seconds.
async function fetchJSON(url, opts={}, timeoutMs=2500){
  const ctrl = new AbortController();
  const t = setTimeout(()=>ctrl.abort(), timeoutMs);
  try{
    const res = await fetch(url, { ...opts, signal: ctrl.signal });
    if(!res.ok) throw new Error(await res.text());
    return await res.json();
  } finally {
    clearTimeout(t);
  }
}

async function postRC(name, value){
  // v0.2.48 safety: if DSP is DISCONNECTED, block control writes.
  // The engine also enforces this (defense-in-depth), but blocking here gives immediate operator feedback.
  if((state.dspHealth && String(state.dspHealth.state||"").toUpperCase()==="DISCONNECTED")){
    const warn = $("#dspControlWarn");
    if(warn){ warn.style.display="block"; }
    throw new Error("DSP control blocked: DSP is disconnected");
  }
  await fetch("/api/rc/" + encodeURIComponent(name), {
    method: "POST",
    headers: { "Content-Type":"application/json" },
    body: JSON.stringify({ value: Number(value) })
  }).then(async res=>{
    if(!res.ok) throw new Error(await res.text());
  });
}

// postSpeakerMuteIntent sends the Speaker Mute action through the "intent" API.
//
// Safety note:
// - In mock mode, this remains non-destructive (log + cache only).
// - In live mode (v0.2.76+), Speaker Mute attempts a real DSP write via the engine.
async function postSpeakerMuteIntent(mute){
  // Reuse the same front-end DSP guard used by postRC for immediate operator feedback.
  if((state.dspHealth && String(state.dspHealth.state||"").toUpperCase()==="DISCONNECTED")){
    const warn = $("#dspControlWarn");
    if(warn){ warn.style.display="block"; }
    throw new Error("DSP control blocked: DSP is disconnected");
  }
  await fetch("/api/intent/speaker/mute", {
    method: "POST",
    headers: { "Content-Type":"application/json" },
    body: JSON.stringify({ mute: !!mute, source: "ui" })
  }).then(async res=>{
    if(!res.ok) throw new Error(await res.text());
  });
}



// ---------------------------------------------------------------------------
// DSP Health (v0.2.48)
//
// IMPORTANT:
// - GET /api/dsp/health is read-only from the UI perspective.
//   The engine maintains a small always-on monitor loop that updates this state.
// - POST /api/dsp/test performs ONE bounded TCP connect and is only called
//   when the operator clicks "Test DSP Now".
// ---------------------------------------------------------------------------

async function fetchDSPHealth(){
  try{
    const j = await getJSON("/api/dsp/health");
    const prevState = _prev.dspHealthState;
    state.dspHealth = {
      state: j.state || "UNKNOWN",
      lastOk: j.lastOk || "",
      failures: Number(j.consecutiveFailures || 0),
      lastError: j.lastError || "",
      lastTestAt: j.lastTestAt || "",
      lastPollAt: j.lastPollAt || "",
      connected: !!j.connected
    };

    // Runtime event logging (UI v0.3.12): DSP health transitions.
    // We log only when the top-level state changes to avoid noise.
    const curState = String(state.dspHealth.state || "UNKNOWN").toUpperCase();
    if(prevState === null){
      _prev.dspHealthState = curState;
      addRuntimeEvent(`DSP health: ${curState}`);
    }else if(prevState !== curState){
      addRuntimeEvent(`DSP health changed: ${prevState} → ${curState}`);
      _prev.dspHealthState = curState;
    }
    renderDSPHealth();
    setPills();
  }catch(e){
    // Health endpoint should be reliable; if not, show unknown.
    state.dspHealth = { state:"UNKNOWN", connected:false, lastOk:"", lastPollAt:"", failures:0, lastError:String(e), lastTestAt:"" };

    // If the health endpoint fails, log once (or when it changes).
    const curState = "UNKNOWN";
    if(_prev.dspHealthState === null){
      _prev.dspHealthState = curState;
      addRuntimeEvent("DSP health: UNKNOWN (health endpoint error)");
    }else if(_prev.dspHealthState !== curState){
      addRuntimeEvent(`DSP health changed: ${_prev.dspHealthState} → UNKNOWN (health endpoint error)`);
      _prev.dspHealthState = curState;
    }
    renderDSPHealth();
    setPills();
  }
}

async function fetchDSPTimeline(){
  try{
    const arr = await getJSON("/api/dsp/timeline?n=50");
    // Render a simple, copy/paste friendly view.
    const lines = (arr||[]).map(e=>{
      const t = e.time || "—";
      const s = e.state || "—";
      const f = (typeof e.failures === "number") ? e.failures : "—";
      const err = e.last_error || e.lastError || "";
      return `${t} | ${s} | failures=${f}${err? " | "+err:""}`;
    });
    $("#dspTimeline").textContent = lines.length ? lines.join("\n") : "—";
  }catch(e){
    $("#dspTimeline").textContent = "Timeline unavailable: " + String(e);
  }
}

function renderDSPHealth(){
  // UI v0.3.40: The Studio page may omit the DSP Health panel entirely
  // (operator requested a clean "fader console"). The engineering page may
  // still render it. Therefore ALL DOM writes here must be null-safe.
  function _setText(id, txt){
    const el = document.getElementById(id);
    if(el) el.textContent = (txt ?? "—");
  }

  _setText("dspHealthState", state.dspHealth.state || "—");
  _setText("dspHealthLastOk", state.dspHealth.lastOk || "—");
  _setText("dspHealthFails", String(state.dspHealth.failures ?? "—"));
  _setText("dspHealthErr", state.dspHealth.lastError || "—");
  _setText("dspHealthLastTest", state.dspHealth.lastTestAt || "—");

  const lp = document.getElementById("dspHealthLastPoll");
  if(lp) lp.textContent = state.dspHealth.lastPollAt || "—";

  // Operator safety message shown when DISCONNECTED.
  const warn = document.getElementById("dspControlWarn");
  if(!warn) return;

  if((state.dspHealth.state||"").toUpperCase() === "DISCONNECTED"){
    warn.style.display = "block";
    warn.textContent = "DSP is DISCONNECTED. Control writes are disabled to prevent silent failure. Click 'Test DSP Now' to verify link.";
  }else{
    warn.style.display = "none";
    warn.textContent = "";
  }
}

function applyStudioStatus(j){
  const newVer = j.version || "—";
  const newMode = j.mode || "—";

  // Runtime event logging (UI v0.3.12)
  // Detect when the engine identity (version/mode) changes. This often happens
  // after a watchdog-driven systemctl restart.
  if(_prev.engineVersion === null){
    _prev.engineVersion = newVer;
    _prev.engineMode = newMode;
    addRuntimeEvent(`Engine status: v${newVer} (${newMode})`);
  }else{
    if(_prev.engineVersion !== newVer){
      addRuntimeEvent(`Engine version changed: v${_prev.engineVersion} → v${newVer}`);
      _prev.engineVersion = newVer;
    }
    if(_prev.engineMode !== newMode){
      addRuntimeEvent(`Engine mode changed: ${String(_prev.engineMode)} → ${String(newMode)}`);
      _prev.engineMode = newMode;
    }
  }

  state.version = newVer;
  state.mode = newMode;

  // speaker
  state.speaker.level = clamp01(j?.speaker?.level);
  state.speaker.mute = !!j?.speaker?.mute;
  state.speaker.automute = !!j?.speaker?.automute;

  // meters (targets)
  const m = j?.meters || {};

  // PlayIt Live meters (UI v0.3.66)
  // Preferred source: RC 462/463 via WebSocket (applyPILMetersFromRC).
  // IMPORTANT: Do not mirror /api/studio/status meters into PlayIt Live.
  // That endpoint may reflect a different tap-point (or be stale), which
  // creates confusing behavior when Composer meters are dead.
  //
  // PlayIt Live meters are RC-only: 462/463.
  //
  // (We keep rsrL/rsrR for the dedicated Remote Studio Return meters.)

  // Fallback removed: do NOT mirror Remote Studio Return meters as part of
  // /api/studio/status (rsrL/rsrR), mirror them into the PlayIt Live meters.
  // This ensures we still get movement even if the engine does not publish
  // meter values onto the RC bus.

  state.meters.pgmL.tgt = clamp01(m.pgmL);
  state.meters.pgmR.tgt = clamp01(m.pgmR);
  state.meters.spkL.tgt = clamp01(m.spkL);
  state.meters.spkR.tgt = clamp01(m.spkR);
  state.meters.rsrL.tgt = clamp01(m.rsrL);
  state.meters.rsrR.tgt = clamp01(m.rsrR);

  updateSpeakerUI();
  setPills();
}

async function pollLoop(){
  try{
    // Remote links can add latency; use the default timeout (a few seconds).
    const j = await fetchJSON("/api/studio/status");
    state.connected = true;
    if(_prev.connected === null){
      _prev.connected = true;
      addRuntimeEvent("Connected to engine");
    }else if(_prev.connected !== true){
      _prev.connected = true;
      addRuntimeEvent("Reconnected to engine");
    }
    state.lastOkAt = Date.now();
    applyStudioStatus(j);
  }catch(e){
    // consider disconnected if we haven't had a good poll in > 2s
    if(Date.now() - state.lastOkAt > 2000){
      state.connected = false;
      if(_prev.connected === null){
        _prev.connected = false;
        addRuntimeEvent("Disconnected from engine");
      }else if(_prev.connected !== false){
        _prev.connected = false;
        addRuntimeEvent("Disconnected from engine");
      }
    }
  }finally{
    setConn(state.connected);
    setTimeout(pollLoop, POLL_MS);
  }
}

// Meter animation smoothing (fast attack, slower release)
function meterAnimate(){
  const ATTACK = 0.35;  // per-frame easing
  const RELEASE = 0.10; // per-frame easing

  for(const key of Object.keys(state.meters)){
    const o = state.meters[key];
    const cur = o.cur;
    const tgt = o.tgt;
    const k = (tgt > cur) ? ATTACK : RELEASE;
    o.cur = cur + (tgt - cur) * k;
  }

  // PlayIt Live meters (RC 462/463)
  setMeterFillV("m_pilL", vuDisplay(state.meters.pilL.cur));
  setMeterFillV("m_pilR", vuDisplay(state.meters.pilR.cur));

  // NOTE:
  // Some top-row meters are rendered incrementally.
  // - Speakers (m_spkL/m_spkR) are wired as of UI v0.3.85.
  // - Program + RSR remain placeholders until their cards render fill elements.
  setMeterFill("m_pgmL", state.meters.pgmL.cur);
  setMeterFill("m_pgmR", state.meters.pgmR.cur);
  // Speakers meters are vertical columns on the Studio page.
  // Use the same display mapping as PlayIt Live (VU -72..0 mapped into fader travel).
  setMeterFillV("m_spkL", vuDisplay(state.meters.spkL.cur));
  setMeterFillV("m_spkR", vuDisplay(state.meters.spkR.cur));
  setMeterFill("m_rsrL", state.meters.rsrL.cur);
  setMeterFill("m_rsrR", state.meters.rsrR.cur);

  requestAnimationFrame(meterAnimate);
}

// --- Engineering PIN gate ---
function showPinModal(show){
  $("#pinModal").classList.toggle("hidden", !show);
  if(show){
    $("#pinMsg").textContent = "";
    $("#pinInput").value = "";
    $("#pinInput").focus();
  }
}

function getSavedPin(){
  return sessionStorage.getItem("admin_pin") || "";
}
function savePin(pin){
  sessionStorage.setItem("admin_pin", pin);
}

async function validatePin(pin){
  // No dedicated "validate" endpoint; use an admin endpoint.
  await fetchJSON("/api/admin/releases", { headers: {"X-Admin-PIN": pin} }, 800);
  return true;
}

function setActivePage(page){
  $all(".tab").forEach(x=>x.classList.toggle("active", x.getAttribute("data-page") === page));
  $("#page-studio").classList.toggle("hidden", page !== "studio");
  $("#page-engineering").classList.toggle("hidden", page !== "engineering");
  if(page === "engineering"){
    $("#adminPin").value = getSavedPin();
    refreshEngineering().catch(()=>{});
    // The watchdog may be started/stopped outside the UI (CLI, installer, etc.).
    // Keep engineering status fresh automatically while this page is visible.
    if(!state._engRefreshTimer){
      state._engRefreshTimer = setInterval(() => {
        // Only refresh if the engineering page is visible.
        if(!$("#page-engineering").classList.contains("hidden")){
          refreshEngineering().catch(()=>{});
        }
      }, 5000);
    }
  }else{
    if(state._engRefreshTimer){
      clearInterval(state._engRefreshTimer);
      state._engRefreshTimer = null;
    }
  }
}

async function refreshEngineering(){
  // Health + state are read-only; admin endpoints still require PIN for update/rollback/releases
  try{
    const h = await fetchJSON("/api/health", {}, 800);
    $("#engineInfo").textContent = JSON.stringify(h, null, 2);

    // Restart-required UX (no manual page refresh required)
    // -----------------------------------------------------
    // Some configuration changes (e.g., switching between mock/live DSP mode)
    // require a stub-engine restart to take effect. The backend will set
    // restartRequired=true, and the watchdog performs the systemctl restart.
    // Historically the UI would show "Waiting for engine restart..." and the
    // user would refresh the whole page to see the new state.
    //
    // Instead, we detect the flag transitions here and:
    //  - show a clear banner while restart is pending
    //  - provide a "Restart engine now" button (safe; it only re-asserts the
    //    restart-required flag) in case something got stuck
    //  - automatically clear the banner once the engine comes back.
    const cfgMsg = $("#cfgMsg");
    const rr = !!h.restartRequired;
    const wasRR = !!state._prevRestartRequired;
    state._prevRestartRequired = rr;

    function ensureRestartButton(){
      // Inject the button only when needed so we don't touch index.html.
      if(!rr) return;
      if(cfgMsg.querySelector("#btnEngineRestart")) return;

      const btn = document.createElement("button");
      btn.id = "btnEngineRestart";
      btn.className = "btn";
      btn.textContent = "Restart engine now";
      btn.style.marginLeft = "10px";
      btn.onclick = async () => {
        try{
          btn.disabled = true;
          btn.textContent = "Restarting…";
          await fetchJSON("/api/admin/restart", {
            method: "POST",
            headers: {"X-Admin-PIN": getSavedPin()}
          }, 3000);
        }catch(e){
          console.error(e);
        }finally{
          // The watchdog restart is async; keep the button disabled while the
          // restartRequired flag remains true.
          btn.disabled = true;
          btn.textContent = "Restarting…";
        }
      };

      cfgMsg.appendChild(btn);
    }

    if(rr){
      // If cfgMsg currently contains a "Saved..." message, keep it; otherwise
      // provide a consistent banner.
      if(!cfgMsg.textContent || cfgMsg.textContent.trim() === ""){
        cfgMsg.textContent = "Restart required. Waiting for engine restart to apply changes…";
      }
      ensureRestartButton();
    }else if(wasRR && !rr){
      // Restart completed.
      cfgMsg.textContent = "Engine restarted. Settings applied.";
      // Clear the message after a short delay so the page doesn't feel "stuck".
      setTimeout(() => {
        // Only clear if nothing else has written to the message area.
        if($("#cfgMsg").textContent === "Engine restarted. Settings applied."){
          $("#cfgMsg").textContent = "";
        }
      }, 4000);
    }
  }catch(e){
    $("#engineInfo").textContent = "Failed to load /api/health";
  }

  try{
    const s = await fetchJSON("/api/state", {}, 800);
    $("#stateDump").textContent = JSON.stringify(s, null, 2);
  }catch(e){
    $("#stateDump").textContent = "Failed to load /api/state";
  }

  // Watchdog status (read-only)
  try{
    const wd = await fetchJSON("/api/watchdog/status", {}, 800);
    // Used by the action button to detect when the status flips.
    window.__lastWatchdogStatus = wd;
    let msg = "";
    if(wd && wd.ok){
      msg = `Enabled: ${wd.enabled} | Active: ${wd.active}`;
      if(wd.notes){ msg += ` — ${wd.notes}`; }
    }else{
      msg = "Watchdog status unavailable";
    }
    $("#watchdogMsg").textContent = msg;

    // v0.2.40: show systemd "Active:" and "SubState" lines verbatim.
    // These strings are meant to match what an operator would see in:
    //   systemctl status stub-ui-watchdog
    //   systemctl show -p SubState stub-ui-watchdog
    const sysEl = $("#watchdogSystemd");
    if(sysEl){
      const lines = [];
      if(wd && wd.systemdActiveLine){ lines.push(wd.systemdActiveLine); }
      if(wd && wd.systemdSubStateLine){ lines.push(wd.systemdSubStateLine); }
      sysEl.textContent = (lines.length ? lines.join("\n") : "No systemd details available");
    }

    // Button: only meaningful when enabled but not running.
    const btn = $("#btnWatchdogStart");
    if(btn){
      // "Start watchdog" should work even if the unit is currently disabled.
      // If the operator disabled it from the CLI, the UI should be able to
      // re-enable and start it.
      const canStart = (wd && wd.active !== "active");
      btn.disabled = !canStart;
      btn.title = canStart ? "Enable & start stub-ui-watchdog" : "No action needed";
    }
  }catch(e){
    $("#watchdogMsg").textContent = "Watchdog status: failed to load";
  }

  // UX hardening:
  // When the browser is refreshed while on the Engineering tab, the config
  // form would reset to placeholders ("mock (default)") even though the
  // engine is still running in live mode.
  //
  // Important nuance:
  // - Loading the *file* config is PIN-gated.
  // - But simply *displaying* the currently-running config should not
  //   require a PIN (otherwise the UI looks "wrong" after every refresh).
  //
  // So: on first entry to Engineering, load the effective config from
  // /api/config and paint it into the form. This never overwrites
  // in-progress edits (dirty form).
  if(state.activePage === "engineering" && !engCfgLoaded && !engCfgDirty){
    // UI v0.3.07: Prefer loading the *persisted* config file into the editor.
    // We can do this safely because the Engineering page is already PIN-gated
    // and we restore the saved PIN into #adminPin when the tab is shown.
    //
    // If, for any reason, the PIN is missing/invalid, fall back to the
    // non-admin /api/config view so the editor still displays something.
    let loaded = false;
    try{ loaded = await loadConfigFromFile({ silent: true }); }catch(_e){ loaded = false; }
    if(!loaded){
      try{ await loadEffectiveConfigIntoForm({ silent: true }); }catch(_e){ /* ignore */ }
    }
    // Either way, keep the small Persisted/Runtime line updated.
    renderConfigClarity();
  }
}

function wireUI(){
  // Tabs with PIN gate
  $all(".tab").forEach(t=>{
    t.addEventListener("click", async ()=>{
      const page = t.getAttribute("data-page");
      if(page === "engineering"){
        const saved = getSavedPin();
        if(saved){
          try{
            await validatePin(saved);
            setActivePage("engineering");
            return;
          }catch(e){
            // fall through to prompt
          }
        }
        showPinModal(true);
        return;
      }
      setActivePage("studio");
    });
  });

  // Modal actions
  $("#btnPinCancel").addEventListener("click", ()=>{
    showPinModal(false);
    setActivePage("studio");
  });
  $("#btnPinUnlock").addEventListener("click", async ()=>{
    const pin = $("#pinInput").value.trim();
    if(!pin) return;
    $("#pinMsg").textContent = "Checking…";
    try{
      await validatePin(pin);
      savePin(pin);
      $("#adminPin").value = pin;
      showPinModal(false);
      setActivePage("engineering");
    }catch(e){
      $("#pinMsg").textContent = "Incorrect PIN.";
    }
  });
  $("#pinInput").addEventListener("keydown", (ev)=>{
    if(ev.key === "Enter") $("#btnPinUnlock").click();
    if(ev.key === "Escape") $("#btnPinCancel").click();
  });

  // Reconnect DSP (operator-safe)
  // NOTE: The Studio page can be configured to omit the "DSP Health" / reconnect panel.
  // In that case, the button won't exist, and we MUST NOT throw at startup.
  // (A single null dereference here prevents hydration from ever completing.)
  const btnReconnect = $("#btnReconnect");
  if(btnReconnect){
    btnReconnect.addEventListener("click", async ()=>{
      const msg = $("#reconnectMsg");
      if(msg) msg.textContent = "Sending…";
      try{
        await fetch("/api/reconnect", { method:"POST" });
        if(msg) msg.textContent = "OK";
        if(msg) setTimeout(()=>msg.textContent="", 1200);
      }catch(e){
        if(msg) msg.textContent = "Failed";
      }
    });
  }

// Manual "Test DSP Now" (single-shot). This is the ONLY place the UI triggers
// DSP network activity, and only on explicit operator request.
// The entire DSP Health panel can be removed from the Studio page; guard accordingly.
const btnDspTest = $("#btnDspTest");
if(btnDspTest){
	  btnDspTest.addEventListener("click", async ()=>{
	    const b = btnDspTest;
	    const msg = $("#dspTestMsg");
	    b.disabled = true;
	    if(msg) msg.textContent = "Testing…";
	    try{
	      const res = await fetch("/api/dsp/test", { method:"POST" });
	      const txt = await res.text();
	      if(!res.ok) throw new Error(txt);
	      // Update snapshot + timeline after test.
	      await fetchDSPHealth();
	      await fetchDSPTimeline();
	      if(msg) msg.textContent = "OK";
	      if(msg) setTimeout(()=>msg.textContent="", 1200);
	    }catch(e){
	      if(msg) msg.textContent = "Failed";
	      // Also refresh health/timeline so operator can see the error.
	      await fetchDSPHealth();
	      await fetchDSPTimeline();
	    }finally{
	      b.disabled = false;
	    }
	  });
}


  // RC controls: sliders
  let sliderRAF = 0;
  $all("input.slider").forEach(sl=>{
    const rc = sl.getAttribute("data-rc");
    sl.addEventListener("input", ()=>{
      const v = clamp01(sl.value);
      // local display while dragging
      $all(`[data-val-for="${rc}"]`).forEach(el=> el.textContent = v.toFixed(2));
      // throttle network writes to animation frames
      if(sliderRAF) cancelAnimationFrame(sliderRAF);
      sliderRAF = requestAnimationFrame(async ()=>{
        try{ await postRC(rc, v); }catch(e){}
      });
    });
  });

  // RC controls: toggles
  $all(".btn.toggle").forEach(btn=>{
    const rc = btn.getAttribute("data-rc");
    btn.addEventListener("click", async ()=>{
      if(rc === "STUB_SPK_AUTOMUTE") return; // indicator only
      if(rc === "STUB_SPK_MUTE"){
        const next = !state.speaker.mute;
        try{
          await postSpeakerMuteIntent(next);
          // Optimistic UI update: the next /api/studio/status poll will confirm.
          state.speaker.mute = next;
          updateSpeakerUI();
        }catch(e){}
        return;
      }
      // Mixer mutes (v0.3.30): DSP/engine is source-of-truth.
      // We derive current state from the latest RC cache and then POST the next state.
      const curOn = (rcGet(rc) >= 0.5);
      const nextOn = !curOn;

      // Optimistic visual update (WS delta will confirm/override).
      state.rc = state.rc || {};
      state.rc[String(rc)] = nextOn ? 1 : 0;
      applyMixerMutesFromRC();

      try{
        await postRC(rc, nextOn ? 1 : 0);
      }catch(e){
        // If the write fails, immediately refresh from the authoritative snapshot.
        await hydrateMixerViaHTTPFallback();
      }
    });
  });

  // Engineering buttons (update/rollback)
  $("#adminPin").addEventListener("input", ()=>{
    const pin = $("#adminPin").value.trim();
    if(pin) savePin(pin);
  });

  // Engineering: Config editor (v0.2.1)
  // This edits ~/.StudioB-UI/config.json so settings persist across updates/rollbacks.

  // Track whether the user has begun editing the form so we never overwrite
  // their in-progress changes during auto-load/poll refreshes.
  // NOTE: the input is #cfgDspIp (not #cfgDspHost).
  ["#cfgMode", "#cfgDspIp", "#cfgDspPort"].forEach(sel=>{
    const el = $(sel);
    if(!el) return;
    el.addEventListener("input", ()=>{ engCfgDirty = true; });
    el.addEventListener("change", ()=>{ engCfgDirty = true; });
  });
  // NOTE: We keep this as an explicit, admin-protected endpoint because it
  // returns extra metadata (path/exists). For status displays we use /api/config.
  
  // Load the *effective* config from the engine (no PIN required).
  //
  // Why this exists:
  // - /api/admin/config/file requires a PIN (by design).
  // - On refresh, the PIN field is empty, so the config editor would otherwise
  //   show defaults (mock) even if the engine is currently running in live mode.
  // - This caused confusion: the system *was* in live, but the form looked like
  //   it reverted.
  async function loadEffectiveConfigIntoForm(opts = {}) {
    // Never overwrite in-progress edits.
    if(engCfgDirty) return false;

    try{
      const cfg = await fetchJSON('/api/config', {}, 1200);
      if(cfg){
        // The engine exposes both a top-level mode and dsp.mode.
        // Prefer the config API's mode, but fall back to the running status if
        // the config payload is missing/empty (some older builds served a
        // partial config schema).
        let mode = (cfg.dsp && cfg.dsp.mode) ? cfg.dsp.mode : (cfg.mode || '');
        if (!mode) {
          try {
            const stResp = await fetch("/api/studio/status", { cache: "no-store" });
            if (stResp.ok) {
              const st = await stResp.json();
              mode = st.mode || mode;
            }
          } catch (_) {
            // ignore; we'll default below
          }
        }
        if (!mode) mode = 'mock';
        $("#cfgMode").value = mode;
        $("#cfgDspIp").value = (cfg.dsp && cfg.dsp.ip) ? cfg.dsp.ip : '';
        $("#cfgDspPort").value = (cfg.dsp && cfg.dsp.port) ? cfg.dsp.port : '';

        // Make it clear this came from the running engine.
        if(!opts.silent){
          $("#cfgMsg").textContent = "Loaded (effective from engine): " + (cfg.sources && cfg.sources.yaml_path ? cfg.sources.yaml_path : "config");
        }
        engCfgLoaded = true;
        engCfgDirty = false;
        return true;
      }
      return false;
    }catch(e){
      if(!opts.silent) $("#cfgMsg").textContent = "Load failed: " + e.message;
      return false;
    }
  }

  async function loadConfigFromFile(opts = {}) {
    const pin = $("#adminPin").value.trim();
    if(!pin) {
      if(opts.silent) return false;
      alert("Enter Admin PIN.");
      return false;
    }

    $("#cfgMsg").textContent = "Loading…";
    try{
      const resp = await fetchJSON("/api/admin/config/file", { headers: {"X-Admin-PIN": pin} }, 1200);
      if(resp && resp.config){
        $("#cfgMode").value = (resp.config.mode || "mock");
        $("#cfgDspIp").value = (resp.config.dsp && resp.config.dsp.ip) ? resp.config.dsp.ip : "";
        $("#cfgDspPort").value = (resp.config.dsp && resp.config.dsp.port) ? resp.config.dsp.port : "";

        // Persisted-vs-runtime clarity (UI v0.3.07): record persisted mode.
        state.cfgClarity.persistedMode = String(resp.config.mode || "mock");

        // Runtime event logging (UI v0.3.12): persisted config changes.
        // This records what the operator intends (applies on restart).
        const pm = String(state.cfgClarity.persistedMode || "").toLowerCase();
        if(_prev.persistedMode === null){
          _prev.persistedMode = pm;
          if(pm) addRuntimeEvent(`Persisted config: ${pm.toUpperCase()} (loaded from disk)`);
        }else if(_prev.persistedMode !== pm){
          addRuntimeEvent(`Persisted mode changed: ${String(_prev.persistedMode||"—").toUpperCase()} → ${pm.toUpperCase()} (loaded from disk)`);
          _prev.persistedMode = pm;
        }
        renderConfigClarity();
      }
      const path = resp.path || "~/.StudioB-UI/config.v1";
      const exists = resp.exists ? "exists" : "missing";
      $("#cfgMsg").textContent = "Loaded (" + exists + "): " + path;
      if(resp.error){ $("#cfgMsg").textContent += " — WARNING: " + resp.error; }

      engCfgLoaded = true;
      engCfgDirty = false;
      return true;
    }catch(e){
      // If we're doing a silent auto-load, don't replace the UI message.
      if(!opts.silent) $("#cfgMsg").textContent = "Load failed: " + e.message;
      return false;
    }
  }

  // Track whether the user has started editing the form so we don't overwrite.
  ["#cfgMode", "#cfgDspIp", "#cfgDspPort"].forEach(sel=>{
    const el = $(sel);
    if(!el) return;
    el.addEventListener("input", ()=>{ engCfgDirty = true; });
    el.addEventListener("change", ()=>{ engCfgDirty = true; });
  });

  $("#btnCfgLoad").addEventListener("click", async ()=>{
    await loadConfigFromFile({ silent: false });
  });

  $("#btnCfgSave").addEventListener("click", async ()=>{
    const pin = $("#adminPin").value.trim();
    if(!pin) return alert("Enter Admin PIN.");
    const body = {
      mode: $("#cfgMode").value,
      dsp: {
        ip: $("#cfgDspIp").value.trim(),
        port: parseInt($("#cfgDspPort").value, 10) || 0,
        mode: $("#cfgMode").value
      }
    };
    $("#cfgMsg").textContent = "Saving…";
    try{
      const resp = await fetch("/api/admin/config/file", {
  method: "PUT",
  headers: { "Content-Type":"application/json", "X-Admin-PIN": pin },
  body: JSON.stringify(body)
}).then(async r=>{
  if(!r.ok) throw new Error(await r.text());
  // The engine returns JSON with optional restart_required=true.
  try { return await r.json(); } catch { return { ok:true }; }
});

if(resp && resp.restart_required){
  $("#cfgMsg").textContent = "Saved. Engine restart requested (watchdog will restart stub-engine).";
} else {
  $("#cfgMsg").textContent = "Saved. Reloading effective config…";
}

// Persisted-vs-runtime clarity (UI v0.3.07): we just wrote the persisted file.
// Record it immediately so the side-by-side line updates without waiting for
// another "Load" click.
state.cfgClarity.persistedMode = String(body.mode || "");

// Runtime event logging (UI v0.3.12): persisted config changes.
// Saving updates the on-disk intent (takes full effect after restart).
const pm = String(state.cfgClarity.persistedMode || "").toLowerCase();
if(_prev.persistedMode === null){
  _prev.persistedMode = pm;
  if(pm) addRuntimeEvent(`Persisted config: ${pm.toUpperCase()} (saved)`);
}else if(_prev.persistedMode !== pm){
  addRuntimeEvent(`Persisted mode changed: ${String(_prev.persistedMode||"—").toUpperCase()} → ${pm.toUpperCase()} (saved)`);
  _prev.persistedMode = pm;
}
renderConfigClarity();

// Refresh /api/config view (and mode pill) immediately.
await loadConfigPill();

if(resp && resp.restart_required){
  // Give the watchdog a moment to restart the engine, then refresh pills again.
  setTimeout(()=>{ loadConfigPill(); }, 2500);
  $("#cfgMsg").textContent = "Saved. Waiting for engine restart to apply changes…";
} else {
  $("#cfgMsg").textContent = "Saved and applied.";
}
    }catch(e){
      $("#cfgMsg").textContent = "Save failed: " + e.message;
    }
  });

  // Engineering: Watchdog start (admin)
  $("#btnWatchdogStart").addEventListener("click", async ()=>{
    const pin = $("#adminPin").value.trim();
    if(!pin) return alert("Enter Admin PIN.");
    $("#watchdogMsg").textContent = "Enabling & starting watchdog…";
    $("#btnWatchdogStart").disabled = true;
    try{
      const r = await fetch("/api/admin/watchdog/start", {
        method: "POST",
        headers: { "X-Admin-PIN": pin }
      });
      // The endpoint now returns JSON with {ok, output, status}.
      const bodyText = await r.text();
      if(!r.ok) throw new Error(bodyText || ("HTTP " + r.status));

      let payload = null;
      try{ payload = bodyText ? JSON.parse(bodyText) : null; }catch(_){ payload = null; }

      if(payload && payload.ok === false){
        const out = payload.output ? ("\n\n" + payload.output) : "";
        throw new Error((payload.error || "watchdog start failed") + out);
      }

      const out = payload && payload.output ? payload.output.trim() : "";
      $("#watchdogMsg").textContent = out ? ("Requested. " + out) : "Requested. Waiting for service…";

      // Poll for up to ~10 seconds so CLI-initiated changes and systemd startup
      // reflect quickly without requiring a manual refresh.
      const startedAt = Date.now();
      while(true){
        await new Promise(res=>setTimeout(res, 1000));
        await refreshEngineering().catch(()=>{});
        // If we've already flipped to active, we can stop polling early.
        const wd = window.__lastWatchdogStatus;
        if(wd && wd.active === "active") break;
        if(Date.now() - startedAt > 10000) break;
      }
    }catch(e){
      $("#watchdogMsg").textContent = "Start failed: " + (e && e.message ? e.message : "unknown error");
    }finally{
      $("#btnWatchdogStart").disabled = false;
    }
  });

  $("#btnUpdate").addEventListener("click", async ()=>{
    const pin = $("#adminPin").value.trim();
    if(!pin) return alert("Enter Admin PIN.");
    if(!confirm("Update to the latest version from GitHub? (This will run the installer and restart the engine)")) return;

    // Best-effort: remember what we're aiming for so we can auto-refresh when it actually lands.
    // IMPORTANT: during an update the engine restarts. That can break the WebSocket and/or leave
    // the UI with a stale version banner until the user manually refreshes.
    // We mark an in-progress update so pollUpdate() can detect a version change via /api/health
    // and refresh automatically.
    const expected = (state.update && state.update.latest) ? state.update.latest : null;
    state.update = state.update || {};
    state.update.inProgress = true;
    // UI hardening:
    // - Clear any previous sticky message
    // - Hide the refresh button until we *know* refresh is needed.
    clearSvcStatus();

    state.update.startVersion = state.version || null;
    // Disable buttons to prevent double-submits.
    $("#btnUpdate").disabled = true;
    $("#btnRollback").disabled = true;
    try{
      const resp = await fetch("/api/updates/apply", {
        method:"POST",
        headers: { "Content-Type":"application/json", "X-Admin-PIN": pin },
        body: "{}"
      });
      // IMPORTANT:
      // We must not call resp.json() and then resp.text() on the same Response.
      // The body can only be consumed once, and Firefox will throw:
      //   "Response.text: Body has already been consumed."
      // To keep error handling robust, we read the body once as text and then
      // try to parse JSON from it.
      const raw = await resp.text();
      let data = {};
      try{
        data = raw ? JSON.parse(raw) : {};
      }catch(_e){
        // Not JSON (or corrupted). Treat the raw body as the error message.
        data = { ok:false, error: raw || "Invalid response (expected JSON)" };
      }
      if(!resp.ok || !data.ok){
        // IMPORTANT:
        // Do NOT embed literal newlines inside a quoted string ("...") here.
        // Some browsers (notably Firefox) treat that as a syntax error and the
        // entire UI JS fails to parse, making the UI appear "dead".
        const tail = (data && data.outputTail)
          ? "\n\n--- output (tail) ---\n" + data.outputTail
          : "";
        throw new Error((data && data.error) ? (data.error + tail) : ("HTTP " + resp.status + tail));
      }
      setSvcStatus("warn", expected
        ? `Update queued. Waiting for ${expected}… (refresh will be required)`
        : "Update queued. Waiting for the service to restart… (refresh will be required)");

      // Start a watchdog that will reload the page once the engine comes back on the new version.
      // (pollUpdate() also watches for a version change and will refresh as soon as it sees one.)
      waitForVersion(expected);
    }catch(e){
      setSvcStatus("bad", "Update failed: " + e.message);
      state.update = state.update || {};
      state.update.inProgress = false;
      $("#btnUpdate").disabled = false;
      $("#btnRollback").disabled = false;
    }
  });

  $("#btnRollback").addEventListener("click", async ()=>{
    const pin = $("#adminPin").value.trim();
    if(!pin) return alert("Enter Admin PIN.");
    try{
      const vers = await fetchJSON("/api/admin/releases", { headers: {"X-Admin-PIN": pin} }, 1200);
      const pick = prompt("Rollback to which release?\n\nAvailable:\n" + vers.join("\n"));
      if(!pick) return;
      if(!confirm("Rollback to " + pick + " ?")) return;
      await fetch("/api/admin/rollback", {
        method:"POST",
        headers: { "Content-Type":"application/json", "X-Admin-PIN": pin },
        body: JSON.stringify({ version: pick })
      }).then(async r=>{ if(!r.ok) throw new Error(await r.text()); });
      setSvcStatus("warn", "Rollback started. Waiting for the service to restart… (refresh will be required)");
      // Reload when the engine comes back (version may change).
      waitForVersion(null);
    }catch(e){
      setSvcStatus("bad", "Rollback failed: " + e.message);
    }
  });
  // Admin status helpers
  // - Refresh is shown explicitly when an update completes (or times out).
  // - Clear lets the operator dismiss a sticky message.
  const btnRefresh = $("#btnRefresh");
  if(btnRefresh){
    btnRefresh.classList.add("hidden");
    btnRefresh.addEventListener("click", ()=> hardReload());
  }
  const btnClear = $("#btnSvcClear");
  if(btnClear){
    btnClear.classList.add("hidden");
    btnClear.addEventListener("click", ()=> clearSvcStatus());
  }


}

wireUI();
pollLoop();

// After an update/rollback, the engine restarts. We keep polling health until it returns,
// then reload when the expected version is seen (or when any version change is detected).
async function waitForVersion(expectedVersion){
  const start = Date.now();
  const maxMs = 3 * 60 * 1000; // 3 minutes
  const before = state.engine && state.engine.version ? state.engine.version : null;

  const tick = async ()=>{
    // Stop after timeout
    if(Date.now() - start > maxMs){
      // Don't leave the operator stuck.
      // We do NOT auto-refresh the page in production; instead we show an explicit button.
      setSvcStatus("warn", "Update is still running (or taking longer than expected). You may refresh to re-check status.");
      showRefreshButton();
      return;
    }

    try{
      // Cache-bust to avoid intermediary caches during restart.
      const h = await fetchJSON(`/api/health?_=${Date.now()}`, {}, 1200);
      const v = h && h.version ? h.version : null;

      // If caller provided an expected version, wait for it.
      if(expectedVersion && v === expectedVersion){
        // Update complete. Tell the operator explicitly and refresh the UI.
        // We still show the button (in case the browser blocks navigation), but we
        // also auto-trigger a cache-busting reload so the operator doesn't have to
        // remember to manually refresh.
        setSvcStatus("ok", `Update complete. Engine is now ${v}. Reloading the UI now (cache-busting)…`);
        showRefreshButton();
        state.update = state.update || {};
        if(!state.update.autoReloadArmed){
          state.update.autoReloadArmed = true;
          setTimeout(() => hardReload(), 1250);
        }
        state.update = state.update || {};
        state.update.inProgress = false;
        // Re-enable admin controls (operator can refresh at their convenience).
        const bu = $("#btnUpdate"); if(bu) bu.disabled = false;
        const br = $("#btnRollback"); if(br) br.disabled = false;
        return;
      }

      // If we don't know the expected version, reload on any version change.
      if(!expectedVersion && before && v && v !== before){
        // Update complete. Tell the operator explicitly and refresh the UI.
        // We still show the button (in case the browser blocks navigation), but we
        // also auto-trigger a cache-busting reload so the operator doesn't have to
        // remember to manually refresh.
        setSvcStatus("ok", `Update complete. Engine is now ${v}. Reloading the UI now (cache-busting)…`);
        showRefreshButton();
        state.update = state.update || {};
        if(!state.update.autoReloadArmed){
          state.update.autoReloadArmed = true;
          setTimeout(() => hardReload(), 1250);
        }
        state.update = state.update || {};
        state.update.inProgress = false;
        // Re-enable admin controls (operator can refresh at their convenience).
        const bu = $("#btnUpdate"); if(bu) bu.disabled = false;
        const br = $("#btnRollback"); if(br) br.disabled = false;
        return;
      }

      // Still not there; keep waiting.
      setTimeout(tick, 1500);
    }catch(_e){
      // During restart / proxy flaps we may get network errors or non-JSON. Keep trying.
      setTimeout(tick, 1500);
    }
  };

  // Small delay so we don't hammer the service immediately.
  setTimeout(tick, 800);
}

// Update check: poll GitHub releases via engine (once/minute)
async function updateLoop(){
  // On cold load, show a friendly placeholder so operators don't see a sticky
  // "failed" banner while the first check is still in-flight.
  // pollUpdate() will overwrite this on the first successful response.
  if(!(state.update && state.update.lastMsg)){
    state.update = state.update || {};
    setUpdateCheckMsg("Update check: pending…", "Waiting for first successful check");
  }
  await pollUpdate();
  setTimeout(updateLoop, 60000);
}
updateLoop();

// Keep the "Update check" message in sync even across transient network hiccups.
// We deliberately do NOT want a sticky false "failed" message when the backend
// is healthy (common during restarts / proxy flaps).
function renderUpdateCheckMsg(){
  const ucm = document.getElementById("updateCheckMsg");
  if(!ucm) return;

  // If we have a last known-good message, prefer it.
  if(state.update && state.update.lastMsg){
    ucm.textContent = state.update.lastMsg;
    ucm.title = state.update.lastTitle || "";
    return;
  }

  // Otherwise keep it honest.
  ucm.textContent = "Update check: failed";
  ucm.title = state.update && state.update.lastErr ? String(state.update.lastErr) : "No details";
}

// Update the on-page message *and* keep our state snapshot in sync.
// This is intentionally simple and "stateless": every successful poll
// should overwrite any previous "failed" message.
function setUpdateCheckMsg(msg, title){
  // Keep state for debugging / tooltips.
  state.update.lastMsg = msg || "";
  state.update.lastTitle = title || "";

  const ucm = document.getElementById("updateCheckMsg");
  if(!ucm) return;
  ucm.textContent = state.update.lastMsg;
  ucm.title = state.update.lastTitle;
}

// Clicking the update pill jumps to Engineering (PIN-gated)
const __upPill = document.getElementById("updatePill");
if(__upPill){
  __upPill.addEventListener("click", ()=>{
    const eng = document.querySelector('.tab[data-page="engineering"]');
    if(eng) eng.click();
  });
}
requestAnimationFrame(meterAnimate);
async function pollUpdate(){
  state.update = state.update || {};
  try{
    // Version normalization helper.
    // The backend has historically returned versions both WITH and WITHOUT a leading "v".
    // If we compare raw strings, "v0.3.09" !== "0.3.09" and we will *wrongly* claim
    // "Update available" even when we are already up to date.
    function normVer(v){
      return (v || "").toString().trim().replace(/^v/i, "");
    }
    // Update-check should never falsely report "failed" just because ONE endpoint
    // is temporarily unreachable during restart / proxy flaps.
    //
    // We always trust /api/update/check for update status.
    // We *optionally* consult /api/health for mode/version because it reflects the
    // running engine even if WebSocket hasn't reconnected yet.
    // NOTE:
    // Operators previously saw a sticky "Update check failed" even when the backend
    // was healthy. The most common cause was a transient non-JSON response during
    // restarts (nginx/engine reload windows). If we cannot parse JSON, treat it as
    // a transient error and *do not* clobber a recent successful message.
    const updText = await fetch("/api/update/check").then(r=>r.text());
    let upd = null;
    try{
      upd = JSON.parse(updText);
    }catch(parseErr){
      throw new Error("update/check returned non-JSON: " + String(updText).slice(0, 120));
    }
    // Expose raw payload for quick operator debugging in the browser console.
    // Example: window.__lastUpdateCheck
    window.__lastUpdateCheck = upd;

    // Render update-check results immediately after parsing so the UI
    // never gets stuck showing the startup placeholder ("pending") just
    // because a later, non-critical step throws.
    // Normalize both current + latest for robust comparison.
    const currFromUpd = normVer(upd.currentVersion || "");
    const latestFromUpd = normVer(upd.latestVersion || upd.latest || "");
    // Prefer our own normalized compare when both versions are present.
    // Reason: some engines historically computed updateAvailable using raw string
    // compares and could claim an update existed when current="v0.3.09" and
    // latest="0.3.09".
    const updAvailByCompare = !!(latestFromUpd && currFromUpd && latestFromUpd !== currFromUpd);
    const updAvailFromUpd = (latestFromUpd && currFromUpd)
      ? updAvailByCompare
      : (typeof upd.updateAvailable === "boolean" ? !!upd.updateAvailable : false);
    const checkedFromUpd = (upd && upd.checkedAt) ? String(upd.checkedAt) : "";
    const earlyMsg = updAvailFromUpd
      ? ("Update available: v" + latestFromUpd)
      : (currFromUpd ? ("Up to date (v" + currFromUpd + ")") : "Update check: ok");
    setUpdateCheckMsg(earlyMsg, checkedFromUpd ? ("Last checked: " + checkedFromUpd) : "");

    let health = null;
    try{
      health = await fetch("/api/health").then(r=>r.json());
    }catch(_e){
      // Non-fatal: keep going using /api/update/check.
      health = null;
    }

    // IMPORTANT (2026-01-07):
    // "Update available" must be based on the UI bundle version, NOT the engine version.
    // The system supports decoupled versioning (UI can advance while engine is pinned).
    //
    // - /api/update/check reports UI update status (current UI version vs latest UI version).
    // - /api/health reports *runtime* state and engine info.
    //
    // Previously we accidentally preferred health.version when present, which caused
    // false "Update available" signals whenever the engine version (e.g. v0.2.97)
    // differed from the UI version (e.g. v0.3.08).
    const engineCurrent = ((health && health.version) || "").toString().trim();
    // UI versions must be normalized (see normVer above).
    const uiCurrent = normVer(upd.currentVersion || "");
    const latest = normVer(upd.latestVersion || upd.latest || "");

    // Keep global state in sync with reality.
    // This fixes the “Update available” banner sticking around until the user refreshes.
    // NOTE: state.version + the header pill are ENGINE-authoritative by design.
    if(engineCurrent){
      state.version = engineCurrent;
      setVersionPill(engineCurrent);
    }

    // If the engine version differs from the UI bundle version, we are almost
    // certainly running stale cached JS/CSS. Trigger a one-time hard reload.
    // This prevents "I updated but it still looks old" confusion.
    try{
      let did = autoRefreshDone;
      try{
        did = did || (sessionStorage.getItem("studiob_autorefresh_done") === "1");
      }catch(_e){ /* storage may be disabled */ }

      // If the *UI* version we just learned from /api/update/check differs from
      // the UI bundle version embedded in this JS, we are almost certainly
      // running stale cached JS/CSS. Trigger a one-time hard reload.
      //
      // NOTE: Do NOT compare engineCurrent here; the system supports decoupled
      // versioning and the engine can be pinned to an older version by design.
      if(!did && uiCurrent && UI_BUILD_VERSION && String(uiCurrent) !== String(UI_BUILD_VERSION)){
        autoRefreshDone = true;
        try{ sessionStorage.setItem("studiob_autorefresh_done", "1"); }catch(_e){ /* ignore */ }
        setStatus(`New UI v${uiCurrent} detected (bundle v${UI_BUILD_VERSION}). Refreshing…`);
        // IMPORTANT: do NOT return early. Some browsers disable storage and/or
        // block the reload, which used to leave the page stuck showing
        // "Update check failed" even though /api/update/check was healthy.
        setTimeout(hardReload, 600);
      }
    }catch(_e){ /* ignore */ }
    if(health && health.mode){
      state.mode = health.mode;
      setModePill(health.mode);
    }
    // Update availability is UI-version-based (uiCurrent vs latest).
    //
    // IMPORTANT (2026-01-07):
    // Do NOT trust an older engine's `updateAvailable` boolean.
    // Some older engines computed it using raw string compares or stale state,
    // which can produce false positives like "Update available" forever.
    //
    // If we don't have BOTH versions, we treat availability as "unknown" and
    // *do not* claim an update exists.
    const uiAvail = !!(latest && uiCurrent && uiCurrent !== latest);

    state.update.ok = !!(upd && (upd.ok === true || uiCurrent || latest || typeof upd.updateAvailable === "boolean"));
    state.update.available = uiAvail;
    state.update.current = uiCurrent;
    state.update.latest = latest;

    const btn = document.getElementById("btnUpdate");
    const up = document.getElementById("updatePill");
    if(state.update.available){
      // Update is available: make it obvious.
      if(up){
        up.classList.add("flash");
        up.classList.remove("pill--muted");
        up.classList.add("pill--warn");
        // Keep the header label short (it's a topbar pill), but make the tooltip explicit.
        up.textContent = "Update";
        up.title = "Update available: v" + latest;
      }
      if(btn){
        btn.classList.add("flash");
        btn.textContent = "Update to v" + latest;
        btn.title = "Update available: v" + latest;
      }
    }else{
      // No update: keep the pill visible as a shortcut to Engineering,
      // but make its tooltip truthful.
      if(up){
        up.classList.remove("flash");
        up.classList.remove("pill--warn");
        up.classList.add("pill--muted");
        up.textContent = "Update";
        up.title = uiCurrent ? ("Up to date (v" + uiCurrent + ")") : "Check for updates";
      }
      if(btn){
        btn.classList.remove("flash");
        btn.textContent = "Update";
        btn.title = uiCurrent ? ("Up to date (v" + uiCurrent + ")") : "No updates available";
      }
    }
    // Surface update-check diagnostics on Engineering page.
    // This is intentionally operator-friendly: if the update pill never shows, this tells us WHY.
    // Compute a human-friendly message and store it on state so it can't get stuck
    // in an old "failed" state.
    let msg = "";
    let title = "";

      // IMPORTANT: "Update check" is NOT the same thing as "Update available".
      // - The check can succeed and still have *no* update available (latest == current).
      // - The check can be "disabled" (repo not configured) without being a system failure.
      // This message should be operator-friendly and never falsely scream "failed".

      // Treat the check as "ok" if the engine explicitly says so OR if it
      // returns the expected fields. This prevents a sticky false-negative UI
      // if an older engine omits the boolean.
      const ok = !!(upd && (upd.ok === true || upd.currentVersion || upd.latestVersion || typeof upd.updateAvailable === "boolean"));
      const notes = (upd && (upd.notes || "")) ? String(upd.notes) : "";
      const checked = (upd && upd.checkedAt) ? String(upd.checkedAt) : "";

      if(ok){
        if(state.update.available){
          msg = "Update available: v" + latest;
        }else if(uiCurrent){
          msg = "Up to date (v" + uiCurrent + ")";
        }else{
          msg = "Update check: ok";
        }
        title = checked ? ("Last checked: " + checked) : "";
      }else{
        // If the engine returns a clear reason (like "not configured"), show that as a
        // non-fatal state instead of "failed".
        const lower = notes.toLowerCase();
        if(lower.includes("not configured") || lower.includes("disabled")){
          msg = "Update check: disabled";
          title = (notes ? notes : "Disabled") + (checked ? ("\nLast checked: " + checked) : "");
        }else if(notes){
          msg = "Update check: failed";
          title = notes + (checked ? ("\nLast checked: " + checked) : "");
        }else{
          // If we have no diagnostic info, keep it short but honest.
          msg = "Update check: failed";
          title = checked ? ("Last checked: " + checked) : "No details";
        }
      }

    // Overwrite any previous "failed" message with the latest known-good result.
    state.update.lastErr = "";
    setUpdateCheckMsg(msg, title);

    // If an update was initiated and the version changed, proactively refresh the page.
    // This ensures the UI JS/CSS bundle always matches the running engine.
    if(state.update && state.update.inProgress && state.update.startVersion && uiCurrent && uiCurrent !== state.update.startVersion){
      state.update.inProgress = false;
      setStatus("Update applied (v" + uiCurrent + "). Refreshing…");
      setTimeout(hardReload, 800);
    }
  }catch(e){
    // ignore; no spam
    const btn = document.getElementById("btnUpdate");
    const up = document.getElementById("updatePill");
    if(up){ up.classList.add("hidden"); up.classList.remove("flash"); }
    if(btn){
      btn.classList.remove("flash");
      btn.textContent = "Update";
      btn.title = "Update check failed";
    }

    // Separation of concerns:
    // - Admin action status lives in #svcMsg
    // - Update-check status lives in #updateCheckMsg
    // During an update/rollback the service *will* restart, so update-check can briefly
    // fail. That is expected and should not spam "failed" while the operator already
    // sees "Update queued…".
    state.update.lastErr = (e && e.message) ? e.message : "Unknown error";
    if(state.update && state.update.inProgress && state.update.lastMsg){
      // Keep the last known-good message during an in-progress update.
      setUpdateCheckMsg(state.update.lastMsg, state.update.lastTitle);
      return;
    }

    // Don't let a brief hiccup overwrite a recent successful check.
    // If we *do* have a last known-good message, keep showing it.
    // Otherwise, show a non-alarming retry message (the engine can legitimately
    // be restarting during updates / nginx reloads).
    if(state.update.lastMsg){
      setUpdateCheckMsg(state.update.lastMsg, state.update.lastTitle);
    }else{
      setUpdateCheckMsg("Update check: retrying…", state.update.lastErr);
    }
  }
}



// v0.2.38 Watchdog Visibility: UI hooks for health summary and recent events

// v0.2.39 Watchdog restart reason visibility
// Display LAST_RESTART_REASON alongside systemd service status in UI

// v0.2.42 DSP Connection Validation
// UI displays DSP link status: OK / Degraded / Disconnected
// Shows last successful DSP contact time and last error

// v0.2.43 DSP Health History Timeline
// The UI should request and render recent DSP state transitions (JSONL) as a timeline.
// Each entry: time, state, failures, last_error.
// This is visibility-only; do not trigger reconnects automatically.

// v0.2.44 Manual 'Test DSP Now'
// This button triggers a single DSP connectivity test via the engine.
// Disable button while test is in progress.
// Display success/failure result and update DSP health panel.

// v0.2.45 DSP Control Safety Gate
// Before sending any DSP control command, check current DSP health state.
// If state is DISCONNECTED:
//   - Block the control request.
//   - Show an explicit operator warning.
//   - Provide a shortcut to run 'Test DSP Now'.
// Rationale: prevent silent no-op controls when DSP link is down.


// ---------------------------------------------------------------------------
// DSP Mode Transition Warning (v0.2.52)
// ---------------------------------------------------------------------------
async function fetchDSPModeStatus(){
  try{
    const m = await getJSON("/api/dsp/mode");
    state.dspModeStatus = m || state.dspModeStatus;

    // Persisted-vs-runtime clarity wiring (UI v0.3.07)
    // Runtime mode is derived from the engine's DSPModeStatus.
    // Persisted mode comes from the config file reader (admin endpoint).
    const newRuntimeMode = (m && m.mode) ? String(m.mode) : "";
    const newActiveMode = (m && m.activeMode) ? String(m.activeMode) : "";

    state.cfgClarity.runtimeMode = newRuntimeMode;
    state.cfgClarity.runtimeActiveMode = newActiveMode;
    state.cfgClarity.lastUpdatedAt = new Date().toISOString();

    // Runtime event logging (UI v0.3.12)
    // Record runtime mode transitions and whether an override is active.
    const rt = String(newRuntimeMode || "").toLowerCase();
    const pm = String(state.cfgClarity.persistedMode || "").toLowerCase();
    const overrideActive = !!(pm && rt && pm !== rt);

    if(_prev.runtimeMode === null){
      _prev.runtimeMode = rt || "";
      _prev.runtimeOverrideActive = overrideActive;
      if(rt) addRuntimeEvent(`Runtime mode: ${rt.toUpperCase()}`);
      if(overrideActive) addRuntimeEvent(`Runtime override active: persisted ${pm.toUpperCase()} → runtime ${rt.toUpperCase()}`);
    }else{
      if(_prev.runtimeMode !== rt){
        addRuntimeEvent(`Runtime mode changed: ${String(_prev.runtimeMode||"—").toUpperCase()} → ${String(rt||"—").toUpperCase()}`);
        _prev.runtimeMode = rt;
      }
      if(_prev.runtimeOverrideActive !== overrideActive){
        if(overrideActive){
          addRuntimeEvent(`Runtime override active: persisted ${pm.toUpperCase()} → runtime ${rt.toUpperCase()}`);
        }else{
          addRuntimeEvent("Runtime override cleared (persisted matches runtime)");
        }
        _prev.runtimeOverrideActive = overrideActive;
      }
    }
    renderConfigClarity();
    const banner = $("#dspTransitionBanner");
    renderWatchdogDSP();
    setPills();
    const ep = $("#dspBannerEndpoint");
    const age = $("#dspBannerValidatedAge");
    const cfgChg = $("#dspBannerConfigChanged");

    if(ep){
      const host = m.host || "—";
      const port = (typeof m.port === "number") ? m.port : "—";
      ep.textContent = `${host}:${port}`;
    }

    // Compute a human-friendly "age" client-side.
    if(age){
      if(m.validatedAt){
        const t = Date.parse(m.validatedAt);
        if(!Number.isNaN(t)){
          const mins = Math.floor((Date.now() - t) / 60000);
          if(mins < 1) age.textContent = "just now";
          else if(mins === 1) age.textContent = "1 minute ago";
          else age.textContent = `${mins} minutes ago`;
        }else{
          age.textContent = m.validatedAt;
        }
      }else{
        age.textContent = "—";
      }
    }

    if(cfgChg){
      cfgChg.style.display = (m.configChanged ? "inline" : "none");
    }

    // Show banner only when entering LIVE without validation.
    // (Option A: controls remain enabled; this is visibility-only.)
    if(m.mode === "live" && !m.validated){
      banner.style.display = "block";
    }else{
      banner.style.display = "none";
    }
  }catch(e){
    // If unavailable, fail closed (no banner)
  }
}


// ---------------------------------------------------------------------------
// PlayIt Live (PIL) integration (UI v0.3.44)
// ---------------------------------------------------------------------------
// Contract:
// - DSP remains authoritative for DSP controls. PIL is a separate app.
// - We poll PIL for its current mode (automationOn) and reflect that on the UI.
// - We attempt to change mode when the operator presses the AUTO/LIVE button.
//   (We use a best-effort PUT/POST with JSON. If the endpoint differs, we fail
//    gracefully and keep showing the polled truth.)
// - START is a momentary command that triggers playout.
//
// IMPORTANT: PIL is on a different origin and uses a self-signed TLS cert.
// To avoid browser CORS/TLS issues, the **engine** exposes a small same-origin
// proxy under /api/pil/*.
let pilState = { automationOn: null, lastOkMs: 0 };

function pilUrl(path){
  const p = path.startsWith("/") ? path : ("/"+path);
  return `/api/pil${p}`;
}

function setPILModeButtonVisual(btn, automationOn){
  if(!btn) return;
  // Green = AUTO (automation ON). Yellow = LIVE (automation OFF).
  btn.classList.toggle("woBtn--pilAuto", automationOn === true);
  btn.classList.toggle("woBtn--pilLive", automationOn === false);
  btn.disabled = (automationOn !== true && automationOn !== false);
  btn.textContent = (automationOn === true) ? "AUTO" : (automationOn === false) ? "LIVE" : "?";
  btn.title = "PlayIt Live mode (AUTO=automation on, LIVE=automation off)";
}

async function pollPILMode(){
  try{
    const res = await fetch(pilUrl("/playoutMode"), { method:"GET", cache:"no-store" });
    if(!res.ok) throw new Error(`HTTP ${res.status}`);
    const j = await res.json();
    if(typeof j.automationOn !== "boolean") throw new Error("Unexpected JSON");
    pilState.automationOn = j.automationOn;
    pilState.lastOkMs = Date.now();
    const btn = document.querySelector("#pilModeBtn");
    setPILModeButtonVisual(btn, pilState.automationOn);
  }catch(e){
    // Do not spam the runtime log; this can fail due to CORS/certs.
    const btn = document.querySelector("#pilModeBtn");
    setPILModeButtonVisual(btn, null);
  }
}

async function setPILAutomationOn(nextOn){
  // Best-effort write.
  //
  // Observed PlayIt Live Control API (docs screenshot, 2026-01-13):
  //   POST /api/control/liveAssist/playoutMode/toggleAutomation
  //   Body: { "on": <boolean> }
  //
  // Our engine exposes a same-origin proxy:
  //   POST /api/pil/playoutMode/toggleAutomation
  //
  // For backwards compatibility (and unknown PIL builds), we also fall back to
  // the older /api/control/liveAssist/playoutMode write path.
  const payloadToggle = JSON.stringify({ on: !!nextOn });
  const payloadLegacy = JSON.stringify({ automationOn: !!nextOn });
  const opts = (method, body)=>({
    method,
    headers:{ "Content-Type":"application/json" },
    body,
  });

  try{
    // Preferred: explicit toggleAutomation endpoint.
    let res = await fetch(pilUrl("/playoutMode/toggleAutomation"), opts("POST", payloadToggle));
    if(!res.ok){
      // Legacy fallbacks: PUT then POST to /playoutMode
      res = await fetch(pilUrl("/playoutMode"), opts("PUT", payloadLegacy));
      if(!res.ok) res = await fetch(pilUrl("/playoutMode"), opts("POST", payloadLegacy));
    }
    if(!res.ok) throw new Error(`HTTP ${res.status}`);
  }catch(e){
    // ignore; poll will show truth
  }finally{
    // Refresh quickly after a write attempt.
    setTimeout(pollPILMode, 250);
  }
}

async function pilStartPlayout(){
  const btn = document.querySelector("#pilStartBtn");
  if(btn){ btn.disabled = true; setTimeout(()=>{ btn.disabled=false; }, 600); }
  try{
    // Engine proxy expects POST (but also accepts GET for legacy). Use POST.
    await fetch(pilUrl("/play"), { method:"POST" });
  }catch(e){
    // ignore; best-effort momentary command
  }
}

function initPlayItLiveControls(){
  const modeBtn  = document.querySelector("#pilModeBtn");
  const startBtn = document.querySelector("#pilStartBtn");
  if(modeBtn){
    modeBtn.addEventListener("click", ()=>{
      // Toggle based on our last known state; if unknown, force AUTO.
      const nextOn = (pilState.automationOn === false) ? true : false;
      setPILAutomationOn(nextOn);
    });
  }
  if(startBtn){
    startBtn.addEventListener("click", pilStartPlayout);
  }
  // Start polling immediately, then every 2 seconds.
  pollPILMode();
  setInterval(pollPILMode, 2000);
}


// ---------------------------------------------------------------------------
// Latest Donations (UI v0.3.91)
// ---------------------------------------------------------------------------
// The donations card is intentionally empty in index.html (placeholder).
// We inject a simple list at runtime so future layout updates to index.html
// don't have to guess about donation-provider output shapes.
//
// Poll rate: 30s (operator preference).
// NOTE: This is read-only and safe to call even when the DSP is disconnected.
// Poll interval for the Latest Donations card.
// User request (2026-01-13): 60s → 30s.
const DONATIONS_POLL_MS = 30000;

// Minimal HTML escape (defense-in-depth).
// Even though our engine is the one scraping/parsing, we still avoid
// rendering raw strings into innerHTML.
function escapeHTML(s){
  return String(s)
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;');
}

// --- New donation flash logic (UI v0.3.93) ---------------------------------
// Operator request: flash the background of NEW donations yellow for 10 minutes.
//
// Implementation notes:
// - We treat each donation item as immutable content (name+amount+message+time).
// - When we first see a donation, we store its "firstSeen" timestamp in localStorage.
// - For the next 10 minutes, we render that row with a flashing CSS class.
// - This survives page reloads (since localStorage is persisted on the console PC).
//
// IMPORTANT: this is purely a UI affordance; the engine remains source-of-truth
// for donation data.
const DONATION_FLASH_MS = 10 * 60 * 1000; // 10 minutes
const DONATION_SEEN_KEY = 'wlcb_donations_seen_v1';

function donationId(it){
  // Prefer time when available; it makes collisions extremely unlikely.
  // Fall back to content-only if older cached items lack time.
  const t = (it && it.time) ? String(it.time) : '';
  const n = (it && it.name) ? String(it.name) : '';
  const a = (it && typeof it.amount === 'number') ? String(it.amount) : String(it && it.amount || '');
  const m = (it && it.message) ? String(it.message) : '';
  return `${t}|${n}|${a}|${m}`;
}

function loadSeenMap(){
  try{
    const raw = localStorage.getItem(DONATION_SEEN_KEY);
    if(!raw) return {};
    const obj = JSON.parse(raw);
    return (obj && typeof obj === 'object') ? obj : {};
  }catch(_e){
    return {};
  }
}

function saveSeenMap(map){
  try{
    localStorage.setItem(DONATION_SEEN_KEY, JSON.stringify(map||{}));
  }catch(_e){
    // Ignore storage errors; worst case is we don't persist highlight state.
  }
}

function markDonationsSeen(items){
  const now = Date.now();
  const seen = loadSeenMap();
  let changed = false;

  (items||[]).forEach(it=>{
    const id = donationId(it);
    if(!id) return;
    if(!(id in seen)){
      seen[id] = now;
      changed = true
    }
  });

  // Garbage-collect: remove entries older than 24h to prevent unbounded growth.
  const cutoff = now - (24*60*60*1000);
  for(const k in seen){
    if(seen[k] < cutoff){
      delete seen[k];
      changed = true;
    }
  }

  if(changed) saveSeenMap(seen);
  return seen;
}

function isDonationFlashing(it, seenMap){
  const id = donationId(it);
  if(!id || !seenMap || !(id in seenMap)) return false;
  const age = Date.now() - Number(seenMap[id]||0);
  return age >= 0 && age < DONATION_FLASH_MS;
}

function ensureDonationsCardBody(){
  const card = document.querySelector('#donationsCard');
  if(!card) return null;

  // If a previous build injected the body, reuse it.
  let body = card.querySelector('.donationsBody');
  if(body) return body;

  // Hide the empty placeholder (kept for legacy + layout stability).
  const empty = card.querySelector('.mixerCard__emptyBody');
  if(empty) empty.style.display = 'none';

  body = document.createElement('div');
  body.className = 'donationsBody';
  body.innerHTML = `
    <div class="donationsMeta" id="donationsMeta"></div>
    <div class="donationsList" id="donationsList"></div>
  `;
  card.appendChild(body);
  return body;
}

function renderLatestDonations(){
  ensureDonationsCardBody();
  const meta = document.querySelector('#donationsMeta');
  const list = document.querySelector('#donationsList');
  if(!meta || !list) return;

  // Operator request (2026-01-13): remove the "Updated" timestamp and replace
  // it with the fundraiser progress that is shown on the website.
  // Example:
  //   Raised $5,295.85 of $10,000.00
  // We still append STALE and error diagnostics when applicable.
  let summaryText = '';
  const s = state.donations.summary;
  if(s && typeof s.raised === 'number'){
    // Use Intl if available (it is on modern browsers); fallback to fixed.
    try{
      const fmt = new Intl.NumberFormat(undefined, { style: 'currency', currency: (s.currency || 'USD') });
      if(typeof s.goal === 'number' && s.goal > 0){
        summaryText = `Raised ${fmt.format(s.raised)} of ${fmt.format(s.goal)}`;
      }else{
        summaryText = `Raised ${fmt.format(s.raised)}`;
      }
    }catch(_e){
      if(typeof s.goal === 'number' && s.goal > 0){
        summaryText = `Raised $${s.raised.toFixed(2)} of $${s.goal.toFixed(2)}`;
      }else{
        summaryText = `Raised $${s.raised.toFixed(2)}`;
      }
    }
  }

  // Build meta line using simple parts so separators are never doubled.
  const parts = [];
  if(summaryText) parts.push(summaryText);
  if(state.donations.stale) parts.push('STALE');
  if(state.donations.lastErr) parts.push(state.donations.lastErr);
  meta.textContent = parts.join(' · ');

  if(!state.donations.items || state.donations.items.length === 0){
    list.innerHTML = `<div class="donationRow donationRow--empty">No donations to display.</div>`;
    return;
  }

  const rows = state.donations.items.map(it=>{
    const flashing = isDonationFlashing(it, state.donations.seenMap);
    const rowClass = flashing ? 'donationRow donationRow--new' : 'donationRow';
    const amt = (typeof it.amount === 'number') ? it.amount.toFixed(2) : String(it.amount || '');
    const msg = (it.message || '').trim();
    const msgHTML = msg ? `<div class="donationComment">${escapeHTML(msg)}</div>` : '';
    return `
      <div class="${rowClass}">
        <div class="donationLine1"><span class="donationName">${escapeHTML(it.name||'')}</span> - <span class="donationAmt">$${escapeHTML(amt)}</span></div>
        ${msgHTML}
      </div>
    `;
  }).join('');

  list.innerHTML = rows;
}

function fetchLatestDonations(){
  // Best-effort only: donations are informational and must never break UI boot.
  fetch('/api/donations/latest?limit=5', { cache: 'no-store' })
    .then(r=>r.json())
    .then(j=>{
      state.donations.items = Array.isArray(j.items) ? j.items : [];
      // Mark newly-seen donations so we can flash them for 10 minutes.
      trackNewDonations(state.donations.items);
      state.donations.seenMap = markDonationsSeen(state.donations.items);
      // Optional campaign progress (Raised/Goal). Keep it as-is; rendering
      // code performs numeric checks.
      state.donations.summary = j.summary || null;
      state.donations.updatedAt = j.updated_at || '';
      state.donations.stale = !!j.stale;
      state.donations.lastErr = j.error || '';
      renderLatestDonations();
    })
    .catch(e=>{
      state.donations.stale = true;
      state.donations.lastErr = String(e && e.message ? e.message : e);
      renderLatestDonations();
    });
}

document.addEventListener("DOMContentLoaded", ()=>{
  // Runtime event timeline (v0.3.12)
  addRuntimeEvent(`UI loaded (v${UI_BUILD_VERSION})`);

  // v0.3.64: repair known legacy labels when index.html is stale.
  // This is safe, idempotent, and provides an on-screen audit trail.
  repairLegacyStaticLabels();

  // Latest Donations (UI v0.3.91)
  // This is informational only; fetch failures fall back to last-known-good.
  ensureDonationsCardBody();
  fetchLatestDonations();
  setInterval(fetchLatestDonations, DONATIONS_POLL_MS);


  // Mixer hydration (v0.3.30): connect to RC WebSocket and wait for an
  // authoritative snapshot before revealing controls.
  connectRCWebSocket();
  setTimeout(hydrateMixerViaHTTPFallback, 900);

  // PlayIt Live integration (UI v0.3.44)
  initPlayItLiveControls();


  // Mixer fader visuals (v0.3.13)
  // Safe to call even if the Studio page is not visible yet.
  initMixerFaders();
  ensureBottomRowVUMeterFills();

  fetchDSPModeStatus();
  setInterval(fetchDSPModeStatus, 5000);

  // v0.2.65: always-on DSP status visibility
  // The engine maintains a continuous DSP monitor loop; the UI must poll the
  // cached health snapshot so operators can see connectivity changes live.
  fetchDSPHealth();
  setInterval(fetchDSPHealth, 2000);

  const ack = $("#btnDspBannerAck");
  if(ack){
    ack.addEventListener("click", ()=>{
      $("#dspTransitionBanner").style.display = "none";
    });
  }

  const t = $("#btnDspBannerTest");
  if(t){
    t.addEventListener("click", ()=>{
      $("#btnDspTest")?.click();
    });
  }
});


// ---------------------------------------------------------------------------
// Watchdog DSP Summary rendering (v0.2.56)
// Keeps a quick DSP snapshot visible near watchdog so operators don't have to
// switch pages during troubleshooting. Visibility-only.
// ---------------------------------------------------------------------------
function renderWatchdogDSP(){
  const modeEl = $("#wdDspMode");
  if(!modeEl) return; // Engineering page only

  const m = state.dspModeStatus || {};
  const h = state.dspHealth || {};

  modeEl.textContent = (m.mode || "—");
          const am = $("#wdDspActiveMode");
          if(am) am.textContent = (m.activeMode || "—");
  $("#wdDspState").textContent = (h.state || "—");
  $("#wdDspLastTest").textContent = (h.lastTestAt || "—");
  const wlp = $("#wdDspLastPoll");
  if(wlp) wlp.textContent = (h.lastPollAt || "—");
  $("#wdDspFailures").textContent = String(h.failures ?? "—");
  // Last DSP write attempt (v0.2.77) — explicit operator feedback.
  const lwEl = $("#wdDspLastWrite");
  if(lwEl){
    const lw = (m.lastWrite || null);
    if(!lw){
      lwEl.textContent = "—";
    }else{
      const ok = lw.ok ? "OK" : "ERROR";
      const val = (typeof lw.value === "number") ? lw.value : "—";
      const ts = lw.ts || "—";
      const err = lw.error ? ` (${lw.error})` : "";
      lwEl.textContent = `${ts}  ${lw.name}=${val}  ${ok}${err}`;
    }
  }

  // Validation context (LIVE only)
  let vtxt = "—";
  if((m.mode||"").toLowerCase() === "live"){
    if(m.validatedAt){
      // compute minutes ago, same as banner logic but resilient
      const t = Date.parse(m.validatedAt);
      if(!Number.isNaN(t)){
        const mins = Math.floor((Date.now() - t) / 60000);
        if(mins < 1) vtxt = "just now";
        else if(mins === 1) vtxt = "1 minute ago";
        else vtxt = `${mins} minutes ago`;
      }else{
        vtxt = m.validatedAt;
      }
    }else{
      vtxt = "NOT VALIDATED";
    }
  }
  $("#wdDspValidated").textContent = vtxt;

  // Config changed since validation?
  let ctxt = "—";
  if((m.mode||"").toLowerCase() === "live"){
    ctxt = m.configChanged ? "CHANGED ⚠" : "unchanged";
  }
  $("#wdDspCfg").textContent = ctxt;

  // Error details (only when meaningful)
  const errBox = $("#wdDspErr");
  const err = (h.lastError || "").trim();
  if(errBox){
    if(err){
      errBox.style.display = "block";
      errBox.textContent = "Last error: " + err;
    }else{
      errBox.style.display = "none";
      errBox.textContent = "";
    }
  }
}