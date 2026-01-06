// StudioB-UI (Studio page) – stable contract polling + named RC control
const POLL_MS = 250;

// UI_BUILD_VERSION MUST match VERSION for this release.
// This is used to detect "new engine / old UI" mismatches caused by browser caching.
// If the engine version differs, we trigger a one-time hardReload() to pull the
// new cache-busted assets.
const UI_BUILD_VERSION = "0.2.99";

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
  // meter smoothing
  meters: {
    pgmL: { cur: 0, tgt: 0 },
    pgmR: { cur: 0, tgt: 0 },
    spkL: { cur: 0, tgt: 0 },
    spkR: { cur: 0, tgt: 0 },
    rsrL: { cur: 0, tgt: 0 },
    rsrR: { cur: 0, tgt: 0 },
  },
  speaker: { level: 0, mute: false, automute: false },
};

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
  $("#verPill").textContent = "v" + (state.version || "—");
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

function setLampAutoMute(on){
  const lamp = $("#lampAutoMute");
  lamp.classList.toggle("on", !!on);
}

function updateSpeakerUI(){
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
  if(state.speaker.automute) note.textContent = "Auto-mute active";
  else if(state.speaker.mute) note.textContent = "Muted";
  else note.textContent = "";
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
    state.dspHealth = {
      state: j.state || "UNKNOWN",
      lastOk: j.lastOk || "",
      failures: Number(j.consecutiveFailures || 0),
      lastError: j.lastError || "",
      lastTestAt: j.lastTestAt || "",
      lastPollAt: j.lastPollAt || "",
      connected: !!j.connected
    };
    renderDSPHealth();
    setPills();
  }catch(e){
    // Health endpoint should be reliable; if not, show unknown.
    state.dspHealth = { state:"UNKNOWN", connected:false, lastOk:"", lastPollAt:"", failures:0, lastError:String(e), lastTestAt:"" };
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
  $("#dspHealthState").textContent = state.dspHealth.state || "—";
  $("#dspHealthLastOk").textContent = state.dspHealth.lastOk || "—";
  $("#dspHealthFails").textContent = String(state.dspHealth.failures ?? "—");
  $("#dspHealthErr").textContent = state.dspHealth.lastError || "—";
  $("#dspHealthLastTest").textContent = state.dspHealth.lastTestAt || "—";
  const lp = $("#dspHealthLastPoll");
  if(lp) lp.textContent = state.dspHealth.lastPollAt || "—";

  // Operator safety message shown when DISCONNECTED.
  const warn = $("#dspControlWarn");
  if((state.dspHealth.state||"").toUpperCase() === "DISCONNECTED"){
    warn.style.display = "block";
    warn.textContent = "DSP is DISCONNECTED. Control writes are blocked to prevent silent failure. Click 'Test DSP Now' to verify link.";
  }else{
    warn.style.display = "none";
    warn.textContent = "";
  }
}

