package engine

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// openRaw opens a database the way code outside this package does, with no
// pragmas applied. It exists so the tests can show what this package prevents.
func openRaw(t *testing.T) (*sql.DB, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "raw.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

func mustExec(t *testing.T, db *sql.DB, query string) {
	t.Helper()

	if _, err := db.Exec(query); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func orphanSchema(t *testing.T, db *sql.DB) {
	t.Helper()

	mustExec(t, db, "CREATE TABLE parent (id INTEGER PRIMARY KEY)")
	mustExec(t, db, "CREATE TABLE child (id INTEGER PRIMARY KEY, pid INTEGER NOT NULL REFERENCES parent(id))")
}

// TestOrphanRowRejected is the acceptance test: with foreign keys on, an
// orphan row must fail.
func TestOrphanRowRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fk.db")
	db, err := OpenSQLite(path, SQLiteOptions{PageSize: 4096, BusyTimeout: time.Second})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	orphanSchema(t, db)

	_, err = db.Exec("INSERT INTO child (id, pid) VALUES (1, 999)")
	if err == nil {
		t.Fatal("orphan insert succeeded, want FOREIGN KEY constraint failed")
	}
	if !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("orphan insert error = %q, want a FOREIGN KEY failure", err)
	}
}

// TestOrphanRowAcceptedWithoutForeignKeys is the control for the test above.
// It opens the same schema with foreign keys left at SQLite's default of off
// and shows the identical insert succeeding, so the rejection above is
// attributable to this package and not to the schema or the driver.
func TestOrphanRowAcceptedWithoutForeignKeys(t *testing.T) {
	db, _ := openRaw(t)

	orphanSchema(t, db)

	if _, err := db.Exec("INSERT INTO child (id, pid) VALUES (1, 999)"); err != nil {
		t.Fatalf("orphan insert with foreign keys off failed: %v", err)
	}
}

// TestForeignKeysReachEveryConnection pins several connections open at once
// and reads the setting back from each. Reading them one after another would
// let database/sql hand back the same pooled connection each time, which is
// exactly how a one-off PRAGMA looks like it works when it does not.
func TestForeignKeysReachEveryConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pool.db")
	db, err := OpenSQLite(path, SQLiteOptions{PageSize: 4096, BusyTimeout: time.Second})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	db.SetMaxOpenConns(8)

	var (
		conns []*sql.Conn
		mu    sync.Mutex
		wg    sync.WaitGroup
	)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := db.Conn(t.Context())
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(conns) != 4 {
		t.Fatalf("held %d connections, want 4", len(conns))
	}
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()

	for i, c := range conns {
		var fk int
		if err := c.QueryRowContext(t.Context(), "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatalf("conn %d: %v", i, err)
		}
		if fk != 1 {
			t.Errorf("connection %d has foreign_keys = %d, want 1", i, fk)
		}
	}
}

func TestJournalModeIsWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.db")
	db, err := OpenSQLite(path, SQLiteOptions{PageSize: 4096, BusyTimeout: time.Second})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("read journal mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
}

// TestPageSizeApplied uses a non-default page size so the assertion cannot be
// satisfied by SQLite's own default of 4096.
func TestPageSizeApplied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "page.db")
	db, err := OpenSQLite(path, SQLiteOptions{PageSize: 8192, BusyTimeout: time.Second})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	var size int
	if err := db.QueryRow("PRAGMA page_size").Scan(&size); err != nil {
		t.Fatalf("read page size: %v", err)
	}
	if size != 8192 {
		t.Fatalf("page_size = %d, want 8192", size)
	}
}

