package app

import (
	"log"
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

		l := vals["STUB_RSR_L"]
		r := vals["STUB_RSR_R"]

		e.mu.Lock()
		e.rc[462] = l
		e.rc[463] = r
		e.mu.Unlock()
	}
}
