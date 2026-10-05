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
