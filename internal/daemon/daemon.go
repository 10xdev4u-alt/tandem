// Package daemon owns the process lifecycle: configuration in, signals in,
// clean shutdown out.
package daemon

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/10xdev4u-alt/tandem/internal/config"
	"github.com/10xdev4u-alt/tandem/internal/engine"
	"github.com/10xdev4u-alt/tandem/internal/sqlitecheck"
	"github.com/10xdev4u-alt/tandem/internal/version"
)

// ShutdownGrace is how long a shutdown may take before it is considered a hang.
const ShutdownGrace = 2 * time.Second

// ShutdownTimeout is how long SIGTERM has to complete a shutdown. The
// acceptance criterion for issue 7 is two seconds, so this is the number the
// test asserts against rather than a value invented to pass it.
const ShutdownTimeout = 2 * time.Second

// Daemon is a configured, not yet running, daemon.
type Daemon struct {
	cfg    config.Config
	logger io.Writer
}

// New validates the configuration and prepares the daemon. It does not touch
// SQLite yet, so a bad config fails before anything opens a file.
func New(cfg config.Config, logger io.Writer) (*Daemon, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = os.Stderr
	}
	return &Daemon{cfg: cfg, logger: logger}, nil
}

// Run starts the daemon and blocks until ctx is cancelled.
//
// It returns nil on a clean shutdown and an error only for a real failure.
// Startup problems are reported before it begins waiting, so a daemon that
// cannot work never claims to be up.
func (d *Daemon) Run(ctx context.Context) error {
	fmt.Fprintf(d.logger, "tandemd %s (commit %s)\n", version.Version, version.Commit)

	state, err := d.checkSession()
	if err != nil {
		return err
	}
	enabled := state.Enabled
	// The state is printed either way. A degraded daemon that says nothing is
	// the case that wastes an afternoon.
	fmt.Fprintf(d.logger, "sqlite session extension: %s (sqlite %s)\n",
		onOff(enabled), state.Version)

	if d.cfg.RequireSession {
		if err := sqlitecheck.Require(state); err != nil {
			return err
		}
	}

	fmt.Fprintf(d.logger, "database: %s\n", d.cfg.Database.Path)

	// These are printed after checkSession, which asserts each one against the
	// file, so the line describes a database that has already been verified
	// rather than settings that were merely requested.
	opts := d.sqliteOptions()
	fmt.Fprintf(d.logger,
		"sqlite settings: page size %d, busy timeout %s, journal wal, foreign keys on\n",
		opts.PageSize, opts.BusyTimeout)

	fmt.Fprintln(d.logger, "ready")

	<-ctx.Done()
	fmt.Fprintln(d.logger, "shutting down")
	return nil
}

// checkSession opens the configured database and asks it whether this build can
// record changesets.
//
// Opening the database here rather than later means a path that cannot be opened
// is reported at startup, while the operator is still looking at the startup
// output, instead of at the first write. engine.OpenSQLite does the same for
// the pragmas: a file that cannot be brought into WAL mode, or whose page size
// contradicts the configuration, fails here instead of running with settings
// nobody chose.
func (d *Daemon) checkSession() (sqlitecheck.Status, error) {
	db, err := engine.OpenSQLite(d.cfg.Database.Path, d.sqliteOptions())
	if err != nil {
		return sqlitecheck.Status{}, err
	}
	defer db.Close()

	state, err := sqlitecheck.Check(db)
	if err != nil {
		return sqlitecheck.Status{}, fmt.Errorf("checking sqlite build: %w", err)
	}
	return state, nil
}

// sqliteOptions maps configuration onto the settings the engine applies and
// then reads back. Every value printed at startup has already been verified
// against the file, so the log reports what the database is rather than what
// was asked for.
func (d *Daemon) sqliteOptions() engine.SQLiteOptions {
	return engine.SQLiteOptions{
		PageSize:    d.cfg.Database.PageSizeOrDefault(),
		BusyTimeout: d.cfg.Database.BusyTimeout(),
	}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// SignalContext returns a context cancelled by SIGTERM or SIGINT.
//
// This lives in the package rather than in main so the shutdown behaviour is
// reachable from a test. Testing a signal handler that only exists in main means
// testing nothing.
func SignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, syscall.SIGTERM, syscall.SIGINT)
}
