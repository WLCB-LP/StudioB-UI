package main

import (
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	app "stub-mixer/internal"
)

// ---------------------------------------------------------------------
// PlayIt Live (PIL) proxy
// ---------------------------------------------------------------------

// NOTE: We proxy PIL from the engine to avoid browser CORS + self-signed TLS issues.
// This is intended for trusted LAN use.
var pilHTTP = &http.Client{
	Timeout: 4 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
	},
}

const pilBaseURL = "https://10.101.0.101:25433"
const pilAPIKey = "d304db66a0e54826834259273c36e57a"

// defaultConfigPath returns the canonical location for the operator configuration.
//
// IMPORTANT: This must match where install.sh writes the config file.
// We keep this logic in one place so the UI/engine/install stay in sync.
func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err == nil && strings.TrimSpace(home) != "" {
		return home + "/.StudioB-UI/config/config.v1"
	}
	// Fallback: relative path (mainly for dev)
	return "config.v1"
}

var version = "dev"

// processStart is used for operator-visible uptime calculations.
// We keep this in main (not Engine) so it remains correct even if the engine
// object is recreated in the future.
var processStart = time.Now()

// ---------------------------------------------------------------------
// Latest Donations (server-side scrape)
// ---------------------------------------------------------------------
// Requirement (operator):
// - The browser UI must NOT scrape directly (CORS, mixed content, security).
// - The engine should fetch + parse, then expose a stable JSON contract.
// - On failures, return the last-known-good results (with a stale flag).
//
// NOTE:
// Today we scrape a single public WordPress page:
//   https://lakesradio.org/support-wlcb/
//
// In the future, if scraping becomes fragile, this can be replaced with a
// proper JSON feed (preferred long-term) without changing the UI contract.

const donationsSourceURL = "https://lakesradio.org/support-wlcb/"

// GiveWP goal scraping note (important):
//
// The public /support-wlcb/ page *does* include a donor wall we can scrape
// reliably (names, dates, amounts, comments), but the fundraising *goal*
// widget ("Raised ... of ...") is rendered client-side by GiveWP and may not
// appear as plain text in the server-rendered HTML.
//
// To make the goal robust without needing API keys, we also consult GiveWP's
// public form-grid endpoint (WordPress REST):
//   /wp-json/give-api/v2/form-grid
//
// That endpoint returns an HTML fragment (as a JSON string) that includes
// the "of $GOAL" number we need.

const givewpFormGridPath = "/wp-json/give-api/v2/form-grid"

type donationItem struct {
	Name    string  `json:"name"`
	Amount  float64 `json:"amount"`
	Message string  `json:"message"`
	Time    string  `json:"time"` // RFC3339
}

// donationSummary exposes the campaign progress numbers shown on the website
// (e.g. "$5,295.85 of $10,000.00").
//
// IMPORTANT:
//   - The UI should NEVER compute totals itself. The engine remains the single
//     source of truth for what we scraped and how we interpreted it.
//   - This is informational-only operator UX, so it follows the same
//     last-known-good caching rules as the donations list.
type donationSummary struct {
	Raised   float64 `json:"raised"`
	Goal     float64 `json:"goal"`
	Currency string  `json:"currency"` // "USD" for now
}

type donationsResponse struct {
	Source    string           `json:"source"`            // "scrape"
	UpdatedAt string           `json:"updated_at"`        // RFC3339
	Stale     bool             `json:"stale"`             // true when returning cached last-good
	Error     string           `json:"error,omitempty"`   // human-readable scrape/parse error
	Summary   *donationSummary `json:"summary,omitempty"` // optional campaign progress
	Items     []donationItem   `json:"items"`             // newest first
}

// donationsCache holds the last-known-good scrape.
// We keep it process-local (in-memory) because:
// - this is a convenience UI surface
// - the watchdog will restart the engine if it is unhealthy
// - we do not want to write website-derived data to disk without a clear need
type donationsCache struct {
	mu        sync.Mutex
	lastGood  donationsResponse
	hasLast   bool
	lastFetch time.Time
}

var donations = &donationsCache{}

// ---------------------------------------------------------------------
// WLCB Status (high-level station/system status)
// ---------------------------------------------------------------------
// WLCB Status (station/system summary)
// ---------------------------------------------------------------------
// Design contract:
// - Engine is the single source of truth.
// - The UI does NOT probe external services directly.
// - Each check is best-effort and returns (ok/detail).
// - "Peak listeners" is tracked locally by the engine and persisted so it
//   survives service restarts.
//
// Requested checks (v0.4.03):
//   - Internet connectivity
//   - lakesradio.org reachability
//   - Icecast "Transmitter" (mount /STL has >= 1 listener)
//   - Icecast "Stream" (mount /stream exists) + current listeners + local peak

// wlcbStatusCheck is a small, UI-friendly row.
//
// NOTE:
// We keep this intentionally boring and explicit.
// The UI renders a green/red dot for Ok and shows Detail as hover text.
//
// Only Stream uses Listeners/Peak; other checks omit them.
type wlcbStatusCheck struct {
	// Name is the operator-visible label for the row.
	// Keep it short; the UI will ellipsize long labels.
	Name string `json:"name"`

	// Key is a stable identifier for this row.
	//
	// Why this exists:
	// The Recording row label changes continuously (filename/time).
	// The UI can poll recording more frequently than other checks and
	// update the correct row by Key without guessing by name.
	Key string `json:"key,omitempty"`

	// Ok controls whether this check is considered "in alarm".
	// The UI renders a green/red dot for Ok unless DotOverride is set.
	Ok bool `json:"ok"`

	// DotOverride controls the color of the status dot:
	//   "ok"   -> green
	//   "bad"  -> red
	//   "info" -> blue (informational, not an alarm)
	//   "off"  -> grey (disabled/unknown)
	//
	// If empty, the UI falls back to ok/bad derived from Ok.
	DotOverride string `json:"dot,omitempty"`

	// Detail is surfaced as a tooltip for diagnostics (HTTP codes, errors, etc).
	Detail    string `json:"detail"`
	CheckedAt string `json:"checkedAt"`

	// SuppressAlarm prevents the UI from applying the red blinking "alarm"
	// background to this row even when Ok=false.
	//
	// Operator requirement:
	// - Recording status MUST show a red/green dot, but MUST NOT flash the row
	//   background red when not recording. The operator wants the default row
	//   background regardless of recording state.
	SuppressAlarm bool `json:"suppressAlarm,omitempty"`

	// Optional (Stream only)
	Listeners int `json:"listeners,omitempty"`
	Peak      int `json:"peak,omitempty"`
}

type wlcbStatusResponse struct {
	UpdatedAt string `json:"updatedAt"`

	// InternetOk is duplicated at the top level so the UI can easily
	// "grey out" dependent checks when the site is offline.
	InternetOk bool `json:"internetOk"`

	Stale  bool              `json:"stale"`
	Error  string            `json:"error,omitempty"`
	Checks []wlcbStatusCheck `json:"checks"`
}

type wlcbStatusCache struct {
	mu        sync.Mutex
	lastGood  wlcbStatusResponse
	hasLast   bool
	lastFetch time.Time
}

var wlcbStatus = &wlcbStatusCache{}

// A small, conservative HTTP client for station health checks.
var wlcbHTTP = &http.Client{Timeout: 3 * time.Second}

// --- Peak persistence -------------------------------------------------

type wlcbPeakStore struct {
	mu   sync.Mutex
	path string
	// Currently we only need one persisted value.
	StreamPeak int `json:"stream_peak"`
}

func newWLCBPeakStore() *wlcbPeakStore {
	// Prefer XDG state location when available.
	// This survives code deployments because it's outside the repo tree.
	home, _ := os.UserHomeDir()
	base := os.Getenv("XDG_STATE_HOME")
	if strings.TrimSpace(base) == "" {
		if home != "" {
			base = filepath.Join(home, ".local", "state")
		} else {
			// Last resort: working directory (still better than nothing).
			base = "."
		}
	}
	dir := filepath.Join(base, "stub-engine")
	_ = os.MkdirAll(dir, 0o755)

	ps := &wlcbPeakStore{path: filepath.Join(dir, "wlcb_status.json")}
	ps.load()
	return ps
}

func (ps *wlcbPeakStore) load() {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	b, err := os.ReadFile(ps.path)
	if err != nil {
		return
	}
	var tmp wlcbPeakStore
	if err := json.Unmarshal(b, &tmp); err != nil {
		return
	}
	ps.StreamPeak = tmp.StreamPeak
}

func (ps *wlcbPeakStore) maybeBumpStreamPeak(current int) int {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if current > ps.StreamPeak {
		ps.StreamPeak = current
		// Best-effort persistence.
		// If we can't write, we still return the updated value for this process.
		b, _ := json.MarshalIndent(struct {
			StreamPeak int `json:"stream_peak"`
		}{ps.StreamPeak}, "", "  ")
		_ = os.WriteFile(ps.path, b, 0o644)
	}
	return ps.StreamPeak
}

var wlcbPeaks = newWLCBPeakStore()

// ---------------------------------------------------------------------
// Recording indicator (UDP from Node-RED)
// ---------------------------------------------------------------------
// Operator requirement:
// - Node-RED sends UDP packets while a program is being recorded.
// - Payload format: <FILENAME>,<TIME>
//   Example: "WLCB_Show_2026-01-16.mp3,00:12:34"
// - If the engine receives data recently (within 5 seconds), the UI shows:
//     green dot + "<FILENAME> (<TIME>)"
// - If no data has been received within 5 seconds, the UI shows:
//     red dot + "Not Recording"
//
// IMPORTANT:
// - This is intentionally UDP (connectionless).
// - We do not attempt to validate filename/time formats; we display what we get.

