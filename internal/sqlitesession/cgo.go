// Package sqlitesession is a minimal cgo binding to the SQLite session extension.
//
// Why this exists and why it looks like this: ADR 0001 chose mattn/go-sqlite3,
// which compiles the SQLite amalgamation but does not export the sqlite3 handle
// and has no binding for the session extension. Its pull request adding one has
// been open about eighteen months. Forcing the handle out means forking a nine
// megabyte dependency.
//
// So instead of forking, this package imports mattn purely for the side effect
// of linking its amalgamation, then declares the handful of C entry points it
// needs as extern and calls them against its own connection handle. The symbols
// resolve at link time against the object mattn already compiled.
//
// The cost is stated plainly: this relies on another package's C symbols being
// present in the final link. That is unconventional and fragile. It is still far
// smaller and more reviewable than forking mattn, and if it breaks it breaks
// loudly at link time rather than silently at runtime.
package sqlitesession

/*
#cgo CFLAGS: -DSQLITE_ENABLE_SESSION -DSQLITE_ENABLE_PREUPDATE_HOOK
#include <stdlib.h>
#include <stdint.h>

typedef struct sqlite3 sqlite3;
typedef struct sqlite3_stmt sqlite3_stmt;
typedef struct sqlite3_session sqlite3_session;
typedef struct sqlite3changeset_iter sqlite3changeset_iter;
typedef struct sqlite3_value sqlite3_value;

extern int   sqlite3_open_v2(const char*, sqlite3**, int, const char*);
extern int   sqlite3_close(sqlite3*);
extern char* sqlite3_errmsg(sqlite3*);
extern int   sqlite3_exec(sqlite3*, const char*, void*, void*, char**);
extern int   sqlite3_prepare_v2(sqlite3*, const char*, int, sqlite3_stmt**, const char**);
extern int   sqlite3_step(sqlite3_stmt*);
extern int   sqlite3_finalize(sqlite3_stmt*);
extern long long sqlite3_column_int64(sqlite3_stmt*, int);
extern int   sqlite3_bind_text(sqlite3_stmt*, int, const char*, int, void*);

// SQLITE_TRANSIENT makes SQLite copy the string. A null destructor means SQLite
// keeps the pointer, so freeing the buffer right after the bind leaves it
// reading freed memory. The sentinel is address -1 cast to a destructor, which
// cannot be written in Go.
int bind_text_copy(sqlite3_stmt *s, int i, const char *v) {
  return sqlite3_bind_text(s, i, v, -1, (void *)(-1));
}
extern const unsigned char* sqlite3_column_text(sqlite3_stmt*, int);
extern void  sqlite3_free(void*);
extern void* sqlite3_malloc64(int64_t);

extern int   sqlite3session_create(sqlite3*, const char*, sqlite3_session**);
extern int   sqlite3session_attach(sqlite3_session*, const char*);
extern int   sqlite3session_enable(sqlite3_session*, int);
extern int   sqlite3session_isempty(sqlite3_session*);
extern int   sqlite3session_changeset(sqlite3_session*, int*, void**);
extern int   sqlite3session_patchset(sqlite3_session*, int*, void**);
extern void  sqlite3session_delete(sqlite3_session*);

extern int sqlite3changeset_start(sqlite3changeset_iter**, int, void*);
extern int sqlite3changeset_next(sqlite3changeset_iter*);
extern int sqlite3changeset_op(sqlite3changeset_iter*, const char**, int*, int*);
extern int sqlite3changeset_finalize(sqlite3changeset_iter*);

extern int sqlite3changeset_invert(int, void*, int*, void**);
extern int sqlite3changeset_concat(int, void*, int, void*, int*, void**);
extern int sqlite3changeset_apply_v2(sqlite3*, int, void*, void*, void*, void*, void**, int*);
*/
import "C"

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"
)

// SQLite result and open codes used here.
const (
	rcOK          = 0
	rcRow         = 100
	rcDone        = 101
	openReadWrite = 0x00000002
	openCreate    = 0x00000004
	prepareTail   = -1
)

// Change operation codes reported by sqlite3changeset_op.
const (
	OpInsert = 1
	OpUpdate = 2
	OpDelete = 3
)

