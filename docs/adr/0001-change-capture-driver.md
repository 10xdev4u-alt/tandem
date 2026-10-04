# 0001. Vendor mattn with our own cgo session binding

- Status: accepted
- Date: 2026-10-03
- Issue: #14
- Supersedes: nothing

## Context

The daemon's purpose is change capture. Both engines keep an append-only log and
we have to read it out of SQLite. No Go driver makes that easy, and the two
candidates fail in opposite directions.

`github.com/mattn/go-sqlite3` v1.14.52 is alive, MIT, 9.2k stars, last push
2026-09-28. It ships SQLite 3.53.4. It does not enable the session extension and
exports no binding for it. The request has been open since 2020 as issue 825, and
pull request 1334 has sat unmerged for eighteen months.

`gosqlite.org` v0.14.0 is a driver fork wrapping `modernc.org/sqlite`. It exposes
the session extension as a typed Go API that works today, with no build tags and
no cgo.

We built both and measured them. Everything below was produced by the two probes
in this directory, on go1.27.0-X:nodwarf5 linux/amd64, gcc 16.2.1, SQLite CLI
3.53.4, psql 18.6.

## Measurements

Both rows are real output, not estimates.

| | mattn v1.14.52 | gosqlite v0.14.0 |
|---|---|---|
| `ENABLE_SESSION` compiled | 0 by default, 1 with `CGO_CFLAGS` | yes, no tag needed |
| session reachable from Go | no | yes |
| capture 100k rows | not possible | 691 to 919 ms across runs |
| changeset size | n/a | 5251.8 KiB, 53.8 bytes per row |
| apply to replica | n/a | 467 to 631 ms, 100000 rows, correct |
| apply a second time | n/a | aborts with an error, row count unchanged |
| invert and apply inverse | n/a | 0 rows, undo works |
| cold build, run 1 | 103670 ms | 43565 ms |
| cold build, run 2 | 103812 ms | 44076 ms |
| binary size | 7.2 MiB | 10.1 MiB |
| `CGO_ENABLED=0` | builds a 3.4 MiB stub that fails at runtime | cross compiles and works |
| transitive dependencies | 2 | libc, mathutil, memory, sqlite |

Reproduce with `go run ./cmd/mattn -rows 100000` and
`go run ./cmd/gosqlite -rows 100000`.

Build times reproduced across two runs with the cache cleared before each, and
agreed within 0.2 percent. The capture and apply timings did not, so treat those
as a range and not a figure. The changeset size did reproduce exactly at 5251.8
KiB, which makes it the number worth arguing about.

## What we found that we did not expect

Re-applying a changeset to a replica that already has the rows aborts with
`query aborted (4)`. It does not duplicate rows and it does not silently
succeed. That is the safe failure, but it means the bridge needs an explicit
conflict handler rather than relying on the default.

`PRAGMA foreign_keys = ON` followed by an insert through `database/sql` did **not**
enforce the foreign key. The pragma is per connection, and the pool handed the
insert to a different one. Anyone who sets the pragma once at startup and assumes
it holds for the process is wrong. This has to be set on every connection the
daemon opens, and it is now recorded as a trap in the operating agreement.

mattn with `CGO_ENABLED=0` produces a 3.4 MiB binary that compiles cleanly and
then fails at runtime with a message saying it is a stub. Cross compilation
silently produces a broken artifact rather than failing the build.

## Decision

Vendor `mattn/go-sqlite3` v1.14.52 and write our own minimal cgo binding to
`sqlite3session_*` and `sqlite3changeset_*`. Build with
`CGO_CFLAGS="-DSQLITE_ENABLE_SESSION -DSQLITE_ENABLE_PREUPDATE_HOOK"`.

## Why

The deciding factor is provenance, not capability.

`gosqlite.org` works today and its API is the nicer one. It has 12 stars, 0
importers on pkg.go.dev, and was created in May 2026. It pins
`modernc.org/sqlite` at v1.54.0, which predates v1.56.0, where modernc
backported a fix for an upstream data-corruption bug in SQLite 3.53.3's journal
rollback. It also pins `modernc.org/libc` in lockstep, so the two cannot be
upgraded independently.

Putting a replication path on a dependency that ships a SQLite release missing a
known corruption fix is exactly how you get a replica that quietly diverges and
nobody notices for a month. That is the one failure mode this project exists to
make visible, so it cannot be the one place we hide it.

mattn ships SQLite 3.53.4, which is the same version as the local CLI and the
version every measurement in our research cites. Its session code is genuine
upstream SQLite C. The cost of that choice is a binding we own, and roughly 100
lines of it.

CGo is not disqualifying here. This is a daemon we deploy, not a library we ship
into someone else's build, so a C toolchain in the image is a normal cost. It
costs about 60 seconds of cold build and forfeits clean cross compilation, both
of which we accept and record.

## Consequences

We accept these, on purpose.

- Cold builds take roughly 104 seconds against 44 for the pure Go option. CI pays
  this on every cold runner.
- Cross compilation needs a C toolchain per target. A `CGO_ENABLED=0` build must
  be treated as a build failure, not a warning, and we add a check for it.
- We own a binding we wrote. It gets reviewed like any other code in the repo and
  it carries a test that captures, applies, re-applies and inverts.
- mattn is effectively maintained by one person. It is an active one, and issue
  825 is worth watching, but the risk is real and named.

## Follow-ups

- Write the binding and its test. The probe here is the specification.
- Add a startup assertion that `sqlite_compileoption_used('ENABLE_SESSION')` is
  1, and fail loudly when it is not.
- Make `CGO_ENABLED=0` builds fail CI explicitly.
- Set `foreign_keys` on every connection, never once at startup.
- Give the bridge an explicit changeset conflict handler.
- Re-measure when SQLite 3.54 ships.

## What we rejected, and why

`crawshaw.io/sqlite` is dead. Tagged in 2020, last push 2024, 37 open issues
unanswered. Not archived, so tooling will not warn us.

`ncruces/go-sqlite3` v0.35.6 is alive and cgo-free, and deliberately leaves the
session extension out of its build configuration to keep the number of compiled
variants small. The maintainer has said he is open to adding it if people insist.
Worth revisiting if we ever need cross compilation more than we need cgo.

`modernc.org/sqlite` v1.60.1 compiles the session extension in but cannot expose
it. Every call needs an internal TLS handle and the driver exports no connection
type. Reachable only through a fork, which is the gosqlite situation again with
one more indirection.

Vendoring pull request 1334 whole was considered and not chosen. It is 1106 lines
across three files from a contributor whose fork is archived, unmerged for
eighteen months. Writing the sixty lines we actually need is smaller and we own
it.

## Reproducing this spike

```
go run ./cmd/mattn    -rows 100000
go run ./cmd/gosqlite -rows 100000
```

Both probes are in this directory and both are committed.