type wlcbRecordingState struct {
	mu sync.Mutex

	lastSeen time.Time
	filename string
	timecode string
	// lastTimeSec is a best-effort parsed representation of timecode.
	//
	// Why this exists:
	// In real-world UDP telemetry, packets can arrive slightly out of order.
	// If we blindly overwrite the displayed timecode with every packet, the UI
	// can briefly jump backwards (e.g., 11:20 -> 11:19 -> 11:21).
	// That looks like a bug even though telemetry is fine.
	//
	// We smooth ONLY small backwards steps for the same filename.
	lastTimeSec int
	hasTimeSec  bool
	lastAddr string
}

var wlcbRecording = &wlcbRecordingState{}

// Accept fallback payloads shaped like "name(time)".
// Compiled once so we don't recompile it on every UDP packet.
var reRecordingParen = regexp.MustCompile(`^(.*)\(([^()]*)\)$`)

// parseRecordingTimecodeSeconds attempts to parse a timecode string into
// seconds.
//
// Supported shapes:
//   - "MM:SS" (e.g., "04:50")
//   - "H:MM:SS" or "HH:MM:SS" (e.g., "1:02:03", "00:12:34")
//
// If parsing fails, ok=false and callers should fall back to raw display.
func parseRecordingTimecodeSeconds(tc string) (sec int, ok bool) {
	tc = strings.TrimSpace(tc)
	if tc == "" {
		return 0, false
	}
	parts := strings.Split(tc, ":")
	if len(parts) == 2 {
		m, err1 := strconv.Atoi(parts[0])
		s, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			return 0, false
		}
		if m < 0 || s < 0 || s >= 60 {
			return 0, false
		}
		return (m * 60) + s, true
	}
	if len(parts) == 3 {
		h, err1 := strconv.Atoi(parts[0])
		m, err2 := strconv.Atoi(parts[1])
		s, err3 := strconv.Atoi(parts[2])
		if err1 != nil || err2 != nil || err3 != nil {
			return 0, false
		}
		if h < 0 || m < 0 || s < 0 || m >= 60 || s >= 60 {
			return 0, false
		}
		return (h * 3600) + (m * 60) + s, true
	}
	return 0, false
}

func startWLCBRecordingUDPListener(port int) {
	addr := &net.UDPAddr{IP: net.IPv4zero, Port: port}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		log.Printf("recording: ERROR: listen udp :%d failed: %v", port, err)
		return
	}
	log.Printf("recording: listening for Node-RED UDP on :%d (payload: <FILENAME>,<TIME>)", port)

	go func() {
		defer conn.Close()
		buf := make([]byte, 2048)
		for {
			n, remote, err := conn.ReadFromUDP(buf)
			if err != nil {
				// UDP read errors can happen during shutdown; keep this non-fatal.
				log.Printf("recording: WARN: udp read failed: %v", err)
				time.Sleep(250 * time.Millisecond)
				continue
			}
			msg := strings.TrimSpace(string(buf[:n]))
			if msg == "" {
				continue
			}
			// Parse payload.
			// Primary required format (operator spec):
			//   <FILENAME>,<TIME>
			// But Node-RED flows in the wild sometimes emit:
			//   <FILENAME>(<TIME>)
			// (no comma) — usually from string concatenation.
			// We accept BOTH formats to reduce operator friction.
			var fn, tc string
			parts := strings.SplitN(msg, ",", 2)
			if len(parts) == 2 {
				fn = strings.TrimSpace(parts[0])
				tc = strings.TrimSpace(parts[1])
			} else {
				// Fallback: "name(time)"
				// Examples:
				//   Show.wav(04:50)
				//   undefined(04:50)
				if m := reRecordingParen.FindStringSubmatch(msg); len(m) == 3 {
					fn = strings.TrimSpace(m[1])
					tc = strings.TrimSpace(m[2])
				} else {
					log.Printf("recording: WARN: bad payload (expected <FILENAME>,<TIME> or <FILENAME>(<TIME>)): %q", msg)
					continue
				}
			}

			// Tolerate missing/placeholder filename (common Node-RED bug).
			if fn == "" || strings.EqualFold(fn, "undefined") || strings.EqualFold(fn, "null") {
				fn = "Recording"
			}
			if tc == "" {
				log.Printf("recording: WARN: bad payload (empty time): %q", msg)
				continue
			}

			now := time.Now()
			newSec, newOK := parseRecordingTimecodeSeconds(tc)

			wlcbRecording.mu.Lock()
			// Always update lastSeen, even if we decide to ignore a slightly
			// out-of-order timecode.
			wlcbRecording.lastSeen = now
			wlcbRecording.lastAddr = remote.String()

			// If filename changes, treat it as a new session and reset smoothing.
			if wlcbRecording.filename != fn {
				wlcbRecording.filename = fn
				wlcbRecording.timecode = tc
				wlcbRecording.hasTimeSec = false
				wlcbRecording.lastTimeSec = 0
				if newOK {
					wlcbRecording.hasTimeSec = true
					wlcbRecording.lastTimeSec = newSec
				}
				wlcbRecording.mu.Unlock()
				continue
			}

			// Same filename: smooth tiny backwards steps caused by UDP reordering.
			// Example we want to suppress:
			//   11:20 -> 11:19 -> 11:21
			//
			// Rules:
			// - If we can't parse timecode, just display raw (no smoothing).
			// - If the new timecode is slightly behind (<=2s), ignore it.
			// - If it's a large jump backwards (>=30s), assume a real reset and accept it.
			const smallBackMax = 2
			const largeBackReset = 30

			if newOK && wlcbRecording.hasTimeSec {
				if newSec < wlcbRecording.lastTimeSec {
					delta := wlcbRecording.lastTimeSec - newSec
					if delta <= smallBackMax {
						// Ignore this update; keep the newer displayed time.
						wlcbRecording.mu.Unlock()
						continue
					}
					// Large backwards jump: treat as a reset (accept).
					if delta >= largeBackReset {
						wlcbRecording.timecode = tc
						wlcbRecording.lastTimeSec = newSec
						wlcbRecording.hasTimeSec = true
						wlcbRecording.mu.Unlock()
						continue
					}
					// Medium backwards jump: accept (operator likely restarted/seeked).
				}
				// Forward or equal: accept.
				wlcbRecording.timecode = tc
				wlcbRecording.lastTimeSec = newSec
				wlcbRecording.hasTimeSec = true
				wlcbRecording.mu.Unlock()
				continue
			}

			// If we can parse time but we didn't have a baseline yet, establish it.
			wlcbRecording.timecode = tc
			if newOK {
				wlcbRecording.lastTimeSec = newSec
				wlcbRecording.hasTimeSec = true
			}
			wlcbRecording.mu.Unlock()
		}
	}()
}

func getWLCBRecordingDisplay(now time.Time) (ok bool, label string, detail string) {
	wlcbRecording.mu.Lock()
	defer wlcbRecording.mu.Unlock()

	// 5 second timeout (requirement)
	if wlcbRecording.lastSeen.IsZero() || now.Sub(wlcbRecording.lastSeen) > 5*time.Second {
		return false, "Not Recording", "no recent UDP"
	}
	return true, fmt.Sprintf("%s (%s)", wlcbRecording.filename, wlcbRecording.timecode), "udp from " + wlcbRecording.lastAddr
}

// --- Check helpers ----------------------------------------------------

func checkInternet() (bool, string, error) {
	// This endpoint is intentionally tiny and widely used for "am I online".
	// Any successful TCP+TLS+HTTP round trip is enough for our purposes.
	u := "https://clients3.google.com/generate_204"
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return false, "build failed", err
	}
	req.Header.Set("Cache-Control", "no-store")
	resp, err := wlcbHTTP.Do(req)
	if err != nil {
		return false, "offline", err
	}
	defer resp.Body.Close()
	io.CopyN(io.Discard, resp.Body, 64) //nolint:errcheck
	if resp.StatusCode == 204 || (resp.StatusCode >= 200 && resp.StatusCode < 400) {
		return true, fmt.Sprintf("HTTP %d", resp.StatusCode), nil
	}
	return false, fmt.Sprintf("HTTP %d", resp.StatusCode), fmt.Errorf("bad status")
}

func checkWebsite(url string) (bool, string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return false, "build failed", err
	}
	req.Header.Set("Cache-Control", "no-store")
	resp, err := wlcbHTTP.Do(req)
	if err != nil {
		return false, "unreachable", err
	}
	defer resp.Body.Close()
	io.CopyN(io.Discard, resp.Body, 256) //nolint:errcheck
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		return true, fmt.Sprintf("HTTP %d", resp.StatusCode), nil
	}
	return false, fmt.Sprintf("HTTP %d", resp.StatusCode), fmt.Errorf("bad status")
}

// icecastStatus mirrors the subset of Icecast JSON we care about.
type icecastStatus struct {
	IceStats struct {
		Source any `json:"source"` // can be object or array
	} `json:"icestats"`
}

type icecastMount struct {
	Mount        string `json:"mount"`
	Listeners    int    `json:"listeners"`
	ListenURL    string `json:"listenurl"`
	ListenerPeak int    `json:"listener_peak"`
}

// normalizeIcecastMount fills in missing fields from other provided fields.
//
// Why this exists:
// Some Icecast builds/themes omit the "mount" JSON field, but they do provide
// a listen URL like:
//
//	"listenurl": "http://host:8000/stream"
//
// The operator requirements reference mounts by path ("/STL", "/stream"), so
// we derive Mount from ListenURL when needed.
func normalizeIcecastMount(m *icecastMount) {
	if m == nil {
		return
	}
	if strings.TrimSpace(m.Mount) != "" {
		return
	}
	if strings.TrimSpace(m.ListenURL) == "" {
		return
	}
	u, err := url.Parse(m.ListenURL)
	if err != nil {
		return
	}
	if strings.TrimSpace(u.Path) != "" {
		m.Mount = u.Path
	}
}

