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

// #cgo CFLAGS: -DSQLITE_ENABLE_SESSION -DSQLITE_ENABLE_PREUPDATE_HOOK
// #include <stdlib.h>
// #include <stdint.h>
//
// typedef struct sqlite3 sqlite3;
// typedef struct sqlite3_stmt sqlite3_stmt;
// typedef struct sqlite3_session sqlite3_session;
// typedef struct sqlite3changeset_iter sqlite3changeset_iter;
// typedef struct sqlite3_value sqlite3_value;
//
// extern int   sqlite3_open_v2(const char*, sqlite3**, int, const char*);
// extern int   sqlite3_close(sqlite3*);
// extern char* sqlite3_errmsg(sqlite3*);
// extern int   sqlite3_exec(sqlite3*, const char*, void*, void*, char**);
// extern int   sqlite3_prepare_v2(sqlite3*, const char*, int, sqlite3_stmt**, const char**);
// extern int   sqlite3_step(sqlite3_stmt*);
// extern int   sqlite3_finalize(sqlite3_stmt*);
// extern long long sqlite3_column_int64(sqlite3_stmt*, int);
// extern void  sqlite3_free(void*);
// extern void* sqlite3_malloc64(int64_t);
//
// extern int   sqlite3session_create(sqlite3*, const char*, sqlite3_session**);
// extern int   sqlite3session_attach(sqlite3_session*, const char*);
// extern int   sqlite3session_enable(sqlite3_session*, int);
// extern int   sqlite3session_isempty(sqlite3_session*);
// extern int   sqlite3session_changeset(sqlite3_session*, int*, void**);
// extern int   sqlite3session_patchset(sqlite3_session*, int*, void**);
// extern void  sqlite3session_delete(sqlite3_session*);
//
// extern int sqlite3changeset_start(sqlite3changeset_iter**, int, void*);
// extern int sqlite3changeset_next(sqlite3changeset_iter*);
// extern int sqlite3changeset_op(sqlite3changeset_iter*, const char**, int*, int*);
// extern int sqlite3changeset_finalize(sqlite3changeset_iter*);
//
// extern int sqlite3changeset_invert(int, void*, int*, void**);
// extern int sqlite3changeset_concat(int, void*, int, void*, int*, void**);
// extern int sqlite3changeset_apply_v2(sqlite3*, int, void*, void*, void*, void*, void**, int*);
import "C"

import (
	"fmt"
	"runtime"
	"unsafe"
)

// SQLite result and open codes used here.
const (
	rcOK          = 0
	rcRow         = 100
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
	h *C.sqlite3
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

	db := &DB{h: h}
	runtime.SetFinalizer(db, func(d *DB) { _ = d.Close() })
	return db, nil
}

// Close releases the connection.
func (d *DB) Close() error {
	if d.h == nil {
		return nil
	}
	h := d.h
	d.h = nil
	runtime.SetFinalizer(d, nil)
	if rc := C.sqlite3_close(h); rc != rcOK {
		return fmt.Errorf("sqlite3_close: rc=%d", rc)
	}
	return nil
}

// Exec runs one or more statements, discarding rows.
func (d *DB) Exec(sql string) error {
	csql, freeSQL := cstr(sql)
	defer freeSQL()

	var errmsg *C.char
	rc := C.sqlite3_exec(d.h, csql, nil, nil, &errmsg)
	if errmsg != nil {
		defer C.sqlite3_free(unsafe.Pointer(errmsg))
	}
	if rc != rcOK {
		if errmsg != nil {
			return fmt.Errorf("exec: %s", C.GoString(errmsg))
		}
		return fmt.Errorf("exec: rc=%d", rc)
	}
	return nil
}

// QueryInt64 runs a statement expected to yield one integer.
func (d *DB) QueryInt64(sql string) (int64, error) {
	csql, freeSQL := cstr(sql)
	defer freeSQL()

	var stmt *C.sqlite3_stmt
	if rc := C.sqlite3_prepare_v2(d.h, csql, prepareTail, &stmt, nil); rc != rcOK || stmt == nil {
		return 0, fmt.Errorf("prepare %q: rc=%d", sql, rc)
	}
	defer C.sqlite3_finalize(stmt)

	if rc := C.sqlite3_step(stmt); rc != rcRow {
		return 0, fmt.Errorf("step %q: rc=%d", sql, rc)
	}
	return int64(C.sqlite3_column_int64(stmt, 0)), nil
}

// SessionEnabled reports whether the linked SQLite has the session extension.
func (d *DB) SessionEnabled() (bool, error) {
	v, err := d.QueryInt64(`SELECT sqlite_compileoption_used('ENABLE_SESSION')`)
	if err != nil {
		return false, err
	}
	return v == 1, nil
}
