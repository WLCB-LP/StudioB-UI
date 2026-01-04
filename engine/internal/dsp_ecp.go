package app

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Q-SYS External Control Protocol (ECP) helper (v0.2.76)
//
// StudioB-UI's v0.2.x branch intentionally keeps DSP control conservative.
// We currently use ONLY one ECP write path (Speaker Mute) and only when:
//   - cfg.DSP.Mode == "live"
//   - DSP health is not DISCONNECTED (enforced by DSPControlAllowed)
//
// Why ECP?
// - It's a simple, line-oriented TCP protocol supported by Q-SYS Core.
// - It lets us set a Named Control value using `csv <name> <value>`.
//
// IMPORTANT SAFETY NOTES:
// - We create a short-lived TCP connection per command.
// - We use timeouts for both connect and read/write.
// - We treat any non-"cv" response as an error and return it verbatim
//   so failures remain visible to the operator.
// ---------------------------------------------------------------------------

// ecpSendCSV sets a named control's *value* using the ECP "csv" command.
//
// Example command:
//
//	csv STUB_SPK_MUTE 1\n
//
// Expected success response is a "cv" line, such as:
//
//	cv "STUB_SPK_MUTE" "" 1 1
//
// NOTE: We do not attempt to parse the full cv payload in v0.2.x.
// We only need a reliable success/failure signal.
func (e *Engine) ecpSendCSV(controlName string, value float64, timeout time.Duration) (string, error) {
	cfg := e.GetConfigCopy()
	host := strings.TrimSpace(cfg.DSP.Host)
	port := cfg.DSP.Port
	if host == "" || port == 0 {
		return "", fmt.Errorf("DSP host/port not configured")
	}

	if timeout <= 0 {
		timeout = 1200 * time.Millisecond
	}

	addr := net.JoinHostPort(host, itoa(port))
	c, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return "", err
	}
	defer c.Close()

	// A single deadline covers both the write and the read.
	_ = c.SetDeadline(time.Now().Add(timeout))

	// Q-SYS ECP is line-oriented. We terminate with \n.
	cmd := fmt.Sprintf("csv %s %v\n", controlName, value)
	if _, err := c.Write([]byte(cmd)); err != nil {
		return "", err
	}

	// Read one response line.
	// On success, Q-SYS returns a single "cv ..." line.
	r := bufio.NewReader(c)
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "cv ") {
		return line, nil
	}
	// Anything else is treated as an error (bad_command, not_found, etc.).
	return line, fmt.Errorf("ecp error: %s", line)
}
