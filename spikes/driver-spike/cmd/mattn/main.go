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
	fmt.Println()
	fmt.Println("foreign key behaviour:")
	for _, tc := range []struct {
		name   string
		pinned bool
	}{
		{"unpinned pool", false},
		{"pinned single connection", true},
	} {
		orphanRejected := false
		func() {
			// Both *sql.DB and *sql.Conn satisfy these three methods, so one
			// branch handles the pool and the pinned case identically.
			conn, err := db.Conn(context.Background())
			if tc.pinned {
				must(err)
				defer conn.Close()
				must(exec(conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`)))
				must(exec(conn.ExecContext(ctx, `DROP TABLE IF EXISTS child`)))
				must(exec(conn.ExecContext(ctx, childSchema)))
				_, err = conn.ExecContext(ctx, `INSERT INTO child (id, parent) VALUES (1, 999999)`)
				orphanRejected = err != nil
				return
			}
			conn.Close()
			must(exec(db.Exec(`PRAGMA foreign_keys=ON`)))
			must(exec(db.Exec(`DROP TABLE IF EXISTS child`)))
			must(exec(db.Exec(childSchema)))
			_, err = db.Exec(`INSERT INTO child (id, parent) VALUES (1, 999999)`)
			orphanRejected = err != nil
		}()
		fmt.Printf("  %-26s orphan rejected=%v\n", tc.name, orphanRejected)
	}

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

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}
