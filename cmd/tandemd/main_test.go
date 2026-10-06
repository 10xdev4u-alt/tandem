package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exit statuses are a contract with anything scripting this binary, so they are
// asserted rather than assumed. Missing flag is 2, unusable input is 1, success
// is 0.
func TestExitStatusForAMissingConfigFlag(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(nil, &out, &errOut); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "--config is required") {
		t.Errorf("stderr %q does not name the missing flag", errOut.String())
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want nothing on a usage error", out.String())
	}
}

func TestExitStatusForAnUnknownFlag(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--nope"}, &out, &errOut); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
}

func TestVersionFlagPrintsAndExitsZero(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--version"}, &out, &errOut); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if !strings.HasPrefix(out.String(), "tandemd ") {
		t.Errorf("stdout = %q, want a version line", out.String())
	}
	if !strings.Contains(out.String(), "commit") {
		t.Errorf("version line %q does not carry the commit", out.String())
	}
}

func TestExitStatusForAConfigMissingARequiredKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(p, []byte(`{"bridge":{"enabled":false}}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	var out, errOut bytes.Buffer
	code := run([]string{"--config", p}, &out, &errOut)
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "database.path") {
		t.Errorf("stderr %q does not name the key to fix", errOut.String())
	}
}

func TestExitStatusForAnUnreadableConfig(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"--config", filepath.Join(t.TempDir(), "absent.json")}, &out, &errOut)
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
}
