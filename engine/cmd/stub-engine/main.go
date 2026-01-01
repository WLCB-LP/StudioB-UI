package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"strings"

	app "stub-mixer/internal"
)

var version = "dev"

func main() {
	var cfgPath string
	flag.StringVar(&cfgPath, "config", "config.yml", "Path to config.yml")
	flag.Parse()

	cfg, err := app.LoadConfig(cfgPath)
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	engine := app.NewEngine(cfg, version)

	mux := http.NewServeMux()

	// Health
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"version": engine.Version(),
			"time":    time.Now().UTC().Format(time.RFC3339),
			"mode":    cfg.DSP.Mode,
		})
	})

	// Version (stable, explicit)
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": engine.Version(),
			"time":    time.Now().UTC().Format(time.RFC3339),
			"mode":    cfg.DSP.Mode,
		})
	})

	// Latest available version (git tags via engine update checker)

	// Config (read-only; safe subset). Useful for debugging mode + DSP connection config.
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET required", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": engine.Version(),
			"time":    time.Now().UTC().Format(time.RFC3339),
			"mode":    cfg.DSP.Mode,
			"dsp": map[string]any{
				"ip":   cfg.DSP.Host,
				"port": cfg.DSP.Port,
			},
			"sources": cfg.Meta,
		})
	})

	// Admin config file editor (Engineering page).
	// This edits ONLY ~/.StudioB-UI/config.json (outside of repo/releases) so upgrades do not overwrite settings.
	mux.HandleFunc("/api/admin/config/file", func(w http.ResponseWriter, r *http.Request) {
		if !engine.CheckAdmin(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
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
				http.Error(w, "bad json", http.StatusBadRequest)
				return
			}
			p, err := app.WriteEditableConfig(body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			// Hot-reload so operator sees immediate effect in /api/config.
			if err := engine.ReloadConfig(); err != nil {
				// File saved, but reload failed. Return 500 with details so operator can act.
				http.Error(w, "config saved to "+p+" but reload failed: "+err.Error(), http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":   true,
				"path": p,
			})
			return

		default:
			http.Error(w, "GET or PUT required", http.StatusMethodNotAllowed)
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
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		if !engine.CheckAdmin(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		go engine.Update()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
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
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		idStr := r.URL.Path[len("/api/rc/"):]
		var body struct {
			Value float64 `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if err := engine.SetRC(idStr, body.Value); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Operator-safe reconnect
	mux.HandleFunc("/api/reconnect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
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
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		if !engine.CheckAdmin(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		go engine.Update()
		w.WriteHeader(http.StatusAccepted)
	})

	mux.HandleFunc("/api/admin/releases", func(w http.ResponseWriter, r *http.Request) {
		if !engine.CheckAdmin(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(engine.ListReleases())
	})

	mux.HandleFunc("/api/admin/rollback", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		if !engine.CheckAdmin(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var body struct {
			Version string `json:"version"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Version == "" {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		go engine.Rollback(body.Version)
		w.WriteHeader(http.StatusAccepted)
	})

	// Watchdog status (read-only) + start (admin)
	mux.HandleFunc("/api/watchdog/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET required", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(engine.WatchdogStatusSnapshot())
	})

	mux.HandleFunc("/api/admin/watchdog/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		if !engine.CheckAdmin(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		go engine.StartWatchdog()
		w.WriteHeader(http.StatusAccepted)
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
