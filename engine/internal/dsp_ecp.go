package app

import (
	"bufio"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Symetrix SymNet Composer Control Protocol helper (v0.3.74)
//
// Studio B uses a Symetrix DSP. Symetrix exposes a simple ASCII control
// protocol over TCP/UDP (default port 48631).
//
// In SymNet Composer control protocol:
//   - Controllers are numeric (1..10000)
//   - Set:  CS <controller> <0..65535><CR>
//   - Get:  GS <controller><CR>        -> <0..65535><CR>
//   - Get2: GS2 <controller><CR>       -> <controller> <0..65535><CR>
//
// We use GS2 because it is easier/safer to parse.
//
// Docs:
//   SymNet Composer Control Protocol v2.0 (TCP/UDP port 48631)
//   - CS syntax and ACK/NAK: "CS <CONTROLLER NUMBER> <CONTROLLER POSITION><CR>"
//   - GS/GS2 syntax: "GS <CONTROLLER NUMBER><CR>" / "GS2 <CONTROLLER NUMBER><CR>"

//
// Safety properties (kept from the earlier design):
//   - short-lived TCP connection per operation
//   - strict timeouts
//   - failures are returned verbatim so operators can see what happened
// ---------------------------------------------------------------------------

// resolveControllerID accepts either:
//   - a raw numeric string ("462")
//   - an RC name ("STUB_SPK_MUTE") if present in rcNameToID
func (e *Engine) resolveControllerID(nameOrID string) (int, error) {
	s := strings.TrimSpace(nameOrID)
	if s == "" {
		return 0, fmt.Errorf("empty controller identifier")
	}
	if id, err := strconv.Atoi(s); err == nil {
		return id, nil
	}
	if id, ok := rcNameToID[s]; ok {
		return id, nil
	}
	return 0, fmt.Errorf("unknown controller: %q", s)
}

// symOpenConn opens a bounded connection to the DSP control port.
//
// Symetrix installs vary: some reliably accept the SymNet ASCII control protocol
// over TCP, others (especially when already connected to Composer) will accept
// UDP but aggressively reset/close TCP sessions.
//
// For writes we still prefer TCP (ACK/NAK semantics are clearer).
// For meter polling we deliberately use UDP (see ecpGetCGUDP) to avoid the
// “connection reset by peer” failure mode you observed.
func (e *Engine) symOpenConn(timeout time.Duration) (net.Conn, error) {
	cfg := e.GetConfigCopy()
	host := strings.TrimSpace(cfg.DSP.Host)
	port := cfg.DSP.Port
	if host == "" || port == 0 {
		return nil, fmt.Errorf("DSP host/port not configured")
	}
	if timeout <= 0 {
		timeout = 1200 * time.Millisecond
	}
	addr := net.JoinHostPort(host, itoa(port))

	// Prefer TCP for control writes.
	c, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	_ = c.SetDeadline(time.Now().Add(timeout))
	return c, nil
}

// symOpenUDP opens a bounded UDP "connection" to the DSP control port.
//
// UDP is used for meter polling to avoid TCP session churn and remote resets.
func (e *Engine) symOpenUDP(timeout time.Duration) (net.Conn, error) {
	cfg := e.GetConfigCopy()
	host := strings.TrimSpace(cfg.DSP.Host)
	port := cfg.DSP.Port
	if host == "" || port == 0 {
		return nil, fmt.Errorf("DSP host/port not configured")
	}
	if timeout <= 0 {
		timeout = 1200 * time.Millisecond
	}
	addr := net.JoinHostPort(host, itoa(port))

	c, err := net.DialTimeout("udp", addr, timeout)
	if err != nil {
		return nil, err
	}
	_ = c.SetDeadline(time.Now().Add(timeout))
	return c, nil
}

// symInitPortBestEffort forces quiet mode ON and echo OFF for the duration of
// this TCP session. These are the factory defaults, but we set them defensively
// because parsing becomes ambiguous if the port is in verbose/echo mode.
func (e *Engine) symInitPortBestEffort(rw *bufio.ReadWriter) {
	// Older code attempted to force quiet mode + echo off (SQ/EH commands).
	// In practice, some Symetrix deployments reject these or respond in verbose
	// ways that complicate parsing.
	//
	// For robustness, we *do not* send any session init commands by default.
	// We rely on strict parsing of the GS2 response instead.
	_ = rw
}

// symReadLine reads one response line, accepting either CR or LF as the
// terminator (different DSP firmware/builds vary).
func symReadLine(r *bufio.Reader) (string, error) {
	var b []byte
	for {
		ch, err := r.ReadByte()
		if err != nil {
			return "", err
		}
		if ch == '\r' || ch == '\n' {
			break
		}
		b = append(b, ch)
		// Safety: avoid unbounded growth if the peer is misbehaving.
		if len(b) > 4096 {
			return "", fmt.Errorf("dsp line too long")
		}
	}
	return strings.TrimSpace(string(b)), nil
}

// ecpSendCSV is a legacy-named wrapper used by the engine.
//
// In v0.3.74 it sends a Symetrix "CS" (Controller Set).
//
// value is assumed to be normalized 0.0–1.0 and will be mapped to 0..65535.
// (For booleans, use 0.0 or 1.0.)
func (e *Engine) ecpSendCSV(controlName string, value float64, timeout time.Duration) (string, error) {
	id, err := e.resolveControllerID(controlName)
	if err != nil {
		return "", err
	}

	// Clamp to 0..1 before scaling.
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}
	pos := int(value * 65535.0)
	if pos < 0 {
		pos = 0
	}
	if pos > 65535 {
		pos = 65535
	}

	// IMPORTANT (Studio B): The Symetrix host actively resets TCP sessions under
	// bursty / short-lived polling and control writes.
	//
	// Meter polling was moved to UDP in v0.3.76. In practice we must also send
	// controller writes (CS) via UDP to avoid the same reset-by-peer failure
	// mode when operators drag faders quickly.
	//
	// SymNet ASCII over UDP is connectionless: we may or may not receive an ACK.
	// To preserve the "DSP is source of truth" rule, we perform a *readback*
	// using GS2 after the write and only report success if the DSP reflects the
	// requested position.

	c, err := e.symOpenUDP(timeout)
	if err != nil {
		return "", err
	}
	defer c.Close()

	rw := bufio.NewReadWriter(bufio.NewReader(c), bufio.NewWriter(c))
	cmd := fmt.Sprintf("CS %d %d\r", id, pos)
	if _, err := rw.WriteString(cmd); err != nil {
		return "", err
	}
	if err := rw.Flush(); err != nil {
		return "", err
	}

	// Best-effort: if the DSP replies with an ACK/NAK, respect it.
	// (Many deployments do not ACK UDP writes, so we treat read timeout as OK.)
	if line, err := symReadLine(rw.Reader); err == nil {
		line = strings.TrimSpace(line)
		if line == "NAK" {
			return line, fmt.Errorf("dsp returned NAK")
		}
		// If we see an ACK, continue to readback below.
	}

	// Readback check (truth): GS2 the same controller and confirm it matches.
	// We allow a small tolerance because some Symetrix controls quantize.
	readback, err := e.ecpGetCGUDP([]string{controlName}, timeout)
	if err != nil {
		return "", fmt.Errorf("dsp write readback failed: %w", err)
	}
	got := readback[controlName]
	// Determine the expected scale. If the DSP returns 0..1 or 0..100, compare
	// in that space; otherwise assume raw 0..65535.
	//
	// This mirrors the robust normalization logic used by the meter poll loop.
	expected := float64(pos)
	// If got looks normalized, convert expected accordingly.
	if got >= 0 && got <= 1.2 {
		expected = float64(pos) / 65535.0
	} else if got >= 0 && got <= 120 {
		expected = (float64(pos) / 65535.0) * 100.0
	}
	if math.Abs(got-expected) > 1.5 { // 1.5 units tolerance (covers % + small jitter)
		return "", fmt.Errorf("dsp write verify mismatch: want %.3f got %.3f", expected, got)
	}
	return "OK", nil
}

