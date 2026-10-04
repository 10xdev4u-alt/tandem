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

`gosqlite.org` v0.14.0 is a driver fork wrapping `modernc.org/sqlite`. The import
path is a vanity URL, and the repository behind it is
`github.com/go-again/sqlite`. Name that repository, not the vanity path, or an
auditor who searches for `gosqlite.org` on GitHub lands on a zero star Pages stub
instead of the real source. It exposes the session extension as a typed Go API
that works today, with no build tags and no cgo.

We built both and measured them. Everything below was produced by the two probes
in this directory, on go1.27.0-X:nodwarf5 linux/amd64, gcc 16.2.1, SQLite CLI
3.53.4, psql 18.6, four cores.

## Measurements

Both columns are real output, not estimates.

| | mattn v1.14.52 | gosqlite v0.14.0 |
|---|---|---|
| `ENABLE_SESSION` compiled | 0 by default, 1 with `CGO_CFLAGS` | yes, no tag needed |
| session reachable from Go | no | yes |
| capture 100k rows | not possible | 674 to 919 ms across runs |
| changeset size | n/a | 5251.8 KiB, 53.8 bytes per row |
| apply to replica | n/a | 467 to 631 ms, 100000 rows, correct |
| apply a second time | n/a | errors, row count unchanged |
| apply over a partial overlap | n/a | errors, all or nothing |
| invert and apply inverse | n/a | 0 rows, undo works |
| cold build, run 1 | 103670 ms | 43565 ms |
| cold build, run 2 | 103812 ms | 44076 ms |
| binary size | 7.2 MiB | 10.1 MiB |
| `CGO_ENABLED=0` | builds a 3.4 MiB stub that fails at runtime | cross compiles and works |
| transitive dependencies | 2 | libc, mathutil, memory, sqlite |

Reproduce from the probe module directory, see the last section.

Build times reproduced across two runs with the cache cleared before each, and
agreed within 0.2 percent. An independent reviewer on different hardware saw
82 s against 35 s for the same two builds, which is ordinary machine variation.
The capture and apply timings moved between runs too, so treat those as a range
rather than a figure. The changeset size reproduced exactly at 5251.8 KiB, which
makes it the number worth arguing about.

## What the measurements actually showed

### An aborted apply is all or nothing

This is the finding that changed what we build.

Re-applying a changeset to a replica that already holds every row errors with
`query aborted (4)` and writes nothing. That alone does not prove atomicity,
because the first insert would conflict and nothing would be written either way.
So the probe pre-seeds a replica with the first 50000 of 100000 rows and then
applies the whole changeset. The apply errors, the row count is 50000 before and
50000 after, `max(id)` does not move, and `integrity_check` returns ok. The 50000
rows that did not previously exist were not partially written.

The bridge can rely on that. A reader applying a changeset either sees the whole
batch or none of it.

Note what this is not. The second apply does not succeed idempotently, it fails.
The bridge needs an explicit conflict handler to decide what a conflict means,
rather than relying on the default abort.

### Foreign keys are enforced here, and the pool was not the reason

An earlier draft of this ADR reported that foreign keys did not enforce, and
blamed `database/sql` handing the insert to a different connection than the one
the pragma was set on. Both halves of that were wrong, and the mistake came from
an inverted boolean in our own probe: it printed `fkErr == nil` next to the words
"foreign key enforced", so a `false` reading meant the constraint had in fact
fired. We read our own mislabelled output as a discovery.

Measured properly, on a replica of the same sequence:

| configuration | orphan rejected |
|---|---|
| unpinned pool | true |
| pinned single connection | true |
| `foreign_keys` in the DSN, unpinned | true |
| `foreign_keys` in the DSN, pinned | true |

The pragma enforces in all four. `database/sql` reuses the single idle connection
across sequential operations, which is why pinned and unpinned agree.

The probe now also demonstrates the per-connection property directly, by setting
the pragma on one pinned connection and writing through a different one:

```
foreign key behaviour:
  unpinned pool                 orphan rejected=true
  pinned single connection      orphan rejected=true
  pragma on conn A, write on B  orphan rejected=false
```

That third line is the one worth keeping. The pragma is per connection, so a
daemon that sets it once at startup and then hands work to a pool has not turned
foreign keys on, it has turned them on for whichever connection happened to run
the pragma. Set them on every connection the daemon opens.

### The corruption bug is real and out of our reach

