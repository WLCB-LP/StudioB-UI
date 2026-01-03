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
	// These are intentionally shallow and only cover the "mode" + DSP connection fields for v0.2.x.
	applyJSONOverrides(&cfg)
	applyEnvOverrides(&cfg)

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

func applyJSONOverrides(cfg *Config) {
	// Default location: ~/.StudioB-UI/config.json
	home := os.Getenv("HOME")
	if strings.TrimSpace(home) == "" {
		return
	}
	p := filepath.Join(home, ".StudioB-UI", "config.json")
	b, err := os.ReadFile(p)
	if err != nil {
		// Missing is fine.
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
	cfg.Meta.JSONPath = p

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
