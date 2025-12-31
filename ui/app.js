// StudioB-UI (Studio page) – stable contract polling + named RC control
const POLL_MS = 250;

const state = {
  connected: false,
  lastOkAt: 0,
  version: "—",
  mode: "—",
  update: { ok:false, available:false, latest:"", checkedAt:"" },
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
    state.update.startVersion = state.version || null;
    // Disable buttons to prevent double-submits.
    $("#btnUpdate").disabled = true;
    $("#btnRollback").disabled = true;
    try{
      await fetch("/api/updates/apply", {
        method:"POST",
        headers: { "Content-Type":"application/json", "X-Admin-PIN": pin },
        body: "{}"
      }).then(async r=>{ if(!r.ok) throw new Error(await r.text()); });
      $("#svcMsg").textContent = expected
        ? `Update queued. Waiting for ${expected}… (page will refresh automatically)`
        : "Update queued. Waiting for the service to restart… (page will refresh automatically)";

      // Start a watchdog that will reload the page once the engine comes back on the new version.
      // (pollUpdate() also watches for a version change and will refresh as soon as it sees one.)
      waitForVersion(expected);
    }catch(e){
      $("#svcMsg").textContent = "Update failed: " + e.message;
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
      $("#svcMsg").textContent = "Rollback started. Page will recover when service restarts.";
      // Reload when the engine comes back (version may change).
      waitForVersion(null);
    }catch(e){
      $("#svcMsg").textContent = "Rollback failed: " + e.message;
    }
  });
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
      // Don't leave the operator stuck. Offer a one-click hard refresh.
      $("#svcMsg").textContent = "Update is still running. If the page doesn’t refresh automatically, click Refresh.";
      const btn = $("#updateBtn");
      if(btn){
        btn.disabled = false;
        btn.textContent = "Refresh";
        btn.onclick = () => hardReload();
      }
      return;
    }

    try{
      // Cache-bust to avoid intermediary caches during restart.
      const h = await fetchJSON(`/api/health?_=${Date.now()}`, {}, 1200);
      const v = h && h.version ? h.version : null;

      // If caller provided an expected version, wait for it.
      if(expectedVersion && v === expectedVersion){
        hardReload();
        return;
      }

      // If we don't know the expected version, reload on any version change.
      if(!expectedVersion && before && v && v !== before){
        hardReload();
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
  await pollUpdate();
  setTimeout(updateLoop, 60000);
}
updateLoop();

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
  try{
    // Prefer /api/health because it reflects the running engine even if the WebSocket
    // hasn't reconnected yet (for example, immediately after an update/restart).
    const health = await fetch("/api/health").then(r=>r.json());
    const latestObj = await fetch("/api/updates/latest").then(r=>r.json());
    const current = (health.version || "").toString().trim();
    const latest = (latestObj.latest || "").toString().trim().replace(/^v/,"");

    // Keep global state in sync with reality.
    // This fixes the “Update available” banner sticking around until the user refreshes.
    if(current){
      state.version = current;
      setVersionPill(current);
    }
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
  }
}


