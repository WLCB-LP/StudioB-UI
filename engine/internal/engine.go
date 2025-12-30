package app

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sort"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Engine struct {
	cfg     *Config
	version string

	mu       sync.RWMutex
	rc       map[int]float64
	lastSent map[int]float64

	upgrader websocket.Upgrader

	clientsMu sync.Mutex
	clients   map[*websocket.Conn]bool
}

// StudioStatus is a UI-friendly snapshot for the Studio page.
// Values are normalized 0.0..1.0 for v1.
// RC mapping (future DSP integration):
//   Speaker Level: RC 160
//   Speaker Mute:  RC 161
//   Auto-mute:     RC 560 (read-only)
//   Meters:        411/412 (program), 460/461 (speakers), 462/463 (remote return)
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
		e.rc[160] = 0.75
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
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return fmt.Errorf("invalid rc id")
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
	s.Speaker.Level = e.rc[160]
	s.Speaker.Mute = e.rc[161] >= 0.5
	s.Speaker.AutoMute = e.rc[560] >= 0.5

	// Meters
	s.Meters.PgmL = e.rc[411]
	s.Meters.PgmR = e.rc[412]
	s.Meters.SpkL = e.rc[460]
	s.Meters.SpkR = e.rc[461]
	s.Meters.RsrL = e.rc[462]
	s.Meters.RsrR = e.rc[463]

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

// Operator-safe reconnect (stub for v1)
func (e *Engine) Reconnect() {
	log.Printf("reconnect requested (mode=%s)", e.cfg.DSP.Mode)
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

// Update: git pull + reinstall (script-backed)
func (e *Engine) Update() {
	e.runAdminScript("update")
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
