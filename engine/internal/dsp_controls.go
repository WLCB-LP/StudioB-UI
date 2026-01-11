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