`gosqlite.org` pins `modernc.org/sqlite` at v1.54.0. modernc v1.56.0, dated
2026-08-03, says it is "picking up `modernc.org/libsqlite3`'s fix for an upstream
data-corruption bug in SQLite 3.53.3's journal rollback". The fix is genuinely
absent from v1.54.0 and genuinely present in mattn's SQLite 3.53.4, which carries
the same `zOut[0]==0` guard. The pin is gosqlite's own, not an artifact of
dependency resolution, and the libc lockstep constraint is real, since v1.54.0
pairs with libc v1.74.1 and v1.56.0 pairs with v1.74.4.

An earlier draft said shipping that version is "exactly how you get a replica
that quietly diverges and nobody notices for a month". That was an
overstatement, and it is worth correcting because the bug is triple gated away
from how this project uses SQLite.

1. It needs rollback journal mode. Super journals only exist there, verified by
   strace: a multi database transaction creates two in `DELETE` mode and zero in
   WAL mode. Our probe runs `PRAGMA journal_mode=WAL`.
2. It needs `ATTACH DATABASE`. The changelog says "a crash during the commit of a
   multi-database (ATTACH) transaction". We are single database. Attaching a
   table to a session is not `ATTACH DATABASE`.
3. It needs a crash during commit, not steady state replication.

So the version discipline is still worth having. This particular bug is not the
reason to avoid the dependency, and the ADR should not pretend otherwise.

## Decision

Vendor `mattn/go-sqlite3` v1.14.52 and write our own minimal cgo binding to
`sqlite3session_*` and `sqlite3changeset_*`. Build with
`CGO_CFLAGS="-DSQLITE_ENABLE_SESSION -DSQLITE_ENABLE_PREUPDATE_HOOK"`.

## Why

The deciding factor is provenance, not capability and not this corruption bug.

`gosqlite.org` works today and has the nicer API. It is
`github.com/go-again/sqlite`, created 2026-05-26, 12 stars, and pkg.go.dev shows
zero importers. Two caveats we checked rather than assumed. pkg.go.dev excludes
same owner submodules, and `gosqlite.org/gorm` and `liteorm.org` are built on it,
so "zero importers" understates usage. And it re-vendors the transpiled engine,
so "cannot be upgraded" is softer than it looks; there was a commit refreshing
the engine and libc dependency on 2026-07-17. Last push was 2026-07-18, so about
two and a half months stale as of this writing.

None of that is disqualifying on its own. What tips it is the combination. A
replication path whose correctness depends on a fork maintained by one person,
with twelve stars, created four months ago, shipping a SQLite release that is
behind on a published corruption fix. That is a lot of single points of failure
lined up in the one component where a silent failure is invisible until someone
compares two databases by hand a month later.

mattn ships SQLite 3.53.4, the same version as our local CLI and the version every
measurement in our research cites. Its session code is genuine upstream SQLite C.
The cost is a binding we own, roughly sixty lines.

CGo is not disqualifying here. This is a daemon we deploy, not a library we ship
into someone else's build, so a C toolchain in the image is a normal cost. It
costs about 60 seconds of cold build and forfeits clean cross compilation, both
of which we accept and record.

## Consequences

We accept these, on purpose.

- Cold builds take roughly 104 seconds against 44 for the pure Go option. CI pays
  this on every cold runner.
- Cross compilation needs a C toolchain per target. A `CGO_ENABLED=0` build
  produces a binary that compiles cleanly and then fails at runtime, so it must
  be treated as a build failure and not a warning. We add a check.
- We own a binding we wrote. It gets reviewed like any other code in the repo and
  it carries tests that capture, apply, re-apply, apply over a partial overlap,
  and invert.
- mattn is effectively maintained by one person. It is an active one, and issue
  825 is worth watching, but the risk is real and named.

## Follow-ups

- Write the binding and its tests. The probes here are the specification.
- Assert at startup that `sqlite_compileoption_used('ENABLE_SESSION')` is 1, and
  fail loudly when it is not.
- Make `CGO_ENABLED=0` builds fail CI explicitly.
- Give the bridge an explicit changeset conflict handler.
- Set pragmas on every connection, never once at startup.
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
type. Reachable only through a fork, which is the gosqlite situation with one
more indirection.

Vendoring pull request 1334 whole was considered and not chosen. It is 1106 lines
across three files from a contributor whose fork is archived, unmerged for
eighteen months. Writing the sixty lines we actually need is smaller, and we own
it.

## Reproducing this spike

Both probes live in their own Go module, so run them from that directory.

```
cd spikes/driver-spike
go run ./cmd/mattn    -rows 100000
go run ./cmd/gosqlite -rows 100000
```

The gosqlite probe asserts nine conditions and exits non-zero if any of them
fail. Pass `-rows 1` to see it fail on purpose, which is the quickest way to
confirm the gate is wired rather than decorative.

Both probes are committed and both are what produced every number above.

