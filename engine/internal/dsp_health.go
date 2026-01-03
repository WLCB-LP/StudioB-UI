package app

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
    "net"
    "strings"
    "time"
)

// DSPHealthState is intentionally small and explicit.
// We use it both for operator visibility AND for server-side safety gates.
//
// IMPORTANT:
// - This is NOT background polling. Values change only when we explicitly test
//   connectivity (e.g., operator presses "Test DSP Now") or when future work
//   (explicitly approved) adds additional signals.
type DSPHealthState string

const (
    DSPHealthUnknown      DSPHealthState = "UNKNOWN"
    DSPHealthOK           DSPHealthState = "OK"
    DSPHealthDegraded     DSPHealthState = "DEGRADED"
    DSPHealthDisconnected DSPHealthState = "DISCONNECTED"
)

// DSPHealthSnapshot is the read-only shape returned to the UI.
type DSPHealthSnapshot struct {
    State               DSPHealthState `json:"state"`
    LastOK              string         `json:"lastOk,omitempty"`
    ConsecutiveFailures int            `json:"consecutiveFailures"`
    LastError           string         `json:"lastError,omitempty"`
    LastTestAt          string         `json:"lastTestAt,omitempty"`
}

// dspHealth is stored on Engine and guarded by dspMu.
type dspHealth struct {
    state      DSPHealthState
    lastOK     time.Time
    failures   int
    lastErr    string
    lastTestAt time.Time
}

func (e *Engine) ensureDSPHealthInit() {
    e.dspOnce.Do(func() {
        e.dsp = &dspHealth{state: DSPHealthUnknown}
    })
}

// DSPHealth returns the current snapshot. This is read-only and safe.
func (e *Engine) DSPHealth() DSPHealthSnapshot {
    e.ensureDSPHealthInit()
    e.dspMu.Lock()
    defer e.dspMu.Unlock()

    snap := DSPHealthSnapshot{
        State:               e.dsp.state,
        ConsecutiveFailures: e.dsp.failures,
    }
    if !e.dsp.lastOK.IsZero() {
        snap.LastOK = e.dsp.lastOK.UTC().Format(time.RFC3339)
    }
    if strings.TrimSpace(e.dsp.lastErr) != "" {
        snap.LastError = e.dsp.lastErr
    }
    if !e.dsp.lastTestAt.IsZero() {
        snap.LastTestAt = e.dsp.lastTestAt.UTC().Format(time.RFC3339)
    }
    return snap
}

// TestDSPConnectivity performs a single bounded TCP connect to the configured DSP host/port.
//
// Why TCP connect?
// - It is protocol-agnostic, so we don't risk sending malformed commands.
// - It reliably tells us whether the DSP endpoint is reachable on the network.
//
// This is NOT polling. It runs only when explicitly requested (UI button).
func (e *Engine) TestDSPConnectivity(timeout time.Duration) DSPHealthSnapshot {
    e.ensureDSPHealthInit()
	cfg := e.GetConfigCopy()
	// v0.2.50 mock/simulate bypass:
	// In mock/simulate mode, there is no external DSP to contact.
	// Returning immediately avoids confusing "Testing…" hangs and guarantees
	// we never generate external network traffic in mock workflows.
	mode := strings.ToLower(strings.TrimSpace(cfg.DSP.Mode))
	if mode == "mock" || mode == "simulate" {
		now := time.Now()
		e.dspMu.Lock()
		prev := e.dsp.state
		e.dsp.lastTestAt = now
		e.dsp.state = DSPHealthOK
		e.dsp.lastOK = now
		e.dsp.failures = 0
		e.dsp.lastErr = ""
		if e.dsp.state != prev {
			// Record the state transition for operator visibility.
			e.appendDSPTimelineLocked(now)
		}
		e.dspMu.Unlock()
		return e.DSPHealth()
	}

    host := strings.TrimSpace(cfg.DSP.Host)
    port := cfg.DSP.Port

    // Default conservative timeout if caller passes 0.
    if timeout <= 0 {
        timeout = 1200 * time.Millisecond
    }

    now := time.Now()
    addr := net.JoinHostPort(host, itoa(port))

    // NOTE: we do NOT hold e.dspMu during the network call.
    c, err := net.DialTimeout("tcp", addr, timeout)
    if err == nil {
        _ = c.Close()
    }

    e.dspMu.Lock()
    defer e.dspMu.Unlock()

    e.dsp.lastTestAt = now

    if err == nil {
        e.dsp.state = DSPHealthOK
        e.dsp.lastOK = now
        e.dsp.failures = 0
        e.dsp.lastErr = ""
	// v0.2.52: mark validation time when in LIVE mode
	mode := strings.ToLower(strings.TrimSpace(cfg.DSP.Mode))
	if mode == "live" {
		e.dspValidatedAt = now
		// v0.2.55: capture the DSP config signature used for this validation.
		e.dspValidatedConfigSig = e.dspConfigSignature()
	}
    } else {
        e.dsp.failures++
        e.dsp.lastErr = err.Error()
        // Conservative state machine:
        // - First/second failure: DEGRADED
        // - Third+ consecutive failure: DISCONNECTED
        if e.dsp.failures >= 3 {
            e.dsp.state = DSPHealthDisconnected
        } else {
            e.dsp.state = DSPHealthDegraded
        }
    }

    return e.DSPHealth()
}

