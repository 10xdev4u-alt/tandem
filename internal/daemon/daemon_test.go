package daemon_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/10xdev4u-alt/tandem/internal/config"
	"github.com/10xdev4u-alt/tandem/internal/daemon"
	"github.com/10xdev4u-alt/tandem/internal/sqlitecheck"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		Database:       config.Database{Path: filepath.Join(t.TempDir(), "tandem.db")},
		RequireSession: true,
	}
}

// start runs a daemon to readiness and returns its output plus a stop function.
// Readiness is the "ready" line rather than a sleep, so the test is not racing
// the goroutine it is about to cancel.
func start(t *testing.T, cfg config.Config) (string, func()) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "tandemd.log")
	f, tail := writeBoth(t, logPath)
	d, err := daemon.New(cfg, f)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	if !tail.await("ready", 5*time.Second) {
		cancel()
		t.Fatalf("daemon never became ready, output:\n%s", tail.String())
	}
	return tail.String(), func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run after cancel: %v", err)
			}
		case <-time.After(daemon.ShutdownTimeout):
			t.Error("Run did not return within the shutdown grace")
		}
	}
}

// TestRunStartsAndPrintsAVersionLine covers the first acceptance criterion.
func TestRunStartsAndPrintsAVersionLine(t *testing.T) {
	out, stop := start(t, testConfig(t))
	defer stop()

	if !strings.Contains(out, "tandemd") {
		t.Errorf("no version line in output:\n%s", out)
	}
	if !strings.Contains(out, "ready") {
		t.Errorf("no readiness line in output:\n%s", out)
	}
}

// TestSIGTERMShutsDownWithinTwoSeconds covers the third criterion, through the
// real signal path rather than a synthetic context cancellation. A daemon that
// only responds to a context in a test has not been shown to respond to the
// signal an orchestrator actually sends.
func TestSIGTERMShutsDownWithinTwoSeconds(t *testing.T) {
	cfg := testConfig(t)
	f, tail := writeBoth(t, filepath.Join(t.TempDir(), "tandemd.log"))
	d, err := daemon.New(cfg, f)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, stop := daemon.SignalContext(context.Background())
	defer stop()

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	if !tail.await("ready", 5*time.Second) {
		t.Fatalf("daemon never became ready, output:\n%s", tail.String())
	}

	start := time.Now()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("kill: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(daemon.ShutdownTimeout):
		t.Fatalf("daemon did not shut down within %s", daemon.ShutdownTimeout)
	}

	if elapsed := time.Since(start); elapsed >= daemon.ShutdownTimeout {
		t.Errorf("shutdown took %s, want under %s", elapsed, daemon.ShutdownTimeout)
	}
	if daemon.ShutdownTimeout > 2*time.Second {
		t.Errorf("ShutdownTimeout is %s but issue 7 requires two seconds", daemon.ShutdownTimeout)
	}
}

// TestMissingDatabasePathIsRejectedAtStartup is the daemon-side half of the
// second criterion. It must never report itself ready.
func TestMissingDatabasePathIsRejectedAtStartup(t *testing.T) {
	f, tail := writeBoth(t, filepath.Join(t.TempDir(), "tandemd.log"))
	if _, err := daemon.New(config.Config{}, f); err == nil {
		t.Fatal("New accepted a config with no database path")
	}
	if s := tail.String(); s != "" {
		t.Errorf("startup wrote output before failing: %q", s)
	}
}

// TestUnopenableDatabasePathFailsAtStartup covers a path that exists in the
// config but cannot be opened. Reporting ready and failing on the first write
// is the failure mode this check exists to prevent.
func TestUnopenableDatabasePathFailsAtStartup(t *testing.T) {
	cfg := testConfig(t)
	cfg.Database.Path = filepath.Join(t.TempDir(), "no-such-dir", "x.db")

	f, tail := writeBoth(t, filepath.Join(t.TempDir(), "tandemd.log"))
	d, err := daemon.New(cfg, f)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := d.Run(context.Background()); err == nil {
		t.Error("Run succeeded against an unopenable path")
	}
	if s := tail.String(); strings.Contains(s, "ready") {
		t.Errorf("daemon reported ready despite an unopenable path:\n%s", s)
	}
}

// TestSessionExtensionStateIsAlwaysReported records that the state is printed
// either way. A degraded daemon that says nothing costs an afternoon.
func TestSessionExtensionStateIsAlwaysReported(t *testing.T) {
	cfg := testConfig(t)
	cfg.RequireSession = false

	out, stop := start(t, cfg)
	defer stop()

	if !strings.Contains(out, "sqlite session extension:") {
		t.Errorf("extension state was not reported:\n%s", out)
	}
}

// TestRequiredSessionIsEnforcedWhenTheBuildLacksIt checks the loud path. The
// build under test either has the extension or it does not, so the opposite
// branch cannot be produced here and is covered by internal/sqlitecheck instead.
func TestRequiredSessionIsEnforcedWhenTheBuildLacksIt(t *testing.T) {
	cfg := testConfig(t)

	f, _ := writeBoth(t, filepath.Join(t.TempDir(), "tandemd.log"))
	d, err := daemon.New(cfg, f)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	db, err := openScratch(t)
	if err != nil {
		t.Skipf("cannot determine build capability: %v", err)
	}
	defer db.Close()

	st, err := sqlitecheck.Check(db)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if st.Enabled {
		t.Skip("this build has the session extension, so the failure branch cannot be reached")
	}

	err = d.Run(context.Background())
	if err == nil {
		t.Fatal("Run succeeded on a build without the extension")
	}
	var noSession *sqlitecheck.ErrNoSession
	if !errors.As(err, &noSession) {
		t.Errorf("error = %v, want *sqlitecheck.ErrNoSession", err)
	}
	if !strings.Contains(err.Error(), "CGO_CFLAGS") {
		t.Errorf("error %q does not tell the operator how to fix it", err)
	}
}

// writeBoth lets the daemon log into a real file while the test reads the same
// bytes live, so a test can wait for readiness instead of sleeping and hoping.
// The daemon logs through an io.Writer precisely so this is possible.
func writeBoth(t *testing.T, path string) (*os.File, *tailer) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return f, newTailer(path)
}

func openScratch(t *testing.T) (*sql.DB, error) {
	return sql.Open("sqlite3", filepath.Join(t.TempDir(), "probe.db"))
}

// tailer reads a log file as it grows, so a test can wait for a line instead of
// guessing how long startup takes.
type tailer struct {
	path string
	buf  bytes.Buffer
}

func newTailer(path string) *tailer { return &tailer{path: path} }

func (t *tailer) String() string {
	if b, err := os.ReadFile(t.path); err == nil {
		return string(b)
	}
	return t.buf.String()
}

// await blocks until want appears in the log, or the deadline passes.
func (t *tailer) await(want string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if strings.Contains(t.String(), want) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
}
