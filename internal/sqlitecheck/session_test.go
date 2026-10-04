package sqlitecheck

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func openTemp(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "check.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestCheckReportsActualState pins whatever the build in use actually is. It
// does not assume the flag is on, because the whole point of the check is that
// a build can silently lose it. When the suite runs without the CGO_CFLAGS the
// test still passes and reports the absence.
func TestCheckReportsActualState(t *testing.T) {
	st, err := Check(openTemp(t))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	t.Logf("ENABLE_SESSION=%v sqlite=%s", st.Enabled, st.Version)

	if st.Version == "" {
		t.Error("sqlite version came back empty, the query did not run")
	}
	if st.Source != compileOption {
		t.Errorf("Source = %q, want %q", st.Source, compileOption)
	}
}

// TestRequireFailsLoudlyWhenAbsent is the failure path the issue asks for. It
// uses a Status value, not a build, so it runs regardless of how the suite was
// compiled.
func TestRequireFailsLoudlyWhenAbsent(t *testing.T) {
	err := Require(Status{Enabled: false, Version: "3.53.4"})
	if err == nil {
		t.Fatal("Require returned nil for a database with no session extension")
	}

	var missing *ErrNoSession
	if !errors.As(err, &missing) {
		t.Fatalf("error is %T, want *ErrNoSession", err)
	}

	// The message has to tell an operator what to do, not just that it broke.
	msg := err.Error()
	for _, want := range []string{
		"session extension is not compiled in",
		"refusing to start",
		"SQLITE_ENABLE_SESSION",
		"3.53.4",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message is missing %q\nfull message: %s", want, msg)
		}
	}
}

func TestRequirePassesWhenPresent(t *testing.T) {
	if err := Require(Status{Enabled: true, Version: "3.53.4"}); err != nil {
		t.Fatalf("Require returned %v for an enabled status", err)
	}
}

// TestErrorMentionsBothFlags guards the build instruction against drift. If the
// flags ever change, this fails rather than sending an operator away to rebuild
// with the wrong pair.
func TestErrorMentionsBothFlags(t *testing.T) {
	msg := (&ErrNoSession{}).Error()
	for _, want := range []string{"-DSQLITE_ENABLE_SESSION", "-DSQLITE_ENABLE_PREUPDATE_HOOK"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message is missing %q\nfull message: %s", want, msg)
		}
	}
}

// TestCheckAndRequireAgreeOnRealDatabase wires the two halves together against
// a live database rather than a hand built Status, because that pairing is what
// the daemon actually calls.
//
// The branch is chosen from the database's own answer, not from the
// environment. Keying off CGO_CFLAGS being non-empty would be wrong: an
// unrelated value such as -O2 would send the test down the enabled path and
// then fail against a database that correctly reports no session extension.
func TestCheckAndRequireAgreeOnRealDatabase(t *testing.T) {
	st, err := Check(openTemp(t))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	requireErr := Require(st)

	if !st.Enabled {
		if requireErr == nil {
			t.Fatalf("database reports no session extension, but Require returned nil")
		}
		var missing *ErrNoSession
		if !errors.As(requireErr, &missing) {
			t.Fatalf("error is %T, want *ErrNoSession", requireErr)
		}
		t.Logf("database has no session extension, correctly refused: %v", requireErr)
		return
	}

	t.Logf("database has the session extension, Require=%v", requireErr)
	if requireErr != nil {
		t.Fatalf("database reports the session extension, but Require refused: %v", requireErr)
	}
}

// TestEnabledBuildIsDetected is the other half, and it only runs where it can
// succeed. A suite built without the flags has no enabled build available, so
// it skips rather than asserting something it cannot know.
func TestEnabledBuildIsDetected(t *testing.T) {
	st, err := Check(openTemp(t))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !st.Enabled {
		t.Skip("built without the session flags, nothing to assert about an enabled build")
	}
	if err := Require(st); err != nil {
		t.Fatalf("Require refused an enabled build: %v", err)
	}
}

// TestUnflaggedBuildIsRefused is the same idea from the other side, and it is
// the assertion that keeps the check honest: a database with no session
// extension has to be refused, so that losing the build flag becomes a loud
// startup failure instead of a silent loss of change capture.
func TestUnflaggedBuildIsRefused(t *testing.T) {
	st, err := Check(openTemp(t))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if st.Enabled {
		t.Skip("built with the session flags, cannot assert the refusal path here")
	}
	if err := Require(st); err == nil {
		t.Fatal("Require accepted a database with no session extension")
	}
}