// cstr allocates through SQLite's own allocator rather than C.CString. mattn's
// amalgamation installs allocator hooks, so freeing a pointer that Go allocated
// is undefined behaviour. Allocating on SQLite's side makes sqlite3_free legal.
func cstr(s string) (*C.char, func()) {
	n := C.int64_t(len(s) + 1)
	p := C.sqlite3_malloc64(n)
	if p == nil {
		return nil, func() {}
	}
	buf := unsafe.Slice((*byte)(p), n)
	for i := 0; i < len(s); i++ {
		buf[i] = s[i]
	}
	buf[len(s)] = 0
	return (*C.char)(p), func() { C.sqlite3_free(p) }
}

// DB is a SQLite connection that can record and replay changesets.
type DB struct {
	mu       sync.Mutex
	h        *C.sqlite3
	sessions map[*Session]struct{}
}

// Open opens or creates a database file.
func Open(path string) (*DB, error) {
	cpath, freePath := cstr(path)
	defer freePath()

	var h *C.sqlite3
	rc := C.sqlite3_open_v2(cpath, &h, C.int(openReadWrite|openCreate), nil)
	if rc != rcOK {
		msg := fmt.Sprintf("rc=%d", rc)
		if h != nil {
			msg = C.GoString(C.sqlite3_errmsg(h))
			C.sqlite3_close(h)
		}
		return nil, fmt.Errorf("sqlite3_open_v2(%q): %s", path, msg)
	}

	db := &DB{h: h, sessions: make(map[*Session]struct{})}
	runtime.SetFinalizer(db, func(d *DB) { _ = d.Close() })
	return db, nil
}

// Close tears down any open sessions and then releases the connection.
//
// Sessions go first. sqlite3_close does not report SQLITE_BUSY for open
// sessions, so closing the handle while one is alive leaves a dangling pointer
// and the later Session.Close touches freed memory.
func (d *DB) Close() error {
	// Snapshot the sessions and drop the handle under the lock, then do the
	// work outside it. Session.Close takes the same lock to unregister, so
	// holding it here would deadlock.
	d.mu.Lock()
	if d.h == nil {
		d.mu.Unlock()
		return nil
	}
	h := d.h
	sessions := make([]*Session, 0, len(d.sessions))
	for s := range d.sessions {
		sessions = append(sessions, s)
	}
	d.sessions = make(map[*Session]struct{})
	d.mu.Unlock()

	for _, s := range sessions {
		_ = s.Close()
	}

	rc := C.sqlite3_close(h)
	if rc != rcOK {
		return fmt.Errorf("sqlite3_close: rc=%d", rc)
	}

	d.mu.Lock()
	d.h = nil
	runtime.SetFinalizer(d, nil)
	d.mu.Unlock()
	return nil
}

func (d *DB) trackSession(s *Session) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sessions == nil {
		d.sessions = make(map[*Session]struct{})
	}
	d.sessions[s] = struct{}{}
}

func (d *DB) untrackSession(s *Session) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.sessions, s)
}

// closed reports whether the handle is gone, which is what every method touching
// C needs to know before it dereferences it.
func (d *DB) closed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.h == nil
}

// stmt is a prepared statement with the small surface this package needs.
type stmt struct{ s *C.sqlite3_stmt }

func (d *DB) prepare(sql *C.char) (*stmt, error) {
	var h *C.sqlite3_stmt
	if rc := C.sqlite3_prepare_v2(d.h, sql, prepareTail, &h, nil); rc != rcOK || h == nil {
		return nil, fmt.Errorf("prepare: rc=%d", rc)
	}
	return &stmt{s: h}, nil
}

func (st *stmt) bindText(i int, v *C.char) error {
	if rc := C.bind_text_copy(st.s, C.int(i), v); rc != rcOK {
		return fmt.Errorf("bind_text(%d): rc=%d", i, rc)
	}
	return nil
}

func (st *stmt) step() C.int       { return C.sqlite3_step(st.s) }
func (st *stmt) int64(i int) int64 { return int64(C.sqlite3_column_int64(st.s, C.int(i))) }

func (st *stmt) text(i int) string {
	p := C.sqlite3_column_text(st.s, C.int(i))
	if p == nil {
		return ""
	}
	return C.GoString((*C.char)(unsafe.Pointer(p)))
}

