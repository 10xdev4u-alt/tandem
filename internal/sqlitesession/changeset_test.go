package sqlitesession_test

import (
	"testing"

	"github.com/10xdev4u-alt/tandem/internal/sqlitesession"
)

// record runs fn against a database with a session open, then returns the
// changeset. Keeping the arrange/act/extract shape in one place stops each test
// from re-deriving it slightly differently.
func record(t *testing.T, ddl string, fn func(db *sqlitesession.DB)) []byte {
	t.Helper()
	db := openDB(t, ddl)

	sess, err := db.StartSession()
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Close()

	fn(db)

	cs, err := sess.Changeset()
	if err != nil {
		t.Fatalf("Changeset: %v", err)
	}
	return cs
}

// requireRecorded fails loudly when a session captured nothing. It exists
// because ApplyChangeset returns nil for an empty buffer, which is correct
// no-op behaviour for the API: without this guard an empty changeset sails
// through the apply and the row count underneath reports the symptom instead
// of the cause, pointing the reader at the apply path when nothing was ever
// recorded. That is how main reported "0 rows" once and sent the investigation
// the wrong way.
func requireRecorded(t *testing.T, cs []byte, after string) {
	t.Helper()
	if len(cs) == 0 {
		t.Fatalf("session recorded nothing after %s: changeset is empty", after)
	}
}

