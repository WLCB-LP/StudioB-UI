package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ConfigMeta captures where each key came from (defaults vs file vs env).
// This is used for transparency/debugging via /api/config and MUST NOT affect behavior.
type ConfigMeta struct {
	LoadedAt string `json:"loaded_at"`
	YAMLPath string `json:"yaml_path,omitempty"`
	JSONPath string `json:"json_path,omitempty"`

	ModeSource    string `json:"mode_source,omitempty"`     // default|yaml|json|env
	DSPHostSource string `json:"dsp_host_source,omitempty"` // default|yaml|json|env
	DSPPortSource string `json:"dsp_port_source,omitempty"` // default|yaml|json|env

	EnvUsed  map[string]string `json:"env_used,omitempty"` // only includes keys we consumed
	Warnings []string          `json:"warnings,omitempty"`
}

type Config struct {
	// Mode is a legacy top-level field from early versions of the project.
	//
	// It is intentionally kept for backwards compatibility with older
	// config.json / config editor code paths. The authoritative write mode
	// is dsp.mode.
	//
	// NOTE: The engine does not use this value for behavior.
	Mode string `yaml:"mode,omitempty" json:"mode,omitempty"`

	DSP struct {
		Host string `yaml:"host"`
		Port int    `yaml:"port"`
		Mode string `yaml:"mode"` // "mock" for v1
	} `yaml:"dsp"`

	UI struct {
		HTTPListen    string `yaml:"http_listen"`
		PublicBaseURL string `yaml:"public_base_url"`
	} `yaml:"ui"`

	Admin struct {
		PIN string `yaml:"pin"`
	} `yaml:"admin"`

	Meters struct {
		PublishHz int     `yaml:"publish_hz"`
		Deadband  float64 `yaml:"deadband"`
	} `yaml:"meters"`

	Updates struct {
		Mode        string `yaml:"mode"`          // "zip" (default) or "git"
		GitHubRepo  string `yaml:"github_repo"`   // e.g. "WLCB/StudioB-UI"
		AssetSuffix string `yaml:"asset_suffix"`  // e.g. ".zip" (default)
		WatchTmpDir string `yaml:"watch_tmp_dir"` // where to drop downloaded zips for the watcher
		TokenEnv    string `yaml:"token_env"`     // env var name holding GitHub token (optional)
	} `yaml:"updates"`

	RCAllowlist []int `yaml:"rc_allowlist"`

	// Meta is not loaded from YAML; it is populated by LoadConfig() for debugging.
	Meta ConfigMeta `yaml:"-" json:"-"`
}

