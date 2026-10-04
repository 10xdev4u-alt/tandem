// Command gosqlite captures a real changeset with the gosqlite.org driver and
// proves it applies to a replica, re-applies without duplicating rows, and
// inverts cleanly.
//
// This is the candidate under test for issue 14.
package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	sqlite "gosqlite.org"
)

const schema = `CREATE TABLE accounts (
	id    INTEGER PRIMARY KEY,
	owner TEXT    NOT NULL,
	email TEXT    NOT NULL,
	cents INTEGER NOT NULL
)`

func main() {
	rows := flag.Int("rows", 100_000, "rows inserted inside the captured session")
	flag.Parse()

	dir, err := os.MkdirTemp("", "tandem-gosqlite-")
	must(err)
	defer os.RemoveAll(dir)

	ctx := context.Background()

	fmt.Printf("go       %s\n", runtime.Version())
	fmt.Printf("goos     %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Printf("cgo      false, pure go\n")
	fmt.Printf("rows     %d\n\n", *rows)

	primary, err := open(filepath.Join(dir, "primary.db"))
	must(err)
	defer primary.close()
	must(exec(primary.sc.ExecContext(ctx, `PRAGMA journal_mode=WAL`)))
	must(exec(primary.sc.ExecContext(ctx, schema)))

	var changeset []byte
	start := time.Now()

	// The session belongs to a connection, so every statement has to run on
	// that same connection or nothing is recorded.
	primary.raw(func(c *sqlite.Conn) {
		sess, err := c.CreateSession("main")
		if err != nil {
			fmt.Println("create session failed:", err)
			return
		}
		defer sess.Close()

		if err := sess.Attach("accounts"); err != nil {
			fmt.Println("attach failed:", err)
			return
		}
		sess.Enable(true)

		if err := insertDriver(ctx, c, *rows); err != nil {
			fmt.Println("insert failed:", err)
			return
		}
		if sess.IsEmpty() {
			fmt.Println("FAIL: session captured nothing")
			return
		}
		changeset, err = sess.Changeset()
		if err != nil {
			fmt.Println("changeset failed:", err)
		}
	})

	if len(changeset) == 0 {
		fmt.Println("RESULT: no changeset produced")
		os.Exit(1)
	}
	capture := time.Since(start)

	fmt.Printf("ENABLE_SESSION compiled: yes, no build tag needed\n")
	fmt.Printf("session reachable from Go: yes\n")
	fmt.Printf("captured rows:           %d\n", *rows)
	fmt.Printf("capture elapsed:         %s\n", capture.Round(time.Millisecond))
	fmt.Printf("changeset size:          %.1f KiB\n", float64(len(changeset))/1024)
	fmt.Printf("bytes per row:           %.1f\n", float64(len(changeset))/float64(*rows))

	// Apply to an empty replica.
	replica, err := open(filepath.Join(dir, "replica.db"))
	must(err)
	defer replica.close()
	must(exec(replica.sc.ExecContext(ctx, `PRAGMA journal_mode=WAL`)))
	must(exec(replica.sc.ExecContext(ctx, schema)))

	applyStart := time.Now()
	replica.raw(func(c *sqlite.Conn) {
		if err := c.ApplyChangeset(changeset); err != nil {
			fmt.Println("apply failed:", err)
		}
	})
	applyElapsed := time.Since(applyStart)

	var afterApply int
	must(replica.sc.QueryRowContext(ctx, `SELECT count(*) FROM accounts`).Scan(&afterApply))

	// Re-apply. A changeset that duplicates rows on reapply is unsafe.
	replica.raw(func(c *sqlite.Conn) {
		if err := c.ApplyChangeset(changeset); err != nil {
			fmt.Println("second apply failed:", err)
		}
	})
	var afterReapply int
	must(replica.sc.QueryRowContext(ctx, `SELECT count(*) FROM accounts`).Scan(&afterReapply))

	// Invert, because undo is what makes a change log trustworthy.
	var invertErr error
	replica.raw(func(c *sqlite.Conn) {
		inverse, err := c.InvertChangeset(changeset)
		if err != nil {
			invertErr = err
			return
		}
		if err := c.ApplyChangeset(inverse); err != nil {
			invertErr = err
		}
	})
	var afterInvert int
	must(replica.sc.QueryRowContext(ctx, `SELECT count(*) FROM accounts`).Scan(&afterInvert))

	fmt.Printf("\napply elapsed:           %s\n", applyElapsed.Round(time.Millisecond))
	fmt.Printf("replica rows:            %d, expected %d, ok=%v\n", afterApply, *rows, afterApply == *rows)
	fmt.Printf("after reapply:           %d, idempotent=%v\n", afterReapply, afterReapply == *rows)
	fmt.Printf("after inverse:           %d, undo works=%v\n", afterInvert, afterInvert == 0 && invertErr == nil)
}

// insertDriver runs the workload on the driver connection so the open session
// records every row.
func insertDriver(ctx context.Context, c *sqlite.Conn, rows int) error {
	// driver.Tx cannot prepare, so the statement is prepared on the connection.
	stmt, err := c.Prepare(`INSERT INTO accounts (id, owner, email, cents) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	tx, err := c.Begin()
	if err != nil {
		return err
	}
	for i := 1; i <= rows; i++ {
		args := []driver.Value{
			int64(i),
			fmt.Sprintf("owner-%d", i),
			fmt.Sprintf("o%d@example.test", i),
			int64(i * 100),
		}
		if _, err := stmt.Exec(args); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

type pinned struct {
	db *sql.DB
	sc *sql.Conn
}

func open(path string) (*pinned, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// One pinned connection, because a session belongs to a connection and the
	// pool must not get a chance to move the work.
	db.SetMaxOpenConns(1)
	sc, err := db.Conn(context.Background())
	if err != nil {
		db.Close()
		return nil, err
	}
	return &pinned{db: db, sc: sc}, nil
}

func (p *pinned) close() {
	p.sc.Close()
	p.db.Close()
}

func (p *pinned) raw(fn func(*sqlite.Conn)) {
	must(p.sc.Raw(func(dc any) error {
		fn(dc.(*sqlite.Conn))
		return nil
	}))
}

// exec unwraps the result so a failed statement is never a silent no-op.
func exec(_ sql.Result, err error) error { return err }

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}
