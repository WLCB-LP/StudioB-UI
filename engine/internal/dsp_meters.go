package app

import (
	"log"
	"math"
	"strconv"
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
	controls := []string{
		// Bottom-row channel VU meters
		"401","402","403","404","405","406","407","408","409","410",
		// Program / speakers / remote return
		"411","412","460","461","462","463",
	}

	cfg2 := e.GetConfigCopy()
	dbFloor := cfg2.Meters.DbFloor
	if dbFloor == 0 {
		dbFloor = -60
	}

	normalize := func(v float64) float64 {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0
		}
		// Heuristics (robust across different controller output types):
		// 1) Already normalized linear 0..1
		if v >= 0 && v <= 1 {
			return v
		}
		// 2) Percent 0..100
		if v >= 0 && v <= 100 {
			return v / 100.0
		}
		// 3) Symetrix 16-bit position 0..65535
		if v >= 0 && v <= 65535 {
			return v / 65535.0
		}
		// 4) dB-style negative values (e.g. -60..0)
		if v < 0 {
			if v <= dbFloor {
				return 0
			}
			if v >= 0 {
				return 1
			}
			// Map [dbFloor..0] -> [0..1]
			return (v - dbFloor) / (0 - dbFloor)
		}
		return 0
	}

	var lastLog int64
	for range t.C {
		// IMPORTANT: We use UDP for meter polling.
		//
		// Field evidence from Studio B:
		//   read tcp ...->10.101.2.2:48631: read: connection reset by peer
		//
		// Symetrix deployments commonly allow multiple UDP pollers but may reset
		// short-lived TCP sessions, especially when Composer is also connected.
		vals, err := e.ecpGetCGUDP(controls, 900*time.Millisecond)
		if err != nil {
			// Truth: if we cannot read meters, publish dead meters.
			e.mu.Lock()
			for _, idStr := range controls {
				id, _ := strconv.Atoi(idStr)
				e.rc[id] = 0
			}
			e.mu.Unlock()
			log.Printf("dsp meter poll failed: %v", err)
			continue
		}

		// Update the authoritative RC cache for ALL configured meter IDs.
		// This is what drives the UI's bottom-row VU meters (401–410) and
		// other meter surfaces.
		e.mu.Lock()
		for _, idStr := range controls {
			id, _ := strconv.Atoi(idStr)
			e.rc[id] = normalize(vals[idStr])
		}
		e.mu.Unlock()

		// Periodic visibility for operators / debugging (every ~5s).
		rawL := vals["462"]
		rawR := vals["463"]
		l := normalize(rawL)
		r := normalize(rawR)
		now := time.Now().Unix()
		if now-lastLog >= 5 {
			lastLog = now
			log.Printf("dsp meters ok: raw(462)=%.3f raw(463)=%.3f -> norm L=%.3f R=%.3f", rawL, rawR, l, r)
		}
	}
}