// ecpGetCG is a legacy-named wrapper used by the meter poll loop.
//
// In v0.3.74 it reads controller positions using Symetrix GS2.
// Returned values are *raw* controller positions (0..65535) as float64.
func (e *Engine) ecpGetCG(controlNames []string, timeout time.Duration) (map[string]float64, error) {
	if len(controlNames) == 0 {
		return nil, fmt.Errorf("no controls provided")
	}
	if timeout <= 0 {
		timeout = 1200 * time.Millisecond
	}

	c, err := e.symOpenConn(timeout)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	rw := bufio.NewReadWriter(bufio.NewReader(c), bufio.NewWriter(c))
	e.symInitPortBestEffort(rw)

	ids := make([]int, 0, len(controlNames))
	for _, s := range controlNames {
		id, err := e.resolveControllerID(s)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
		if _, err := rw.WriteString(fmt.Sprintf("GS2 %d\r", id)); err != nil {
			return nil, err
		}
	}
	if err := rw.Flush(); err != nil {
		return nil, err
	}

	out := make(map[string]float64, len(controlNames))
	for i, id := range ids {
		// In quiet mode, GS2 returns: "<controller> <position><CR>"
		line, err := symReadLine(rw.Reader)
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "NAK" {
			return nil, fmt.Errorf("dsp returned NAK for controller %d", id)
		}

		fields := strings.Fields(line)
		if len(fields) == 1 {
			// Some Symetrix stacks respond with a single token (no spaces). We support:
			//   "12345"          (position-only)
			//   "00410=12345"    (id=value)
			//   "#00410=12345"   (quiet-prefixed id=value)
			pos, err := parseSymPositionToken(fields[0])
			if err != nil {
				return nil, fmt.Errorf("failed to parse controller position from %q: %w", line, err)
			}
			out[strings.TrimSpace(controlNames[i])] = pos
			continue
		}
		if len(fields) < 2 {
			return nil, fmt.Errorf("malformed GS2 response: %q", line)
		}
		// fields[0] may include leading zeros; ignore and trust the order.
		pos, err := parseSymPositionToken(fields[len(fields)-1])
		if err != nil {
			return nil, fmt.Errorf("failed to parse controller position from %q: %w", line, err)
		}
		out[strings.TrimSpace(controlNames[i])] = pos
	}
	return out, nil
}