func fetchIcecastStatus(baseURL string) ([]icecastMount, error) {
	// Icecast commonly exposes this endpoint.
	// Example: http(s)://host:port/status-json.xsl
	u := strings.TrimRight(baseURL, "/") + "/status-json.xsl"

	// Try HTTPS first (user requested https). If it fails, fallback to HTTP.
	try := []string{u}
	if strings.HasPrefix(u, "https://") {
		try = append(try, "http://"+strings.TrimPrefix(u, "https://"))
	}

	var lastErr error
	for _, uu := range try {
		req, err := http.NewRequest(http.MethodGet, uu, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Cache-Control", "no-store")
		resp, err := wlcbHTTP.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}
		var st icecastStatus
		decErr := json.NewDecoder(resp.Body).Decode(&st)
		resp.Body.Close()
		if decErr != nil {
			lastErr = decErr
			continue
		}

		// Normalize source into []icecastMount.
		src := st.IceStats.Source
		out := []icecastMount{}
		switch v := src.(type) {
		case []any:
			for _, item := range v {
				b, _ := json.Marshal(item)
				var m icecastMount
				if err := json.Unmarshal(b, &m); err == nil {
					normalizeIcecastMount(&m)
					out = append(out, m)
				}
			}
		case map[string]any:
			b, _ := json.Marshal(v)
			var m icecastMount
			if err := json.Unmarshal(b, &m); err == nil {
				normalizeIcecastMount(&m)
				out = append(out, m)
			}
		default:
			// No mounts.
		}
		return out, nil
	}
	return nil, lastErr
}

func findMount(mounts []icecastMount, mount string) *icecastMount {
	want := strings.TrimPrefix(strings.TrimSpace(mount), "/")
	wantLower := strings.ToLower(want)

	for i := range mounts {
		got := strings.TrimPrefix(strings.TrimSpace(mounts[i].Mount), "/")
		if got == want {
			return &mounts[i]
		}
		if strings.ToLower(got) == wantLower {
			return &mounts[i]
		}
	}
	return nil
}

// checkPlayingNow fetches the RDS/Now Playing text file from lakesradio.org.
//
// This is intentionally small and defensive:
// - We only read a small prefix of the body (operators just need the message).
// - We trim whitespace and collapse internal newlines.
func checkPlayingNow(url string) (string, string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", "build failed", err
	}
	req.Header.Set("Cache-Control", "no-store")
	resp, err := wlcbHTTP.Do(req)
	if err != nil {
		return "", "unreachable", err
	}
	defer resp.Body.Close()

	// Read at most 512 bytes; playingnow.txt should be tiny.
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	txt := strings.TrimSpace(string(b))
	txt = strings.ReplaceAll(txt, "\r", "")
	// Collapse any remaining newlines to a readable single line.
	txt = strings.Join(strings.Fields(txt), " ")

	// playingnow.txt is served from WordPress and may include HTML entities
	// (e.g. "Brick&#8217;s" for the apostrophe). The UI renders text as text
	// (it should never inject HTML), so we normalize to clean UTF-8 here.
	// This avoids operator-facing "&#8217;" artifacts.
	txt = html.UnescapeString(txt)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return txt, fmt.Sprintf("HTTP %d", resp.StatusCode), fmt.Errorf("bad status")
	}
	if txt == "" {
		txt = "—"
	}
	return txt, fmt.Sprintf("HTTP %d", resp.StatusCode), nil
}

func buildWLCBStatus() wlcbStatusResponse {
	now := time.Now().UTC()

	resp := wlcbStatusResponse{
		UpdatedAt:  now.Format(time.RFC3339),
		InternetOk: false,
		Checks:     []wlcbStatusCheck{},
	}

	add := func(key string, name string, ok bool, dot string, detail string, suppressAlarm bool) {
		resp.Checks = append(resp.Checks, wlcbStatusCheck{
			Key:         key,
			Name:        name,
			Ok:          ok,
			DotOverride: dot,
			Detail:      detail,
			CheckedAt:   now.Format(time.RFC3339),
			SuppressAlarm: suppressAlarm,
		})
	}

	addStream := func(ok bool, dot string, detail string, listeners int, peak int) {
		resp.Checks = append(resp.Checks, wlcbStatusCheck{
			Key:        "stream",
			Name:        "Stream",
			Ok:          ok,
			DotOverride: dot,
			Detail:      detail,
			CheckedAt:   now.Format(time.RFC3339),
			Listeners:   listeners,
			Peak:        peak,
		})
	}

	// ------------------------------------------------------------
	// 1) Internet (root dependency)
	// ------------------------------------------------------------
	inetOK, inetDetail, inetErr := checkInternet()
	if inetErr == nil && inetOK {
		resp.InternetOk = true
		add("internet","Internet", true, "ok", inetDetail, false)
	} else {
		resp.InternetOk = false
		add("internet","Internet", false, "bad", inetDetail, false)
	}

	// ------------------------------------------------------------
	// 1b) Recording (local UDP from Node-RED)
	// ------------------------------------------------------------
	// This check is intentionally independent of Internet connectivity.
	// It reflects whether we have received recording telemetry recently.
	//
	// UI behavior is driven purely by Ok + Name:
	//   - Ok=false  => red dot + "Not Recording" (and blink alarm)
	//   - Ok=true   => green dot + "<FILENAME> (<TIME>)"
	recOK, recLabel, recDetail := getWLCBRecordingDisplay(now)
	// Recording: never blink the background, regardless of Ok.
	add("recording", recLabel, recOK, "", recDetail, true)

	// ------------------------------------------------------------
	// 2) Transmitter + Stream (Icecast)
	// ------------------------------------------------------------
	//
	// Requirements:
	// - Transmitter: mount /STL must have >= 1 listener.
	// - Stream: check mount /stream exists (active mount).
	//   Show "x Listeners / x Peak" pill on the Stream row.
	//
	// NOTE:
	// The operator-facing UI decides whether to "grey out" downstream checks
	// when Internet is down. The engine still attempts the probes so we can
	// record useful diagnostics in Detail (HTTP codes / errors).
	iceBase := "https://seahorse.juststreamwith.us:8006"
	mounts, iceErr := fetchIcecastStatus(iceBase)
	if iceErr != nil {
		add("transmitter","Transmitter", false, "bad", "unreachable", false)
		// If Icecast is unreachable, Stream can't be determined either.
		addStream(false, "bad", "unreachable", 0, wlcbPeaks.StreamPeak)
	} else {
		// Transmitter (/STL): at least one listener.
		stl := findMount(mounts, "/STL")
		if stl == nil {
			add("transmitter","Transmitter", false, "bad", "mount missing", false)
		} else if stl.Listeners >= 1 {
			add("transmitter","Transmitter", true, "ok", fmt.Sprintf("%d listener(s)", stl.Listeners), false)
		} else {
			add("transmitter","Transmitter", false, "bad", "0 listeners", false)
		}

		// Stream (/stream): mount exists = OK (even if listeners == 0).
		stream := findMount(mounts, "/stream")
		if stream == nil {
			addStream(false, "bad", "mount missing", 0, wlcbPeaks.StreamPeak)
		} else {
			peak := wlcbPeaks.maybeBumpStreamPeak(stream.Listeners)
			addStream(true, "ok", "OK", stream.Listeners, peak)
		}
	}

	// ------------------------------------------------------------
	// 3) RDS (playingnow.txt) — informational row under Transmitter
	// ------------------------------------------------------------
	//
	// UI requirement:
	// - Blue circle
	// - Visible text in the label: "RDS: <text>"
	//
	// We treat this as informational (not an alarm) even if unreachable.
	rdsText, rdsDetail, rdsErr := checkPlayingNow("https://lakesradio.org/playingnow.txt")
	if rdsErr != nil {
		// Keep it informational, but include diagnostic detail.
		if strings.TrimSpace(rdsText) == "" {
			rdsText = "unavailable"
		}
		add("rds","RDS: "+rdsText, true, "info", rdsDetail, false)
	} else {
		add("rds","RDS: "+rdsText, true, "info", rdsDetail, false)
	}

	// ------------------------------------------------------------
	// 4) Web site
	// ------------------------------------------------------------
	webOK, webDetail, webErr := checkWebsite("https://lakesradio.org")
	if webErr == nil && webOK {
		add("website","Web Site", true, "ok", webDetail, false)
	} else {
		add("website","Web Site", false, "bad", webDetail, false)
	}

	// Reorder to match UI intent:
	//   Internet
	//   Transmitter
	//   RDS
	//   Web Site
	//   Stream
	//   Recording (bottom)
	//
	// IMPORTANT:
	// Recording updates more frequently than other rows, so the UI depends on
	// a stable Key ("recording") to patch the correct row.
	order := map[string]int{
		"internet":    0,
		"transmitter": 1,
		"rds":         2,
		"website":     3,
		"stream":      4,
		"recording":   5,
	}
	sort.SliceStable(resp.Checks, func(i, j int) bool {
		a := resp.Checks[i]
		b := resp.Checks[j]

		ai, okA := order[a.Key]
		bi, okB := order[b.Key]

		// Back-compat: older rows may not set Key (or future experimental rows).
		// Fall back to name-based classification for determinism.
		if !okA {
			name := a.Name
			if strings.HasPrefix(name, "RDS:") {
				ai, okA = order["rds"], true
			}
		}
		if !okB {
			name := b.Name
			if strings.HasPrefix(name, "RDS:") {
				bi, okB = order["rds"], true
			}
		}

		if okA && okB {
			return ai < bi
		}
		if okA {
			return true
		}
		if okB {
			return false
		}
		return a.Name < b.Name
	})

	return resp
}

