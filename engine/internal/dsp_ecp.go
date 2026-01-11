package app

import (
	"bufio"
	"fmt"
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
// citeturn5view0
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

// symOpenConn opens a bounded TCP connection to the DSP control port.
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
	c, err := net.DialTimeout("tcp", addr, timeout)
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
	// Quiet mode ON: SQ 1<CR>
	// Echo mode OFF: EH 0<CR>
	// If these fail, we keep going — next reads may still work if the DSP is
	// already in defaults.
	_, _ = rw.WriteString("SQ 1\r")
	_, _ = rw.WriteString("EH 0\r")
	_ = rw.Flush()
	// Drain up to 2 short lines (ACK/NAK/verbose strings) without blocking too long.
	// Deadlines are already set on the connection.
	for i := 0; i < 2; i++ {
		line, err := rw.ReadString('\r')
		if err != nil {
			return
		}
		_ = line
	}
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

	c, err := e.symOpenConn(timeout)
	if err != nil {
		return "", err
	}
	defer c.Close()

	rw := bufio.NewReadWriter(bufio.NewReader(c), bufio.NewWriter(c))
	e.symInitPortBestEffort(rw)

	cmd := fmt.Sprintf("CS %d %d\r", id, pos)
	if _, err := rw.WriteString(cmd); err != nil {
		return "", err
	}
	if err := rw.Flush(); err != nil {
		return "", err
	}

	line, err := rw.ReadString('\r')
	if err != nil {
		return "", err
	}
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "ACK") {
		return line, nil
	}
	return line, fmt.Errorf("dsp error: %s", line)
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
		line, err := rw.ReadString('\r')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "NAK" {
			return nil, fmt.Errorf("dsp returned NAK for controller %d", id)
		}

		fields := strings.Fields(line)
		if len(fields) == 1 {
			// Some systems might respond to GS2 like GS (position only) if GS2 is
			// not supported. Accept that.
			pos, err := strconv.Atoi(fields[0])
			if err != nil {
				return nil, fmt.Errorf("failed to parse controller position from %q: %w", line, err)
			}
			out[strings.TrimSpace(controlNames[i])] = float64(pos)
			continue
		}
		if len(fields) < 2 {
			return nil, fmt.Errorf("malformed GS2 response: %q", line)
		}
		// fields[0] may include leading zeros; ignore and trust the order.
		pos, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil {
			return nil, fmt.Errorf("failed to parse controller position from %q: %w", line, err)
		}
		out[strings.TrimSpace(controlNames[i])] = float64(pos)
	}
	return out, nil
}
