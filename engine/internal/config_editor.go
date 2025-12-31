package app

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ConfigFilePath returns the operator-editable JSON config path.
//
// IMPORTANT: This file lives OUTSIDE of the repo and OUTSIDE of runtime releases.
// That ensures upgrades/rollbacks do not clobber operator settings.
func ConfigFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("cannot determine HOME for config file: %v", err)
	}
	return filepath.Join(home, ".StudioB-UI", "config.json"), nil
}

type EditableConfig struct {
	Mode string `json:"mode"`
	DSP  struct {
		IP   string `json:"ip"`
		Port int    `json:"port"`
	} `json:"dsp"`
}

// ReadEditableConfig reads the JSON config file if it exists.
// Missing file is not an error; Exists=false is returned.
func ReadEditableConfig() (cfg EditableConfig, exists bool, raw string, err error) {
	p, err := ConfigFilePath()
	if err != nil {
		return cfg, false, "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, false, "", nil
		}
		return cfg, false, "", err
	}
	raw = string(b)
	exists = true

	// Parse the file best-effort. If parsing fails, we still return raw for debugging.
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, exists, raw, fmt.Errorf("invalid json: %w", err)
	}
	return cfg, exists, raw, nil
}

// ValidateEditableConfig ensures operator edits are sane and safe.
func ValidateEditableConfig(c EditableConfig) error {
	m := strings.ToLower(strings.TrimSpace(c.Mode))
	if m != "" && m != "mock" && m != "live" {
		return fmt.Errorf("mode must be 'mock' or 'live' (got %q)", c.Mode)
	}
	if strings.TrimSpace(c.DSP.IP) != "" {
		if ip := net.ParseIP(strings.TrimSpace(c.DSP.IP)); ip == nil {
			return fmt.Errorf("dsp.ip must be a valid IP address (got %q)", c.DSP.IP)
		}
	}
	if c.DSP.Port != 0 {
		if c.DSP.Port < 1 || c.DSP.Port > 65535 {
			return fmt.Errorf("dsp.port must be 1-65535 (got %d)", c.DSP.Port)
		}
	}
	return nil
}

// WriteEditableConfig atomically writes config.json and keeps a timestamped backup
// of the previous file (if present).
func WriteEditableConfig(c EditableConfig) (string, error) {
	if err := ValidateEditableConfig(c); err != nil {
		return "", err
	}
	p, err := ConfigFilePath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return "", err
	}

	// Backup existing file (best-effort).
	if b, err := os.ReadFile(p); err == nil {
		bak := p + ".bak-" + time.Now().UTC().Format("20060102T150405Z")
		_ = os.WriteFile(bak, b, 0644)
	}

	// Pretty JSON for operator readability.
	out, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", err
	}
	out = append(out, '\n')

	// Atomic write: write temp in same dir then rename.
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, out, 0644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return p, nil
}
