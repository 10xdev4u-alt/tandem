// Package daemon owns the process lifecycle: configuration in, signals in,
// clean shutdown out.
package daemon

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/10xdev4u-alt/tandem/internal/config"
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
// output, instead of at the first write.
func (d *Daemon) checkSession() (sqlitecheck.Status, error) {
	db, err := sql.Open("sqlite3", d.cfg.Database.Path)
	if err != nil {
		return sqlitecheck.Status{}, fmt.Errorf("opening database %s: %w", d.cfg.Database.Path, err)
	}
	defer db.Close()

	state, err := sqlitecheck.Check(db)
	if err != nil {
		return sqlitecheck.Status{}, fmt.Errorf("checking sqlite build: %w", err)
	}
	return state, nil
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