func countRows(t *testing.T, db *sqlitesession.DB, table string) int64 {
	t.Helper()
	n, err := db.QueryInt64("SELECT count(*) FROM " + table)
	if err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestChangesetIsEmptyWithNoWrites(t *testing.T) {
	db := openDB(t, accountsDDL)
	sess, err := db.StartSession()
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Close()

	cs, err := sess.Changeset()
	if err != nil {
		t.Fatalf("Changeset: %v", err)
	}
	if len(cs) != 0 {
		t.Errorf("changeset is %d bytes with no writes, want 0", len(cs))
	}
}

// TestInsertUpdateDeleteEachProduceChanges is the core assertion of issue 17.
// The changeset is walked rather than measured, so the claim is about what is in
// it and not about how big it is.
func TestChangesetAppliesToIdenticalReplica(t *testing.T) {
	src := openDB(t, accountsDDL)
	sess, err := src.StartSession("accounts")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Close()

	if err := src.Exec(`INSERT INTO accounts VALUES (1,'ada',100),(2,'grace',300)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := src.Exec(`UPDATE accounts SET cents=250 WHERE id=1`); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := src.Exec(`DELETE FROM accounts WHERE id=2`); err != nil {
		t.Fatalf("delete: %v", err)
	}

	cs, err := sess.Changeset()
	if err != nil {
		t.Fatalf("Changeset: %v", err)
	}
	requireRecorded(t, cs, "insert, update and delete")

	dst := openDB(t, accountsDDL)
	if err := dst.ApplyChangeset(cs); err != nil {
		t.Fatalf("ApplyChangeset: %v", err)
	}

	if got := countRows(t, dst, "accounts"); got != 1 {
		t.Errorf("replica has %d rows, want 1", got)
	}
	cents, err := dst.QueryInt64(`SELECT cents FROM accounts WHERE id=1`)
	if err != nil {
		t.Fatalf("read replica: %v", err)
	}
	if cents != 250 {
		t.Errorf("replica has cents=%d, want the updated 250", cents)
	}
}

// TestInvertedChangesetUndoesTheOriginal is what makes a change log trustworthy.
func TestInvertedChangesetUndoesTheOriginal(t *testing.T) {
	src := openDB(t, accountsDDL)
	sess, err := src.StartSession("accounts")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Close()

	if err := src.Exec(`INSERT INTO accounts VALUES (1,'ada',100)`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	cs, err := sess.Changeset()
	if err != nil {
		t.Fatalf("Changeset: %v", err)
	}
	requireRecorded(t, cs, "one insert")

	dst := openDB(t, accountsDDL)
	if err := dst.ApplyChangeset(cs); err != nil {
		t.Fatalf("apply forward: %v", err)
	}
	if got := countRows(t, dst, "accounts"); got != 1 {
		t.Fatalf("after forward apply replica has %d rows, want 1", got)
	}

	inverse, err := sqlitesession.InvertChangeset(cs)
	if err != nil {
		t.Fatalf("InvertChangeset: %v", err)
	}
	if len(inverse) == 0 {
		t.Fatal("inverted changeset is empty")
	}
	if err := dst.ApplyChangeset(inverse); err != nil {
		t.Fatalf("apply inverse: %v", err)
	}
	if got := countRows(t, dst, "accounts"); got != 0 {
		t.Errorf("after inverse replica has %d rows, want 0", got)
	}
}

// TestContradictoryApplyWritesNothing is the atomicity property the bridge will
// rely on. The replica is pre-seeded so the changeset conflicts part way through.
func TestContradictoryApplyWritesNothing(t *testing.T) {
	src := openDB(t, accountsDDL)
	sess, err := src.StartSession("accounts")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Close()

	if err := src.Exec(`INSERT INTO accounts VALUES (1,'ada',1),(2,'grace',2),(3,'linus',3)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	cs, err := sess.Changeset()
	if err != nil {
		t.Fatalf("Changeset: %v", err)
	}

	dst := openDB(t, accountsDDL)
	// Pre-seed one row that the changeset also creates, forcing a conflict.
	if err := dst.Exec(`INSERT INTO accounts VALUES (2,'conflict',99)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before := countRows(t, dst, "accounts")

	if err := dst.ApplyChangeset(cs); err == nil {
		t.Error("applying a contradictory changeset reported no error")
	}

	if after := countRows(t, dst, "accounts"); after != before {
		t.Errorf("replica went from %d to %d rows, a failed apply must write nothing", before, after)
	}
	owner, err := dst.QueryInt64(`SELECT cents FROM accounts WHERE id=2`)
	if err != nil {
		t.Fatalf("read conflict row: %v", err)
	}
	if owner != 99 {
		t.Errorf("conflicting row was modified, cents=%d, want the original 99", owner)
	}
}

func TestEmptyChangesetApplyIsNoOp(t *testing.T) {
	db := openDB(t, accountsDDL)
	if err := db.ApplyChangeset(nil); err != nil {
		t.Errorf("applying an empty changeset: %v", err)
	}
}

func TestChangesetOnClosedSessionFails(t *testing.T) {
	db := openDB(t, accountsDDL)
	sess, err := db.StartSession("accounts")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := sess.Changeset(); err == nil {
		t.Error("Changeset on a closed session returned no error")
	}
}

// TestConcatChangesetsProducesOneReplayableResult covers the reason concat
// exists. Two changesets from two independent sources, applied as one unit, must
// reach the same state as applying them separately. The bridge replays into a
// single connection that may already be holding unapplied work from another
// source, so a combined changeset has to stay replayable.
func TestConcatChangesetsProducesOneReplayableResult(t *testing.T) {
	// Two separate sources, so each changeset describes a distinct row. Using one
	// session twice would not work: Changeset reports everything since the
	// session started and does not reset, so the second call returns the first
	// call's changes again rather than an increment.
	alpha := record(t, accountsDDL, func(db *sqlitesession.DB) {
		if err := db.Exec("INSERT INTO accounts VALUES (1, 'alpha', 100)"); err != nil {
			t.Fatalf("alpha insert: %v", err)
		}
	})
	beta := record(t, accountsDDL, func(db *sqlitesession.DB) {
		if err := db.Exec("INSERT INTO accounts VALUES (2, 'beta', 200)"); err != nil {
			t.Fatalf("beta insert: %v", err)
		}
	})

	combined, err := sqlitesession.ConcatChangesets(alpha, beta)
	if err != nil {
		t.Fatalf("ConcatChangesets: %v", err)
	}
	if len(combined) == 0 {
		t.Fatal("ConcatChangesets returned an empty changeset for two non-empty inputs")
	}
	if len(combined) <= len(alpha) && len(combined) <= len(beta) {
		t.Error("the concatenated changeset is not larger than either input, " +
			"so at least one of them was dropped")
	}

	dst := openDB(t, accountsDDL)
	if err := dst.ApplyChangeset(combined); err != nil {
		t.Fatalf("ApplyChangeset on the concatenated result: %v", err)
	}
	for _, tc := range []struct {
		id   string
		want int64
	}{{"1", 100}, {"2", 200}} {
		if got, err := dst.QueryInt64(`SELECT cents FROM accounts WHERE id = ` + tc.id); err != nil {
			t.Errorf("query id=%s: %v", tc.id, err)
		} else if got != tc.want {
			t.Errorf("cents for id=%s = %d, want %d", tc.id, got, tc.want)
		}
	}
}

// TestConcatChangesetsPassesThroughEmptyInput pins the short circuits, so a
// changeset with nothing in it does not cost an allocation or a copy.
func TestConcatChangesetsPassesThroughEmptyInput(t *testing.T) {
	only := record(t, accountsDDL, func(db *sqlitesession.DB) {
		if err := db.Exec("INSERT INTO accounts VALUES (1, 'a', 100)"); err != nil {
			t.Fatalf("insert: %v", err)
		}
	})

	for _, tc := range []struct {
		name string
		a, b []byte
	}{
		{"both empty", nil, nil},
		{"first empty", nil, only},
		{"second empty", only, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sqlitesession.ConcatChangesets(tc.a, tc.b)
			if err != nil {
				t.Fatalf("ConcatChangesets: %v", err)
			}
			if tc.a == nil && tc.b == nil {
				if len(got) != 0 {
					t.Errorf("got %d bytes, want empty", len(got))
				}
				return
			}
			if len(got) != len(only) {
				t.Errorf("got %d bytes, want the non-empty input verbatim (%d)", len(got), len(only))
			}
		})
	}
}