// DSPControlAllowed answers: "should we accept an operator RC write?"
//
// Defense-in-depth rationale:
// - UI already blocks control attempts when DISCONNECTED.
// - This server-side check prevents silent no-op controls if UI is stale
//   (cached JS) or a non-UI client calls the API.
func (e *Engine) DSPControlAllowed() (bool, string) {
    e.ensureDSPHealthInit()
    // In simulate mode, there is no external DSP; always allow.
    mode := strings.ToLower(strings.TrimSpace(e.GetConfigCopy().DSP.Mode))
		if mode == "simulate" || mode == "mock" {
        return true, ""
    }

    e.dspMu.Lock()
    defer e.dspMu.Unlock()

    if e.dsp.state == DSPHealthDisconnected {
        return false, "DSP is disconnected (run 'Test DSP Now' to confirm link)"
    }
    return true, ""
}

// itoa is a tiny int->string conversion helper.
// We keep it here to avoid pulling in fmt for hot paths.
func itoa(v int) string {
    if v == 0 {
        return "0"
    }
    neg := v < 0
    if neg {
        v = -v
    }
    buf := make([]byte, 0, 12)
    for v > 0 {
        buf = append(buf, byte('0'+v%10))
        v /= 10
    }
    if neg {
        buf = append(buf, '-')
    }
    for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
        buf[i], buf[j] = buf[j], buf[i]
    }
    return string(buf)
}


// --- DSP timeline persistence (v0.2.48) ---
//
// We persist a small, append-only history so operators can answer:
// "When did the DSP go disconnected?" without digging through journald.
//
// IMPORTANT SAFETY PROPERTIES:
// - The timeline is written ONLY when DSP health STATE CHANGES.
// - The file is bounded (last 200 lines) to avoid unbounded disk growth.
// - This does NOT talk to the DSP. Only TestDSPConnectivity does a TCP connect.
// - If stateDir is unavailable, we fail silently (visibility-only feature).
type dspTimelineEntry struct {
	Time      string        `json:"time"`
	State     DSPHealthState `json:"state"`
	Failures  int           `json:"failures"`
	LastError string        `json:"last_error,omitempty"`
}

func (e *Engine) dspTimelinePath() string {
	if strings.TrimSpace(e.stateDir) == "" {
		return ""
	}
	return filepath.Join(e.stateDir, "dsp_health_timeline.jsonl")
}

func (e *Engine) appendDSPTimelineLocked(now time.Time) {
	// Caller must hold e.dspMu and must have updated e.dsp.* already.
	path := e.dspTimelinePath()
	if path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0755)

	ent := dspTimelineEntry{
		Time:      now.UTC().Format(time.RFC3339),
		State:     e.dsp.state,
		Failures:  e.dsp.failures,
		LastError: e.dsp.lastErr,
	}

	// Append one line (JSONL).
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err == nil {
		enc, _ := json.Marshal(ent)
		_, _ = f.Write(append(enc, '\n'))
		_ = f.Close()
	}

	// Bound the file (best-effort). If this fails, we do not error out.
	e.boundDSPTimeline(path, 200)
}

func (e *Engine) boundDSPTimeline(path string, maxLines int) {
	if maxLines <= 0 {
		return
	}
	// Read all lines (file is intended to be small; max 200).
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		// small safety cap to avoid pathological growth if file was corrupted
		if len(lines) > maxLines*5 {
			break
		}
	}
	if len(lines) <= maxLines {
		return
	}
	lines = lines[len(lines)-maxLines:]

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

func (e *Engine) ReadDSPTimeline(n int) []dspTimelineEntry {
	if n <= 0 {
		n = 50
	}
	path := e.dspTimelinePath()
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	raw := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(raw) > n {
		raw = raw[len(raw)-n:]
	}
	out := make([]dspTimelineEntry, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e2 dspTimelineEntry
		if json.Unmarshal([]byte(line), &e2) == nil {
			out = append(out, e2)
		}
	}
	return out
}