// stripTags converts HTML into a conservative plain-text form.
//
// IMPORTANT:
// The WordPress/GiveWP donor wall is rendered as normal HTML headings/divs.
// When we remove tags, headings become *plain text* (e.g. "Mark Massimo"),
// NOT markdown like "### Mark Massimo".
//
// Therefore, our donation parser must key off of stable textual anchors
// that survive tag stripping. The most stable anchor on the current page is
// the literal label:
//
//	"Amount Donated"
//
// followed by the amount line ("$50.00").
//
// Upstream example (after stripping tags into lines):
//
//	MM
//	Mark Massimo
//	January 3, 2026
//	Keep it coming !!!
//	Amount Donated
//	$50.00
func stripTags(in string) string {
	// Remove script/style blocks first (best-effort).
	reScript := regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	reStyle := regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	s := reScript.ReplaceAllString(in, "")
	s = reStyle.ReplaceAllString(s, "")
	// Convert <br> to newlines so comments don't collapse.
	reBR := regexp.MustCompile(`(?i)<br\s*/?>`)
	s = reBR.ReplaceAllString(s, "\n")
	// Drop all remaining tags.
	reTags := regexp.MustCompile(`(?s)<[^>]+>`)
	s = reTags.ReplaceAllString(s, "\n")
	// Decode HTML entities (&amp; etc)
	s = html.UnescapeString(s)
	return s
}

// parseMoney converts a currency-ish string into a float64.
//
// The donations page mixes formats depending on where the number came from:
//   - "$10,000.00" (visible text)
//   - "10000" or "10000.00" (data-* attributes / inline JSON)
//   - sometimes with stray whitespace
//
// This helper is intentionally small and boring: strip common decoration and
// parse as float.
func parseMoney(s string) (float64, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "$")
	s = strings.ReplaceAll(s, ",", "")
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty money string")
	}
	return strconv.ParseFloat(s, 64)
}

func parseDonationsFromText(txt string, limit int) ([]donationItem, float64, error) {
	// Normalize into trimmed, non-empty lines.
	linesRaw := strings.Split(txt, "\n")
	lines := make([]string, 0, len(linesRaw))
	for _, l := range linesRaw {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		// Avoid the header menu etc. We only care about the repeated donation blocks.
		lines = append(lines, l)
	}

	// Also compute a "Raised this year" total from the same donor wall.
	// The user asked to simplify progress parsing by:
	//   - scraping GOAL from the page
	//   - computing RAISED as the sum of donations in the current year
	//
	// This avoids fragile "Raised $X of $Y" widget text parsing, while still
	// staying truthful (derived directly from the displayed donations list).
	// Scan for donation blocks using the "Amount Donated" anchor.
	// This survives HTML stripping and is less fragile than DOM selectors.
	items := make([]donationItem, 0, limit)
	loc, _ := time.LoadLocation("America/Chicago") // best-effort

	// Also compute a "Raised this year" total from the same donor wall.
	// The user asked to simplify progress parsing by:
	//   - scraping GOAL from the page
	//   - computing RAISED as the sum of donations in the current year
	//
	// This avoids fragile "Raised $X of $Y" widget text parsing, while still
	// staying truthful (derived directly from the displayed donations list).
	currentYear := time.Now().In(loc).Year()
	yearTotal := 0.0

	for k := 0; k < len(lines); k++ {
		lk := strings.TrimSpace(lines[k])
		// The stable anchor we expect on the Support page.
		// Some themes/plugins may add punctuation or extra whitespace, so we match
		// as a substring rather than requiring an exact line match.
		if !strings.Contains(strings.ToLower(lk), "amount donated") {
			continue
		}

		// Amount is usually the *next* line, but some layouts render it on the
		// same line as the label.
		amtLine := ""
		if strings.Contains(lk, "$") {
			amtLine = lk
		} else if k+1 < len(lines) {
			amtLine = strings.TrimSpace(lines[k+1])
		} else {
			continue
		}

		// Extract the numeric amount from whichever line we decided is the amount line.
		amtStr := amtLine
		if idx := strings.Index(amtStr, "$"); idx >= 0 {
			amtStr = amtStr[idx+1:]
		}
		amtStr = strings.TrimPrefix(amtStr, "$")
		amtStr = strings.ReplaceAll(amtStr, ",", "")
		amt, aerr := strconv.ParseFloat(amtStr, 64)
		if aerr != nil {
			continue
		}

		// Walk backwards to find the date line ("January 3, 2026").
		dateIdx := -1
		var t time.Time
		for b := k - 1; b >= 0 && b >= k-12; b-- {
			ts := strings.TrimSpace(lines[b])
			// Be flexible: some donors walls may include extra words after the date.
			// Example possibilities:
			//   "January 3, 2026"
			//   "January 3, 2026 at 5:01 pm"
			// We parse the first 3 comma-separated tokens that match the date shape.
			// Best-effort: if we can't parse, we keep scanning.
			dateCandidate := ts
			if at := strings.Index(strings.ToLower(dateCandidate), " at "); at > 0 {
				dateCandidate = strings.TrimSpace(dateCandidate[:at])
			}
			pt, terr := time.ParseInLocation("January 2, 2006", dateCandidate, loc)
			if terr == nil {
				dateIdx = b
				t = pt
				break
			}
		}
		if dateIdx == -1 {
			// Can't identify this block.
			continue
		}
		// We only have a date (no time). Use noon local to avoid DST edge weirdness.
		t = time.Date(t.Year(), t.Month(), t.Day(), 12, 0, 0, 0, loc).UTC()

		// Name is typically the line immediately before the date.
		name := ""
		if dateIdx-1 >= 0 {
			name = strings.TrimSpace(lines[dateIdx-1])
		}
		if name == "" {
			continue
		}
		// Ignore obvious non-name lines.
		if strings.EqualFold(name, "Load more") || strings.EqualFold(name, "Support WLCB") {
			continue
		}

		// Message/comment = any lines between dateIdx+1 and k-1.
		// Join with newlines so multi-line comments survive.
		msgParts := []string{}
		for m := dateIdx + 1; m <= k-1; m++ {
			v := strings.TrimSpace(lines[m])
			if v == "" {
				continue
			}
			// Defensive: skip repeated labels.
			if strings.EqualFold(v, "Amount Donated") {
				continue
			}
			msgParts = append(msgParts, v)
		}
		msg := strings.Join(msgParts, "\n")

		// Add to yearTotal when we have a parsed date in the current year.
		// If date parsing failed (t is zero), we skip it for the year total.
		if dateIdx != -1 && t.Year() == currentYear {
			yearTotal += amt
		}

		// Only keep the newest N items for the UI list, but keep scanning
		// the whole page to compute the yearly total.
		if len(items) < limit {
			items = append(items, donationItem{
				Name:    name,
				Amount:  amt,
				Message: msg,
				Time:    t.Format(time.RFC3339),
			})
		}

		// Advance past the amount line if the amount was on the next line.
		if amtLine != lk {
			k = k + 1
		}
	}

	if len(items) == 0 {
		return nil, 0, fmt.Errorf("no donation blocks found")
	}
	return items, yearTotal, nil
}

// parseGoalFromHTML extracts the campaign GOAL amount from the *raw HTML*.
//
// Why HTML instead of stripped text?
// - The donor wall list is plain text after stripping tags (great!)
// - The campaign goal/progress widget is often rendered as:
//   - data-* attributes, or
//   - inline JSON, or
//   - aria-label text
//     which may be lost or rearranged by naive tag stripping.
//
// This is best-effort and intentionally tolerant of multiple formats.
func parseGoalFromHTML(html string) (float64, error) {
	// GiveWP (WordPress) goal stats panel (the exact DOM the operator pasted):
	//
	//   <span class="givewp-layouts-goal_stats-panel_stat-value">$10,000.00</span>
	//   <span class="givewp-layouts-goal_stats-panel_stat-label">Goal</span>
	//
	// The order is typically VALUE then LABEL (as above), but some templates may
	// render LABEL then VALUE. We support both.
	//
	// Why regex instead of an HTML parser?
	// - We keep the engine dependency-light and deterministic for this appliance.
	// - The markup is stable enough that a targeted regex is safer than a
	//   broad heuristic ("largest dollar amount").
	{
		// The browser DOM you pasted shows VALUE + LABEL spans inside a list-item.
		// In practice, GiveWP themes can add extra classes/attributes or reorder spans.
		//
		// Strategy:
		// 1) Find each goal stat list-item block.
		// 2) Within each block, find the *label* text and *value* text.
		// 3) If label == "Goal", parse the value as currency.
		//
		// This is more robust than assuming immediate adjacency like:
		//   <span class="...value">$10,000.00</span><span class="...label">Goal</span>
		// because templates sometimes insert wrappers or reorder spans.
		reItem := regexp.MustCompile(`(?is)<li[^>]*givewp-layouts-goal_stats-panel_list-item[^>]*>(.*?)</li>`)
		reLabel := regexp.MustCompile(`(?is)givewp-layouts-goal_stats-panel_stat-label[^>]*>\s*([^<]+?)\s*</span>`)
		reValue := regexp.MustCompile(`(?is)givewp-layouts-goal_stats-panel_stat-value[^>]*>\s*\$?\s*([^<]+?)\s*</span>`)

		items := reItem.FindAllStringSubmatch(html, 20) // goal panel is small; cap for safety
		for _, it := range items {
			if len(it) != 2 {
				continue
			}
			block := it[1]
			lm := reLabel.FindStringSubmatch(block)
			vm := reValue.FindStringSubmatch(block)
			if len(lm) != 2 || len(vm) != 2 {
				continue
			}
			label := strings.TrimSpace(lm[1])
			if !strings.EqualFold(label, "Goal") {
				continue
			}
			if v, err := parseMoney(vm[1]); err == nil && v > 0 {
				return v, nil
			}
		}
	}

	// Strong, theme-agnostic capture:
	// Many donation widgets include a literal "... $10,000.00 Goal ..." string in the HTML.
	// We capture the first "$X Goal" we find. This is typically the campaign goal.
	if m := regexp.MustCompile(`\$\s*([0-9][0-9,]*\.?[0-9]{0,2})\s*Goal`).FindStringSubmatch(html); len(m) == 2 {
		if v, err := parseMoney(m[1]); err == nil && v > 0 {
			return v, nil
		}
	}

	// Keep the search space reasonable.
	// We already cap the HTTP read (see caller), but we also cap here so that
	// any future call sites that pass huge strings won't cause regex blowups.
	if len(html) > 6<<20 {
		html = html[:6<<20]
	}

	// 1) Look for explicit data-goal style attributes (common in fundraising widgets).
	// Examples we try to match:
	//   data-goal="10000"
	//   data-goal-amount="10000.00"
	//   data-goal_amount="10000"
	reDataGoal := regexp.MustCompile(`(?i)data-goal(?:-amount)?\s*=\s*"?([0-9][0-9,]*(?:\.[0-9]{2})?)"?`)
	if m := reDataGoal.FindStringSubmatch(html); len(m) == 2 {
		if v, err := parseMoney(m[1]); err == nil && v > 0 {
			return v, nil
		}
	}

	// 2) Look for JSON-ish goal fields.
	// Examples:
	//   "goal":10000
	//   "goal_amount":"10000.00"
	reJSONGoal := regexp.MustCompile(`(?i)"goal(?:_amount|Amount)?"\s*:\s*"?([0-9][0-9,]*(?:\.[0-9]{2})?)"?`)
	if m := reJSONGoal.FindStringSubmatch(html); len(m) == 2 {
		if v, err := parseMoney(m[1]); err == nil && v > 0 {
			return v, nil
		}
	}

	// 3) As a last resort, search for a visible "Goal" label near a currency amount.
	// This works even if the goal is only present in the rendered HTML (not in stripped text).
	reGoalLabel := regexp.MustCompile(`(?is)\bgoal\b[^$]{0,120}\$\s*([0-9][0-9,]*(?:\.[0-9]{2})?)`)
	if m := reGoalLabel.FindStringSubmatch(html); len(m) == 2 {
		if v, err := parseMoney(m[1]); err == nil && v > 0 {
			return v, nil
		}
	}

	return 0, fmt.Errorf("goal not found in html")
}

