package app

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

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

	info := e.fetchLatestRelease()
	e.updateChecked = time.Now()
	e.updateCached = &info
	return info
}

func (e *Engine) fetchLatestRelease() UpdateInfo {
	info := UpdateInfo{Ok: false, CurrentVersion: e.version}
	repo := strings.TrimSpace(e.cfg.Updates.GitHubRepo)
	if repo == "" {
		info.Notes = "updates.github_repo not configured"
		info.CheckedAt = time.Now().UTC().Format(time.RFC3339)
		return info
	}

	req, err := http.NewRequest("GET", "https://api.github.com/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		info.Notes = err.Error()
		info.CheckedAt = time.Now().UTC().Format(time.RFC3339)
		return info
	}
	req.Header.Set("User-Agent", "stub-engine/"+e.version)

	token := ""
	if e.cfg.Updates.TokenEnv != "" {
		token = strings.TrimSpace(os.Getenv(e.cfg.Updates.TokenEnv))
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		info.Notes = err.Error()
		info.CheckedAt = time.Now().UTC().Format(time.RFC3339)
		return info
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		info.Notes = fmt.Sprintf("github %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
		info.CheckedAt = time.Now().UTC().Format(time.RFC3339)
		return info
	}

	var payload struct {
		TagName     string `json:"tag_name"`
		HtmlURL     string `json:"html_url"`
		PublishedAt string `json:"published_at"`
		Assets      []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
		ZipballURL string `json:"zipball_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		info.Notes = err.Error()
		info.CheckedAt = time.Now().UTC().Format(time.RFC3339)
		return info
	}

	latest := normalizeVersion(payload.TagName)
	info.LatestVersion = latest
	info.PageURL = payload.HtmlURL
	info.PublishedAt = payload.PublishedAt
	info.CheckedAt = time.Now().UTC().Format(time.RFC3339)

	cur := normalizeVersion(e.version)
	info.UpdateAvailable = (latest != "" && latest != cur)

	// Pick an asset ending with AssetSuffix (default .zip). Fall back to zipball_url.
	suffix := e.cfg.Updates.AssetSuffix
	if suffix == "" {
		suffix = ".zip"
	}
	for _, a := range payload.Assets {
		if strings.HasSuffix(strings.ToLower(a.Name), strings.ToLower(suffix)) {
			info.DownloadURL = a.BrowserDownloadURL
			break
		}
	}
	if info.DownloadURL == "" {
		info.DownloadURL = payload.ZipballURL
	}

	info.Ok = true
	return info
}

func (e *Engine) QueueUpdateLatest() error {
	info := e.CheckUpdateCached()
	if !info.Ok {
		return fmt.Errorf("update check failed: %s", info.Notes)
	}
	if !info.UpdateAvailable {
		return fmt.Errorf("no update available")
	}
	if info.DownloadURL == "" {
		return fmt.Errorf("no download url")
	}

	// Download the release zip into the watcher tmp directory. The watcher will deploy it.
	tmpDir := e.cfg.Updates.WatchTmpDir
	if tmpDir == "" {
		tmpDir = "/mnt/NAS/Engineering/Audio Network/Studio B/UI/tmp"
	}
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return err
	}

	name := fmt.Sprintf("StudioB-UI_%s%s", info.LatestVersion, e.cfg.Updates.AssetSuffix)
	if e.cfg.Updates.AssetSuffix == "" {
		name = fmt.Sprintf("StudioB-UI_%s.zip", info.LatestVersion)
	}
	dest := filepath.Join(tmpDir, name)

	req, err := http.NewRequest("GET", info.DownloadURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "stub-engine/"+e.version)
	token := ""
	if e.cfg.Updates.TokenEnv != "" {
		token = strings.TrimSpace(os.Getenv(e.cfg.Updates.TokenEnv))
	}
	if token != "" && strings.Contains(info.DownloadURL, "api.github.com") {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("download %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	// Write atomically.
	tmpFile := dest + ".part"
	f, err := os.Create(tmpFile)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		_ = os.Remove(tmpFile)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpFile)
		return err
	}
	if err := os.Rename(tmpFile, dest); err != nil {
		_ = os.Remove(tmpFile)
		return err
	}
	log.Printf("queued update: %s", dest)
	return nil
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

// Update:
// - mode=git: git pull + reinstall (script-backed)
// - mode=zip: check GitHub releases and drop newest zip into watcher tmp dir
func (e *Engine) Update() {
	if strings.ToLower(strings.TrimSpace(e.cfg.Updates.Mode)) == "git" {
		e.runAdminScript("update")
		return
	}
	if err := e.QueueUpdateLatest(); err != nil {
		log.Printf("update queue failed: %v", err)
	}
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

func (e *Engine) runAdminScript(action string, args ...string) {
	repoDir, _ := os.Getwd()

	var script string
	switch action {
	case "update":
		script = "scripts/admin-update.sh"
	case "rollback":
		script = "scripts/admin-rollback.sh"
	default:
		log.Printf("unknown admin action: %s", action)
		return
	}

	all := append([]string{script}, args...)
	cmd := exec.Command("bash", all...)
	cmd.Dir = repoDir

	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("%s failed: %v\n%s", action, err, string(out))
		return
	}
	log.Printf("%s ok:\n%s", action, string(out))
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
