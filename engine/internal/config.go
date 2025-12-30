package app

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

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
		cfg.Updates.Mode = "zip"
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
	if len(cfg.RCAllowlist) == 0 {
		return nil, fmt.Errorf("rc_allowlist is empty")
	}
	return &cfg, nil
}
