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
	"strings"
	"time"
)

// ErrMissingKey names a required key that was absent or empty.
var ErrMissingKey = errors.New("missing required config key")

// ErrInvalidValue names a key that was present but outside the range SQLite
// will accept, reported before startup rather than at the first write.
var ErrInvalidValue = errors.New("invalid config value")

// Defaults for the SQLite settings. They are constants rather than bare
// literals in the loader so that the documented default and the applied
// default are the same line of code, and cannot drift apart.
const (
	// DefaultPageSize is SQLite's own default page size in bytes. It is the
	// default here for the same reason: a lab database gains nothing from
	// deviating, and a configured page size that does not match the file
	// fails startup rather than silently not applying.
	DefaultPageSize = 4096

	// DefaultBusyTimeout is how long a connection waits for a lock before
	// returning SQLITE_BUSY. Five seconds is long enough to ride out another
	// process's transaction and short enough that a real deadlock still
	// surfaces while someone is watching.
	DefaultBusyTimeout = 5 * time.Second
)

// Config is the daemon's validated configuration.
type Config struct {
	Database       Database `json:"database"`
	Bridge         Bridge   `json:"bridge"`
	RequireSession bool     `json:"-"`
}

// Database describes where SQLite lives and how it is opened.
//
// PageSize and BusyTimeoutMS are pointers so that an absent key and an
// explicit 0 are distinguishable. A plain int cannot tell them apart, and
// busy_timeout of 0 is a meaningful value — it means "do not wait for the
// lock" — while an absent key must mean "use the default". Collapsing the
// two would make the configuration say something its author did not write.
type Database struct {
	// Path is required. SQLite is a file, and the session extension is bound to
	// one connection, so the daemon owns exactly one path.
	Path string `json:"path"`

	// PageSize is the SQLite page size in bytes. Nil uses DefaultPageSize.
	// SQLite requires a power of two between 512 and 65536, which Validate
	// enforces rather than letting SQLite fail later on a write.
	//
	// The page size is fixed the first time data is written to a file. Setting
	// it on a database that already has content changes nothing, so a mismatch
	// is a startup error naming both sizes instead of a setting that quietly
	// does not apply.
	PageSize *int `json:"page_size"`

	// BusyTimeoutMS is how long a connection waits for a lock, in
	// milliseconds. Nil uses DefaultBusyTimeout; an explicit 0 means do not
	// wait at all.
	BusyTimeoutMS *int `json:"busy_timeout_ms"`
}

// PageSizeOrDefault returns the configured page size or the default when the
// key was absent.
func (d Database) PageSizeOrDefault() int {
	if d.PageSize == nil {
		return DefaultPageSize
	}
	return *d.PageSize
}

// BusyTimeout returns the configured lock wait, or the default when the key
// was absent. An explicit 0 returns 0: no waiting.
func (d Database) BusyTimeout() time.Duration {
	if d.BusyTimeoutMS == nil {
		return DefaultBusyTimeout
	}
	return time.Duration(*d.BusyTimeoutMS) * time.Millisecond
}

// IsPowerOfTwo reports whether n is a power of two greater than zero.
func IsPowerOfTwo(n int) bool {
	return n > 0 && n&(n-1) == 0
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

// Validate checks every required key and every range. It collects all
// problems rather than stopping at the first, so fixing a config does not
// take three restarts.
func (c Config) Validate() error {
	var missing []string
	if c.Database.Path == "" {
		missing = append(missing, "database.path")
	}
	if c.Bridge.Enabled && c.Bridge.TargetURL == "" {
		missing = append(missing, "bridge.target_url")
	}

	// Only values that were actually supplied are checked. An absent key has
	// already been decided by the defaults, and reporting a default as invalid
	// would blame the operator for something they did not write.
	var invalid []string
	if c.Database.PageSize != nil {
		if p := *c.Database.PageSize; !IsPowerOfTwo(p) || p < 512 || p > 65536 {
			invalid = append(invalid, fmt.Sprintf(
				"database.page_size must be a power of two between 512 and 65536, got %d", p))
		}
	}
	if c.Database.BusyTimeoutMS != nil && *c.Database.BusyTimeoutMS < 0 {
		invalid = append(invalid, fmt.Sprintf(
			"database.busy_timeout_ms must be zero or greater, got %d", *c.Database.BusyTimeoutMS))
	}

	if len(missing) == 0 && len(invalid) == 0 {
		return nil
	}
	// Both kinds are reported together. Stopping at the first would make a
	// config with a missing path and a bad page size take two restarts to
	// fix, which is the exact cost this collecting is meant to avoid.
	sort.Strings(missing)
	sort.Strings(invalid)

	var errs []error
	if len(missing) > 0 {
		errs = append(errs, fmt.Errorf("%w: %v", ErrMissingKey, missing))
	}
	if len(invalid) > 0 {
		errs = append(errs, fmt.Errorf("%w: %s", ErrInvalidValue, strings.Join(invalid, "; ")))
	}
	return errors.Join(errs...)
}
