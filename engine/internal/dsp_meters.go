package app

import (
	"log"
	"math"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// DSP meter polling (v0.3.72)
//
// The UI consumes *truth* via WebSocket. Publishing a meters stream is
// necessary but insufficient unless the engine also populates RC 462/463 from
// the DSP in live mode.
//
// In earlier releases, the engine only performed DSP health checks and wrote a
// small subset of controls, but it did not read meter controls. This caused the
// UI's PlayIt Live meters to appear stuck at 0 in live mode.
//
// Minimal MVP:
//   - Poll STUB_RSR_L + STUB_RSR_R using ECP "cg".
//   - Store values into e.rc[462]/e.rc[463] (normalized 0.0–1.0).
//   - If polling fails, force both meters to 0 so they visibly go dead.
// ---------------------------------------------------------------------------

func (e *Engine) dspMetersPollLoop() {
	cfg := e.GetConfigCopy()
	if strings.ToLower(strings.TrimSpace(cfg.DSP.Mode)) != "live" {
		return
	}

	hz := cfg.Meters.DspPollHz
	if hz <= 0 {
		hz = 10
	}
	if hz > 50 {
		// Safety cap: avoid hammering the DSP.
		hz = 50
	}

	t := time.NewTicker(time.Second / time.Duration(hz))
	defer t.Stop()

	controls := []string{"STUB_RSR_L", "STUB_RSR_R"}

	// Normalization strategy (v0.3.73)
	// ------------------------------
	// We need to publish normalized 0.0–1.0 values to the UI, but Q-SYS meter
	// controls can be configured to output in different units:
	//   - normalized linear 0..1
	//   - percent 0..100
	//   - dBFS (negative numbers, where 0 is full-scale)
	//
	// We use a simple heuristic to convert to 0..1:
	//   1) If 0 <= v <= 1: treat as already normalized.
	//   2) If 1 < v <= 100: treat as percent (v/100).
	//   3) Otherwise: treat as dBFS and map [dbFloor..0] -> [0..1].
	//
	// dbFloor is configurable via cfg.Meters.DbFloor (defaults to -60).
	normalize := func(v float64) float64 {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0
		}
		// (1) already normalized
		if v >= 0 && v <= 1 {
			return v
		}
		// (2) percent
		if v > 1 && v <= 100 {
			return v / 100.0
		}
		// (3) dBFS
		dbFloor := cfg.Meters.DbFloor
		if dbFloor == 0 {
			dbFloor = -60
		}
		if v >= 0 {
			// Defensive: some controls may report 0..X where X is not percent.
			// Cap at 1 rather than exploding.
			return 1
		}
		if v <= dbFloor {
			return 0
		}
		// Linear mapping in dB space (UI-friendly & predictable).
		n := (v - dbFloor) / (0 - dbFloor)
		if n < 0 {
			n = 0
		}
		if n > 1 {
			n = 1
		}
		return n
	}

	for range t.C {
		vals, err := e.ecpGetCG(controls, 900*time.Millisecond)
		if err != nil {
			// Make failure visible in the UI: meters go dead.
			e.mu.Lock()
			e.rc[462] = 0
			e.rc[463] = 0
			e.mu.Unlock()
			log.Printf("dsp meter poll failed: %v", err)
			continue
		}

		l := normalize(vals["STUB_RSR_L"])
		r := normalize(vals["STUB_RSR_R"])

		e.mu.Lock()
		e.rc[462] = l
		e.rc[463] = r
		e.mu.Unlock()
	}
}
