package main

import (
	"crypto/subtle"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	app "stub-mixer/internal"
)

// defaultConfigPath returns the canonical location for the operator configuration.
//
// IMPORTANT: This must match where install.sh writes the config file.
// We keep this logic in one place so the UI/engine/install stay in sync.
func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err == nil && strings.TrimSpace(home) != "" {
		return home + "/.StudioB-UI/config/config.v1"
	}
	// Fallback: relative path (mainly for dev)
	return "config.v1"
}

var version = "dev"

func main() {
	var cfgPath string
	flag.StringVar(&cfgPath, "config", defaultConfigPath(), "Path to operator config.v1")
	flag.Parse()

	cfg, err := app.LoadConfig(cfgPath)
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	engine := app.NewEngine(cfg, version, cfgPath)

	mux := http.NewServeMux()

	// Health
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":           true,
			"version":      engine.Version(),
			"time":         time.Now().UTC().Format(time.RFC3339),
			"mode":         strings.ToLower(strings.TrimSpace(engine.GetConfigCopy().DSP.Mode)),
			"dspWriteMode": engine.DSPModeStatus().ActiveMode,
			"desiredWriteMode": strings.ToLower(strings.TrimSpace(engine.GetConfigCopy().DSP.Mode)),
		})
	})

	// Version (stable, explicit)
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": engine.Version(),
			"time":    time.Now().UTC().Format(time.RFC3339),
			"mode":    "mock",
		})
	})

	// Latest available version (git tags via engine update checker)

	// Config (read-only; safe subset). Useful for debugging mode + DSP connection config.
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "GET required")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": engine.Version(),
			"time":    time.Now().UTC().Format(time.RFC3339),
			"mode":    engine.GetConfigCopy().DSP.Mode,
			"dsp": map[string]any{
				"ip":   engine.GetConfigCopy().DSP.Host,
				"port": engine.GetConfigCopy().DSP.Port,
				"mode": engine.GetConfigCopy().DSP.Mode,
			},
			"sources": engine.GetConfigCopy().Meta,
		})
	})

	// Admin config file editor (Engineering page).
	// This edits ONLY ~/.StudioB-UI/config.json (outside of repo/releases) so upgrades do not overwrite settings.
	mux.HandleFunc("/api/admin/config/file", func(w http.ResponseWriter, r *http.Request) {
		if !engine.CheckAdmin(r) {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		w.Header().Set("Content-Type", "application/json")

		switch r.Method {
		case http.MethodGet:
			cfg, exists, raw, err := app.ReadEditableConfig()
			resp := map[string]any{
				"ok":     err == nil,
				"exists": exists,
				"raw":    raw,
				"config": cfg,
			}
			if p, perr := app.ConfigFilePath(); perr == nil {
				resp["path"] = p
			}
			if err != nil {
				resp["error"] = err.Error()
			}
			_ = json.NewEncoder(w).Encode(resp)
			return

		case http.MethodPut:
			var body app.EditableConfig
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				writeAPIError(w, http.StatusBadRequest, "bad json")
				return
			}
			p, err := app.WriteEditableConfig(body)
			if err != nil {
				writeAPIError(w, http.StatusBadRequest, err.Error())
				return
			}
			// Hot-reload so operator sees immediate effect in /api/config.
			if err := engine.ReloadConfigFrom(p); err != nil {
				// File saved, but reload failed. Return 500 with details so operator can act.
				writeAPIError(w, http.StatusInternalServerError, "config saved to "+p+" but reload failed: "+err.Error())
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":   true,
				"path": p,
			})
			return

		default:
			writeAPIError(w, http.StatusMethodNotAllowed, "GET or PUT required")
			return
		}
	})

	mux.HandleFunc("/api/updates/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		info := engine.CheckUpdateCached()
		latest := info.LatestVersion
		if latest != "" && !strings.HasPrefix(latest, "v") {
			latest = "v" + latest
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"latest": latest})
	})

	// Apply latest update (admin PIN required). Uses git/script-backed update flow.
	mux.HandleFunc("/api/updates/apply", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "GET or POST required"})
			return
		}
		if !requireAdminPin(w, r, cfg.Admin.PIN) {
			return
		}
		// Run update synchronously so the UI can display a *real* result.
		// Previous versions fired-and-forgot, which caused the UI to claim success
		// even when the installer failed (e.g., Go build errors).
		outStr, err := engine.UpdateSync()
		resp := map[string]any{"ok": err == nil}
		if err != nil {
			resp["error"] = err.Error()
		}
		// Return a small tail for quick troubleshooting in the browser.
		if len(outStr) > 0 {
			const max = 4000
			if len(outStr) > max {
				resp["outputTail"] = outStr[len(outStr)-max:]
			} else {
				resp["outputTail"] = outStr
			}
		}
		// writeJSON signature is (w, statusCode, payload)
		writeJSON(w, http.StatusOK, resp)
	})

	// Snapshot
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(engine.StateSnapshot())
	})

	// Studio UI status (stable contract)
	mux.HandleFunc("/api/studio/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(engine.StudioStatusSnapshot())
	})

	// Set RC (allowlisted)
	mux.HandleFunc("/api/rc/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		idStr := r.URL.Path[len("/api/rc/"):]
		var body struct {
			Value float64 `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeAPIError(w, http.StatusBadRequest, "bad json")
			return
		}
		// v0.2.46 defense-in-depth: server-side DSP control guard.
		// The UI already blocks control attempts when DISCONNECTED, but we also
		// enforce it here to protect against stale cached JS or non-UI clients.
		if ok, reason := engine.DSPControlAllowed(); !ok {
			writeAPIError(w, http.StatusConflict, reason)
			return
		}
		if err := engine.SetRC(idStr, body.Value); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// -----------------------------------------------------------------------
	// Operator intents (v0.2.75)
	//
	// Phase 1 control plumbing (safe / non-destructive): Speaker Mute.
	//
	// Contract:
	// - UI sends an explicit intent.
	// - Engine logs the intent (timestamped) to ~/.StudioB-UI/state/intents.jsonl.
	// - Engine updates its in-memory RC cache so the UI reflects the new state.
	// - DSP writes remain mocked/blocked in this phase.
	// -----------------------------------------------------------------------
	mux.HandleFunc("/api/intent/speaker/mute", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		var body struct {
			Mute   *bool  `json:"mute"`
			Source string `json:"source"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeAPIError(w, http.StatusBadRequest, "bad json")
			return
		}
		if body.Mute == nil {
			writeAPIError(w, http.StatusBadRequest, "missing field: mute")
			return
		}
		// Defense-in-depth: keep the same DSP control guard used by /api/rc.
		if ok, reason := engine.DSPControlAllowed(); !ok {
			writeAPIError(w, http.StatusConflict, reason)
			return
		}
		src := strings.TrimSpace(body.Source)
		if src == "" {
			src = "ui"
		}
		if err := engine.ApplySpeakerMuteIntent(*body.Mute, src); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// DSP health + manual connectivity test (operator-driven; no polling).

	mux.HandleFunc("/api/dsp/mode", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(engine.DSPModeStatus())
	})

	mux.HandleFunc("/api/dsp/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(engine.DSPHealth())
	})

	mux.HandleFunc("/api/dsp/test", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		// Single-shot test only. Timeout is conservative and fixed here.
		snap := engine.TestDSPConnectivity(1200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snap)
	})

	// Operator-safe reconnect

	mux.HandleFunc("/api/dsp/timeline", func(w http.ResponseWriter, r *http.Request) {
		// Read-only: returns recent DSP health transitions.
		// Query param: ?n=50 (default 50, max 200)
		n := 50
		if v := strings.TrimSpace(r.URL.Query().Get("n")); v != "" {
			if i, err := strconv.Atoi(v); err == nil {
				n = i
			}
		}
		if n > 200 {
			n = 200
		}
		if n < 1 {
			n = 1
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(engine.ReadDSPTimeline(n))
	})

	mux.HandleFunc("/api/reconnect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		engine.Reconnect()
		w.WriteHeader(http.StatusNoContent)
	})

	// Update check (GitHub latest release). No admin PIN required; safe read-only.
	mux.HandleFunc("/api/update/check", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(engine.CheckUpdateCached())
	})

	// Admin update/rollback
	mux.HandleFunc("/api/admin/update", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		if !engine.CheckAdmin(r) {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		go engine.Update()
		w.WriteHeader(http.StatusAccepted)
	})

	mux.HandleFunc("/api/admin/releases", func(w http.ResponseWriter, r *http.Request) {
		if !engine.CheckAdmin(r) {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(engine.ListReleases())
	})

	mux.HandleFunc("/api/admin/rollback", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		if !engine.CheckAdmin(r) {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var body struct {
			Version string `json:"version"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Version == "" {
			writeAPIError(w, http.StatusBadRequest, "bad json")
			return
		}
		go engine.Rollback(body.Version)
		w.WriteHeader(http.StatusAccepted)
	})

	// Watchdog status (read-only) + start (admin)
	mux.HandleFunc("/api/watchdog/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "GET required")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(engine.WatchdogStatusSnapshot())
	})

	mux.HandleFunc("/api/admin/watchdog/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		if !engine.CheckAdmin(r) {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		// Run synchronously so we can return a meaningful success/failure.
		out, err := engine.StartWatchdogSync()
		resp := map[string]any{
			"action": "watchdog-start",
			"output": out,
			"status": engine.WatchdogStatusSnapshot(),
		}
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			resp["ok"] = false
			resp["error"] = err.Error()
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		resp["ok"] = true
		_ = json.NewEncoder(w).Encode(resp)
	})

	// WebSocket stream
	mux.HandleFunc("/ws", engine.HandleWS)

	addr := cfg.UI.HTTPListen
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("stub-engine %s listening on %s", engine.Version(), addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// requireAdminPin is a tiny helper used by a couple of admin-only routes.
// It validates the caller-provided admin PIN against the configured PIN.
//
// IMPORTANT:
//   - The PIN MUST be provided by the caller via the X-Admin-PIN header.
//   - The server does NOT accept the PIN via URL query parameters (those leak
//     too easily via logs and browser history).
//   - We intentionally keep this helper local to main.go to avoid accidental
//     reuse in other packages.
func requireAdminPin(w http.ResponseWriter, r *http.Request, expectedPIN string) bool {
	callerPIN := strings.TrimSpace(r.Header.Get("X-Admin-PIN"))
	if expectedPIN == "" {
		// Misconfiguration: we cannot authorize anything safely.
		writeAPIError(w, http.StatusServiceUnavailable, "admin PIN not configured")
		return false
	}
	// Constant-time compare to avoid trivial timing leaks.
	if subtle.ConstantTimeCompare([]byte(callerPIN), []byte(expectedPIN)) != 1 {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	return true
}

// writeJSON writes a JSON response with a stable Content-Type.
// This keeps client-side parsing predictable (jq, fetch, etc.).
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeAPIError is a convenience wrapper for returning a consistent JSON error
// payload across all API endpoints.
//
// This is important because tools like `jq` expect valid JSON even on failures.
func writeAPIError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{
		"ok":    false,
		"error": msg,
	})
}