// parseSymPositionToken extracts a numeric position from a Symetrix response token.
//
// We've observed at least three real-world formats:
//   - "12345"         (position-only)
//   - "00410=12345"   (id=value)
//   - "#00410=12345"  (quiet-prefixed id=value)
//
// The engine only cares about the *value*.
func parseSymPositionToken(tok string) (float64, error) {
	// Trim whitespace and common terminators.
	s := strings.TrimSpace(tok)

	// If an '=' exists, trust the last segment as the value.
	// Example: "#00410=40491" -> "40491"
	if idx := strings.LastIndex(s, "="); idx >= 0 && idx < len(s)-1 {
		s = s[idx+1:]
	}

	// Final trim (in case of weird padding).
	s = strings.TrimSpace(s)
	return strconv.ParseFloat(s, 64)
}

// ecpGetCGUDP reads controller positions using Symetrix GS2 over UDP.
//
// Why this exists:
//   - Studio B’s Symetrix host actively resets TCP sessions when polled rapidly.
//   - UDP avoids that failure mode while remaining standards-compliant for the
//     SymNet ASCII protocol.
//
// Returned values are raw controller positions as float64.
func (e *Engine) ecpGetCGUDP(controlNames []string, timeout time.Duration) (map[string]float64, error) {
	if len(controlNames) == 0 {
		return nil, fmt.Errorf("no controls provided")
	}
	if timeout <= 0 {
		timeout = 1200 * time.Millisecond
	}

	c, err := e.symOpenUDP(timeout)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	rw := bufio.NewReadWriter(bufio.NewReader(c), bufio.NewWriter(c))
	ids := make([]int, 0, len(controlNames))
	for _, s := range controlNames {
		id, err := e.resolveControllerID(s)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
		if _, err := rw.WriteString(fmt.Sprintf("GS2 %d\r", id)); err != nil {
			return nil, err
		}
	}
	if err := rw.Flush(); err != nil {
		return nil, err
	}

	// Symetrix can return extra lines (e.g. "ACK") before the numeric payload,
	// and UDP can deliver controller responses out-of-order. So we collect by
	// controller id instead of assuming one line per requested id.
	wantByID := make(map[int]string, len(ids))
	for i, id := range ids {
		wantByID[id] = strings.TrimSpace(controlNames[i])
	}

	out := make(map[string]float64, len(controlNames))
	got := make(map[int]struct{}, len(ids))
	for len(got) < len(ids) {
		line, err := symReadLine(rw.Reader)
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Ignore benign acknowledgements.
		if strings.EqualFold(line, "ACK") {
			continue
		}
		if strings.EqualFold(line, "NAK") {
			return nil, fmt.Errorf("dsp returned NAK")
		}

		// Parse either "#00410=40491" or "410 40491".
		id, pos, ok := parseSymControllerLine(line)
		if !ok {
			// Unrecognized line; ignore so we don't break polling.
			continue
		}
		name, wanted := wantByID[id]
		if !wanted {
			// Response for a controller we didn't ask for; ignore.
			continue
		}
		out[name] = pos
		got[id] = struct{}{}
	}
	return out, nil
}

// parseSymControllerLine parses a single Symetrix controller response line.
// Supported forms:
//   "#00410=40491" (or float)
//   "410 40491"    (or with extra tokens)
// It returns (id, position, ok).
func parseSymControllerLine(line string) (int, float64, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return 0, 0, false
	}

	// #00410=40491
	if strings.HasPrefix(line, "#") && strings.Contains(line, "=") {
		parts := strings.SplitN(line[1:], "=", 2)
		if len(parts) != 2 {
			return 0, 0, false
		}
		id, err := strconv.Atoi(strings.TrimLeft(parts[0], "0"))
		if err != nil {
			// if id is all zeros, TrimLeft returns ""; handle that.
			if strings.TrimLeft(parts[0], "0") == "" {
				id = 0
			} else {
				return 0, 0, false
			}
		}
		pos, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err != nil {
			return 0, 0, false
		}
		return id, pos, true
	}

	// "410 40491" or "410 ... 40491" (take first as id, last as value)
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0, 0, false
	}
	id, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, false
	}
	pos, err := strconv.ParseFloat(fields[len(fields)-1], 64)
	if err != nil {
		return 0, 0, false
	}
	return id, pos, true
}
