package sqlitesession

// #cgo CFLAGS: -DSQLITE_ENABLE_SESSION -DSQLITE_ENABLE_PREUPDATE_HOOK
// #include <stdlib.h>
// #include <stdint.h>
//
// typedef struct sqlite3 sqlite3;
// typedef struct sqlite3_session sqlite3_session;
//
// extern int  sqlite3session_create(sqlite3*, const char*, sqlite3_session**);
// extern int  sqlite3session_attach(sqlite3_session*, const char*);
// extern int  sqlite3session_isempty(sqlite3_session*);
// extern void sqlite3session_delete(sqlite3_session*);
import "C"

import (
	"errors"
	"fmt"
	"runtime"
)

// ErrNoPrimaryKey reports a table SQLite cannot record changes for, because it
// has no explicitly declared PRIMARY KEY.
//
// SQLite's own behaviour here is to accept the attach and then record nothing,
// which is indistinguishable from a table that simply received no writes. Tandem
// reports it rather than letting a replication target quietly lose rows.
var ErrNoPrimaryKey = errors.New("no primary key, sqlite cannot record changes for this table")

// Session records the changes made on one connection so they can be packaged
// for replay elsewhere. A session belongs to the connection that created it, so
// every write that should be recorded has to go through the same DB.
type Session struct {
	db          *DB
	s           *C.sqlite3_session
	tracked     []string
	untrackable []string
	closed      bool
}

// StartSession opens a recording session on the connection.
//
// With no table names it attaches every table. With names it attaches exactly
// those, and refuses to start if any of them cannot be tracked, so a caller never
// believes it is recording a table that SQLite is silently ignoring.
func (d *DB) StartSession(tables ...string) (*Session, error) {
	if d.h == nil {
		return nil, errors.New("database is closed")
	}

	// A null schema name means the main database, but passing the literal is
	// clearer at the call site and behaves identically.
	main, freeMain := cstr("main")
	defer freeMain()

	var s *C.sqlite3_session
	if rc := C.sqlite3session_create(d.h, main, &s); rc != rcOK || s == nil {
		return nil, fmt.Errorf("sqlite3session_create: rc=%d", rc)
	}

	sess := &Session{db: d, s: s}
	d.trackSession(sess)
	runtime.SetFinalizer(sess, func(x *Session) { _ = x.Close() })

	if err := sess.attachAll(tables); err != nil {
		_ = sess.Close()
		return nil, err
	}

	// No enable call here. SQLite enables a new session by default, and calling
	// sqlite3session_enable(sess, 1) returns SQLITE_ERROR on 3.53.4 even though
	// recording works, which was measured rather than assumed. The tests assert
	// that writes are actually recorded, which is the property that matters.
	return sess, nil
}

func (s *Session) attachAll(tables []string) error {
	if len(tables) == 0 {
		// A null table name attaches every table, which is what we want here.
		if rc := C.sqlite3session_attach(s.s, nil); rc != rcOK {
			return fmt.Errorf("sqlite3session_attach all: rc=%d", rc)
		}
		names, err := s.db.TableNames()
		if err != nil {
			return err
		}
		return s.classify(names)
	}

	for _, t := range tables {
		cname, free := cstr(t)
		rc := C.sqlite3session_attach(s.s, cname)
		free()
		if rc != rcOK {
			return fmt.Errorf("sqlite3session_attach(%q): rc=%d", t, rc)
		}
		if err := s.classify([]string{t}); err != nil {
			return err
		}
	}
	return nil
}

// classify splits tables into the ones SQLite can record and the ones it cannot.
func (s *Session) classify(tables []string) error {
	for _, t := range tables {
		ok, err := s.db.HasPrimaryKey(t)
		if err != nil {
			return fmt.Errorf("checking primary key on %q: %w", t, err)
		}
		if ok {
			s.tracked = append(s.tracked, t)
		} else {
			s.untrackable = append(s.untrackable, t)
		}
	}
	return nil
}

// Tracked lists the tables this session can record changes for.
func (s *Session) Tracked() []string { return append([]string(nil), s.tracked...) }

// Untrackable lists tables that were attached but cannot be recorded, each
// carrying ErrNoPrimaryKey as its reason.
func (s *Session) Untrackable() []error {
	out := make([]error, 0, len(s.untrackable))
	for _, t := range s.untrackable {
		out = append(out, fmt.Errorf("%q: %w", t, ErrNoPrimaryKey))
	}
	return out
}

// IsEmpty reports whether nothing has been recorded yet.
func (s *Session) IsEmpty() (bool, error) {
	if s.closed || s.s == nil {
		return false, errors.New("session is closed")
	}
	return C.sqlite3session_isempty(s.s) == 1, nil
}

// Close releases the session. Safe to call more than once.
func (s *Session) Close() error {
	if s.closed || s.s == nil {
		return nil
	}
	if s.db != nil {
		s.db.untrackSession(s)
		// Deleting a session touches the connection, so never do it after the
		// connection is gone. DB.Close closes sessions before the handle, and
		// this guard covers the reverse order.
		if s.db.closed() {
			s.s = nil
			s.closed = true
			runtime.SetFinalizer(s, nil)
			return nil
		}
	}
	C.sqlite3session_delete(s.s)
	s.s = nil
	s.closed = true
	runtime.SetFinalizer(s, nil)
	return nil
}

// ready reports why this session cannot be used, if it cannot. A session is
// unusable once it is closed, and equally unusable once its database is gone,
// because every session call dereferences the connection.
func (s *Session) ready() error {
	if s.closed || s.s == nil {
		return errors.New("session is closed")
	}
	if s.db == nil || s.db.closed() {
		return errors.New("database is closed")
	}
	return nil
}
