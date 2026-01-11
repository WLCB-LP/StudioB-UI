package app

import (
	"log"
	"math"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// DSP control state sync (v0.3.82)
//
// The UI should reflect DSP truth on page load.
//
// Meters are continuously polled (dspMetersPollLoop), but controls such as
// fader levels and mutes may be changed by Composer or other tools.
//
// Minimal approach:
//   - On engine startup (LIVE mode), read the current controller positions for
//     the critical control RCs and seed the authoritative cache.
//   - We do NOT run a periodic resync by default (can be added later if drift
//     becomes a real operational issue).
//
// RCs synced here:
//   - Bottom-row faders: 101..110
//   - Bottom-row mutes:  121..130
//   - Speakers fader:    160
//   - Speakers automute: 560 (indicator)
//
// NOTE:
// Symetrix controller positions are typically 0..65535. We normalize to 0..1
// for the UI contract.
// ---------------------------------------------------------------------------

func (e *Engine) dspControlStateSyncOnce() {
	cfg := e.GetConfigCopy()
	if strings.ToLower(strings.TrimSpace(cfg.DSP.Mode)) != "live" {
		return
	}

	controls := []string{
		"101", "102", "103", "104", "105", "106", "107", "108", "109", "110",
		"121", "122", "123", "124", "125", "126", "127", "128", "129", "130",
		"160", "560",
	}

	vals, err := e.ecpGetCGUDP(controls, 900*time.Millisecond)
	if err != nil {
		log.Printf("dsp control sync failed: %v", err)
		return
	}

	normalize := func(v float64) float64 {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0
		}
		// Already normalized.
		if v >= 0 && v <= 1 {
			return v
		}
		// 16-bit controller position.
		if v >= 0 && v <= 65535 {
			return v / 65535.0
		}
		// Fallback: treat any non-zero as "on" for indicators.
		if v != 0 {
			return 1
		}
		return 0
	}

	e.mu.Lock()
	for _, idStr := range controls {
		id, _ := strconv.Atoi(idStr)
		e.rc[id] = normalize(vals[idStr])
	}
	e.mu.Unlock()

	log.Printf("dsp control sync ok: seeded %d controllers", len(controls))
}

// dspControlReadNow performs a best-effort read of the given controller IDs from
// the DSP and seeds the authoritative RC cache.
//
// v0.3.83 rationale:
// We attempted a one-time sync at engine startup, but operators often start/stop
// the UI at different times than the engine. Doing a best-effort read right
// before sending the WebSocket snapshot ensures the first render reflects DSP
// truth (when available) without requiring the browser to poll.
func (e *Engine) dspControlReadNow(ids []int, timeout time.Duration) {
	cfg := e.GetConfigCopy()
	if strings.ToLower(strings.TrimSpace(cfg.DSP.Mode)) != "live" {
		return
	}
	if len(ids) == 0 {
		return
	}
	if timeout <= 0 {
		timeout = 900 * time.Millisecond
	}

	controls := make([]string, 0, len(ids))
	for _, id := range ids {
		controls = append(controls, strconv.Itoa(id))
	}

	vals, err := e.ecpGetCGUDP(controls, timeout)
	if err != nil {
		log.Printf("dsp control read failed: %v", err)
		return
	}

	normalize := func(v float64) float64 {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0
		}
		if v >= 0 && v <= 1 {
			return v
		}
		if v >= 0 && v <= 65535 {
			return v / 65535.0
		}
		if v != 0 {
			return 1
		}
		return 0
	}

	e.mu.Lock()
	for _, id := range ids {
		k := strconv.Itoa(id)
		if raw, ok := vals[k]; ok {
			e.rc[id] = normalize(raw)
		}
	}
	e.mu.Unlock()
}
