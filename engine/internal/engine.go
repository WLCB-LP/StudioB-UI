package app

import (
	"net"
	"path/filepath"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"regexp"
)

// NOTE ABOUT UPDATE CHECKS
// -----------------------
// We intentionally support *two* ways to determine the latest version:
//   1) Remote tags via `git ls-remote` (preferred)
//   2) Local tags via `git -C /home/wlcb/devel/StudioB-UI tag` (fallback)
//
// Why the fallback exists:
// - The update *apply* path uses the local repo and may work even when the
//   service (stub-engine) cannot reach the network due to sandboxing.
// - Operators should not see "Update check: failed" if the system already
//   knows the latest tag locally.
//
// This keeps the UI operator-friendly and avoids false alarms.

// Stable RC identifiers (names) used by UI/engine.
// These MUST remain stable; numeric IDs are internal / DSP-level wiring.
var rcNameToID = map[string]int{
	"STUB_SPK_LEVEL":    160,
	"STUB_SPK_MUTE":     161,
	"STUB_SPK_AUTOMUTE": 560,
	"STUB_MIC_HOST":     121,
	"STUB_MIC_GUEST_1":  122,
	"STUB_MIC_GUEST_2":  123,
	"STUB_MIC_GUEST_3":  124,
	"STUB_PGM_L":        411,
	"STUB_PGM_R":        412,
	"STUB_SPK_L":        460,
	"STUB_SPK_R":        461,
	"STUB_RSR_L":        462,
	"STUB_RSR_R":        463,
	// Reserved (not yet implemented): STUB_SPK_TX, STUB_PGM_TX, STUB_RSR_TX, STUB_STUDIO_MODE
}

func resolveRC(idOrName string) (int, error) {
	if id, ok := rcNameToID[idOrName]; ok {
		return id, nil
	}
	id, err := strconv.Atoi(idOrName)
	if err != nil {
		return 0, fmt.Errorf("invalid rc id")
	}
	return id, nil
}

type Engine struct {
	// cfgMu protects access to e.cfg at runtime.
	cfgMu sync.RWMutex
	// v0.2.52 DSP mode transition visibility
	// Timestamp of last successful DSP validation in LIVE mode
	dspValidatedAt time.Time
	// v0.2.55: signature of DSP-relevant config at last LIVE validation
	dspValidatedConfigSig string
	// ------------------------------------------------------------------
	// DSP monitor (v0.2.61)
	//
	// Requirement: UI should always be able to reflect DSP connectivity/state.
	// We run a tiny read-only monitor loop that periodically attempts a bounded
	// TCP connect to the configured DSP host:port and updates dspHealth.
	//
	// SAFETY: This monitor is READ-ONLY (TCP connect only). It does not send
	// any DSP control commands. Control writes are still gated elsewhere.
	// ------------------------------------------------------------------
	dspMonStop chan struct{}
	dspMonMu   sync.Mutex
	dspMonOn   bool
	// Base state directory (written by installer). Used for small, append-only state files.
	stateDir string
	cfg     *Config
	version string

	mu       sync.RWMutex
	rc       map[int]float64
	lastSent map[int]float64

	upgrader websocket.Upgrader

	clientsMu sync.Mutex
	clients   map[*websocket.Conn]bool

	updateMu      sync.Mutex
	updateCached  *UpdateInfo
	updateChecked time.Time

	// adminUpdateMu guards adminUpdateStatus (last update-from-UI attempt).
	adminUpdateMu     sync.Mutex
	adminUpdateStatus AdminUpdateStatus
// v0.2.47 DSP health / guard state (defense-in-depth)
// IMPORTANT:
// - These values are updated ONLY by explicit operator-triggered tests.
// - There is NO background polling in this phase.
dspOnce sync.Once
dspMu   sync.Mutex
dsp     *dspHealth
}

