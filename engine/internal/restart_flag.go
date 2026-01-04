package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RestartFlagPath returns the well-known path used to request an engine restart.
//
// Why this exists:
// - Changing DSP write mode is a *high-stakes* action.
// - We intentionally require a process restart to apply it deterministically.
// - The watchdog is the component with authority to restart services.
// - The engine simply records "restart requested" in a durable, inspectable file.
//
// This is not meant to be hidden. Operators can view/remove this file if needed.
func RestartFlagPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	home = strings.TrimSpace(home)
	if home == "" {
		return "", os.ErrInvalid
	}
	return filepath.Join(home, ".StudioB-UI", "state", "restart_required.json"), nil
}

// RequestEngineRestart writes/overwrites the restart flag file with a reason and timestamp.
func RequestEngineRestart(reason string) error {
	p, err := RestartFlagPath()
	if err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Dir(p), 0755)

	payload := map[string]any{
		"ts":     time.Now().UTC().Format(time.RFC3339),
		"reason": strings.TrimSpace(reason),
	}
	b, _ := json.Marshal(payload)
	b = append(b, '\n')
	return os.WriteFile(p, b, 0644)
}

// RestartRequired returns true if a restart has been requested.
func RestartRequired() bool {
	p, err := RestartFlagPath()
	if err != nil {
		return false
	}
	_, statErr := os.Stat(p)
	return statErr == nil
}