// TestPageSizeMismatchRejected shows the assertion firing. The driver's own
// default busy timeout is 5000ms, so the same care applies here: a database
// that already holds data keeps its page size, and asking for a different one
// must be a startup error rather than a setting that quietly does not apply.
func TestPageSizeMismatchRejected(t *testing.T) {
	db, path := openRaw(t)
	mustExec(t, db, "PRAGMA page_size = 4096")
	mustExec(t, db, "CREATE TABLE t (x)")
	mustExec(t, db, "INSERT INTO t VALUES (1)")

	_, err := OpenSQLite(path, SQLiteOptions{PageSize: 8192, BusyTimeout: time.Second})
	if err == nil {
		t.Fatal("open succeeded, want a page size mismatch error")
	}
	if !strings.Contains(err.Error(), "page size") {
		t.Fatalf("error = %q, want it to name the page size", err)
	}
	if !strings.Contains(err.Error(), "VACUUM") {
		t.Fatalf("error = %q, want it to say how to fix it", err)
	}
}

// TestBusyTimeoutApplied uses a value other than the driver's built-in 5000ms
// so a pass means the configuration reached SQLite, not that the default was
// read back.
func TestBusyTimeoutApplied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.db")
	db, err := OpenSQLite(path, SQLiteOptions{PageSize: 4096, BusyTimeout: 1234 * time.Millisecond})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	var got int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&got); err != nil {
		t.Fatalf("read busy timeout: %v", err)
	}
	if got != 1234 {
		t.Fatalf("busy_timeout = %d, want 1234", got)
	}
}

func TestZeroBusyTimeoutMeansNoWait(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy0.db")
	db, err := OpenSQLite(path, SQLiteOptions{PageSize: 4096, BusyTimeout: 0})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	var got int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&got); err != nil {
		t.Fatalf("read busy timeout: %v", err)
	}
	if got != 0 {
		t.Fatalf("busy_timeout = %d, want 0", got)
	}
}

func TestPathWithQuestionMarkRejected(t *testing.T) {
	_, err := OpenSQLite(filepath.Join(t.TempDir(), "bad?.db"),
		SQLiteOptions{PageSize: 4096, BusyTimeout: time.Second})
	if err == nil {
		t.Fatal("open succeeded, want an error for a '?' in the path")
	}
	if !strings.Contains(err.Error(), "must not contain") {
		t.Fatalf("error = %q, want it to explain the rejected character", err)
	}
}

func TestInvalidOptionsRejected(t *testing.T) {
	tests := []struct {
		name string
		opts SQLiteOptions
		want string
	}{
		{"zero page size", SQLiteOptions{PageSize: 0, BusyTimeout: time.Second}, "power of two"},
		{"page size not a power of two", SQLiteOptions{PageSize: 5000, BusyTimeout: time.Second}, "power of two"},
		{"page size too small", SQLiteOptions{PageSize: 256, BusyTimeout: time.Second}, "power of two"},
		{"page size too large", SQLiteOptions{PageSize: 131072, BusyTimeout: time.Second}, "power of two"},
		{"negative busy timeout", SQLiteOptions{PageSize: 4096, BusyTimeout: -time.Second}, "zero or greater"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := OpenSQLite(filepath.Join(t.TempDir(), "x.db"), tt.opts)
			if err == nil {
				t.Fatal("open succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

// TestUnreachablePathReportsAtStartup covers the ping. A path in a directory
// that does not exist must fail now rather than at the first write.
func TestUnreachablePathReportsAtStartup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no", "such", "dir", "tandem.db")
	_, err := OpenSQLite(path, SQLiteOptions{PageSize: 4096, BusyTimeout: time.Second})
	if err == nil {
		t.Fatal("open succeeded, want an error for a path in a missing directory")
	}
	if !errors.Is(err, os.ErrNotExist) && !strings.Contains(err.Error(), "unable to open") {
		t.Fatalf("error = %v, want an open failure naming the path", err)
	}
}

// TestBadFileRejected covers a path that exists but is not a database, so the
// assertion path reports it instead of returning a handle that fails later.
func TestBadFileRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notadb.db")
	if err := os.WriteFile(path, []byte("this is not a sqlite database"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := OpenSQLite(path, SQLiteOptions{PageSize: 4096, BusyTimeout: time.Second})
	if err == nil {
		t.Fatal("open succeeded, want an error for a file that is not a database")
	}
}