func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	// Populate metadata (sources) for transparency. This MUST NOT change behavior.
	cfg.Meta = ConfigMeta{
		LoadedAt: time.Now().UTC().Format(time.RFC3339),
		YAMLPath: path,
		EnvUsed:  map[string]string{},
	}
	// If the YAML file provided these values, mark their source now.
	if cfg.DSP.Mode != "" {
		cfg.Meta.ModeSource = "yaml"
	}
	if cfg.DSP.Host != "" {
		cfg.Meta.DSPHostSource = "yaml"
	}
	if cfg.DSP.Port != 0 {
		cfg.Meta.DSPPortSource = "yaml"
	}
	if cfg.UI.HTTPListen == "" {
		cfg.UI.HTTPListen = "127.0.0.1:8787"
	}
	if cfg.DSP.Mode == "" {
		cfg.DSP.Mode = "mock"
	}
	if cfg.Meters.PublishHz <= 0 {
		cfg.Meters.PublishHz = 20
	}
	if cfg.Meters.Deadband <= 0 {
		cfg.Meters.Deadband = 0.01
	}
	if cfg.Admin.PIN == "" {
		cfg.Admin.PIN = "CHANGE_ME"
	}

	if cfg.Updates.Mode == "" {
		cfg.Updates.Mode = "git"
	}
	if cfg.Updates.GitHubRepo == "" {
		cfg.Updates.GitHubRepo = "WLCB-LP/StudioB-UI"
	}
	if cfg.Updates.AssetSuffix == "" {
		cfg.Updates.AssetSuffix = ".zip"
	}
	if cfg.Updates.WatchTmpDir == "" {
		cfg.Updates.WatchTmpDir = "/mnt/NAS/Engineering/Audio Network/Studio B/UI/tmp"
	}
	if cfg.Updates.TokenEnv == "" {
		cfg.Updates.TokenEnv = "GITHUB_TOKEN"
	}
	// Apply optional JSON config (~/.StudioB-UI/config.json) and env overrides.
	//
	// IMPORTANT (v0.2.90+): historically we treated config.json as "persistent UI edits" and
	// allowed it to override the YAML file. That works fine *when config.json is current*, but it
	// becomes dangerous if a user/admin updates config.v1 directly (or via installer) and an older
	// config.json remains on disk — the engine would silently keep running the stale JSON values.
	//
	// To make this safe and self-healing:
	//   - If config.v1 exists and is NEWER than config.json, YAML wins.
	//   - When YAML wins, we best-effort sync config.json to match YAML (so future UI edits start
	//     from the correct baseline).
	//
	// JSON overrides are intentionally shallow and only cover the "mode" + DSP connection fields
	// for v0.2.x.
	// NOTE: The caller passes the YAML config path in via `path`.
	// We use that same path when deciding whether config.json overrides
	// are applicable (newer than the YAML, etc.).
	applyJSONOverrides(&cfg, path)
	applyEnvOverrides(&cfg)

	// Backward compatibility:
	// Some earlier releases briefly wrote the requested mode to the deprecated
	// top-level `mode` field, instead of dsp.mode. If we see that situation, we
	// treat the top-level value as authoritative and migrate it in-memory.
	//
	// NOTE: We do NOT delete the legacy field here because it lives in the YAML
	// file on disk — but once the user re-saves via the UI (or updates via a
	// newer release), both fields will be kept in sync.
	if strings.TrimSpace(cfg.DSP.Mode) == "" && strings.TrimSpace(cfg.Mode) != "" {
		cfg.DSP.Mode = cfg.Mode
		if cfg.Meta.ModeSource == "" {
			cfg.Meta.ModeSource = "yaml-legacy"
		}
		cfg.Meta.Warnings = append(cfg.Meta.Warnings, "config uses deprecated top-level 'mode'; treating it as dsp.mode")
	}

	// If mode is still unset for any reason, default to mock (safe).
	if strings.TrimSpace(cfg.DSP.Mode) == "" {
		cfg.DSP.Mode = "mock"
		if cfg.Meta.ModeSource == "" {
			cfg.Meta.ModeSource = "default"
		}
	}
	// Normalize/validate mode.
	cfg.DSP.Mode = strings.ToLower(strings.TrimSpace(cfg.DSP.Mode))
	switch cfg.DSP.Mode {
	case "mock", "live":
		// ok
	default:
		cfg.Meta.Warnings = append(cfg.Meta.Warnings, fmt.Sprintf("invalid STUDIOB_UI_MODE %q; forcing mock", cfg.DSP.Mode))
		cfg.DSP.Mode = "mock"
		cfg.Meta.ModeSource = "default"
	}

	// Backfill sources if a value exists but we never tagged it.
	if cfg.DSP.Host != "" && cfg.Meta.DSPHostSource == "" {
		cfg.Meta.DSPHostSource = "yaml"
	}
	if cfg.DSP.Port != 0 && cfg.Meta.DSPPortSource == "" {
		cfg.Meta.DSPPortSource = "yaml"
	}
	if len(cfg.RCAllowlist) == 0 {
		return nil, fmt.Errorf("rc_allowlist is empty")
	}
	return &cfg, nil
}

// NOTE: helper functions below intentionally avoid external dependencies and do not
// change behavior unless the user explicitly configures mock/live.