// parseGoalByMaxDollar is a LAST-RESORT heuristic to recover the campaign GOAL
// when the widget stores it in an unexpected format.
//
// Why this exists:
//   - WordPress fundraising plugins sometimes change markup without warning.
//   - We already have several "exact" parsers (data-* fields, JSON-ish fields,
//     "$X Goal" text).
//   - If all of those fail, operators still want to see "Raised $X of $Y".
//
// Heuristic:
//   - Scan the raw HTML for all "$<number>" occurrences.
//   - Pick the *largest* value that is >= minExpected.
//   - Also require it to be reasonably large (>= $100) so a single generous
//     donation doesn't accidentally become the "goal".
//
// This is intentionally conservative. If it can't find a plausible goal,
// it returns an error and the UI falls back to showing only "Raised $X".
func parseGoalByMaxDollar(html string, minExpected float64) (float64, error) {
	re := regexp.MustCompile(`\$\s*([0-9][0-9,]*(?:\.[0-9]{2})?)`)
	matches := re.FindAllStringSubmatch(html, -1)
	max := 0.0
	for _, m := range matches {
		if len(m) != 2 {
			continue
		}
		v, err := parseMoney(m[1])
		if err != nil {
			continue
		}
		if v > max {
			max = v
		}
	}
	// Require a plausible size so we don't treat a $100 donation as the "goal".
	if max >= 100 && max >= minExpected {
		return max, nil
	}
	return 0, fmt.Errorf("no plausible goal found (max=$%.2f, minExpected=$%.2f)", max, minExpected)
}

// parseGoalFromText extracts the campaign GOAL amount from the website text.
// shown near the donation form.
//
// Upstream currently renders both of these as plain text, for example:
//
//	"$5,295.85 Raised; 39 Donations; $10,000.00 Goal. $5,295.85 of $10,000.00 amount."
//
// We prefer the most direct pattern first:
//
//	"$RAISED of $GOAL"
//
// and fall back to independent "Raised" / "Goal" captures if necessary.
//
// This function is BEST-EFFORT:
// - If it fails, we still return the donations list.
// - The cache preserves the last-known-good summary.
func parseGoalFromText(txt string) (float64, error) {
	// Strong capture for the common plain-text pattern:
	//   "$10,000.00 Goal"
	if m := regexp.MustCompile(`\$\s*([0-9][0-9,]*\.?[0-9]{0,2})\s*Goal`).FindStringSubmatch(txt); len(m) == 2 {
		if v, err := parseMoney(m[1]); err == nil && v > 0 {
			return v, nil
		}
	}

	// We only need the GOAL from the campaign widget/page.
	// The "Raised" number will be computed as the sum of current-year donations
	// from the donor wall list itself (see parseDonationsFromText).
	//
	// We intentionally accept a few possible formats, because WordPress/GiveWP
	// widgets can change markup without changing the visible text.
	flat := strings.Join(strings.Fields(txt), " ")

	// 1) If the page contains a progress string like "$X of $Y", use the second value as GOAL.
	reOf := regexp.MustCompile(`\$([0-9][0-9,]*(?:\.[0-9]{2})?)\s+of\s+\$([0-9][0-9,]*(?:\.[0-9]{2})?)`)
	if m := reOf.FindStringSubmatch(flat); len(m) == 3 {
		goal, err := parseMoney(m[2])
		if err == nil {
			return goal, nil
		}
	}

	// 2) Look for an explicit "goal" label near a currency amount.
	// Examples we try to match:
	//   "Goal $10,000.00"
	//   "Goal: $10,000"
	//   "… goal is $10,000 …"
	reGoal := regexp.MustCompile(`(?i)\bgoal\b[^\$]{0,60}\$([0-9][0-9,]*(?:\.[0-9]{2})?)`)
	if m := reGoal.FindStringSubmatch(flat); len(m) == 2 {
		goal, err := parseMoney(m[1])
		if err == nil {
			return goal, nil
		}
		return 0, fmt.Errorf("goal parse failed: %v", err)
	}

	return 0, fmt.Errorf("goal not found")
}

// parseGoalFromGiveWPFormGrid attempts to extract the campaign GOAL from
// GiveWP's public "form grid" REST endpoint.
//
// Why:
//   - The donor wall page (/support-wlcb/) is scrape-friendly for donations,
//     but the GOAL widget is often rendered client-side.
//   - lakesradio.org exposes the GiveWP REST namespace "give-api/v2" publicly,
//     including /wp-json/give-api/v2/form-grid.
//   - The form-grid response includes an HTML fragment containing text like:
//     "... of $10,000 ..."
//
// We keep this intentionally simple and defensive:
// - No authentication.
// - Small body cap.
// - Prefer the first "of $X" that appears after the "Support WLCB" card title.
func parseGoalFromGiveWPFormGrid(client *http.Client, donorWallURL string) (float64, error) {
	if client == nil {
		client = &http.Client{Timeout: 6 * time.Second}
	}
	u, err := url.Parse(donorWallURL)
	if err != nil {
		return 0, fmt.Errorf("bad donor wall url: %v", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return 0, fmt.Errorf("bad donor wall url: missing scheme/host")
	}
	gridURL := (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: givewpFormGridPath}).String()

	resp, err := client.Get(gridURL)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("upstream status %d", resp.StatusCode)
	}
	bb, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20)) // 2MB cap (form grid is small)
	if err != nil {
		return 0, err
	}

	// The endpoint commonly returns a JSON string that contains HTML.
	frag := ""
	if err := json.Unmarshal(bb, &frag); err != nil {
		// Not JSON? fall back to raw bytes.
		frag = string(bb)
	}
	frag = html.UnescapeString(frag)

	// Prefer the goal that appears after the "Support WLCB" title.
	needle := "Support WLCB"
	idx := strings.Index(frag, needle)
	search := frag
	if idx >= 0 {
		// Only scan a limited window after the title to avoid accidentally
		// matching unrelated currency values in other widgets/cards.
		start := idx
		end := idx + 1500
		if end > len(frag) {
			end = len(frag)
		}
		search = frag[start:end]
	}

	reOf := regexp.MustCompile(`(?i)\bof\s+\$\s*([0-9][0-9,]*(?:\.[0-9]{2})?)`)
	if m := reOf.FindStringSubmatch(search); len(m) == 2 {
		if v, err := parseMoney(m[1]); err == nil && v > 0 {
			return v, nil
		}
	}

	// Final fallback: if we couldn't find the title, take the largest "of $X".
	// This is still safer than "largest $X" because it keys off the GiveWP
	// goal text.
	matches := reOf.FindAllStringSubmatch(frag, -1)
	max := 0.0
	for _, mm := range matches {
		if len(mm) != 2 {
			continue
		}
		v, err := parseMoney(mm[1])
		if err != nil {
			continue
		}
		if v > max {
			max = v
		}
	}
	if max > 0 {
		return max, nil
	}

	return 0, fmt.Errorf("goal not found in form-grid")
}

