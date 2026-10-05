// Package config loads and validates the daemon's configuration.
//
// Validation happens at load time rather than at first use. A daemon that
// starts and then fails when it first needs a setting looks healthy to an
// orchestrator right up until it is not, so every required key is checked
// before the process reports itself as up.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
)

// ErrMissingKey names a required key that was absent or empty.
var ErrMissingKey = errors.New("missing required config key")

// Config is the daemon's validated configuration.
type Config struct {
	Database       Database `json:"database"`
	Bridge         Bridge   `json:"bridge"`
	RequireSession bool     `json:"-"`
}

// Database describes where SQLite lives.
type Database struct {
	// Path is required. SQLite is a file, and the session extension is bound to
	// one connection, so the daemon owns exactly one path.
	Path string `json:"path"`
}

// Bridge describes the optional Postgres side of the lab.
type Bridge struct {
	Enabled bool `json:"enabled"`
	// TargetURL is required only when Enabled is true. Validating it
	// conditionally keeps an unused bridge from blocking startup while still
	// refusing to start a bridge that cannot possibly connect.
	TargetURL string `json:"target_url"`
}

// Load reads a configuration file and validates it.
//
// The error for a missing key names the key in dotted form, because that is
// what the operator has to go and fix.
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parsing config %s: %w", path, err)
	}

	cfg.RequireSession = true
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, nil
}

// Validate checks every required key. It collects all problems rather than
// stopping at the first, so fixing a config does not take three restarts.
func (c Config) Validate() error {
	var missing []string
	if c.Database.Path == "" {
		missing = append(missing, "database.path")
	}
	if c.Bridge.Enabled && c.Bridge.TargetURL == "" {
		missing = append(missing, "bridge.target_url")
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("%w: %v", ErrMissingKey, missing)
}