func applyJSONOverrides(cfg *Config, yamlPath string) {
	// Default location: ~/.StudioB-UI/config.json
	//
	// config.json is written by the UI and is meant to persist across updates, but it
	// should never silently override a newer YAML config written/managed by install
	// scripts or an operator.
	//
	// Rule:
	//   - If config.v1 (YAML) exists -> YAML is the *only* source of truth.
	//     We DO NOT apply JSON overrides at all (but we best-effort sync JSON to YAML
	//     so older tooling doesn't drift).
	//   - If YAML does not exist -> JSON may be used (legacy-only fallback).
	//	(Env vars still win over everything.)
	home := os.Getenv("HOME")
	if strings.TrimSpace(home) == "" {
		return
	}
	p := filepath.Join(home, ".StudioB-UI", "config.json")

	jsonInfo, err := os.Stat(p)
	if err != nil {
		// Missing is fine.
		return
	}
	// Record path even if we later choose to ignore JSON due to staleness.
	cfg.Meta.JSONPath = p

	// If YAML exists, NEVER apply JSON overrides.
	//
	// Why so strict?
	// A stale config.json (often containing mode=mock from early development) can
	// silently flip the system back to mock after a refresh/restart, even when the
	// operator explicitly set live mode via the v1 config.
	if _, err := os.Stat(yamlPath); err == nil {
		mtime := "unknown"
		if jsonInfo != nil {
			mtime = jsonInfo.ModTime().UTC().Format(time.RFC3339)
		}
		cfg.Meta.Warnings = append(cfg.Meta.Warnings,
			fmt.Sprintf("config.json exists (mtime=%s) but %s is present; ignoring JSON overrides and syncing JSON to YAML", mtime, yamlPath))
		syncJSONToConfig(cfg, p)
		return
	}

	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	type jsonCfg struct {
		Mode string `json:"mode"`
		DSP  struct {
			Host string `json:"ip"`
			Port int    `json:"port"`
		} `json:"dsp"`
	}
	var jc jsonCfg
	if err := json.Unmarshal(b, &jc); err != nil {
		cfg.Meta.Warnings = append(cfg.Meta.Warnings, fmt.Sprintf("config.json parse error (%s): %v", p, err))
		return
	}

	if strings.TrimSpace(jc.Mode) != "" {
		cfg.DSP.Mode = jc.Mode
		cfg.Meta.ModeSource = "json"
	}
	if strings.TrimSpace(jc.DSP.Host) != "" {
		cfg.DSP.Host = jc.DSP.Host
		cfg.Meta.DSPHostSource = "json"
	}
	if jc.DSP.Port != 0 {
		cfg.DSP.Port = jc.DSP.Port
		cfg.Meta.DSPPortSource = "json"
	}
}

// syncJSONToConfig writes a minimal config.json file that matches the currently loaded
// config. This is best-effort and should never fail the engine start.
func syncJSONToConfig(cfg *Config, jsonPath string) {
	tmp := jsonPath + ".tmp"
	out := map[string]any{
		"mode": cfg.DSP.Mode,
		"dsp": map[string]any{
			"ip":   cfg.DSP.Host,
			"port": cfg.DSP.Port,
		},
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		cfg.Meta.Warnings = append(cfg.Meta.Warnings, fmt.Sprintf("config.json sync marshal error: %v", err))
		return
	}
	// Write atomically.
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		cfg.Meta.Warnings = append(cfg.Meta.Warnings, fmt.Sprintf("config.json sync write error (%s): %v", tmp, err))
		return
	}
	if err := os.Rename(tmp, jsonPath); err != nil {
		cfg.Meta.Warnings = append(cfg.Meta.Warnings, fmt.Sprintf("config.json sync rename error (%s): %v", jsonPath, err))
		_ = os.Remove(tmp)
		return
	}
}

func applyEnvOverrides(cfg *Config) {
	// Env vars take precedence over everything.
	if v := strings.TrimSpace(os.Getenv("STUDIOB_UI_MODE")); v != "" {
		cfg.DSP.Mode = v
		cfg.Meta.ModeSource = "env"
		cfg.Meta.EnvUsed["STUDIOB_UI_MODE"] = v
	}
	if v := strings.TrimSpace(os.Getenv("STUDIOB_DSP_IP")); v != "" {
		cfg.DSP.Host = v
		cfg.Meta.DSPHostSource = "env"
		cfg.Meta.EnvUsed["STUDIOB_DSP_IP"] = v
	}
	if v := strings.TrimSpace(os.Getenv("STUDIOB_DSP_PORT")); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.DSP.Port = p
			cfg.Meta.DSPPortSource = "env"
			cfg.Meta.EnvUsed["STUDIOB_DSP_PORT"] = v
		} else {
			cfg.Meta.Warnings = append(cfg.Meta.Warnings, fmt.Sprintf("invalid STUDIOB_DSP_PORT %q: %v", v, err))
		}
	}
}
