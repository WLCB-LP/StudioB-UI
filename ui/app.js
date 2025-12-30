const state = { rc: {}, version: "?", connected: false };

function $(sel){ return document.querySelector(sel); }
function $all(sel){ return Array.from(document.querySelectorAll(sel)); }

function setConn(text, ok){
  const el = $("#connStatus");
  el.textContent = text;
  el.style.color = ok ? "var(--ok)" : "var(--muted)";
}

function setMeter(id, v){
  const el = document.getElementById("m"+id);
  if(!el) return;
  const pct = Math.max(0, Math.min(1, Number(v||0))) * 100;
  el.style.width = pct.toFixed(1) + "%";
}

function setLamp560(v){
  const on = Number(v||0) >= 0.5;
  const lamp = $("#lamp560");
  lamp.classList.toggle("on", on);
}

function updateValueDisplays(){
  $all("[data-val-for]").forEach(el=>{
    const id = Number(el.getAttribute("data-val-for"));
    const v = state.rc[id];
    if(v === undefined) el.textContent = "—";
    else el.textContent = (Number(v).toFixed(2));
  });
}

async function postJSON(url, obj, headers={}){
  const res = await fetch(url, {
    method: "POST",
    headers: { "Content-Type":"application/json", ...headers },
    body: JSON.stringify(obj||{})
  });
  if(!res.ok) throw new Error(await res.text());
}

async function refreshEngineInfo(){
  const res = await fetch("/api/health");
  const j = await res.json();
  $("#engineInfo").textContent = JSON.stringify(j, null, 2);
}

function applyDelta(rcDelta){
  for(const [k,v] of Object.entries(rcDelta||{})){
    const id = Number(k);
    state.rc[id] = v;
  }
  setMeter(411, state.rc[411]); setMeter(412, state.rc[412]);
  setMeter(460, state.rc[460]); setMeter(461, state.rc[461]);
  setMeter(462, state.rc[462]); setMeter(463, state.rc[463]);
  setLamp560(state.rc[560]);
  updateValueDisplays();
  syncToggles();
}

function syncToggles(){
  $all(".btn.toggle").forEach(btn=>{
    const id = Number(btn.getAttribute("data-rc"));
    const on = Number(state.rc[id]||0) >= 0.5;
    btn.classList.toggle("on", on);
  });
  $all("input.slider").forEach(sl=>{
    const id = Number(sl.getAttribute("data-rc"));
    if(state.rc[id] !== undefined){
      sl.value = String(state.rc[id]);
    }
  });
}

function connectWS(){
  const ws = new WebSocket((location.protocol === "https:" ? "wss://" : "ws://") + location.host + "/ws");
  ws.onopen = ()=>{ state.connected = true; setConn("Connected", true); };
  ws.onclose = ()=>{ state.connected = false; setConn("Disconnected (retrying…)", false); setTimeout(connectWS, 800); };
  ws.onerror = ()=>{};
  ws.onmessage = (ev)=>{
    try{
      const msg = JSON.parse(ev.data);
      if(msg.type === "snapshot"){
        const snap = msg.data;
        state.version = snap.version;
        applyDelta(snap.rc);
        refreshEngineInfo().catch(()=>{});
      } else if(msg.type === "delta"){
        applyDelta(msg.rc);
      }
    }catch(e){}
  };
}

function wireUI(){
  // tabs
  $all(".tab").forEach(t=>{
    t.addEventListener("click", ()=>{
      $all(".tab").forEach(x=>x.classList.remove("active"));
      t.classList.add("active");
      const page = t.getAttribute("data-page");
      $("#page-studio").classList.toggle("hidden", page !== "studio");
      $("#page-engineering").classList.toggle("hidden", page !== "engineering");
    });
  });

  // toggles
  $all(".btn.toggle").forEach(btn=>{
    btn.addEventListener("click", async ()=>{
      const id = Number(btn.getAttribute("data-rc"));
      const next = (Number(state.rc[id]||0) >= 0.5) ? 0 : 1;
      try{
        await postJSON("/api/rc/"+id, { value: next });
        // optimistic
        state.rc[id] = next;
        syncToggles();
      }catch(e){
        alert("Failed: " + e.message);
      }
    });
  });

  // sliders
  $all("input.slider").forEach(sl=>{
    let t = null;
    sl.addEventListener("input", ()=>{
      const id = Number(sl.getAttribute("data-rc"));
      const v = Number(sl.value);
      state.rc[id] = v;
      updateValueDisplays();
      if(t) clearTimeout(t);
      t = setTimeout(async ()=>{
        try{
          await postJSON("/api/rc/"+id, { value: v });
        }catch(e){
          $("#svcMsg").textContent = "Failed to set RC " + id + ": " + e.message;
        }
      }, 80);
    });
  });

  $("#btnReconnect").addEventListener("click", async ()=>{
    try{
      await fetch("/api/reconnect", { method:"POST" });
      $("#svcMsg").textContent = "Reconnect requested.";
    }catch(e){
      $("#svcMsg").textContent = "Reconnect failed.";
    }
  });

  // update/rollback
  $("#btnUpdate").addEventListener("click", async ()=>{
    const pin = $("#adminPin").value.trim();
    if(!pin) return alert("Enter Admin PIN.");
    if(!confirm("Update engine/UI from GitHub on the VM?")) return;
    try{
      await postJSON("/api/admin/update", {}, {"X-Admin-PIN": pin});
      $("#svcMsg").textContent = "Update started. Page will recover when service restarts.";
      setTimeout(()=>location.reload(), 2500);
    }catch(e){
      $("#svcMsg").textContent = "Update failed: " + e.message;
    }
  });

  $("#btnRollback").addEventListener("click", async ()=>{
    const pin = $("#adminPin").value.trim();
    if(!pin) return alert("Enter Admin PIN.");
    const ver = $("#releaseSelect").value;
    if(!ver) return alert("Select a release/tag first.");
    if(!confirm("Rollback to " + ver + " ?")) return;
    try{
      await postJSON("/api/admin/rollback", {version: ver}, {"X-Admin-PIN": pin});
      $("#svcMsg").textContent = "Rollback started. Page will recover when service restarts.";
      setTimeout(()=>location.reload(), 2500);
    }catch(e){
      $("#svcMsg").textContent = "Rollback failed: " + e.message;
    }
  });

  $("#adminPin").addEventListener("change", loadReleases);
}

async function loadReleases(){
  const pin = $("#adminPin").value.trim();
  if(!pin) return;
  try{
    const res = await fetch("/api/admin/releases", { headers: {"X-Admin-PIN": pin}});
    if(!res.ok) throw new Error(await res.text());
    const vers = await res.json();
    const sel = $("#releaseSelect");
    sel.innerHTML = "";
    vers.forEach(v=>{
      const o = document.createElement("option");
      o.value = v; o.textContent = v;
      sel.appendChild(o);
    });
  }catch(e){
    $("#svcMsg").textContent = "Can't load releases: " + e.message;
  }
}

wireUI();
setConn("Connecting…", false);
connectWS();
refreshEngineInfo().catch(()=>{});