function applyStudioStatus(j){
  state.version = j.version || "—";
  state.mode = j.mode || "—";

  // speaker
  state.speaker.level = clamp01(j?.speaker?.level);
  state.speaker.mute = !!j?.speaker?.mute;
  state.speaker.automute = !!j?.speaker?.automute;

  // meters (targets)
  const m = j?.meters || {};
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
    state.lastOkAt = Date.now();
    applyStudioStatus(j);
  }catch(e){
    // consider disconnected if we haven't had a good poll in > 2s
    if(Date.now() - state.lastOkAt > 2000){
      state.connected = false;
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

  setMeterFill("m_pgmL", state.meters.pgmL.cur);
  setMeterFill("m_pgmR", state.meters.pgmR.cur);
  setMeterFill("m_spkL", state.meters.spkL.cur);
  setMeterFill("m_spkR", state.meters.spkR.cur);
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
    try{ await loadEffectiveConfigIntoForm({ silent: true }); }catch(_e){ /* ignore */ }
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
  $("#btnReconnect").addEventListener("click", async ()=>{
    $("#reconnectMsg").textContent = "Sending…";
    try{
      await fetch("/api/reconnect", { method:"POST" });
      $("#reconnectMsg").textContent = "OK";
      setTimeout(()=>$("#reconnectMsg").textContent="", 1200);
    }catch(e){
      $("#reconnectMsg").textContent = "Failed";
    }
  });

// Manual "Test DSP Now" (single-shot). This is the ONLY place the UI triggers
// DSP network activity, and only on explicit operator request.
$("#btnDspTest").addEventListener("click", async ()=>{
  const b = $("#btnDspTest");
  const msg = $("#dspTestMsg");
  b.disabled = true;
  msg.textContent = "Testing…";
  try{
    const res = await fetch("/api/dsp/test", { method:"POST" });
    const txt = await res.text();
    if(!res.ok) throw new Error(txt);
    // Update snapshot + timeline after test.
    await fetchDSPHealth();
    await fetchDSPTimeline();
    msg.textContent = "OK";
    setTimeout(()=>msg.textContent="", 1200);
  }catch(e){
    msg.textContent = "Failed";
    // Also refresh health/timeline so operator can see the error.
    await fetchDSPHealth();
    await fetchDSPTimeline();
  }finally{
    b.disabled = false;
  }
});


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
      // mic toggles: UI-visible, logic-stubbed (store local visual state)
      const next = !(btn.dataset.on === "1");
      btn.dataset.on = next ? "1" : "0";
      btn.classList.toggle("on", next);
      try{ await postRC(rc, next ? 1 : 0); }catch(e){}
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
  ["#cfgMode", "#cfgDspHost", "#cfgDspPort"].forEach(sel=>{
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
        const mode = (cfg.dsp && cfg.dsp.mode) ? cfg.dsp.mode : (cfg.mode || 'mock');
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
    const currFromUpd = (upd.currentVersion || "").toString().trim();
    const latestFromUpd = (upd.latestVersion || upd.latest || "").toString().trim().replace(/^v/,"");
    const updAvailFromUpd = (typeof upd.updateAvailable === "boolean")
      ? !!upd.updateAvailable
      : !!(latestFromUpd && currFromUpd && latestFromUpd !== currFromUpd);
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

    const current = ((health && health.version) || upd.currentVersion || "").toString().trim();
    const latest = (upd.latestVersion || upd.latest || "").toString().trim().replace(/^v/,"");

    // Keep global state in sync with reality.
    // This fixes the “Update available” banner sticking around until the user refreshes.
    if(current){
      state.version = current;
      setVersionPill(current);
    }

    // If the engine version differs from the UI bundle version, we are almost
    // certainly running stale cached JS/CSS. Trigger a one-time hard reload.
    // This prevents "I updated but it still looks old" confusion.
    try{
      let did = autoRefreshDone;
      try{
        did = did || (sessionStorage.getItem("studiob_autorefresh_done") === "1");
      }catch(_e){ /* storage may be disabled */ }

      if(!did && current && UI_BUILD_VERSION && String(current) !== String(UI_BUILD_VERSION)){
        autoRefreshDone = true;
        try{ sessionStorage.setItem("studiob_autorefresh_done", "1"); }catch(_e){ /* ignore */ }
        setStatus(`New engine v${current} detected (UI v${UI_BUILD_VERSION}). Refreshing…`);
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
    state.update.ok = !!current;
    state.update.available = !!(latest && current && current !== latest);
    state.update.current = current;
    state.update.latest = latest;

    const btn = document.getElementById("btnUpdate");
    const up = document.getElementById("updatePill");
    if(state.update.available){
      if(up){ up.classList.remove("hidden"); up.classList.add("flash"); up.textContent = "Update v" + latest; }
      if(btn){
        btn.classList.add("flash");
        btn.textContent = "Update to v" + latest;
        btn.title = "Update available: v" + latest;
      }
    }else{
      if(up){ up.classList.add("hidden"); up.classList.remove("flash"); }
      if(btn){
        btn.classList.remove("flash");
        btn.textContent = "Update";
        btn.title = "No updates available";
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
        }else if(current){
          msg = "Up to date (v" + current + ")";
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
    if(state.update && state.update.inProgress && state.update.startVersion && current && current !== state.update.startVersion){
      state.update.inProgress = false;
      setStatus("Update applied (v" + current + "). Refreshing…");
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

document.addEventListener("DOMContentLoaded", ()=>{
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
