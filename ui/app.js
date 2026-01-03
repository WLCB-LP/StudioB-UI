// StudioB-UI (Studio page) – stable contract polling + named RC control
const POLL_MS = 250;

// UI_BUILD_VERSION MUST match VERSION for this release.
// This is used to detect "new engine / old UI" mismatches caused by browser caching.
// If the engine version differs, we trigger a one-time hardReload() to pull the
// new cache-busted assets.
const UI_BUILD_VERSION="0.2.37";

// One-time auto-refresh guard. We *try* to use sessionStorage so a refresh
// survives a reload, but we also keep an in-memory flag so browsers with
// disabled storage won't get stuck in a refresh loop.
let autoRefreshDone = false;

const state = {
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

function $(sel){ return document.querySelector(sel); }
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
  $("#verPill").textContent = "v" + (state.version || "—");
  $("#modePill").textContent = "mode: " + (state.mode || "—");
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

async function fetchJSON(url, opts={}, timeoutMs=500){
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
  await fetch("/api/rc/" + encodeURIComponent(name), {
    method: "POST",
    headers: { "Content-Type":"application/json" },
    body: JSON.stringify({ value: Number(value) })
  }).then(async res=>{
    if(!res.ok) throw new Error(await res.text());
  });
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
    const j = await fetchJSON("/api/studio/status", {}, 500);
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
        try{ await postRC(rc, next ? 1 : 0); }catch(e){}
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
  $("#btnCfgLoad").addEventListener("click", async ()=>{
    const pin = $("#adminPin").value.trim();
    if(!pin) return alert("Enter Admin PIN.");
    $("#cfgMsg").textContent = "Loading…";
    try{
      const resp = await fetchJSON("/api/admin/config/file", { headers: {"X-Admin-PIN": pin} }, 1200);
      if(resp && resp.config){
        $("#cfgMode").value = (resp.config.mode || "mock");
        $("#cfgDspIp").value = (resp.config.dsp && resp.config.dsp.ip) ? resp.config.dsp.ip : "";
        $("#cfgDspPort").value = (resp.config.dsp && resp.config.dsp.port) ? resp.config.dsp.port : "";
      }
      const path = resp.path || "~/.StudioB-UI/config.json";
      const exists = resp.exists ? "exists" : "missing";
      $("#cfgMsg").textContent = "Loaded (" + exists + "): " + path;
      if(resp.error){ $("#cfgMsg").textContent += " — WARNING: " + resp.error; }
    }catch(e){
      $("#cfgMsg").textContent = "Load failed: " + e.message;
    }
  });

  $("#btnCfgSave").addEventListener("click", async ()=>{
    const pin = $("#adminPin").value.trim();
    if(!pin) return alert("Enter Admin PIN.");
    const body = {
      mode: $("#cfgMode").value,
      dsp: {
        ip: $("#cfgDspIp").value.trim(),
        port: parseInt($("#cfgDspPort").value, 10) || 0
      }
    };
    $("#cfgMsg").textContent = "Saving…";
    try{
      await fetch("/api/admin/config/file", {
        method: "PUT",
        headers: { "Content-Type":"application/json", "X-Admin-PIN": pin },
        body: JSON.stringify(body)
      }).then(async r=>{ if(!r.ok) throw new Error(await r.text()); });
      $("#cfgMsg").textContent = "Saved. Reloading effective config…";
      // Refresh /api/config view (and mode pill) immediately.
      await loadConfigPill();
      $("#cfgMsg").textContent = "Saved and applied.";
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
        // Update complete. Tell the operator explicitly and provide a refresh button.
        setSvcStatus("ok", `Update complete. Engine is now ${v}. Refresh required to load the new UI.`);
        showRefreshButton();
        state.update = state.update || {};
        state.update.inProgress = false;
        // Re-enable admin controls (operator can refresh at their convenience).
        const bu = $("#btnUpdate"); if(bu) bu.disabled = false;
        const br = $("#btnRollback"); if(br) br.disabled = false;
        return;
      }

      // If we don't know the expected version, reload on any version change.
      if(!expectedVersion && before && v && v !== before){
        // Update complete. Tell the operator explicitly and provide a refresh button.
        setSvcStatus("ok", `Update complete. Engine is now ${v}. Refresh required to load the new UI.`);
        showRefreshButton();
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
