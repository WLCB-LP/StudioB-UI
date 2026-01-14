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
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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

func parseDonationsFromText(txt string, limit int) ([]donationItem, error) {
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

	// Scan for donation blocks using the "Amount Donated" anchor.
	// This survives HTML stripping and is less fragile than DOM selectors.
	items := make([]donationItem, 0, limit)
	loc, _ := time.LoadLocation("America/Chicago") // best-effort

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

		items = append(items, donationItem{
			Name:    name,
			Amount:  amt,
			Message: msg,
			Time:    t.Format(time.RFC3339),
		})

		if len(items) >= limit {
			break
		}
		// Advance past the amount line if the amount was on the next line.
		if amtLine != lk {
			k = k + 1
		}
	}

	if len(items) == 0 {
		return nil, fmt.Errorf("no donation blocks found")
	}
	return items, nil
}

// parseCampaignProgressFromText extracts the "Raised" and "Goal" dollar amounts
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
func parseCampaignProgressFromText(txt string) (*donationSummary, error) {
	// Flatten whitespace to make regex simpler and less fragile across line breaks.
	flat := strings.Join(strings.Fields(txt), " ")

	parseMoney := func(s string) (float64, error) {
		s = strings.ReplaceAll(s, ",", "")
		return strconv.ParseFloat(s, 64)
	}

	// 1) Preferred: "$X of $Y" (this appears on the page as a progress string).
	// Example: "$5,295.85 of $10,000.00"
	reOf := regexp.MustCompile(`\$([0-9][0-9,]*\.[0-9]{2})\s+of\s+\$([0-9][0-9,]*\.[0-9]{2})`)
	if m := reOf.FindStringSubmatch(flat); len(m) == 3 {
		raised, err1 := parseMoney(m[1])
		goal, err2 := parseMoney(m[2])
		if err1 == nil && err2 == nil {
			return &donationSummary{Raised: raised, Goal: goal, Currency: "USD"}, nil
		}
		// If one parse fails, keep looking via fallbacks.
	}

	// 2) Fallback: "$X Raised" and "$Y Goal" somewhere in the text.
	reRaised := regexp.MustCompile(`\$([0-9][0-9,]*\.[0-9]{2})\s+Raised`)
	reGoal := regexp.MustCompile(`\$([0-9][0-9,]*\.[0-9]{2})\s+Goal`)

	var (
		raisedStr string
		goalStr   string
	)
	if m := reRaised.FindStringSubmatch(flat); len(m) == 2 {
		raisedStr = m[1]
	}
	if m := reGoal.FindStringSubmatch(flat); len(m) == 2 {
		goalStr = m[1]
	}
	if raisedStr != "" && goalStr != "" {
		raised, err1 := parseMoney(raisedStr)
		goal, err2 := parseMoney(goalStr)
		if err1 == nil && err2 == nil {
			return &donationSummary{Raised: raised, Goal: goal, Currency: "USD"}, nil
		}
		return nil, fmt.Errorf("raised/goal parse failed: raised=%v goal=%v", err1, err2)
	}

	return nil, fmt.Errorf("campaign progress not found")
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
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20)) // 2MB cap
	if err != nil {
		return c.fallback(err, limit)
	}
	text := stripTags(string(b))
	items, err := parseDonationsFromText(text, limit)
	if err != nil {
		return c.fallback(err, limit)
	}

	// Best-effort: campaign progress (Raised/Goal). If this fails we still
	// return the donation list. The UI prefers this summary, but it must never
	// block operator visibility of the latest donations.
	summary, sumErr := parseCampaignProgressFromText(text)
	if sumErr != nil {
		log.Printf("donations: progress parse failed: %v", sumErr)
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
