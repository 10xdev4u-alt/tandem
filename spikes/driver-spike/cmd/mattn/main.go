// Command mattn probes the mattn/go-sqlite3 driver.
//
// The only question this can answer without an upstream binding is whether the
// session extension is compiled into the library at all, and whether foreign
// keys are enforced. Both matter to the bridge and neither needs a shim.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const schema = `CREATE TABLE accounts (
	id    INTEGER PRIMARY KEY,
	owner TEXT    NOT NULL,
	email TEXT    NOT NULL,
	cents INTEGER NOT NULL
)`

const childSchema = `CREATE TABLE child (
	id     INTEGER PRIMARY KEY,
	parent INTEGER NOT NULL REFERENCES accounts(id)
)`

func main() {
	rows := flag.Int("rows", 100_000, "rows to insert")
	flag.Parse()

	dir, err := os.MkdirTemp("", "tandem-mattn-")
	must(err)
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "mattn.db")
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000")
	must(err)
	defer db.Close()

	ctx := context.Background()

	fmt.Printf("go       %s\n", runtime.Version())
	fmt.Printf("goos     %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Printf("rows     %d\n\n", *rows)

	var compiled int
	must(db.QueryRow(`SELECT sqlite_compileoption_used('ENABLE_SESSION')`).Scan(&compiled))
	fmt.Printf("ENABLE_SESSION compiled: %d\n", compiled)
	fmt.Printf("session reachable from Go: false, no exported binding\n")

	must(exec(db.Exec(schema)))
	start := time.Now()
	must(insert(db, *rows))
	elapsed := time.Since(start).Round(time.Millisecond)

	var n int
	must(db.QueryRow(`SELECT count(*) FROM accounts`).Scan(&n))

	// Foreign keys are off by default here and always on in Postgres. Test it
	// both ways, because an earlier version of this probe reported the boolean
	// inverted and drew a conclusion from it that was not true.
	//
	// The check is "did the insert get rejected", so the error being non-nil is
	// the enforcing case.
	// The parent id below must never exist in accounts, at any -rows value.
	// Accounts are inserted with ids 1..rows, so a fixed id inside that range
	// would start existing once -rows reached it, and the probe would then
	// report a false "not enforced". Negative ids are never inserted.
	const orphanParent = -1

	reset := func(target execer) {
		must(exec(target.ExecContext(ctx, `PRAGMA foreign_keys=ON`)))
		must(exec(target.ExecContext(ctx, `DROP TABLE IF EXISTS child`)))
		must(exec(target.ExecContext(ctx, childSchema)))
	}

	fmt.Println()
	fmt.Println("foreign key behaviour:")
	for _, tc := range []struct {
		name   string
		pinned bool
	}{
		{"unpinned pool", false},
		{"pinned single connection", true},
	} {
		rejected := false
		if tc.pinned {
			conn, err := db.Conn(ctx)
			must(err)
			reset(conn)
			_, err = conn.ExecContext(ctx, `INSERT INTO child (id, parent) VALUES (1, ?)`, orphanParent)
			rejected = err != nil
			conn.Close()
		} else {
			reset(db)
			_, err := db.ExecContext(ctx, `INSERT INTO child (id, parent) VALUES (1, ?)`, orphanParent)
			rejected = err != nil
		}
		fmt.Printf("  %-26s orphan rejected=%v\n", tc.name, rejected)
	}

	// The actual point of the exercise. Pragmas are per connection, so setting
	// one on connection A and writing through connection B proves it. That is
	// why the daemon must set pragmas on every connection it opens rather than
	// once at startup.
	a, err := db.Conn(ctx)
	must(err)
	defer a.Close()
	reset(a)

	b, err := db.Conn(ctx)
	must(err)
	defer b.Close()
	_, crossErr := b.ExecContext(ctx, `INSERT INTO child (id, parent) VALUES (1, ?)`, orphanParent)
	fmt.Printf("  %-26s orphan rejected=%v (expected false, pragma was set on another connection)\n",
		"pragma on conn A, write on B", crossErr != nil)

	fi, _ := os.Stat(path)
	fmt.Printf("rows inserted:          %d\n", n)
	fmt.Printf("insert elapsed:         %s\n", elapsed)
	fmt.Printf("db file size:           %.1f MiB\n", float64(fi.Size())/(1024*1024))
}

func insert(db *sql.DB, rows int) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO accounts (id, owner, email, cents) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	for i := 1; i <= rows; i++ {
		if _, err := stmt.Exec(i, fmt.Sprintf("owner-%d", i), fmt.Sprintf("o%d@example.test", i), i*100); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func exec(_ any, err error) error { return err }

// execer is satisfied by both *sql.DB and *sql.Conn, so the probe can treat the
// pool and a pinned connection the same way.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}