func (c *donationsCache) getLatest(limit int) donationsResponse {
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}

	// Gentle rate-limit: if multiple UI clients request simultaneously,
	// don't hammer the website. We allow a fresh fetch at most every 15 seconds.
	c.mu.Lock()
	tooSoon := c.lastFetch.After(time.Now().Add(-15 * time.Second))
	lastGood := c.lastGood
	hasLast := c.hasLast
	c.mu.Unlock()

	if tooSoon && hasLast {
		// Return cached without marking stale.
		out := lastGood
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
		}
		return out
	}

	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Get(donationsSourceURL)
	if err != nil {
		return c.fallback(err, limit)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.fallback(fmt.Errorf("upstream status %d", resp.StatusCode), limit)
	}
	// NOTE: Some WordPress/GiveWP pages can be surprisingly large (lots of markup,
	// inlined scripts, and long donor walls). We previously capped at 2MB, but
	// that can truncate the part of the HTML where the Goal widget/config is
	// defined (which would make goal parsing fail even though the browser shows it).
	//
	// We still keep a hard cap for appliance safety, but raise it to 6MB.
	//
	// ALSO: io.LimitReader truncates silently. We *intentionally* don't attempt
	// to detect truncation here; instead we oversize the cap enough that this
	// is unlikely for our use case.
	b, err := io.ReadAll(io.LimitReader(resp.Body, 6<<20)) // 6MB cap
	if err != nil {
		return c.fallback(err, limit)
	}
	text := stripTags(string(b))
	items, raisedThisYear, err := parseDonationsFromText(text, limit)
	if err != nil {
		return c.fallback(err, limit)
	}

	// Best-effort: campaign progress summary.
	// We scrape GOAL from the page and compute RAISED as the sum of current-year donations.
	summary := (*donationSummary)(nil)
	goal, goalErr := parseGoalFromHTML(string(b))
	if goalErr != nil {
		// Fall back to stripped-text parsing (older themes may render the goal as plain text).
		goal, goalErr = parseGoalFromText(text)
	}
	if goalErr != nil {
		// Next fallback: ask GiveWP itself (public WP REST endpoint).
		// This is much more reliable than trying to infer the goal from the donor wall.
		goal, goalErr = parseGoalFromGiveWPFormGrid(client, donationsSourceURL)
	}
	if goalErr != nil {
		// Final fallback: heuristic scan for the largest "$X" value in the raw HTML.
		// We constrain it by the computed raisedThisYear so we don't accidentally
		// treat a single donation as the goal.
		goal, goalErr = parseGoalByMaxDollar(string(b), raisedThisYear)
	}
	if goalErr != nil {
		log.Printf("donations: goal parse failed: %v (html_bytes=%d)", goalErr, len(b))
		// Still publish RAISED if we were able to compute it.
		// The UI will show "Raised $X" when Goal is missing/zero.
		if raisedThisYear > 0 {
			summary = &donationSummary{Raised: raisedThisYear, Goal: 0, Currency: "USD"}
		}
	} else {
		summary = &donationSummary{Raised: raisedThisYear, Goal: goal, Currency: "USD"}
	}

	out := donationsResponse{
		Source:    "scrape",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Stale:     false,
		Summary:   summary,
		Items:     items,
	}

	c.mu.Lock()
	c.lastGood = out
	c.hasLast = true
	c.lastFetch = time.Now()
	c.mu.Unlock()

	return out
}

func (c *donationsCache) fallback(err error, limit int) donationsResponse {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.lastFetch = time.Now()
	if c.hasLast {
		out := c.lastGood
		out.Stale = true
		out.Error = err.Error()
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
		}
		return out
	}

	return donationsResponse{
		Source:    "scrape",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Stale:     true,
		Error:     err.Error(),
		Items:     []donationItem{},
	}
}

