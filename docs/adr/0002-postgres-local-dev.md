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

All empty. No server, no listener, no package, and on **this machine** no way to
install one non-interactively:

```
$ sudo -n true
sudo: a password is required
```

That claim is about this box and nothing wider. It does not establish anything
about CI: GitHub-hosted Linux runners are documented as providing passwordless
sudo, and this spike never ran there, so "the build cannot apt-install
Postgres" would have been a guess dressed as a measurement. The system package
path is rejected below for reasons that hold regardless of who has sudo.

Two other paths exist on this machine. Docker is running
(`29.7.2`), and the network reaches both `proxy.golang.org` (200) and
`repo1.maven.org` (200), where zonky publishes
`embedded-postgres-binaries-linux-amd64` with a `18.6.0` in its metadata.

## Measurements

`github.com/fergusstrange/embedded-postgres` v1.34.0, verified by booting a real
server and querying it with the pinned client rather than trusting the library's
own handshake. A library that reported success without a server would have
passed unnoticed otherwise.

The whole spike was one program in a scratch directory, which is exactly why it
belongs in this document: nothing in the repository runs it, so without the
source here the numbers below are not reproducible by anyone else. It is
reproduced in full.

```go
package main

import (
	"fmt"
	"os"
	"os/exec"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

func main() {
	os.Exit(run())
}

func run() int {
	const port = uint32(54329)

	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Username("tandem").
		Password("tandem").
		Database("tandem").
		Port(port).
		Version(embeddedpostgres.PostgresVersion("18.6.0")))

	if err := pg.Start(); err != nil {
		fmt.Println("START FAILED:", err)
		return 1
	}
	// Registered only once the server is up, and returned to rather than exited
	// from: os.Exit does not run deferred calls, so an os.Exit on the psql path
	// below would skip this and leave the child process unreleased.
	defer pg.Stop()

	// Verified through the pinned psql client rather than the library's own
	// handshake. A library that reported success without a server would pass
	// unnoticed otherwise, which is the whole reason for this line.
	out, err := exec.Command("psql",
		"host=127.0.0.1 port=54329 user=tandem password=tandem dbname=tandem",
		"-tAc", "select version(), current_user;").CombinedOutput()
	if err != nil {
		fmt.Println("PSQL FAILED:", err, string(out))
		return 1
	}
	fmt.Println("PSQL OK:", string(out))
	fmt.Println("SPIKE PASS")
	return 0
}
```

Run with `go run .` in a module containing only this file and
`github.com/fergusstrange/embedded-postgres`.

The `main`/`run` split is not decoration. The scratch program I actually ran
called `os.Exit(1)` on the psql failure path, which skips every deferred call
and would have left the child process unreleased. It did not affect the numbers
below, because psql succeeded and the deferred `Stop()` on the success path
still ran — but a spike whose whole argument is "verify rather than assume"
should not contain a resource leak on the path where verification fails. The first run substituted
`embeddedpostgres.V18` for the version literal; that is the only difference
between the two transcripts below.

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

**System package via apt.** Rejected for reasons that do not depend on whether
sudo works. A distro package ships whichever version the archive carries, so
the lab would compare against a server version nobody pinned — and this
repository pins `psql 18.6` in `.db-versions` precisely so the two sides agree.
It also installs system-wide state and needs root, which puts an environment
change in front of every test run.

On this machine it additionally cannot run at all, since `sudo -n` asks for a
password. That is a fact about this machine only. Whether CI can apt-install is
untested here, and stating it either way without evidence would have been the
same mistake as the port claim above.

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
