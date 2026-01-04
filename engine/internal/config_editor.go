package app

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ConfigFilePath returns the operator-editable YAML config path.
//
// IMPORTANT:
// This file lives OUTSIDE of the repo and OUTSIDE of runtime releases.
// That ensures upgrades/rollbacks do not clobber operator settings.
//
// NOTE:
// The rest of the system already uses this YAML as the canonical config source
// (systemd starts the engine with it, and install scripts validate it), so the
// Engineering-page editor should modify THIS file.
func ConfigFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("cannot determine HOME for config file: %v", err)
	}
	return filepath.Join(home, ".StudioB-UI", "config", "config.v1"), nil
}


// LegacyConfigFilePath returns the pre-v0.2.77 operator config path.
//
// We keep this only to migrate existing installs forward.
func LegacyConfigFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("cannot determine HOME for legacy config file: %v", err)
	}
	return filepath.Join(home, ".StudioB-UI", "config", "config.yml"), nil
}
// EditableConfig is the small subset of config.v1 the UI is allowed to edit.
//
// We intentionally limit edits to mode + DSP host/port to reduce risk.
// Other keys remain managed by install scripts and advanced operators.
type EditableConfig struct {
	Mode string `yaml:"mode" json:"mode"`
	DSP  struct {
		IP   string `yaml:"ip" json:"ip"`
		Port int    `yaml:"port" json:"port"`
	} `yaml:"dsp" json:"dsp"`
}

// ReadEditableConfig reads the YAML config file if it exists.
// Missing file is not an error; Exists=false is returned.
func ReadEditableConfig() (cfg EditableConfig, exists bool, raw string, err error) {
	p, err := ConfigFilePath()
	// If the new file does not exist yet, but the legacy config.yml does,
	// copy it forward first so we preserve all existing keys.
	if err == nil {
		if _, statErr := os.Stat(p); os.IsNotExist(statErr) {
			lp, lerr := LegacyConfigFilePath();
			if lerr == nil {
				if b, rerr := os.ReadFile(lp); rerr == nil {
					_ = os.MkdirAll(filepath.Dir(p), 0755)
					_ = os.WriteFile(p, b, 0644)
				}
			}
		}
	}
	if err != nil {
		return cfg, false, "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		// Backwards compatibility: if the new file is missing but the legacy
		// config.yml exists, read it so the Engineering page can display current
		// settings and migrate forward on next Save.
		lp, lerr := LegacyConfigFilePath()
		if lerr == nil {
			if lb, lread := os.ReadFile(lp); lread == nil {
				p = lp
				b = lb
				err = nil
			}
		}
	}
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, false, "", nil
		}
		return cfg, false, "", err
	}
	raw = string(b)
	exists = true

	// Parse the file best-effort. If parsing fails, we still return raw for debugging.
	// We parse into the full Config struct, then map out the editable subset.
	var full Config
	if err := yaml.Unmarshal(b, &full); err != nil {
		return cfg, exists, raw, fmt.Errorf("invalid yaml: %w", err)
	}
	cfg.Mode = strings.TrimSpace(full.DSP.Mode)
	cfg.DSP.IP = strings.TrimSpace(full.DSP.Host)
	cfg.DSP.Port = full.DSP.Port
	return cfg, exists, raw, nil
}

// ValidateEditableConfig ensures operator edits are sane and safe.
func ValidateEditableConfig(c EditableConfig) error {
	m := strings.ToLower(strings.TrimSpace(c.Mode))
	// The UI may send labels like "live (reserved)".
	// We accept anything that begins with "live" or "mock" to keep the UX friendly
	// while the engine stays strict (it only ever writes "live" or "mock" to disk).
	if strings.HasPrefix(m, "live") {
		m = "live"
	}
	if strings.HasPrefix(m, "mock") {
		m = "mock"
	}
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

// WriteEditableConfig atomically writes config.v1 and keeps a timestamped backup
// of the previous file (if present).
func WriteEditableConfig(c EditableConfig) (string, error) {
	if err := ValidateEditableConfig(c); err != nil {
		return "", err
	}
	p, err := ConfigFilePath()
	// If the new file does not exist yet, but the legacy config.yml does,
	// copy it forward first so we preserve all existing keys.
	if err == nil {
		if _, statErr := os.Stat(p); os.IsNotExist(statErr) {
			lp, lerr := LegacyConfigFilePath();
			if lerr == nil {
				if b, rerr := os.ReadFile(lp); rerr == nil {
					_ = os.MkdirAll(filepath.Dir(p), 0755)
					_ = os.WriteFile(p, b, 0644)
				}
			}
		}
	}
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

	// Read the existing YAML and update only the editable subset.
	var full Config
	if b, err := os.ReadFile(p); err == nil {
		// Existing file: preserve all keys we know about.
		if err := yaml.Unmarshal(b, &full); err != nil {
			return "", fmt.Errorf("cannot update %s: invalid yaml: %w", p, err)
		}
	}
	// Apply edits.
	if strings.TrimSpace(c.Mode) != "" {
		nm := strings.ToLower(strings.TrimSpace(c.Mode))
		if strings.HasPrefix(nm, "live") {
			nm = "live"
		}
		if strings.HasPrefix(nm, "mock") {
			nm = "mock"
		}
		full.DSP.Mode = nm
	}
	if strings.TrimSpace(c.DSP.IP) != "" {
		full.DSP.Host = strings.TrimSpace(c.DSP.IP)
	}
	if c.DSP.Port != 0 {
		full.DSP.Port = c.DSP.Port
	}

	// Marshal back to YAML for operator readability.
	out, err := yaml.Marshal(&full)
	if err != nil {
		return "", err
	}
	// Ensure trailing newline (makes diffs/logs nicer).
	if len(out) == 0 || out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}

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
