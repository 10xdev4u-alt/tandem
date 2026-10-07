# 0003. Go 1.27 daemon over a Rust daemon

## Context

The daemon owns both engines, the change bridge and the write-ahead log work.
It is the process that opens the SQLite file, holds the session that captures
changes, and applies them into Postgres. Everything else in this repository is
either a document or a test of that process.

Go was chosen over Rust. This ADR records why, and — because a decision record
that only states the winner is a preference list — records what Rust would have
brought, what that cost, and the conditions under which the choice should be
opened again.

Issue #1 asked for three things: that the file state the tradeoff rather than
only the choice, that it name the losing option and why it lost, and that it
cite go.dev for the release date. The first two are the substance. The third
turned out to be worth doing carefully, because verifying the date led to
discovering that the premise attached to it was wrong.

## Measurements

### The release date

Go 1.27.0 shipped **2026-08-19**, from the Go project's own release history:
<https://go.dev/doc/devel/release#go1.27.0>. Go 1.26.0 shipped 2026-02-10,
which makes 1.27 the six-month release following it. Both dates are stated by
the Go project, not derived.

### What Go 1.27 actually added

The Go 1.27 release notes, "Changes to the language":
<https://go.dev/doc/go1.27>, tracking issue <https://go.dev/issue/77273>.

Go 1.27 now supports **generic methods**: a method declaration may declare its
own type parameters. The same section then says, in the next sentence:

> Note that methods of interfaces may not declare type parameters nor can
> interface methods be implemented by generic methods.

### The probe

Because that sentence is load-bearing for how the engine interface is shaped,
it was checked against a real compiler rather than trusted. Go 1.27.0,
`go vet`:

```go
package probe

type Engine interface {
	Query[T any](sql string) ([]T, error)
}
```

```
vet: ./a.go:4:7: interface method must have no type parameters
```

And the same toolchain accepts the two shapes that are legal:

```go
// A generic method on a concrete type.
func (e *SqliteEngine) Query[T any](sql string) ([]T, error)

// A generic function over a non-generic interface method.
func QueryAs[T any](e Engine, sql string) (T, error)
```

Both `go vet` clean.

## What issue #1 said, and what Go 1.27 actually permits

Issue #1's Decision block reads:

> Go 1.27 shipped 2026-08-19 with generic methods, which is what lets one
> engine interface expose `Query[T any]`.

The date is right. The consequence drawn from it is not.

Generic methods and generic *interface* methods are different things, and Go
1.27 shipped only the first. An interface method may not declare type
parameters, before 1.27 and after — so an engine interface cannot expose
`Query[T any]`. The release notes that introduce the feature say so explicitly,
and the compiler refuses the shape with `interface method must have no type
parameters`.

This matters beyond a wording slip, because it is a decision record's job to
say what the system can do. The two shapes that do work are:

1. a generic method on each concrete engine type, which gives `Query[T]` at the
   call sites that already know their engine, and gives no polymorphism; and
2. a non-generic `Query` on the interface plus a generic adapter function over
   it, which keeps polymorphism and puts `T` in the function, not the method.

Issue #13 ("expose engine interface with generic query method") has to pick
between them, and this ADR is why picking is still on the table rather than
already decided by a mistaken premise.

## Decision

Go 1.27 for the daemon, TypeScript for the lab.

## The tradeoff

Go buys: a straightforward cgo path to SQLite that we have already walked
(ADR 0001 owns a session binding against mattn, built and tested), goroutines
that make capture-and-apply two straightforward concurrent pipelines, a single
static binary for a daemon that has to run anywhere, and a short edit-compile
loop.

Go costs: a garbage collector on the write path, a type system that expresses
less of the schema contract than Rust's does, and `interface{}` or `any` at the
boundaries where a Rust return type would have been exact. The cost we are
actually paying is the second and third — not the collector.

Rust buys: no pauses, so the apply path's latency distribution has no GC tail;
ownership that makes the "who owns this connection" question structural rather
than documented; and `sqlx`/`rusqlite` with compile-time checked queries.

Rust costs: the session binding in ADR 0001 is written in cgo against mattn and
is the single hardest thing in this repository. It would be rewritten as unsafe
FFI or replaced by a different capture mechanism. The toolchain doubles —
`.tool-versions` pins a Go toolchain today, and a Rust daemon needs `cargo`
beside it in every environment and in CI. And the iteration loop on a daemon
whose failure modes are timing-dependent is slower when the language is slower
to change.

## Why Rust lost

Not on performance. On **where the risk already sits**.

The dangerous part of tandem is not how fast a row moves; it is whether a
captured change means the same thing on both engines. That risk is in the
change capture and the apply, both of which are already built and tested in Go.
Choosing Rust would move the well-understood cost of the toolchain and the FFI
rewrite into the path, in exchange for latency headroom a lab bridge does not
need — a bridge that reads a change log and applies it is not latency-bound,
and ADR 0002's own spike shows the Postgres side spends its time in process
startup, not in row transfer.

The honest version of the tradeoff: we accepted a slower, less precise type
boundary and a collector on the write path to keep working code and a single
toolchain.

## What would reopen this

Three things, any one of them:

- a measured GC pause on the apply path that is visible in the bridge's own
  metrics — the number belongs to issue #79, not here;
- tandem becoming a latency-sensitive component rather than a lab, where
  Rust's tail latency is a requirement instead of a headroom; or
- the session binding being replaceable by a capture mechanism that does not
  depend on Go's cgo, which is what makes the rewrite cheap.

Until one is true, reopening costs more than it returns.

## Consequences

- The daemon is Go 1.27 and the toolchain is pinned in `.tool-versions`.
- The engine interface is constrained by the interface-method rule above.
  Issue #13 must choose shape 1 or shape 2; `Query[T any]` on the interface is
  not available and cannot be made available by upgrading.
- cgo stays on the build path, which is why `CGO_CFLAGS` is exported by the
  Makefile and why the session flags are a whole-program concern (ADR 0001).
- Rust is recorded as evaluated and rejected on these grounds, so a future
  proposal to adopt it has to address them rather than restate them.

## What was not measured

No Go-versus-Rust benchmark was run for this ADR, and a Rust toolchain is
installed on the machine that wrote it, so that was a choice rather than an
obstacle.

It was a choice because a microbenchmark would not have moved this decision.
The reasons Rust lost are that the binding is written, the toolchain is
pinned and the risk is correctness — none of which a benchmark reports.
Running one anyway and recording the winner would have manufactured evidence
for a conclusion already reached on other grounds, which is the same error as
claiming a port that was never tested (see ADR 0002). The comparison numbers
that are actually wanted belong to issue #79, where they will be measured
against the drivers we use rather than against a synthetic loop.

## Reproducing the probe

```sh
mkdir probe && cd probe && go mod init probe
cat > a.go <<'EOF'
package probe

type Engine interface {
	Query[T any](sql string) ([]T, error)
}
EOF
go vet ./...
```

Expected: `interface method must have no type parameters`, on Go 1.27.0.
Delete the interface method and add a generic method on a struct instead, and
the same command exits clean.
