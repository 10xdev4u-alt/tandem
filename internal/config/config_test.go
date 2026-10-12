package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/10xdev4u-alt/tandem/internal/config"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return p
}

// TestLoadNamesTheMissingKey covers the second acceptance criterion for issue 7.
// The operator has to be told which key to fix, so the error names it rather
// than saying something went wrong.
func TestLoadNamesTheMissingKey(t *testing.T) {
	_, err := config.Load(write(t, `{"bridge":{"enabled":false}}`))
	if err == nil {
		t.Fatal("Load accepted a config with no database path")
	}
	if !errors.Is(err, config.ErrMissingKey) {
		t.Errorf("error = %v, want ErrMissingKey", err)
	}
	if !contains(err.Error(), "database.path") {
		t.Errorf("error %q does not name database.path", err)
	}
}

// TestLoadCollectsEveryMissingKey checks that a config missing several keys
// reports all of them, so fixing one does not take three restarts to discover
// the rest.
func TestLoadCollectsEveryMissingKey(t *testing.T) {
	_, err := config.Load(write(t, `{"bridge":{"enabled":true}}`))
	if err == nil {
		t.Fatal("Load accepted a config missing both keys")
	}
	for _, want := range []string{"database.path", "bridge.target_url"} {
		if !contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}

// TestBridgeTargetURLOnlyRequiredWhenEnabled keeps an unused bridge from
// blocking startup, while still refusing to start one that cannot connect.
func TestBridgeTargetURLOnlyRequiredWhenEnabled(t *testing.T) {
	if _, err := config.Load(write(t, `{"database":{"path":"a.db"}}`)); err != nil {
		t.Errorf("a disabled bridge with no target_url was rejected: %v", err)
	}
	if _, err := config.Load(write(t,
		`{"database":{"path":"a.db"},"bridge":{"enabled":true}}`)); err == nil {
		t.Error("an enabled bridge with no target_url was accepted")
	}
}

// TestLoadReportsUnreadableAndMalformedFiles covers the two ways a config file
// itself can be wrong before its contents matter.
func TestLoadReportsUnreadableAndMalformedFiles(t *testing.T) {
	if _, err := config.Load(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Error("Load accepted a missing file")
	}
	if _, err := config.Load(write(t, `{not json`)); err == nil {
		t.Error("Load accepted malformed JSON")
	}
}

// TestRequireSessionDefaultsOn records that a missing key still gets a loud
// failure when the extension is absent. Silently degrading is the failure mode
// this whole project exists to prevent.
func TestRequireSessionDefaultsOn(t *testing.T) {
	cfg, err := config.Load(write(t, `{"database":{"path":"a.db"}}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.RequireSession {
		t.Error("RequireSession defaulted to false; it must default to true")
	}
}

// TestSQLiteDefaultsApplyWhenKeysAbsent pins the documented defaults: page
// size 4096 and a five second lock wait, both applied when the keys are
// missing rather than left as zero.
func TestSQLiteDefaultsApplyWhenKeysAbsent(t *testing.T) {
	cfg, err := config.Load(write(t, `{"database":{"path":"a.db"}}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Database.PageSizeOrDefault(); got != config.DefaultPageSize {
		t.Errorf("PageSizeOrDefault() = %d, want %d", got, config.DefaultPageSize)
	}
	if got := cfg.Database.BusyTimeout(); got != config.DefaultBusyTimeout {
		t.Errorf("BusyTimeout() = %s, want %s", got, config.DefaultBusyTimeout)
	}
}

// TestZeroBusyTimeoutIsHonouredNotDefaulted is the reason the fields are
// pointers. A plain int cannot tell an absent key from an explicit zero, and
// zero here means "do not wait for the lock" while absent means "use the
// default". Reading back the default would make the configuration say
// something its author did not write.
func TestZeroBusyTimeoutIsHonouredNotDefaulted(t *testing.T) {
	cfg, err := config.Load(write(t, `{"database":{"path":"a.db","busy_timeout_ms":0}}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Database.BusyTimeout(); got != 0 {
		t.Errorf("BusyTimeout() = %s, want 0 for an explicit zero", got)
	}
}

func TestConfiguredSQLiteValuesAreUsed(t *testing.T) {
	cfg, err := config.Load(write(t,
		`{"database":{"path":"a.db","page_size":8192,"busy_timeout_ms":1234}}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Database.PageSizeOrDefault(); got != 8192 {
		t.Errorf("PageSizeOrDefault() = %d, want 8192", got)
	}
	if got := cfg.Database.BusyTimeout(); got != 1234*time.Millisecond {
		t.Errorf("BusyTimeout() = %s, want 1234ms", got)
	}
}

func TestInvalidSQLiteValuesRejected(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"page size not a power of two", `{"database":{"path":"a.db","page_size":5000}}`},
		{"page size too small", `{"database":{"path":"a.db","page_size":256}}`},
		{"page size too large", `{"database":{"path":"a.db","page_size":131072}}`},
		{"negative busy timeout", `{"database":{"path":"a.db","busy_timeout_ms":-1}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.Load(write(t, tt.body))
			if err == nil {
				t.Fatal("Load accepted an out of range value")
			}
			if !errors.Is(err, config.ErrInvalidValue) {
				t.Errorf("error = %v, want ErrInvalidValue", err)
			}
		})
	}
}

// TestMissingAndInvalidReportedTogether is why Validate returns a joined
// error. A config missing its path and carrying a bad page size must be fixed
// in one restart, not discovered one failure at a time.
func TestMissingAndInvalidReportedTogether(t *testing.T) {
	_, err := config.Load(write(t, `{"database":{"page_size":5000}}`))
	if err == nil {
		t.Fatal("Load accepted a config with no database path and a bad page size")
	}
	if !errors.Is(err, config.ErrMissingKey) {
		t.Errorf("error = %v, want ErrMissingKey also reported", err)
	}
	if !errors.Is(err, config.ErrInvalidValue) {
		t.Errorf("error = %v, want ErrInvalidValue also reported", err)
	}
	if !strings.Contains(err.Error(), "database.path") {
		t.Errorf("error = %v, want it to name database.path", err)
	}
	if !strings.Contains(err.Error(), "database.page_size") {
		t.Errorf("error = %v, want it to name database.page_size", err)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(needle) == 0 || indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
