package sqlitesession

/*
#cgo CFLAGS: -DSQLITE_ENABLE_SESSION -DSQLITE_ENABLE_PREUPDATE_HOOK
#include <stdlib.h>
#include <stdint.h>

typedef struct sqlite3 sqlite3;
typedef struct sqlite3_session sqlite3_session;
typedef struct sqlite3changeset_iter sqlite3changeset_iter;

extern int sqlite3session_changeset(sqlite3_session*, int*, void**);
extern int sqlite3session_patchset(sqlite3_session*, int*, void**);
extern int sqlite3changeset_invert(int, void*, int*, void**);
extern int sqlite3changeset_concat(int, void*, int, void*, int*, void**);
extern int sqlite3changeset_apply_v2(sqlite3*, int, void*, void*, void*, void*, void**, int*);
extern void sqlite3_free(void*);

// SQLite calls the conflict handler when a changeset collides with existing
// rows. Passing a null handler compiles fine and then segfaults on the first
// conflict, because SQLite dereferences it. Aborting is the safe default:
// nothing is written and the caller decides what to do.
int tandem_conflict(void *ctx, int eConflict, sqlite3changeset_iter *p) {
  (void)ctx; (void)eConflict; (void)p;
  return 2; // SQLITE_CHANGESET_ABORT
}

int tandem_filter(void *ctx, const char *zTab) {
  (void)ctx; (void)zTab;
  return 1; // accept every table in the changeset
}

// The session functions return their buffer through a length out-parameter, so
// they cannot be handed to Go as function values. Static wrappers hand both
// results back in a shape cgo can return.
static int wrap_changeset(sqlite3_session *s, int64_t *n, void **p) {
  int len = 0;
  int rc = sqlite3session_changeset(s, &len, p);
  *n = (int64_t)len;
  return rc;
}

static int wrap_patchset(sqlite3_session *s, int64_t *n, void **p) {
  int len = 0;
  int rc = sqlite3session_patchset(s, &len, p);
  *n = (int64_t)len;
  return rc;
}


static int wrap_invert(int nIn, void *pIn, int64_t *n, void **p) {
  int len = 0;
  int rc = sqlite3changeset_invert(nIn, pIn, &len, p);
  *n = (int64_t)len;
  return rc;
}

static int wrap_concat(int nA, void *pA, int nB, void *pB, int64_t *n, void **p) {
  int len = 0;
  int rc = sqlite3changeset_concat(nA, pA, nB, pB, &len, p);
  *n = (int64_t)len;
  return rc;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

// Changeset returns everything recorded since the session started. Unlike a
// patchset it carries enough old values to be inverted, which is what lets the
// receiving side detect a data conflict rather than silently overwriting.
//
// It does not reset the session. Calling it twice returns the same accumulated
// changes both times, not an increment, so a caller that wants successive
// batches must not assume each call is a delta. That is sqlite3session_changeset
// behaviour rather than a choice here, and it is called out because the
// alternative assumption is easy to make and quietly wrong.
func (s *Session) Changeset() ([]byte, error) {
	if s.closed || s.s == nil {
		return nil, errors.New("session is closed")
	}
	var n C.int64_t
	var p unsafe.Pointer
	if rc := C.wrap_changeset(s.s, &n, &p); rc != rcOK {
		return nil, fmt.Errorf("sqlite3session_changeset: rc=%d", rc)
	}
	return takeBlob(p, n), nil
}

// Patchset is the smaller form. It omits old values, so it cannot be inverted and
// cannot detect data conflicts. Exposed so the difference can be measured rather
// than taken on faith from the documentation.
func (s *Session) Patchset() ([]byte, error) {
	if s.closed || s.s == nil {
		return nil, errors.New("session is closed")
	}
	var n C.int64_t
	var p unsafe.Pointer
	if rc := C.wrap_patchset(s.s, &n, &p); rc != rcOK {
		return nil, fmt.Errorf("sqlite3session_patchset: rc=%d", rc)
	}
	return takeBlob(p, n), nil
}

// takeBlob copies a SQLite allocated blob into Go memory and frees the original,
// which is the point at which a C pointer stops being a C pointer.
func takeBlob(p unsafe.Pointer, n C.int64_t) []byte {
	if p == nil || n == 0 {
		if p != nil {
			C.sqlite3_free(p)
		}
		return nil
	}
	out := C.GoBytes(p, C.int(n))
	C.sqlite3_free(p)
	return out
}

// ApplyChangeset replays a changeset onto this connection.
func (d *DB) ApplyChangeset(cs []byte) error {
	if len(cs) == 0 {
		return nil
	}
	if d.h == nil {
		return errors.New("database is closed")
	}
	// Same rule as Changes: SQLite may hold the buffer past the call, so it is
	// copied onto SQLite's own heap first.
	cp, free := cbytes(cs)
	defer free()

	if rc := C.sqlite3changeset_apply_v2(d.h, C.int(len(cs)), cp,
		C.tandem_filter, C.tandem_conflict, nil, nil, nil); rc != rcOK {
		return fmt.Errorf("sqlite3changeset_apply_v2: rc=%d", rc)
	}
	return nil
}

// InvertChangeset returns a changeset that undoes the given one.
func InvertChangeset(cs []byte) ([]byte, error) {
	if len(cs) == 0 {
		return nil, nil
	}
	p, free := cbytes(cs)
	defer free()

	var n C.int64_t
	var out unsafe.Pointer
	if rc := C.wrap_invert(C.int(len(cs)), p, &n, &out); rc != rcOK {
		return nil, fmt.Errorf("sqlite3changeset_invert: rc=%d", rc)
	}
	return takeBlob(out, n), nil
}

// ConcatChangesets merges two changesets so they replay as one.
func ConcatChangesets(a, b []byte) ([]byte, error) {
	if len(a) == 0 {
		return b, nil
	}
	if len(b) == 0 {
		return a, nil
	}
	ap, freeA := cbytes(a)
	defer freeA()
	bp, freeB := cbytes(b)
	defer freeB()

	var n C.int64_t
	var out unsafe.Pointer
	rc := C.wrap_concat(C.int(len(a)), ap, C.int(len(b)), bp, &n, &out)
	if rc != rcOK {
		return nil, fmt.Errorf("sqlite3changeset_concat: rc=%d", rc)
	}
	return takeBlob(out, n), nil
}
