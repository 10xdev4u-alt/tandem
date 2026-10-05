package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

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