// queryStrings runs a text query and collects every row's first column.
func (d *DB) queryStrings(sql string, args ...string) ([]string, error) {
	if d.closed() {
		return nil, errors.New("database is closed")
	}
	csql, free := cstr(sql)
	defer free()

	st, err := d.prepare(csql)
	if err != nil {
		return nil, err
	}
	defer st.finalize()

	for i, a := range args {
		ca, freeA := cstr(a)
		err := st.bindText(i+1, ca)
		freeA()
		if err != nil {
			return nil, err
		}
	}

	var out []string
	for {
		rc := st.step()
		if rc == rcDone {
			return out, nil
		}
		if rc != rcRow {
			return nil, fmt.Errorf("step %q: rc=%d", sql, rc)
		}
		out = append(out, st.text(0))
	}
}
func (st *stmt) finalize() { C.sqlite3_finalize(st.s) }

// queryInt64 runs a single integer query, optionally binding one text argument.
func (d *DB) queryInt64(sql string, args ...string) (int64, error) {
	if d.closed() {
		return 0, errors.New("database is closed")
	}
	csql, freeSQL := cstr(sql)
	defer freeSQL()

	var cargs *C.char
	if len(args) > 0 && args[0] != "" {
		var freeArg func()
		cargs, freeArg = cstr(args[0])
		defer freeArg()
	}

	st, err := d.prepare(csql)
	if err != nil {
		return 0, err
	}
	defer st.finalize()

	if cargs != nil {
		if err := st.bindText(1, cargs); err != nil {
			return 0, err
		}
	}
	if rc := st.step(); rc != rcRow {
		return 0, fmt.Errorf("step %q: rc=%d", sql, rc)
	}
	return st.int64(0), nil
}

// HasPrimaryKey reports whether a table has an explicitly declared PRIMARY KEY.
//
// SQLite records changes only for tables that do, and its own behaviour is to
// attach such a table and then record nothing for it. That looks identical to a
// table that simply received no writes, so Tandem asks rather than assumes.
func (d *DB) HasPrimaryKey(table string) (bool, error) {
	if table == "" {
		return false, errors.New("empty table name")
	}
	n, err := d.queryInt64(
		`SELECT EXISTS(SELECT 1 FROM pragma_table_info(?) WHERE pk > 0)`, table)
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// TableNames lists user tables in the main schema.
func (d *DB) TableNames() ([]string, error) {
	rows, err := d.queryStrings(
		`SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// cbytes copies a Go byte slice onto SQLite's heap so a C call may read it.
// cgo forbids passing Go pointers into C, so the data has to live on the C side.
func cbytes(b []byte) (unsafe.Pointer, func()) {
	p := C.sqlite3_malloc64(C.int64_t(len(b)))
	if p == nil {
		return nil, func() {}
	}
	buf := unsafe.Slice((*byte)(p), len(b))
	copy(buf, b)
	return p, func() { C.sqlite3_free(p) }
}

// QueryInt64 reads a single integer from a one-row, one-column query.
func (d *DB) QueryInt64(sql string) (int64, error) {
	return d.queryInt64(sql)
}

// hasTable reports whether a table exists, which is a different question from
// whether it has a primary key.
func (d *DB) hasTable(name string) bool {
	if d.closed() {
		return false
	}
	rows, err := d.queryStrings(
		`SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = ? LIMIT 1`, name)
	if err != nil {
		return false
	}
	return len(rows) > 0
}

// Exec runs a statement that returns no rows.
func (d *DB) Exec(sql string) error {
	if d.closed() {
		return errors.New("database is closed")
	}
	csql, free := cstr(sql)
	defer free()

	st, err := d.prepare(csql)
	if err != nil {
		return err
	}
	defer st.finalize()

	switch rc := st.step(); rc {
	case rcOK, rcDone:
		return nil
	default:
		return fmt.Errorf("sqlite3_step: rc=%d: %s", rc, d.errMessage())
	}
}

// errMessage reads the connection's error string. It copies out of C memory and
// frees it, because sqlite3_errmsg returns a pointer the caller owns.
func (d *DB) errMessage() string {
	cs := C.sqlite3_errmsg(d.h)
	if cs == nil {
		return "unknown error"
	}
	return C.GoString(cs)
}

// SessionEnabled reports whether this build has the session extension.
//
// This asks the compile options rather than sqlite3session_config, which is
// variadic and cannot be called from cgo. The question is really about the
// build, not the connection, so the compile option is the honest source.
func (d *DB) SessionEnabled() (bool, error) {
	v, err := d.QueryInt64(`SELECT sqlite_compileoption_used('ENABLE_SESSION')`)
	if err != nil {
		return false, err
	}
	return v == 1, nil
}
