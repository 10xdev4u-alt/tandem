# SQLite has one writer, and WAL does not change that

## The short version

SQLite allows one writer at a time. WAL does not give you two. What WAL changes
is that **readers stop blocking the writer**, and the writer stops blocking
readers.

This is the sentence most often stated wrongly, in both directions, so the demo
below is runnable and the claims are asserted in CI.

## See it

One command, both cases:

```
go run ./demos/single-writer
```

```
rollback journal (SQLite's default)
  journal_mode : delete
  reader holds a transaction, writer tries to write
  result       : refused (database is locked)

write ahead log
  journal_mode : wal
  reader holds a transaction, writer tries to write
  result       : the write went through
```

The scenario is a reader holding a transaction open while a second connection
writes. That is not artificial. It is the shape of change capture: something is
reading the database while something else writes to it.

## Why it matters here

The daemon captures changes while the application writes to the same database,
so a capture session is permanently reading. Under the default journal that
reader and the application contend, and writes start failing with
`database is locked`. Under WAL the capture session and the application coexist.

This is also why the bridge has to funnel writes rather than pool them. Two
writers still serialise, in WAL as much as in rollback mode, so "we have WAL now
so let's add a second writer pool" is not a thing that works. One writer, many
readers, and every write deliberately funnelled through it.

The session binding in `internal/sqlitesession` adds a further constraint on
top of this, which is why a session belongs to the connection that created it.
That is recorded in the amendment to ADR 0001.

## This cannot rot

`demos/single-writer` is not just a script. `go test ./demos/...` asserts both
claims and fails if either stops being true on this SQLite build, so the
documentation above cannot quietly become false:

```
go test ./demos/... -v
--- PASS: TestRunDemonstratesBothOutcomes
--- PASS: TestCollideRefusesInRollbackAndAllowsInWAL
    --- PASS: .../rollback_journal_refuses_the_writer
    --- PASS: .../wal_allows_the_writer
    --- PASS: .../a_bare_BEGIN_takes_no_read_lock
```

That last case is there for a specific reason. `BEGIN` on its own takes no read
lock, so a demo that only issued `BEGIN` would appear to prove the rollback
case and would go on appearing to prove it if SQLite's behaviour changed. The
test pins that a transaction which took no lock does *not* block a writer, so
the interesting test is known to be testing the lock and not the syntax.

The command itself exits non-zero if reality disagrees with the prose, so it is
safe to run in a pipeline.

## What is not claimed

- WAL does not make two concurrent writes succeed.
- WAL does not make this database multi-process safe for writes. It makes the
  contention visible and narrow rather than removing it.
- The demo says nothing about Postgres, which does allow concurrent writers.
  The comparison between the two engines is the whole point of the lab, and it
  has not been measured yet.
