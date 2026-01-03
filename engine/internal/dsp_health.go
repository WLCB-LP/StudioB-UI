package app

import (
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

    host := strings.TrimSpace(e.cfg.DSP.Host)
    port := e.cfg.DSP.Port

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
    mode := strings.ToLower(strings.TrimSpace(e.cfg.DSP.Mode))
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
