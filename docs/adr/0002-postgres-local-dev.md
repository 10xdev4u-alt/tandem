# 0002. Run Postgres from embedded binaries, pinned to 18.6.0

- Status: accepted
- Date: 2026-10-07
- Issue: #21
- Supersedes: nothing
- Amended: nothing

## Context

Half of this lab is Postgres and the box has no Postgres server. The spike was
to find out what a developer and CI can actually start, and what they cannot.

`psql (PostgreSQL) 18.6` is on the path. That is a client, and it is the reason
`.db-versions` pins `psql 18.6`. It answers nothing:

```
$ pg_isready
/run/postgresql:5432 - no response
$ ss -ltn | grep 5432
$ which initdb pg_ctl postgres
$ dpkg -l | grep postgresql
$ apt-cache policy postgresql
```

All empty. No server, no listener, no package, and no way to install one
non-interactively:

```
$ sudo -n true
sudo: a password is required
```

So `apt-get install postgresql` is not something the build can run. That rules
out the system package path for both CI and a fresh clone.

Two other paths exist on this machine. Docker is running
(`29.7.2`), and the network reaches both `proxy.golang.org` (200) and
`repo1.maven.org` (200), where zonky publishes
`embedded-postgres-binaries-linux-amd64` with a `18.6.0` in its metadata.

## Measurements

`github.com/fergusstrange/embedded-postgres` v1.34.0, verified by booting a real
server and querying it with the pinned client rather than trusting the library's
own handshake. A library that reported success without a server would have
passed unnoticed otherwise.

First run, using the library's `V18` constant:

```
PSQL OK: PostgreSQL 18.3 on x86_64-pc-linux-gnu, compiled by gcc
(GCC) 7.5.0, 64-bit|tandem
SPIKE PASS
```

It started, it answered, and it was the wrong version. The constant is not
"the 18 line":

```go
// config.go:156
V18 = PostgresVersion("18.3.0")
```

`PostgresVersion` is a plain string type, so the constant can be bypassed
entirely:

```go
Version(embeddedpostgres.PostgresVersion("18.6.0"))
```

Second run:

```
PSQL OK: PostgreSQL 18.6 on x86_64-pc-linux-gnu, compiled by gcc
(Ubuntu 7.5.0-3ubuntu1~18.04) 7.5.0, 64-bit|tandem
SPIKE PASS
```

Server 18.6, client 18.6, exit 0. Both sides now match the pin in
`.db-versions`.

## Decision

Use `fergusstrange/embedded-postgres` to start the server, and pass the exact
version string. Never pass `V18`.

The version is pinned literally in code rather than through the constant
because the constant silently drifts: it names one patch release and will keep
naming it while the zonky repository gains newer ones. A version mismatch here
would not fail loudly, it would just mean the lab compared a 18.6 client against
a 18.3 server and nobody wrote that down.

## Alternatives rejected

**System package via apt.** Requires a password for sudo. Not automatable, and a
build that cannot run its own preconditions is not a build.

**Docker.** Present and working, and remains a reasonable fallback. Rejected as
the primary path because it puts a daemon, an image pull and container
lifecycle in front of every test run. The Go test suite currently needs nothing
but a compiler and network access for one download, which is a smaller thing to
be wrong about.

**System Postgres already running.** Not running here, and depending on one
would make the test suite pass or fail according to the machine it landed on
rather than according to the code.

## Consequences

- Postgres tests own their lifecycle: start, use, stop. No shared server and no
  state left behind for the next run.
- **The port is not free of conflict, and an earlier version of this document
  said otherwise.** `embedded-postgres` v1.34.0 defaults to `5432` and
  `Start()` returns an error when that port is occupied. This box has no server
  listening, which is why the spike passed; a developer who does have Postgres
  running locally would fail to start the harness through no fault of their
  own. The ADR picks a version and nothing else, so selecting a port is left to
  #22, and "our own lifecycle means no conflict with preinstalled services" is
  exactly the kind of claim that looks true on the machine that produced it.
- First run downloads the server binary. It is a one-time cost and it needs
  network; a machine with no network cannot run the Postgres tests.
- The client pin and the server pin are now two places holding one fact. If
  `.db-versions` moves to a new 18.x, the literal version in the harness has to
  move with it. That check does not exist yet, because there is no harness to
  attach it to: it belongs with #22 when the lifecycle code lands, not to a
  document that claims coverage it has not been given.

## Traps worth remembering

`V18` reads like a major-line selector and is not one. Anyone following it
will get 18.3.0 and see a perfectly healthy server, which is the least helpful
form of being wrong.