func main() {
	var cfgPath string
	flag.StringVar(&cfgPath, "config", defaultConfigPath(), "Path to operator config.v1")
	flag.Parse()

	// ---------------------------------------------------------------------
	// Canonicalize config path.
	//
	// We have *two* sources of truth that must stay in sync:
	//   1) The engine startup flag (--config ...) typically set by systemd.
	//   2) The Engineering UI config editor which always targets the canonical
	//      operator path returned by app.ConfigFilePath().
	//
	// In the field we observed cases where the running engine was reading one
	// config path while the UI was writing another, leading to confusing
	// "Saved, waiting for restart..." loops where the engine restarted back
	// into MOCK. To prevent that class of drift, we prefer the canonical path
	// when it is available.
	// ---------------------------------------------------------------------
	if p, err := app.ConfigFilePath(); err == nil && strings.TrimSpace(p) != "" {
		// If the flag is relative or empty, always replace it.
		if strings.TrimSpace(cfgPath) == "" || !filepath.IsAbs(cfgPath) {
			cfgPath = p
		} else if filepath.Clean(cfgPath) != filepath.Clean(p) {
			// If the flag points somewhere else, keep it but log loudly.
			log.Printf("WARN: engine --config path (%s) differs from canonical UI path (%s). Using canonical to stay in sync.", cfgPath, p)
			cfgPath = p
		}
	}

	cfg, err := app.LoadConfig(cfgPath)
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	engine := app.NewEngine(cfg, version, cfgPath)

	// ------------------------------------------------------------
	// Recording indicator UDP listener (Node-RED -> Engine)
	// ------------------------------------------------------------
	// Default port: 55123
	// Override via env:
	//   WLCB_RECORDING_UDP_PORT=55123
	// NOTE: We start this even in mock mode; it is independent of DSP writes.
	udpPort := 55123
	if s := strings.TrimSpace(os.Getenv("WLCB_RECORDING_UDP_PORT")); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 && v < 65536 {
			udpPort = v
		} else {
			log.Printf("recording: WARN: invalid WLCB_RECORDING_UDP_PORT=%q; using %d", s, udpPort)
		}
	}
	startWLCBRecordingUDPListener(udpPort)

	mux := http.NewServeMux()

	// Health
	//
	// This endpoint is used by:
	//   - install.sh health checks
	//   - the watchdog (curl --max-time 2)
	//   - the UI status indicator
	//
	// Therefore it MUST be:
	//   - fast (no DSP I/O)
	//   - reliable (never "empty reply")
	//   - explicit about mock/live safety
	//
	// IMPORTANT:
	// A previous regression caused watchdog restart loops because curl saw
	// "empty reply from server". That symptom is typically a panic in a
	// handler or a handler that is blocked until the service is restarted.
	//
	// This handler is now deliberately minimal and wrapped with a hard
	// panic-recovery that ALWAYS emits a JSON response.
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Always ensure we send an HTTP status line. (Without this, a panic
		// or abrupt close can look like "empty reply" to curl.)
		w.WriteHeader(http.StatusOK)

		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic in /api/health: %v", rec)
				// Best effort JSON error. If the client already got partial output,
				// this may fail, but the status line was already sent.
				_ = json.NewEncoder(w).Encode(map[string]any{
					"ok":      false,
					"version": engine.Version(),
					"time":    time.Now().UTC().Format(time.RFC3339),
					"error":   "panic in /api/health",
				})
			}
		}()

		// Desired mode: what the running engine believes the operator config contains.
		// NOTE: We do NOT re-read config files from disk here.
		cfg := engine.GetConfigCopy()
		desiredMode := strings.ToLower(strings.TrimSpace(cfg.DSP.Mode))
		if desiredMode == "" {
			desiredMode = "mock"
		}

		// Effective write mode.
		//
		// IMPORTANT (v0.2.94):
		// /api/health is used by the watchdog. It MUST return quickly and
		// deterministically.
		//
		// Earlier releases derived an "active" mode by consulting additional
		// engine state (DSPLiveActive / DSP health locks). In some scenarios,
		// that could cause /api/health to stall while the DSP monitor was mid-check,
		// leading to watchdog restarts and curl "Empty reply" symptoms.
		//
		// To harden the watchdog path, we now report effective write mode strictly
		// from the loaded config.
		active := desiredMode

		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":               true,
			"version":          engine.Version(),
			"time":             time.Now().UTC().Format(time.RFC3339),
			"desiredWriteMode": desiredMode,
			"dspWriteMode":     active,
			// Back-compat field used by some UI bits.
			"mode":            active,
			"restartRequired": app.RestartRequired(),
		})
	})

	// Version (stable, explicit)
	//
	// This MUST be safe to call from the watchdog at any time.
	// Keep it extremely small and avoid anything that could block.
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic in /api/version: %v", rec)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"version": engine.Version(),
					"time":    time.Now().UTC().Format(time.RFC3339),
					"error":   "panic in /api/version",
				})
			}
		}()

		cfg := engine.GetConfigCopy()
		// Report BOTH desired + active write mode for clarity.
		desired := strings.ToLower(strings.TrimSpace(cfg.DSP.Mode))
		if desired == "" {
			desired = "mock"
		}
		// v0.2.94: match /api/health hardening.
		// We keep /api/version lock-free and deterministic by deriving the
		// effective mode from the loaded config.
		active := desired

		_ = json.NewEncoder(w).Encode(map[string]any{
			"version":          engine.Version(),
			"time":             time.Now().UTC().Format(time.RFC3339),
			"desiredWriteMode": desired,
			"dspWriteMode":     active,
		})
	})

	// Latest available version (git tags via engine update checker)

	// Config (read-only; safe subset). Useful for debugging mode + DSP connection config.
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "GET required")
			return
		}
		// NOTE:
		// This endpoint is used by the Engineering UI to *display* configuration after a page
		// refresh. We intentionally report the engine's **desired** DSP write mode, not merely
		// the last-loaded config field, because:
		//   - The engine may be running in desired=live while actively disconnected / in a safe
		//     "mock" write state.
		//   - During a migration window, older config files could hold stale values.
		//
		// Reporting the desired mode prevents confusing UX where the top-right status shows
		// dsp writes LIVE, but the Configuration dropdown snaps back to "mock (default)" after
		// a refresh.
		// GetConfigCopy() returns a concrete app.Config (not a pointer), so it will never be nil.
		// Keep this as a value copy so /api/config is always safe to serve even if the engine
		// is mid-reload.
		cfg := engine.GetConfigCopy()
		dspStatus := engine.DSPModeStatus()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": engine.Version(),
			"time":    time.Now().UTC().Format(time.RFC3339),
			"mode":    dspStatus.DesiredMode,
			"dsp": map[string]any{
				"ip":   cfg.DSP.Host,
				"port": cfg.DSP.Port,
				"mode": dspStatus.DesiredMode,
			},
			"sources": cfg.Meta,
		})
	})

	// -----------------------------------------------------------------
	// Latest Donations (UI card data source)
	// -----------------------------------------------------------------
	// Contract:
	//   GET /api/donations/latest?limit=5
	// Returns:
	//   {
	//     "source": "scrape",
	//     "updated_at": "2026-01-13T23:15:00Z",
	//     "stale": false,
	//     "items": [ {"name":"...","amount":25,"message":"...","time":"..."}, ... ]
	//   }
	// Notes:
	// - Server-side scrape avoids browser CORS + mixed-content issues.
	// - On any failure, we return last-known-good with stale=true.
	mux.HandleFunc("/api/donations/latest", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "GET required")
			return
		}
		limit := 5
		if s := strings.TrimSpace(r.URL.Query().Get("limit")); s != "" {
			if v, err := strconv.Atoi(s); err == nil {
				limit = v
			}
		}
		out := donations.getLatest(limit)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(out)
	})

	// ------------------------------------------------------------
	// WLCB Status (high-level station/system status)
	// ------------------------------------------------------------
	// Endpoint:
	//   GET /api/wlcb/status
	//
	// Contract:
	// - The engine probes external services (not the browser).
	// - On internal errors, we return last-known-good with stale=true.
	mux.HandleFunc("/api/wlcb/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		wlcbStatus.mu.Lock()
		// Tiny throttle: if multiple clients request within a short window,
		// reuse the most recent snapshot.
		if wlcbStatus.hasLast && time.Since(wlcbStatus.lastFetch) < 2*time.Second {
			_ = json.NewEncoder(w).Encode(wlcbStatus.lastGood)
			wlcbStatus.mu.Unlock()
			return
		}
		wlcbStatus.mu.Unlock()

		// Build a fresh snapshot (best-effort). This never returns an error for
		// downstream check failures; each check records its own ok/detail.
		var snap wlcbStatusResponse
		var buildErr error
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					buildErr = fmt.Errorf("panic: %v", rec)
				}
			}()
			// NOTE: The WLCB Status snapshot is intentionally self-contained.
			// It performs its own probes and does not depend on engine internals.
			snap = buildWLCBStatus()
		}()

		wlcbStatus.mu.Lock()
		defer wlcbStatus.mu.Unlock()

		if buildErr == nil {
			wlcbStatus.lastGood = snap
			wlcbStatus.hasLast = true
			wlcbStatus.lastFetch = time.Now()
			_ = json.NewEncoder(w).Encode(snap)
			return
		}

		// Internal build failure: return last-known-good if we have it.
		if wlcbStatus.hasLast {
			cached := wlcbStatus.lastGood
			cached.Stale = true
			cached.Error = buildErr.Error()
			_ = json.NewEncoder(w).Encode(cached)
			return
		}

		// No cached value: return a minimal stale response.
		_ = json.NewEncoder(w).Encode(wlcbStatusResponse{
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			Stale:     true,
			Error:     buildErr.Error(),
			Checks:    []wlcbStatusCheck{},
		})
	})


	// ------------------------------------------------------------
	// WLCB Recording status (fast path)
	// ------------------------------------------------------------
	// Endpoint:
	//   GET /api/wlcb/recording
	//
	// Why this exists:
	// The main /api/wlcb/status endpoint is intentionally polled slowly (5s)
	// because it performs external network probes (Internet, website, Icecast).
	//
	// Recording telemetry, however, is LOCAL UDP from Node-RED and updates
	// continuously (timecode). Operators requested a smoother UI update cadence
	// for the recording row without increasing the probe cadence for the other
	// rows.
	//
	// Therefore:
	// - This endpoint does NOT perform any external I/O.
	// - It is safe to poll at 500ms.
	mux.HandleFunc("/api/wlcb/recording", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "GET required")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		now := time.Now().UTC()
		ok, label, detail := getWLCBRecordingDisplay(now)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"updatedAt": now.Format(time.RFC3339),
			"key":       "recording",
			"ok":        ok,
			"label":     label,
			"detail":    detail,
		})
	})

	// Admin config file editor (Engineering page).
	// This edits ONLY ~/.StudioB-UI/config.json (outside of repo/releases) so upgrades do not overwrite settings.
	mux.HandleFunc("/api/admin/config/file", func(w http.ResponseWriter, r *http.Request) {
		if !engine.CheckAdmin(r) {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		w.Header().Set("Content-Type", "application/json")

		switch r.Method {
		case http.MethodGet:
			cfg, exists, raw, err := app.ReadEditableConfig()
			resp := map[string]any{
				"ok":     err == nil,
				"exists": exists,
				"raw":    raw,
				"config": cfg,
			}
			if p, perr := app.ConfigFilePath(); perr == nil {
				resp["path"] = p
			}
			if err != nil {
				resp["error"] = err.Error()
			}
			_ = json.NewEncoder(w).Encode(resp)
			return

		case http.MethodPut:
			// The UI historically sent mode in two shapes:
			//  1. { "mode": "live", "dsp": { "ip": "...", "port": 123 } }
			//  2. { "dsp": { "mode": "live", "ip": "...", "port": 123 } }
			//
			// We accept BOTH to avoid silent "mode stays mock" situations when the client
			// uses the nested form. (Unknown JSON fields are ignored by default decoding.)
			type editableConfigWire struct {
				Mode string `json:"mode"`
				DSP  struct {
					Mode string `json:"mode"`
					IP   string `json:"ip"`
					Port int    `json:"port"`
				} `json:"dsp"`
			}
			var wire editableConfigWire
			if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
				writeAPIError(w, http.StatusBadRequest, "bad json")
				return
			}

			// MODE NORMALIZATION / BACKWARDS COMPATIBILITY
			//
			// Over multiple releases, the Engineering UI has sent mode in two shapes:
			//  1. { "mode": "live", "dsp": { "ip": "...", "port": 123 } }
			//  2. { "dsp": { "mode": "live", "ip": "...", "port": 123 } }
			//
			// Some UI builds can (briefly) send BOTH, where the top-level "mode" still
			// contains a stale default label like "mock (default)" while dsp.mode is the
			// operator's real selection.
			//
			// To prevent "I picked LIVE but it saved MOCK", we always prefer dsp.mode when
			// it is present.
			modeInTop := strings.TrimSpace(wire.Mode)
			modeInDSP := strings.TrimSpace(wire.DSP.Mode)
			modeSource := "mode"
			modeChosen := modeInTop
			if modeInDSP != "" {
				modeChosen = modeInDSP
				modeSource = "dsp.mode"
			}
			wire.Mode = modeChosen
			var body app.EditableConfig
			body.Mode = wire.Mode
			body.DSP.IP = wire.DSP.IP
			body.DSP.Port = wire.DSP.Port
			p, err := app.WriteEditableConfig(body)
			if err != nil {
				writeAPIError(w, http.StatusBadRequest, err.Error())
				return
			}

			// SAFETY: Mode changes (mock/live) are applied ONLY on engine restart.
			// This makes the system deterministic and keeps "live writes" from being
			// enabled mid-flight inside a long-running process.
			//
			// The watchdog is responsible for observing this flag and restarting the
			// stub-engine service.
			_ = app.RequestEngineRestart("config saved via Engineering UI")

			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":   true,
				"path": p,
				// Saved/normalized mode (the engine's config expects: mock|live).
				"mode_saved": strings.ToLower(strings.TrimSpace(body.Mode)),
				// Debug: what we received and which field we trusted.
				"mode_input_top":   modeInTop,
				"mode_input_dsp":   modeInDSP,
				"mode_source":      modeSource,
				"restart_required": true,
			})
			return
		default:
			writeAPIError(w, http.StatusMethodNotAllowed, "GET or PUT required")
			return
		}
	})

	mux.HandleFunc("/api/updates/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		info := engine.CheckUpdateCached()
		latest := info.LatestVersion
		if latest != "" && !strings.HasPrefix(latest, "v") {
			latest = "v" + latest
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"latest": latest})
	})

	// Apply latest update (admin PIN required). Uses git/script-backed update flow.
	mux.HandleFunc("/api/updates/apply", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "GET or POST required"})
			return
		}
		if !requireAdminPin(w, r, cfg.Admin.PIN) {
			return
		}
		// Run update synchronously so the UI can display a *real* result.
		// Previous versions fired-and-forgot, which caused the UI to claim success
		// even when the installer failed (e.g., Go build errors).
		outStr, err := engine.UpdateSync()
		resp := map[string]any{"ok": err == nil}
		if err != nil {
			resp["error"] = err.Error()
		}
		// Return a small tail for quick troubleshooting in the browser.
		if len(outStr) > 0 {
			const max = 4000
			if len(outStr) > max {
				resp["outputTail"] = outStr[len(outStr)-max:]
			} else {
				resp["outputTail"] = outStr
			}
		}
		// writeJSON signature is (w, statusCode, payload)
		writeJSON(w, http.StatusOK, resp)
	})

	// Snapshot
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(engine.StateSnapshot())
	})

	// Studio UI status (stable contract)
	mux.HandleFunc("/api/studio/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(engine.StudioStatusSnapshot())
	})

	// Set RC (allowlisted)
	mux.HandleFunc("/api/rc/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		idStr := r.URL.Path[len("/api/rc/"):]
		var body struct {
			Value float64 `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeAPIError(w, http.StatusBadRequest, "bad json")
			return
		}
		// v0.2.46 defense-in-depth: server-side DSP control guard.
		// The UI already blocks control attempts when DISCONNECTED, but we also
		// enforce it here to protect against stale cached JS or non-UI clients.
		if ok, reason := engine.DSPControlAllowed(); !ok {
			writeAPIError(w, http.StatusConflict, reason)
			return
		}
		if err := engine.SetRC(idStr, body.Value); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// ------------------------------------------------------------
	// PlayIt Live (PIL) proxy endpoints
	// - Avoids browser CORS
	// - Allows self-signed TLS by terminating/validating at the engine
	// ------------------------------------------------------------
	mux.HandleFunc("/api/pil/playoutMode", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			pilProxy(w, r, http.MethodGet, "/api/control/liveAssist/playoutMode")
		case http.MethodPost:
			// Attempt to write the mode. Pass through JSON payload.
			// Some PIL builds expect POST (not PUT).
			pilProxy(w, r, http.MethodPost, "/api/control/liveAssist/playoutMode")
		case http.MethodPut:
			// Some PIL builds expect PUT.
			pilProxy(w, r, http.MethodPut, "/api/control/liveAssist/playoutMode")
		default:
			writeAPIError(w, http.StatusMethodNotAllowed, "GET/POST required")
		}
	})

	// v0.3.87: Toggle automation explicitly (PlayIt Live Control API)
	//
	// Some PlayIt Live deployments expose automation toggling as a dedicated
	// endpoint:
	//   POST /api/control/liveAssist/playoutMode/toggleAutomation
	// with JSON:
	//   {"on": true}
	//
	// We expose this under the same-origin engine proxy to avoid CORS/TLS issues:
	//   POST /api/pil/playoutMode/toggleAutomation
	//
	// NOTE: This is best-effort. The UI still polls /api/pil/playoutMode for truth.
	mux.HandleFunc("/api/pil/playoutMode/toggleAutomation", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		pilProxy(w, r, http.MethodPost, "/api/control/liveAssist/playoutMode/toggleAutomation")
	})

	mux.HandleFunc("/api/pil/play", func(w http.ResponseWriter, r *http.Request) {
		// UI uses a momentary action. Prefer POST.
		// We also accept GET for backwards compatibility with older UIs.
		switch r.Method {
		case http.MethodPost:
			pilProxy(w, r, http.MethodPost, "/api/control/liveAssist/masterControl/play")
		case http.MethodGet:
			pilProxy(w, r, http.MethodPost, "/api/control/liveAssist/masterControl/play")
		default:
			writeAPIError(w, http.StatusMethodNotAllowed, "GET/POST required")
			return
		}
	})

	// -----------------------------------------------------------------------
	// Operator intents (v0.2.75)
	//
	// Phase 1 control plumbing (safe / non-destructive): Speaker Mute.
	//
	// Contract:
	// - UI sends an explicit intent.
	// - Engine logs the intent (timestamped) to ~/.StudioB-UI/state/intents.jsonl.
	// - Engine updates its in-memory RC cache so the UI reflects the new state.
	// - DSP writes remain mocked/blocked in this phase.
	// -----------------------------------------------------------------------
	mux.HandleFunc("/api/intent/speaker/mute", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		var body struct {
			Mute   *bool  `json:"mute"`
			Source string `json:"source"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeAPIError(w, http.StatusBadRequest, "bad json")
			return
		}
		if body.Mute == nil {
			writeAPIError(w, http.StatusBadRequest, "missing field: mute")
			return
		}
		// Defense-in-depth: keep the same DSP control guard used by /api/rc.
		if ok, reason := engine.DSPControlAllowed(); !ok {
			writeAPIError(w, http.StatusConflict, reason)
			return
		}
		src := strings.TrimSpace(body.Source)
		if src == "" {
			src = "ui"
		}
		if err := engine.ApplySpeakerMuteIntent(*body.Mute, src); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// DSP health + manual connectivity test (operator-driven; no polling).

	mux.HandleFunc("/api/dsp/mode", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(engine.DSPModeStatus())
	})

	mux.HandleFunc("/api/dsp/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(engine.DSPHealth())
	})

	mux.HandleFunc("/api/dsp/test", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		// Single-shot test only. Timeout is conservative and fixed here.
		snap := engine.TestDSPConnectivity(1200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snap)
	})

	// Operator-safe reconnect

	mux.HandleFunc("/api/dsp/timeline", func(w http.ResponseWriter, r *http.Request) {
		// Read-only: returns recent DSP health transitions.
		// Query param: ?n=50 (default 50, max 200)
		n := 50
		if v := strings.TrimSpace(r.URL.Query().Get("n")); v != "" {
			if i, err := strconv.Atoi(v); err == nil {
				n = i
			}
		}
		if n > 200 {
			n = 200
		}
		if n < 1 {
			n = 1
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(engine.ReadDSPTimeline(n))
	})

	mux.HandleFunc("/api/reconnect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		engine.Reconnect()
		w.WriteHeader(http.StatusNoContent)
	})

	// Update check (GitHub latest release). No admin PIN required; safe read-only.
	mux.HandleFunc("/api/update/check", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(engine.CheckUpdateCached())
	})

	// Admin update/rollback
	mux.HandleFunc("/api/admin/update", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		if !engine.CheckAdmin(r) {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		go engine.Update()
		w.WriteHeader(http.StatusAccepted)
	})

	mux.HandleFunc("/api/admin/releases", func(w http.ResponseWriter, r *http.Request) {
		if !engine.CheckAdmin(r) {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(engine.ListReleases())
	})

	mux.HandleFunc("/api/admin/rollback", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		if !engine.CheckAdmin(r) {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var body struct {
			Version string `json:"version"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Version == "" {
			writeAPIError(w, http.StatusBadRequest, "bad json")
			return
		}
		go engine.Rollback(body.Version)
		w.WriteHeader(http.StatusAccepted)
	})

	// Admin: request an engine restart.
	//
	// We do *not* restart the process directly here. Instead we create the same
	// restart-required flag file used by config changes. The watchdog observes
	// that flag and performs the systemctl restart (and logs the details).
	mux.HandleFunc("/api/admin/restart", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		if !engine.CheckAdmin(r) {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		// Best-effort: if we fail to create the flag, return a helpful error.
		if err := app.RequestEngineRestart("manual restart requested from UI"); err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})

	// Watchdog status (read-only) + start (admin)
	mux.HandleFunc("/api/watchdog/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "GET required")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(engine.WatchdogStatusSnapshot())
	})

	mux.HandleFunc("/api/admin/watchdog/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		if !engine.CheckAdmin(r) {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		// Run synchronously so we can return a meaningful success/failure.
		out, err := engine.StartWatchdogSync()
		resp := map[string]any{
			"action": "watchdog-start",
			"output": out,
			"status": engine.WatchdogStatusSnapshot(),
		}
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			resp["ok"] = false
			resp["error"] = err.Error()
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		resp["ok"] = true
		_ = json.NewEncoder(w).Encode(resp)
	})

	// WebSocket stream
	mux.HandleFunc("/ws", engine.HandleWS)

	addr := cfg.UI.HTTPListen
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("stub-engine %s listening on %s", engine.Version(), addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// requireAdminPin is a tiny helper used by a couple of admin-only routes.
// It validates the caller-provided admin PIN against the configured PIN.
//
// IMPORTANT:
//   - The PIN MUST be provided by the caller via the X-Admin-PIN header.
//   - The server does NOT accept the PIN via URL query parameters (those leak
//     too easily via logs and browser history).
//   - We intentionally keep this helper local to main.go to avoid accidental
//     reuse in other packages.
func requireAdminPin(w http.ResponseWriter, r *http.Request, expectedPIN string) bool {
	callerPIN := strings.TrimSpace(r.Header.Get("X-Admin-PIN"))
	if expectedPIN == "" {
		// Misconfiguration: we cannot authorize anything safely.
		writeAPIError(w, http.StatusServiceUnavailable, "admin PIN not configured")
		return false
	}
	// Constant-time compare to avoid trivial timing leaks.
	if subtle.ConstantTimeCompare([]byte(callerPIN), []byte(expectedPIN)) != 1 {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	return true
}

// writeJSON writes a JSON response with a stable Content-Type.
// This keeps client-side parsing predictable (jq, fetch, etc.).
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeAPIError is a convenience wrapper for returning a consistent JSON error
// payload across all API endpoints.
//
// This is important because tools like `jq` expect valid JSON even on failures.
func writeAPIError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{
		"ok":    false,
		"error": msg,
	})
}

// pilProxy forwards a small set of PlayIt Live REST calls.
// We intentionally keep the surface area tiny and explicit.
// pilProxy is used by our /api/pil/* endpoints to forward a request to PlayIt Live.
//
// Why we proxy:
//   - Browser -> PIL would be blocked by CORS.
//   - PIL uses a self-signed certificate; the engine can safely ignore TLS
//     validation on this *specific* upstream without weakening the browser.
//
// `method` allows our API to expose a clean contract while still matching PIL's
// expected verbs (e.g. we may accept POST but forward PUT).
func pilProxy(w http.ResponseWriter, r *http.Request, method string, path string) {
	// Build target URL
	u := pilBaseURL + path
	if strings.Contains(u, "?") {
		u = u + "&apiKey=" + pilAPIKey
	} else {
		u = u + "?apiKey=" + pilAPIKey
	}

	// Copy body (if any)
	var body io.Reader
	if r.Body != nil {
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		body = strings.NewReader(string(b))
		// Reset body for potential reuse isn't necessary (we don't reuse).
	}

	req, err := http.NewRequest(method, u, body)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "PIL request build failed")
		return
	}
	// Preserve content type for JSON PUT/POST.
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}

	resp, err := pilHTTP.Do(req)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "PIL request failed")
		return
	}
	defer resp.Body.Close()

	// Pass through response body (JSON) for the UI to consume.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
