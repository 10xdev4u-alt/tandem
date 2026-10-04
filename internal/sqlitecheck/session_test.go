package sqlitecheck

import (
	"database/sql"
	"errors"
	"os"
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
// a live database rather than a hand built Status. That pairing is the thing
// the daemon will actually call, so it gets tested as a pair.
//
// The expectation is inverted on purpose. When the suite is built without the
// CGO_CFLAGS this must fail loudly, which is the behaviour ADR 0001 depends on.
// When it is built with them, Check reports enabled and Require is satisfied.
// Either way the assertion below states which build it is looking at, so a
// reader is never left guessing why a run passed.
func TestCheckAndRequireAgreeOnRealDatabase(t *testing.T) {
	st, err := Check(openTemp(t))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	requireErr := Require(st)

	const wantEnabled = "0"
	if os.Getenv("CGO_CFLAGS") == "" {
		// Built without the flags. Require must refuse.
		if requireErr == nil {
			t.Fatalf("built without CGO_CFLAGS, ENABLE_SESSION=%v, but Require returned nil", st.Enabled)
		}
		var missing *ErrNoSession
		if !errors.As(requireErr, &missing) {
			t.Fatalf("built without CGO_CFLAGS, error is %T, want *ErrNoSession", requireErr)
		}
		t.Logf("built without flags, correctly refused: %v", requireErr)
		return
	}

	// Built with the flags, or by a caller that set them another way.
	t.Logf("built with CGO_CFLAGS=%s, ENABLE_SESSION=%v, Require=%v",
		os.Getenv("CGO_CFLAGS"), st.Enabled, requireErr)
	if requireErr != nil {
		t.Fatalf("built with CGO_CFLAGS but Require refused: %v", requireErr)
	}
	if !st.Enabled {
		t.Fatalf("CGO_CFLAGS is set but ENABLE_SESSION reported false")
	}
	_ = wantEnabled
}
