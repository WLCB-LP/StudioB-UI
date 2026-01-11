package app

import (
	"log"
	"math"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// DSP meter polling (v0.3.74)
//
// Studio B uses a Symetrix DSP. Symetrix meters and other read-only parameters
// are exposed as controller numbers (1..10000) that return a 16-bit position
// (0..65535). Meters typically use a linear 0..65535 range.
//
// Requirement:
//   - Read the PlayIt Live meters from DSP controller IDs 462/463.
//   - Store normalized 0.0–1.0 values into e.rc[462]/e.rc[463].
//   - If reads fail, force both meters to 0 so the UI goes dead (truthful).
//
// Symetrix protocol notes:
//   - Get2: GS2 <controller><CR> -> <controller> <position><CR>
//   - The returned position is 0..65535.
// citeturn5view0
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

	// We read by controller ID. Passing numeric strings lets the shared helper
	// resolve them without needing name mappings.
	controls := []string{"462", "463"}

	normalize := func(pos float64) float64 {
		if math.IsNaN(pos) || math.IsInf(pos, 0) {
			return 0
		}
		// Symetrix positions are integers but we tolerate float.
		if pos <= 0 {
			return 0
		}
		if pos >= 65535 {
			return 1
		}
		return pos / 65535.0
	}

	for range t.C {
		vals, err := e.ecpGetCG(controls, 900*time.Millisecond)
		if err != nil {
			e.mu.Lock()
			e.rc[462] = 0
			e.rc[463] = 0
			e.mu.Unlock()
			log.Printf("dsp meter poll failed: %v", err)
			continue
		}

		l := normalize(vals["462"])
		r := normalize(vals["463"])

		e.mu.Lock()
		e.rc[462] = l
		e.rc[463] = r
		e.mu.Unlock()
	}
}