// WatchdogStatus describes the current systemd status of stub-ui-watchdog.
// Read-only; safe to expose without admin privileges.
type WatchdogStatus struct {
	Ok        bool   `json:"ok"`
	Enabled   string `json:"enabled"` // enabled|disabled|static|masked|unknown
	Active    string `json:"active"`  // active|inactive|failed|unknown
	CheckedAt string `json:"checkedAt"`
	Notes     string `json:"notes,omitempty"`

	// v0.2.40 visibility-only fields:
	// These are pulled from systemd and shown *verbatim* in the UI so operators can
	// quickly see what systemd thinks is happening without SSH.
	// Example (from `systemctl status stub-ui-watchdog`):
	//   "Active: active (running) since Tue 2026-01-03 10:00:00 CST; 2h ago"
	SystemdActiveLine  string `json:"systemdActiveLine,omitempty"`
	// Example (from `systemctl show -p SubState stub-ui-watchdog`):
	//   "SubState=running"
	SystemdSubStateLine string `json:"systemdSubStateLine,omitempty"`
}


// AdminUpdateStatus tracks the last update-from-UI attempt.
// This is safe to expose because it only contains installer output (already visible via journal).
type AdminUpdateStatus struct {
	Ok         bool   `json:"ok"`
	Running    bool   `json:"running"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
	Error      string `json:"error,omitempty"`
	OutputTail string `json:"outputTail,omitempty"`
}

// StudioStatus is a UI-friendly snapshot for the Studio page.
// Values are normalized 0.0..1.0 for v1.
// RC mapping (future DSP integration):
//
//	Speaker Level: RC 160
//	Speaker Mute:  RC 161
//	Auto-mute:     RC 560 (read-only)
//	Meters:        411/412 (program), 460/461 (speakers), 462/463 (remote return)
type StudioStatus struct {
	Ok      bool   `json:"ok"`
	Time    string `json:"ts"`
	Version string `json:"version"`
	Mode    string `json:"mode"`
	Speaker struct {
		Level    float64 `json:"level"`
		Mute     bool    `json:"mute"`
		AutoMute bool    `json:"automute"`
	} `json:"speaker"`
	Meters struct {
		SpkL float64 `json:"spkL"`
		SpkR float64 `json:"spkR"`
		PgmL float64 `json:"pgmL"`
		PgmR float64 `json:"pgmR"`
		RsrL float64 `json:"rsrL"`
		RsrR float64 `json:"rsrR"`
	} `json:"meters"`
}

func NewEngine(cfg *Config, version string) *Engine {
	e := &Engine{
		cfg:      cfg,
		version:  version,
		rc:       make(map[int]float64),
		lastSent: make(map[int]float64),
		upgrader: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
		clients:  make(map[*websocket.Conn]bool),
	}

	// v0.2.48: derive stateDir from the config YAML path.
	// Installer creates: /home/wlcb/.StudioB-UI/state
	// Config lives at:   /home/wlcb/.StudioB-UI/config/config.yml
	// We compute baseDir = parent(parent(YAMLPath)) and then stateDir = baseDir/state.
	// This keeps behavior explicit and avoids hidden magic.
	if cfg != nil && cfg.Meta.YAMLPath != "" {
		p := cfg.Meta.YAMLPath
		// Best-effort absolute path.
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		base := filepath.Dir(filepath.Dir(p))
		e.stateDir = filepath.Join(base, "state")
	}

	// Initialize known RCs to sane defaults
	for _, id := range cfg.RCAllowlist {
		e.rc[id] = 0
		e.lastSent[id] = math.NaN()
	}

	// Friendly defaults for v1 UI
	if e.allowed(160) {
		e.rc[rcNameToID["STUB_SPK_LEVEL"]] = 0.75
	}
	if e.allowed(161) {
		e.rc[161] = 0
	}
	if e.allowed(560) {
		e.rc[560] = 0
	}

	// Start mock meter generator and publisher
	go e.mockLoop()
	go e.publishLoop()
	go e.dspMonitorLoop()

	return e
}

func (e *Engine) Version() string { return e.version }

func (e *Engine) allowed(id int) bool {
	for _, v := range e.cfg.RCAllowlist {
		if v == id {
			return true
		}
	}
	return false
}

func (e *Engine) SetRC(idStr string, value float64) error {
	id, err := resolveRC(idStr)
	if err != nil {
		return err
	}
	if !e.allowed(id) {
		return fmt.Errorf("rc %d not allowlisted", id)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.rc[id] = value
	return nil
}

func (e *Engine) StateSnapshot() map[string]any {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := map[string]any{
		"version": e.version,
		"rc":      e.rc,
		"time":    time.Now().UTC().Format(time.RFC3339),
	}
	return out
}

// StudioStatusSnapshot returns a stable schema snapshot for the Studio UI.
// This is intentionally separate from /api/state (debug) so the UI can depend on it.
func (e *Engine) StudioStatusSnapshot() StudioStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var s StudioStatus
	s.Ok = true
	s.Time = time.Now().UTC().Format(time.RFC3339)
	s.Version = e.version
	s.Mode = e.cfg.DSP.Mode

	// Controls
	s.Speaker.Level = e.rc[rcNameToID["STUB_SPK_LEVEL"]]
	s.Speaker.Mute = e.rc[rcNameToID["STUB_SPK_MUTE"]] >= 0.5
	s.Speaker.AutoMute = e.rc[rcNameToID["STUB_SPK_AUTOMUTE"]] >= 0.5

	// Meters
	s.Meters.PgmL = e.rc[rcNameToID["STUB_PGM_L"]]
	s.Meters.PgmR = e.rc[rcNameToID["STUB_PGM_R"]]
	s.Meters.SpkL = e.rc[rcNameToID["STUB_SPK_L"]]
	s.Meters.SpkR = e.rc[rcNameToID["STUB_SPK_R"]]
	s.Meters.RsrL = e.rc[rcNameToID["STUB_RSR_L"]]
	s.Meters.RsrR = e.rc[rcNameToID["STUB_RSR_R"]]

	return s
}

func (e *Engine) HandleWS(w http.ResponseWriter, r *http.Request) {
	c, err := e.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	e.clientsMu.Lock()
	e.clients[c] = true
	e.clientsMu.Unlock()

	// Send immediate snapshot
	_ = c.WriteJSON(map[string]any{"type": "snapshot", "data": e.StateSnapshot()})

	// Keep alive / read pump
	go func() {
		defer func() {
			e.clientsMu.Lock()
			delete(e.clients, c)
			e.clientsMu.Unlock()
			_ = c.Close()
		}()
		for {
			_, _, err := c.ReadMessage()
			if err != nil {
				return
			}
		}
	}()
}

func (e *Engine) broadcast(v any) {
	b, _ := json.Marshal(v)
	e.clientsMu.Lock()
	defer e.clientsMu.Unlock()
	for c := range e.clients {
		_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if err := c.WriteMessage(websocket.TextMessage, b); err != nil {
			_ = c.Close()
			delete(e.clients, c)
		}
	}
}

func (e *Engine) publishLoop() {
	ticker := time.NewTicker(time.Second / time.Duration(e.cfg.Meters.PublishHz))
	defer ticker.Stop()

	for range ticker.C {
		e.mu.Lock()
		delta := make(map[int]float64)
		for id, val := range e.rc {
			last := e.lastSent[id]
			if math.IsNaN(last) || math.Abs(val-last) >= e.cfg.Meters.Deadband {
				delta[id] = val
				e.lastSent[id] = val
			}
		}
		e.mu.Unlock()

		if len(delta) > 0 {
			e.broadcast(map[string]any{"type": "delta", "rc": delta, "t": time.Now().UnixMilli()})
		}
	}
}

// Mock loop generates plausible meter motion for v1 UI testing.
func (e *Engine) mockLoop() {
	rand.Seed(time.Now().UnixNano())
	for {
		e.mu.Lock()
		// meters: 411/412 program, 460/461 speakers, 462/463 rs return
		meterIDs := []int{411, 412, 460, 461, 462, 463}
		for _, id := range meterIDs {
			// random walk
			cur := e.rc[id]
			step := (rand.Float64() - 0.5) * 0.15
			next := cur + step
			if next < 0 {
				next = 0
			}
			if next > 1 {
				next = 1
			}
			e.rc[id] = next
		}
		// indicator 560 toggles occasionally
		if rand.Intn(200) == 0 {
			if e.rc[560] < 0.5 {
				e.rc[560] = 1
			} else {
				e.rc[560] = 0
			}
		}
		e.mu.Unlock()

		time.Sleep(50 * time.Millisecond)
	}
}

// UpdateInfo describes the latest available release (as seen from GitHub).
type UpdateInfo struct {
	Ok              bool   `json:"ok"`
	CurrentVersion  string `json:"currentVersion"`
	LatestVersion   string `json:"latestVersion"`
	UpdateAvailable bool   `json:"updateAvailable"`
	CheckedAt       string `json:"checkedAt"`
	PublishedAt     string `json:"publishedAt,omitempty"`
	PageURL         string `json:"pageUrl,omitempty"`
	DownloadURL     string `json:"downloadUrl,omitempty"`
	Notes           string `json:"notes,omitempty"`
}

// Operator-safe reconnect (stub for v1)
func (e *Engine) Reconnect() {
	log.Printf("reconnect requested (mode=%s)", e.cfg.DSP.Mode)
}

// ReloadConfig reloads the YAML config and re-applies JSON/env overrides.
//
// This is intentionally conservative: it only changes in-memory config.
// It does NOT restart services and does NOT touch runtime releases.
func (e *Engine) ReloadConfig() error {
	// NOTE: called by Engineering UI after saving config.yml.
	// We want the running engine to reflect file changes immediately.
	//
	// IMPORTANT: e.cfg is protected by cfgMu (not e.mu). e.mu is for other
	// runtime state (meters, RC cache, websocket clients, etc.).
	e.cfgMu.RLock()
	yamlPath := ""
	if e.cfg != nil {
		yamlPath = strings.TrimSpace(e.cfg.Meta.YAMLPath)
	}
	e.cfgMu.RUnlock()

	if yamlPath == "" {
		return fmt.Errorf("cannot reload config: YAML path unknown")
	}

	newCfg, err := LoadConfig(yamlPath)
	if err != nil {
		return err
	}

	e.cfgMu.Lock()
	e.cfg = newCfg
	e.cfgMu.Unlock()

	log.Printf("config reloaded (mode=%s dsp=%s:%d)", newCfg.DSP.Mode, newCfg.DSP.Host, newCfg.DSP.Port)
	return nil
}


func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	return v
}

func (e *Engine) CheckUpdateCached() UpdateInfo {
	// Cache results for ~60s to avoid GitHub rate limits.
	e.updateMu.Lock()
	defer e.updateMu.Unlock()

	if e.updateCached != nil && time.Since(e.updateChecked) < 60*time.Second {
		c := *e.updateCached
		c.CheckedAt = time.Now().UTC().Format(time.RFC3339)
		return c
	}

	info := e.fetchLatestTag()
	e.updateChecked = time.Now()
	e.updateCached = &info
	return info
}

func (e *Engine) fetchLatestTag() UpdateInfo {
	info := UpdateInfo{Ok: false, CurrentVersion: e.version}
	repo := strings.TrimSpace(e.cfg.Updates.GitHubRepo)
	if repo == "" {
		info.Notes = "updates.github_repo not configured"
		info.CheckedAt = time.Now().UTC().Format(time.RFC3339)
		return info
	}

	// We intentionally avoid GitHub Releases/zipball logic. Source of truth is git tags.
	remote := "https://github.com/" + repo + ".git"

	cmd := exec.Command("git", "ls-remote", "--tags", "--refs", remote)
	out, err := cmd.Output()
	if err != nil {
		// Remote check failed. Fall back to local tags from the on-disk repo.
		// This is *not* a perfect replacement for a remote check, but it is
		// better than reporting "failed" when the system has enough info
		// locally to say "up to date".
		latestTag, lerr := latestLocalTag("/home/wlcb/devel/StudioB-UI")
		if lerr != nil {
			info.Notes = err.Error()
			info.CheckedAt = time.Now().UTC().Format(time.RFC3339)
			return info
		}
		applyLatest(&info, repo, latestTag)
		info.Ok = true
		info.Notes = "offline: using local tags"
		info.CheckedAt = time.Now().UTC().Format(time.RFC3339)
		return info
	}

	tags := []string{}
	re := regexp.MustCompile(`^refs/tags/v(\d+)\.(\d+)\.(\d+)$`)
	for _, line := range splitLines(string(out)) {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		ref := fields[1]
		m := re.FindStringSubmatch(ref)
		if m != nil {
			// keep the full semver string without leading refs/tags/
			tags = append(tags, "v"+m[1]+"."+m[2]+"."+m[3])
		}
	}

	if len(tags) == 0 {
		// Remote worked, but no semver tags were found. Fall back to local tags.
		latestTag, lerr := latestLocalTag("/home/wlcb/devel/StudioB-UI")
		if lerr != nil {
			info.Notes = "no semver tags found"
			info.CheckedAt = time.Now().UTC().Format(time.RFC3339)
			return info
		}
		applyLatest(&info, repo, latestTag)
		info.Ok = true
		info.Notes = "remote had no semver tags; using local tags"
		info.CheckedAt = time.Now().UTC().Format(time.RFC3339)
		return info
	}

	latestTag := latestSemverTag(tags)
	applyLatest(&info, repo, latestTag)
	info.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	info.Ok = true
	return info
}

// applyLatest fills the common fields of UpdateInfo based on a semver tag.
func applyLatest(info *UpdateInfo, repo string, latestTag string) {
	latest := normalizeVersion(latestTag)
	info.LatestVersion = latest
	info.UpdateAvailable = normalizeVersion(info.CurrentVersion) != latest
	info.PageURL = "https://github.com/" + repo
}

// latestLocalTag returns the latest semver tag from a local git repo.
// This is intentionally "read-only" and does not perform any fetch.
func latestLocalTag(repoPath string) (string, error) {
	cmd := exec.Command("git", "-C", repoPath, "tag", "--list", "v*.*.*")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	tags := []string{}
	for _, l := range splitLines(string(out)) {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		// Filter to strict semver tags only.
		if regexp.MustCompile(`^v\d+\.\d+\.\d+$`).MatchString(l) {
			tags = append(tags, l)
		}
	}
	if len(tags) == 0 {
		return "", fmt.Errorf("no semver tags in local repo")
	}
	return latestSemverTag(tags), nil
}

// latestSemverTag returns the latest tag (highest semver) from a slice.
// Tags must be formatted like vMAJOR.MINOR.PATCH.
func latestSemverTag(tags []string) string {
	// Sort tags by semver ascending, take last as latest.
	sort.Slice(tags, func(i, j int) bool {
		ai := strings.TrimPrefix(tags[i], "v")
		aj := strings.TrimPrefix(tags[j], "v")
		as := strings.Split(ai, ".")
		bs := strings.Split(aj, ".")
		atoi := func(s string) int {
			n := 0
			for _, ch := range s {
				n = n*10 + int(ch-'0')
			}
			return n
		}
		amj, ami, apt := atoi(as[0]), atoi(as[1]), atoi(as[2])
		bmj, bmi, bpt := atoi(bs[0]), atoi(bs[1]), atoi(bs[2])
		if amj != bmj {
			return amj < bmj
		}
		if ami != bmi {
			return ami < bmi
		}
		return apt < bpt
	})
	return tags[len(tags)-1]
}

func (e *Engine) QueueUpdateLatest() error {
	return fmt.Errorf("zip-based runtime updates are disabled; use git-based install workflow")
}

// Admin auth via X-Admin-PIN header
func (e *Engine) CheckAdmin(r *http.Request) bool {
	got := r.Header.Get("X-Admin-PIN")
	want := e.cfg.Admin.PIN
	if want == "" {
		want = "CHANGE_ME"
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// Update (git-based only): runs the admin update script.
// UpdateSync runs the admin update script and returns combined output.
//
// IMPORTANT: This function blocks until the update attempt completes.
// The UI uses this to accurately report success/failure instead of assuming
// an update "probably" succeeded.
func (e *Engine) UpdateSync() (string, error) {
	// NOTE: runAdminScriptWithResult() expects a logical *action* key ("update"),
	// not the underlying script filename ("admin-update.sh").
	// Passing the filename causes the engine to reject the action and the UI update
	// will fail with: "unknown admin action: admin-update.sh".
	return e.runAdminScriptWithResult("update")
}

func (e *Engine) Update() {
	// Start an async update run, recording status so the UI can poll.
	e.adminUpdateMu.Lock()
	if e.adminUpdateStatus.Running {
		e.adminUpdateMu.Unlock()
		return
	}
	// Stored as RFC3339 string to keep the JSON payload simple and predictable.
	e.adminUpdateStatus = AdminUpdateStatus{Running: true, StartedAt: time.Now().Format(time.RFC3339)}
	e.adminUpdateMu.Unlock()

	go func() {
		out, err := e.UpdateSync()

		e.adminUpdateMu.Lock()
		st := e.adminUpdateStatus
		st.Running = false
		// Stored as RFC3339 string to keep the JSON payload simple and predictable.
		st.FinishedAt = time.Now().Format(time.RFC3339)
		if err != nil {
			st.Ok = false
			st.Error = err.Error()
			st.OutputTail = tailLines(out, 80)
		} else {
			st.Ok = true
			st.Error = ""
			st.OutputTail = tailLines(out, 40)
		}
		e.adminUpdateStatus = st
		e.adminUpdateMu.Unlock()
	}()
}

// GetUpdateStatus returns the last update-from-UI status snapshot.
func (e *Engine) GetUpdateStatus() AdminUpdateStatus {
	e.adminUpdateMu.Lock()
	defer e.adminUpdateMu.Unlock()
	return e.adminUpdateStatus
}

// Rollback: checkout tag + reinstall
func (e *Engine) Rollback(version string) {
	e.runAdminScript("rollback", version)
}

func (e *Engine) ListReleases() []string {
	// List release directories under /opt/studiob-ui/releases (newest first).
	releasesDir := "/opt/studiob-ui/releases"
	entries, err := os.ReadDir(releasesDir)
	if err == nil {
		names := []string{}
		for _, ent := range entries {
			if ent.IsDir() {
				names = append(names, ent.Name())
			}
		}
		// Sort reverse lexicographically (stamp prefix makes this newest-first).
		sort.Slice(names, func(i, j int) bool { return names[i] > names[j] })
		if len(names) > 0 {
			if len(names) > 50 {
				return names[:50]
			}
			return names
		}
	}
	// Fallback: git tags if running from repo.
	repoDir, _ := os.Getwd()
	cmd := exec.Command("bash", "-lc", "git tag --sort=-creatordate 2>/dev/null | head -n 20")
	cmd.Dir = repoDir
	out, err2 := cmd.Output()
	if err2 == nil {
		lines := []string{}
		for _, l := range splitLines(string(out)) {
			if l != "" {
				lines = append(lines, l)
			}
		}
		return lines
	}
	return []string{}
}

// runAdminScriptWithResult executes one of our whitelisted admin scripts via sudo
// and returns the combined stdout/stderr output.
//
// This allows the API layer to report actionable errors to the UI (e.g. missing
// NOPASSWD rules, missing systemd units, etc.).
func (e *Engine) runAdminScriptWithResult(action string, args ...string) (string, error) {
	repoDir, _ := os.Getwd()

	var script string
	switch action {
	case "update":
		script = "scripts/admin-update.sh"
	case "rollback":
		script = "scripts/admin-rollback.sh"
	case "watchdog-start":
		script = "scripts/admin-watchdog-start.sh"
	default:
		log.Printf("unknown admin action: %s", action)
		return "", fmt.Errorf("unknown admin action: %s", action)
	}

	all := append([]string{script}, args...)

	// IMPORTANT:
	// Admin scripts must be executed with elevated privileges so they can:
	// - restart system services (systemctl)
	// - update /etc/nginx/* and reload nginx
	// - write /etc/sudoers.d/*
	//
	// The engine runs as an unprivileged user, so we invoke them via sudo in
	// non-interactive mode. install_full.sh provisions a sudoers rule that
	// allows these specific scripts to run without a password.
	// NOTE (field issue / future-proofing):
	// We avoid mixing fixed arguments + a slice expansion directly in the
	// exec.Command() call.
	//
	// In the field we hit a compile-time error at this call site:
	//   "too many arguments in call to exec.Command"
	// Even though `exec.Command("sudo", "-n", "bash", all...)` is normally valid.
	// Building the full arg slice first is unambiguous and keeps the code
	// stable across toolchain versions.
	cmdArgs := append([]string{"-n", "bash"}, all...)
	cmd := exec.Command("sudo", cmdArgs...)
	cmd.Dir = repoDir

	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runAdminScript is the legacy fire-and-forget wrapper used by older code.
// It logs the output and returns no error.
func (e *Engine) runAdminScript(action string, args ...string) {
	out, err := e.runAdminScriptWithResult(action, args...)
	if err != nil {
		log.Printf("%s failed: %v\n%s", action, err, out)
		return
	}
	log.Printf("%s ok:\n%s", action, out)
}

func splitLines(s string) []string {
	res := []string{}
	cur := ""
	for _, r := range s {
		if r == '\n' || r == '\r' {
			if cur != "" {
				res = append(res, cur)
				cur = ""
			}
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		res = append(res, cur)
	}
	return res
}

func runCmdTimeout(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("command timed out: %s", name)
	}
	return string(out), err
}

// WatchdogStatusSnapshot returns the current systemd status of stub-ui-watchdog.
func (e *Engine) WatchdogStatusSnapshot() WatchdogStatus {
	s := WatchdogStatus{CheckedAt: time.Now().UTC().Format(time.RFC3339)}

	// systemctl is-enabled returns... Exit code !=0 for disabled/masked etc, so we parse output.
	enabledOut, _ := runCmdTimeout(2*time.Second, "systemctl", "is-enabled", "stub-ui-watchdog")
	enabled := strings.TrimSpace(enabledOut)
	if enabled == "" {
		enabled = "unknown"
	}

	activeOut, _ := runCmdTimeout(2*time.Second, "systemctl", "is-active", "stub-ui-watchdog")
	active := strings.TrimSpace(activeOut)
	if active == "" {
		active = "unknown"
	}

	s.Enabled = enabled
	s.Active = active
	s.Ok = true

	// v0.2.40: capture systemd's human-readable Active line and the SubState.
	// We show these *verbatim* in the UI so operators can copy/paste and compare
	// with `systemctl status` output.
	if statusOut, _ := runCmdTimeout(2*time.Second, "systemctl", "status", "stub-ui-watchdog", "--no-pager"); statusOut != "" {
		for _, line := range strings.Split(statusOut, "\n") {
			// systemctl status lines are indented; we look for the first line containing "Active:".
			if strings.Contains(line, "Active:") {
				s.SystemdActiveLine = strings.TrimSpace(line)
				break
			}
		}
	}
	if subOut, _ := runCmdTimeout(2*time.Second, "systemctl", "show", "-p", "SubState", "stub-ui-watchdog"); subOut != "" {
		// Keep the raw key=value line so it's truly "verbatim".
		s.SystemdSubStateLine = strings.TrimSpace(strings.Split(subOut, "\n")[0])
	}

	if enabled == "disabled" && active == "inactive" {
		s.Notes = "Watchdog is installed but disabled."
	} else if enabled == "enabled" && active != "active" {
		s.Notes = "Watchdog is enabled but not running. You can start it from Engineering."
	}
	return s
}

// StartWatchdog requests a start of the watchdog service via a controlled admin script.
//
// NOTE: This is intentionally asynchronous for backward compatibility with older
// UI behavior, but it provides no feedback to the caller.
// Prefer StartWatchdogSync for API handlers.
func (e *Engine) StartWatchdog() { go e.runAdminScript("watchdog-start") }

// StartWatchdogSync starts/enables the watchdog and returns the command output.
// Use this from API handlers so the UI can display errors immediately.
func (e *Engine) StartWatchdogSync() (string, error) {
	return e.runAdminScriptWithResult("watchdog-start")
}

// tailLines returns the last N lines from a big string.
func tailLines(s string, n int) string {
	if n <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}


// DSPModeStatus is returned to the UI for transition warnings.
// DSPModeStatus is returned to the UI for transition warnings.
type DSPModeStatus struct {
    Mode          string `json:"mode"`
    Host          string `json:"host,omitempty"`
    Port          int    `json:"port,omitempty"`
    Validated     bool   `json:"validated"`
    ValidatedAt   string `json:"validatedAt,omitempty"`
    ConfigChanged bool   `json:"configChanged"`
}

func (e *Engine) DSPModeStatus() DSPModeStatus {
    cfg := e.GetConfigCopy()
    mode := strings.ToLower(strings.TrimSpace(cfg.DSP.Mode))
    host := strings.TrimSpace(cfg.DSP.Host)
    port := cfg.DSP.Port

    validated := false
    var ts string
    if mode == "live" && !e.dspValidatedAt.IsZero() {
        validated = true
        ts = e.dspValidatedAt.UTC().Format(time.RFC3339)
    }

    // configChanged is meaningful primarily in LIVE mode.
    // If we have never validated, we treat it as changed=false (banner already covers unvalidated).
    changed := false
    if validated {
        curSig := e.dspConfigSignature()
        if strings.TrimSpace(e.dspValidatedConfigSig) != "" && curSig != e.dspValidatedConfigSig {
            changed = true
        }
    }

    return DSPModeStatus{
        Mode:          mode,
        Host:          host,
        Port:          port,
        Validated:     validated,
        ValidatedAt:   ts,
        ConfigChanged: changed,
    }
}


// dspConfigSignature creates a small, stable string representing the DSP-relevant config.
// We keep it explicit and easy to reason about.
func (e *Engine) dspConfigSignature() string {
    // Use a snapshot to avoid races.
    c := e.GetConfigCopy()
    mode := strings.ToLower(strings.TrimSpace(c.DSP.Mode))
    host := strings.TrimSpace(c.DSP.Host)
    port := c.DSP.Port
    return mode + "|" + host + "|" + itoa(port)
}





// ---------------------------------------------------------------------------
// Runtime config application (v0.2.59)
//
// We keep this conservative and explicit.
// - The running engine historically used e.cfg as a *Config pointer.
// - The Engineering UI can save config.yml, but operators expect the running engine
//   to reflect the new mode/host/port immediately (without a restart).
//
// This adds two small helpers:
// - GetConfigCopy(): returns a safe by-value snapshot for readers.
// - ApplyConfig():  swaps the engine config pointer after validation + disk write.
//
// IMPORTANT SAFETY:
// - If DSP-relevant config changes, we CLEAR validation state and set DSP health to UNKNOWN.
// - We do NOT auto-test the DSP. Operator still must click "Test DSP Now".
// - No polling is introduced.
// ---------------------------------------------------------------------------

// GetConfigCopy returns a by-value snapshot of the current config.
// Callers can read fields safely without holding locks.
func (e *Engine) GetConfigCopy() Config {
    e.cfgMu.RLock()
    defer e.cfgMu.RUnlock()
    if e.cfg == nil {
        return Config{}
    }
    return *e.cfg
}

// dspConfigSignatureFrom creates a small stable string representing DSP-relevant config.
func dspConfigSignatureFrom(cfg *Config) string {
    if cfg == nil {
        return ""
    }
    mode := strings.ToLower(strings.TrimSpace(cfg.DSP.Mode))
    host := strings.TrimSpace(cfg.DSP.Host)
    port := cfg.DSP.Port
    return mode + "|" + host + "|" + itoa(port)
}

// ApplyConfig updates the running engine's config pointer in-memory.
// The config MUST already be validated and written to disk by the handler.
func (e *Engine) ApplyConfig(newCfg *Config) {
    // Determine whether DSP-relevant config changed (compare old vs new signatures).
    e.cfgMu.RLock()
    oldSig := dspConfigSignatureFrom(e.cfg)
    e.cfgMu.RUnlock()

    newSig := dspConfigSignatureFrom(newCfg)

    e.cfgMu.Lock()
    e.cfg = newCfg
    e.cfgMu.Unlock()

    if oldSig != newSig {
        // Clear validation + set state UNKNOWN so operator is prompted to validate in LIVE mode.
        e.ensureDSPHealthInit()
        e.dspMu.Lock()
        e.dspValidatedAt = time.Time{}
        e.dspValidatedConfigSig = ""
        e.dsp.state = DSPHealthUnknown
        e.dsp.lastErr = ""
        e.dsp.failures = 0
        e.dsp.lastTestAt = time.Time{}
        e.dspMu.Unlock()
    }
}