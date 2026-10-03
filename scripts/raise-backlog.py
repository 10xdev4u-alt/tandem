#!/usr/bin/env python3
"""Raise the Tandem backlog on GitHub.

Idempotent: an issue whose title already exists is skipped, so this can be
re-run after adding entries. Data lives in BACKLOG below, one tuple per issue.

    python3 scripts/raise-backlog.py --dry-run
    python3 scripts/raise-backlog.py
"""

import argparse
import json
import subprocess
import sys

REPO = "10xdev4u-alt/tandem"

# (title, [labels], body)
BACKLOG = [

    # ---------------------------------------------------------------- infra
    ("docs: record architecture decision, Go daemon over Rust", ["type:docs", "area:docs", "P0"],
     "## Context\n\nThe daemon owns both engines, the change bridge and the write-ahead log work.\n"
     "Go was chosen over Rust.\n\n"
     "## Decision\n\n"
     "Go 1.27 daemon, TypeScript lab. Go 1.27 shipped 2026-08-19 with generic methods,\n"
     "which is what lets one engine interface expose `Query[T any]`.\n\n"
     "## Acceptance\n\n"
     "- ADR file exists at `docs/adr/` and states the tradeoff, not just the choice\n"
     "- Names the losing option and why it lost\n"
     "- Cites go.dev for the release date\n"),

    ("infra: pin toolchain versions for Go and TypeScript", ["type:chore", "area:infra", "P0"],
     "## Context\n\nLocal box already has go1.27.0, node 26.7.0, bun 1.3.13, sqlite3 3.53.4, psql 18.6.\n\n"
     "## Acceptance\n\n"
     "- `go.mod` declares go 1.27\n"
     "- `.tool-versions` or equivalent pins node and bun\n"
     "- CI uses the same versions as local, verified by one command\n"),

    ("infra: commit hook enforces six word subjects", ["type:chore", "area:infra", "P0"],
     "## Context\n\nRule 2 of the operating agreement. Six words after type and optional scope.\n\n"
     "## Acceptance\n\n"
     "- Hook rejects a five word subject\n"
     "- Hook rejects a seven word subject\n"
     "- Hook accepts the three worked examples from WORKFLOW.md\n"
     "- Hook prints the count so the failure is obvious\n"),

    ("infra: commit hook enforces single co-author trailer", ["type:chore", "area:infra", "P0"],
     "## Context\n\nRule 3. the-ai-developer is the only permitted co-author.\n\n"
     "## Acceptance\n\n"
     "- Commit with no trailer is rejected\n"
     "- Commit with two co-authors is rejected\n"
     "- Commit with a co-author other than the-ai-developer is rejected\n"
     "- Correct commit passes\n"),

    ("infra: configure branch protection on main", ["type:chore", "area:infra", "P0"],
     "## Context\n\nMakes rules 5 and 6 enforceable rather than aspirational.\n\n"
     "## Acceptance\n\n"
     "- Require a review before merge is on\n"
     "- Require status checks to pass is on\n"
     "- Allow squash merge is off\n"
     "- Allow rebase merge is off\n"
     "- Allow merge commit is on\n"
     "- Force push is off\n"),

    ("infra: add pull request template with validation block", ["type:chore", "area:infra", "P1"],
     "## Context\n\nRule 8. A PR states its issue, the validation it ran, and what it left undone.\n\n"
     "## Acceptance\n\n"
     "- Template has Issue, Validation run, Not done, and Risk sections\n"
     "- Validation section asks for pasted command output, not a claim\n"),

    # ---------------------------------------------------------- daemon core
    ("engine: daemon boots with empty configuration", ["type:feature", "area:engine", "P0"],
     "## Context\n\nFirst slice. Nothing works until the process starts, reads config, and shuts down cleanly.\n\n"
     "## Acceptance\n\n"
     "- `tandemd --config path` starts and prints a version line\n"
     "- Missing required config key exits non-zero with the key named\n"
     "- SIGTERM shuts down within two seconds\n"
     "- One test asserts each of the three above\n"),

    ("engine: structured logging with engine and request fields", ["type:chore", "area:engine", "P1"],
     "## Context\n\nThe lab shows why two engines behave differently. Logs are half of that story.\n\n"
     "## Acceptance\n\n"
     "- Log lines carry engine name, request id and duration\n"
     "- Format is machine readable, one object per line\n"
     "- No engine name field means the line is dropped in tests\n"),

    ("engine: configuration schema with validation", ["type:feature", "area:engine", "P1"],
     "## Context\n\nConfig drives which engine, which file, which port. It needs a schema, not ad hoc parsing.\n\n"
     "## Acceptance\n\n"
     "- Unknown keys are an error, not ignored\n"
     "- Paths are resolved relative to the config file, not the working directory\n"
     "- A documented example config ships in the repo\n"),

    ("engine: graceful shutdown drains in-flight bridge work", ["type:feature", "area:engine", "P1"],
     "## Context\n\nKilling the daemon mid-write is exactly what the lab will do on purpose. Shutdown must not corrupt.\n\n"
     "## Acceptance\n\n"
     "- In-flight transactions commit or roll back, never half-applied\n"
     "- SQLite checkpoint completes before exit\n"
     "- A test kills the process during a write and reopens the file clean\n"),

    ("engine: health endpoint reports both engine states", ["type:feature", "area:engine", "P1"],
     "## Context\n\nThe web layer needs to know if each engine is reachable before it renders anything.\n\n"
     "## Acceptance\n\n"
     "- Endpoint reports per-engine up or down with a reason\n"
     "- Responds even when both engines are down\n"
     "- Never returns a 500 to signal engine failure\n"),

    ("engine: single binary build for linux and macos", ["type:chore", "area:engine", "P2"],
     "## Context\n\nA daemon people can run without a package manager is the difference between a toy and a tool.\n\n"
     "## Acceptance\n\n"
     "- One command produces both binaries\n"
     "- Each binary runs the full test suite on its target platform in CI\n"),

    ("engine: expose engine interface with generic query method", ["type:feature", "area:engine", "P0"],
     "## Context\n\nThe reason we chose Go 1.27. Two engines, one interface, typed results.\n\n"
     "## Acceptance\n\n"
     "- Interface has a generic method that returns a typed result\n"
     "- Both a SQLite and a Postgres implementation satisfy it\n"
     "- A compile-time assertion proves both implement it\n"
     "- Adding a third implementation needs no change to the interface\n"),

    # ------------------------------------------------------- SQLite driver
    ("spike: decide the Go change capture driver", ["type:spike", "area:engine", "P0"],
     "## Context\n\nRiskiest decision in the project. No Go driver makes changeset capture easy.\n\n"
     "Findings so far:\n"
     "- mattn/go-sqlite3 v1.14.52 is alive but has no session support. Request open\n"
     "  since 2020 as issue 825. Pull request 1334 unmerged for 18 months.\n"
     "- modernc.org/sqlite v1.60.1 compiles session in but cannot expose it.\n"
     "- ncruces/go-sqlite3 v0.35.6 leaves session out of its build config on purpose.\n"
     "- go-again/sqlite v0.14.0 works and has the cleanest API, but ships SQLite\n"
     "  3.53.3 without the journal rollback corruption fix backported in modernc\n"
     "  v1.56.0, has 12 stars and 0 importers.\n"
     "- crawshaw.io/sqlite is dead.\n\n"
     "## Acceptance\n\n"
     "- Two candidate prototypes built, each capturing a changeset\n"
     "- Each measured on build time and on a 100k row change capture\n"
     "- Recommendation written as an ADR with the losing option named\n"
     "- Recommendation reviewed before any production code depends on it\n"),

    ("engine: verify session extension is compiled into the build", ["type:chore", "area:engine", "P0"],
     "## Context\n\nSQLite ships the session extension disabled by default. Silent absence would be invisible until the bridge fails.\n\n"
     "## Acceptance\n\n"
     "- Startup runs the compile option query and logs the answer\n"
     "- Startup fails loudly when session is unavailable\n"
     "- A test asserts the failure path\n"),

    ("engine: open SQLite file in WAL mode with foreign keys on", ["type:feature", "area:engine", "P0"],
     "## Context\n\n`foreign_keys` is off by default in SQLite and always on in Postgres. This asymmetry will bite the bridge.\n\n"
     "## Acceptance\n\n"
     "- Daemon opens every file in WAL mode\n"
     "- Foreign keys enforced and asserted at startup\n"
     "- Page size and busy timeout are configuration, with documented defaults\n"
     "- A test inserts an orphan row and expects it to fail\n"),

    ("engine: capture a changeset for one table", ["type:feature", "area:bridge", "P0"],
     "## Context\n\nFirst real use of whatever the driver spike chose.\n\n"
     "## Acceptance\n\n"
     "- Insert, update and delete each produce a changeset\n"
     "- Changeset applied to a second identical database reproduces the state\n"
     "- Table without a primary key is reported as untrackable, not silently skipped\n"),

    ("engine: invert and concatenate changesets", ["type:feature", "area:bridge", "P1"],
     "## Context\n\nUndo and merge are what make a change log trustworthy rather than decorative.\n\n"
     "## Acceptance\n\n"
     "- Inverted changeset undoes the original exactly\n"
     "- Concatenated changesets apply in order\n"
     "- Both proven by test, not by inspection\n"),

    ("engine: changeset conflict handler with explicit policy", ["type:feature", "area:bridge", "P1"],
     "## Context\n\nApplying a changeset can conflict. The policy has to be a decision, not whatever the default happens to be.\n\n"
     "## Acceptance\n\n"
     "- Every conflict class has a named policy\n"
     "- Unknown conflict class aborts rather than guessing\n"
     "- Each policy has a test that proves it\n"),

    ("engine: instrument byte and page counts per transaction", ["type:feature", "area:engine", "P2"],
     "## Context\n\nThe lab shows cost. Page level accounting is how we show it honestly.\n\n"
     "## Acceptance\n\n"
     "- Per transaction page count and byte count recorded\n"
     "- Numbers come from the engine, not estimated\n"),

    # ------------------------------------------------------- Postgres side
    ("spike: verify Postgres server availability for local dev", ["type:spike", "area:engine", "P0"],
     "## Context\n\nMachine has psql 18.6 client. A client is not a server.\n\n"
     "## Acceptance\n\n"
     "- Confirmed whether a server exists locally\n"
     "- If not, chosen dev route is written down: embedded binaries, container, or both\n"
     "- Route verified with a real query, not assumed from the client version\n"),

    ("engine: embedded Postgres lifecycle with start parameters", ["type:feature", "area:engine", "P0"],
     "## Context\n\nLogical decoding needs specific settings at boot, not at runtime.\n\n"
     "## Acceptance\n\n"
     "- Starts and stops cleanly, twice in a row without leftover state\n"
     "- Start parameters can set the write-ahead log level and slot and sender limits\n"
     "- Data directory is isolated per run and cleaned up\n"),

    ("engine: connect over pgx with typed round trip", ["type:feature", "area:engine", "P0"],
     "## Context\n\nFirst Postgres query, and it should already be typed.\n\n"
     "## Acceptance\n\n"
     "- Connection pool configured and observable\n"
     "- One typed query round trips\n"
     "- Connection failure produces a typed error, not a string\n"),

    ("engine: run one query against both engines and diff", ["type:feature", "area:lab", "P0"],
     "## Acceptance\n\n"
     "- Same SQL runs on both engines in one call\n"
     "- Results compared field by field, not by string equality\n"
     "- Divergence is reported per field with both values\n"
     "- A deliberately incompatible query shows a clean, explained difference\n"),

    ("engine: detect and report schema drift between engines", ["type:feature", "area:lab", "P1"],
     "## Context\n\nTwo engines holding the same logical schema will drift. Detecting it is the product.\n\n"
     "## Acceptance\n\n"
     "- Compares tables, columns, types and constraints\n"
     "- Reports each difference with the engine it came from\n"
     "- Exits non-zero in CI when drift is detected\n"),

    ("engine: time travel read against a historical snapshot", ["type:feature", "area:engine", "P2"],
     "## Context\n\nPostgres branching and instant restore are commercial features. Prove what we can do locally instead of pretending.\n\n"
     "## Acceptance\n\n"
     "- States plainly which capability is local and which is hosted\n"
     "- Local snapshot restore proven with a test\n"
     "- No claim in docs that we cannot demonstrate\n"),

    # -------------------------------------------------------- bridge: trig
    ("bridge: change log table with before and after images", ["type:feature", "area:bridge", "P0"],
     "## Context\n\nThe trigger path. Roughly twenty lines, works in every SQLite build ever shipped.\n\n"
     "## Acceptance\n\n"
     "- Triggers on insert, update and delete write to a log table\n"
     "- Old and new values captured for updates and deletes\n"
     "- Log survives a rollback with nothing written\n"),

    ("bridge: assign monotonic log sequence per transaction", ["type:feature", "area:bridge", "P0"],
     "## Acceptance\n\n"
     "- Every change in one transaction shares a transaction id\n"
     "- Sequence is monotonic with no gaps after a rollback\n"
     "- Test asserts the gap behaviour explicitly\n"),

    ("bridge: apply log entries into Postgres with conflict policy", ["type:feature", "area:bridge", "P0"],
     "## Acceptance\n\n"
     "- Entries apply in sequence order\n"
     "- Duplicate application is idempotent\n"
     "- Conflicting update has a named, tested policy\n"),

    ("bridge: reverse direction, Postgres into SQLite", ["type:feature", "area:bridge", "P1"],
     "## Acceptance\n\n"
     "- One round trip each way proven\n"
     "- A write loop does not diverge over repeated runs\n"
     "- Divergence check compares row counts and a content hash\n"),

    ("bridge: expose the stream over WebSocket", ["type:feature", "area:bridge", "P1"],
     "## Context\n\nThe web layer needs a live view. Effect's socket module is unstable, so this sits behind our own interface.\n\n"
     "## Acceptance\n\n"
     "- Client receives log entries as they are committed\n"
     "- Our interface hides the Effect socket type from the rest of the codebase\n"
     "- Slow client does not block the bridge\n"
     "- Dropped client reconnects and resumes from a cursor\n"),

    ("bridge: reject orphaned rows before they reach Postgres", ["type:feature", "area:bridge", "P1"],
     "## Context\n\nSQLite will happily log an orphan that Postgres then refuses. Better to catch it at the source.\n\n"
     "## Acceptance\n\n"
     "- Bridge reports the offending row rather than failing opaquely\n"
     "- Cause is distinguishable from a genuine constraint violation\n"
     "- Test proves the error message names the constraint\n"),

    ("bridge: backfill existing rows before streaming begins", ["type:feature", "area:bridge", "P1"],
     "## Acceptance\n\n"
     "- Fresh replica syncs existing state then streams\n"
     "- No window where a row is neither present nor pending\n"
     "- Interrupted backfill resumes without duplicating rows\n"),

    ("bridge: metrics for entries applied, skipped and failed", ["type:feature", "area:bridge", "P2"],
     "## Acceptance\n\n"
     "- Three counters exported\n"
     "- Skipped count is non-zero in at least one test, proving it is wired\n"),

    # ----------------------------------------------------- bridge: changeset
    ("bridge: implement changeset path as a second CDC strategy", ["type:feature", "area:bridge", "P1"],
     "## Context\n\nWe ship two CDC strategies on purpose so the tradeoff is measurable rather than argued.\n\n"
     "## Acceptance\n\n"
     "- Both strategies sit behind one interface\n"
     "- A switch selects between them at construction\n"
     "- Neither leaks its own types past the interface\n"),

    ("spike: document why session extension loses to triggers for a sidecar", ["type:research", "area:bridge", "P1"],
     "## Context\n\nA published argument exists against the session extension for sidecar use. We should hold our own view.\n\n"
     "## Acceptance\n\n"
     "- Our own write-up, in our words, citing the published one\n"
     "- States the three known drawbacks and whether we hit any\n"
     "- Conclusion is a decision, not a survey\n"),

    ("bridge: capture schema changes, not only row changes", ["type:feature", "area:bridge", "P2"],
     "## Context\n\nPostgres logical decoding cannot capture DDL. Turso's capture can. Worth measuring whether we can.\n\n"
     "## Acceptance\n\n"
     "- Create and drop of a table is observed by the bridge\n"
     "- Schema change and row change are distinguishable in the stream\n"
     "- Docs state plainly that Postgres will not replay the schema change\n"),

    ("bridge: cap changeset memory and document the ceiling", ["type:chore", "area:bridge", "P2"],
     "## Acceptance\n\n"
     "- Measured ceiling for a large transaction\n"
     "- Number published in docs\n"
     "- Behaviour at the ceiling is defined and tested\n"),

    ("bridge: resume a stream from an arbitrary cursor", ["type:feature", "area:bridge", "P1"],
     "## Acceptance\n\n"
     "- Given a cursor, the stream resumes without replaying or skipping\n"
     "- Cursor from the future is rejected rather than silently empty\n"
     "- Property style test over random cursor positions\n"),

    ("bridge: verify a changeset survives a byte level round trip", ["type:feature", "area:bridge", "P2"],
     "## Acceptance\n\n"
     "- Changeset written to disk and read back applies identically\n"
     "- Truncated changeset is detected, not applied\n"),

    ("bridge: document the license position on every candidate library", ["type:docs", "area:bridge", "P0"],
     "## Context\n\nsqlite-sync is Elastic License 2.0 and not open source. We nearly assumed otherwise.\n\n"
     "## Acceptance\n\n"
     "- Every dependency has its license recorded with the URL\n"
     "- Anything non-permissive is either excluded or flagged with the reason\n"
     "- Checked by a command that fails on an unknown license\n"),

    # ------------------------------------------------------- concurrency lab
    ("lab: reproduce SQLITE_BUSY with twenty concurrent writers", ["type:spike", "area:lab", "P0"],
     "## Context\n\nThe first thirty minutes of the original four hour exercise. It teaches the whole project.\n\n"
     "## Acceptance\n\n"
     "- Twenty writers on one file, outcome captured\n"
     "- Repeated with WAL enabled and a busy timeout, outcome captured\n"
     "- Both outcomes written up with the error shape, not a summary\n"
     "- Runs as a test that can be read as documentation\n"),

    ("lab: expose the single writer limit in the interface", ["type:feature", "area:lab", "P1"],
     "## Context\n\nWe are not hiding the gap. We are measuring it.\n\n"
     "## Acceptance\n\n"
     "- Interface documents that SQLite accepts one writer at a time\n"
     "- Postgres accepts many, and the difference is visible in the UI\n"
     "- No wrapper pretends otherwise\n"),

    ("lab: pragma and GUC mapping table as data", ["type:feature", "area:lab", "P1"],
     "## Context\n\njournal_mode, busy_timeout, foreign_keys, mmap_size, cache_size on one side. synchronous_commit, lock_timeout, shared_buffers on the other.\n\n"
     "## Acceptance\n\n"
     "- Mapping lives in data, not in code branches\n"
     "- Every entry names the nearest analogue and admits when there is none\n"
     "- UI renders straight from the table\n"),

    ("lab: toggle pragmas live and show the effect", ["type:feature", "area:lab", "P1"],
     "## Acceptance\n\n"
     "- Each pragma can be changed at runtime where SQLite allows it\n"
     "- A change that requires a reopen says so instead of failing silently\n"
     "- Before and after states shown together\n"),

    ("lab: measure latency across both engines on one query", ["type:feature", "area:lab", "P1"],
     "## Acceptance\n\n"
     "- Same query, both engines, repeated runs\n"
     "- Report median and spread, not a single number\n"
     "- Warm-up excluded and the exclusion is stated\n"
     "- Corpus is committed so the numbers can be reproduced\n"),

    ("lab: explain every error the concurrency run produced", ["type:feature", "area:lab", "P2"],
     "## Acceptance\n\n"
     "- No unclassified error code reaches the UI\n"
     "- Each code has a plain sentence explaining the cause\n"
     "- Unknown codes render as unknown, not as a guess\n"),

    ("lab: make concurrency runs deterministic", ["type:chore", "area:lab", "P2"],
     "## Acceptance\n\n"
     "- Seeded runs produce the same outcome twice\n"
     "- Unseeded runs allowed but marked as such in output\n"),

    # ------------------------------------------------------------- plan diff
    ("lab: parse SQLite EXPLAIN QUERY PLAN output", ["type:feature", "area:lab", "P0"],
     "## Acceptance\n\n"
     "- Every plan node type parsed, including virtual tables\n"
     "- Unknown node types render as unknown rather than dropping silently\n"
     "- Parser tested against a corpus of real plans\n"),

    ("lab: parse Postgres EXPLAIN output", ["type:feature", "area:lab", "P0"],
     "## Acceptance\n\n"
     "- Text format parsed into a tree\n"
     "- Cost estimates captured where present\n"
     "- A plan with no estimates is valid, not an error\n"),

    ("lab: render both plans side by side", ["type:feature", "area:web", "P1"],
     "## Context\n\nThis is the money view. Someone should be able to look at it and understand both engines.\n\n"
     "## Acceptance\n\n"
     "- Both trees visible at once\n"
     "- Nodes that exist in only one engine are marked, not hidden\n"
     "- Readable at 1280 wide and at 375 wide\n"),

    ("lab: map plan node types across engines where a mapping exists", ["type:research", "area:lab", "P2"],
     "## Acceptance\n\n"
     "- Table of equivalences, with honest gaps\n"
     "- A full scan in one engine has no direct counterpart in the other, and that is stated\n"),

    ("lab: record plan for every query the lab runs", ["type:chore", "area:lab", "P2"],
     "## Acceptance\n\n\n"
     "- Plans stored keyed by normalized query\n"
     "- Re-running a query reuses the stored plan rather than re-planning\n"),

    ("lab: diff two runs of the same query", ["type:feature", "area:lab", "P2"],
     "## Acceptance\n\n"
     "- Structural diff, not a text diff\n"
     "- Added and removed nodes both listed\n"),

    # ------------------------------------------------------------- web layer
    ("web: scaffold TypeScript lab with strict settings", ["type:chore", "area:web", "P0"],
     "## Acceptance\n\n"
     "- Vite, TypeScript strict, Effect 4.0.0 pinned exactly\n"
     "- Lockfile committed\n"
     "- Build and typecheck scripts exit clean on a fresh clone\n"),

    ("web: generate the daemon API contract from one source", ["type:feature", "area:web", "P0"],
     "## Context\n\nHand-maintained types on two sides drift. That is the bug we are here to avoid.\n\n"
     "## Acceptance\n\n"
     "- Contract defined once\n"
     "- TypeScript types generated from it\n"
     "- CI fails when the generated output is stale\n"),

    ("web: wrap Effect socket behind our own interface", ["type:feature", "area:web", "P0"],
     "## Context\n\nEffect marks the socket module unstable. One adapter file means a version bump is not a rewrite.\n\n"
     "## Acceptance\n\n"
     "- Exactly one file imports the Effect socket types\n"
     "- Everything else depends on our interface\n"
     "- A test asserts no other file imports Effect socket\n"),

    ("web: split query console, one pane per engine", ["type:feature", "area:web", "P1"],
     "## Acceptance\n\n"
     "- One editor, two result panes\n"
     "- Either pane can run alone\n"
     "- Errors render in the pane that caused them\n"),

    ("web: live change stream view", ["type:feature", "area:web", "P1"],
     "## Acceptance\n\n"
     "- Entries append as they arrive\n"
     "- Reconnect resumes from the last cursor without duplicates\n"
     "- Long streams virtualize so the page stays responsive\n"),

    ("web: pragma control panel driven by the mapping table", ["type:feature", "area:web", "P2"],
     "## Acceptance\n\n"
     "- Controls generated from data\n"
     "- A control that needs a reopen is marked before it is clicked\n"),

    ("web: error states for every failure the daemon can report", ["type:feature", "area:web", "P1"],
     "## Acceptance\n\n"
     "- No raw error string ever reaches the user\n"
     "- Engine down, bridge rejected, query failed each have a distinct state\n"
     "- Each state tested\n"),

    ("web: keyboard navigation through the whole lab", ["type:feature", "area:web", "P1"],
     "## Acceptance\n\n"
     "- Every control reachable and operable by keyboard\n"
     "- Focus order matches visual order\n"
     "- Focus is always visible\n"),

    ("web: meet WCAG 2.2 AA across lab and landing page", ["type:chore", "area:web", "P0"],
     "## Acceptance\n\n"
     "- Automated contrast audit reports zero failures\n"
     "- Checked in CI, not once by hand\n"
     "- Audit script is committed so the number is reproducible\n"),

    ("web: reduce motion preference respected everywhere", ["type:feature", "area:web", "P2"],
     "## Acceptance\n\n"
     "- No animation plays when reduce motion is set\n"
     "- No layout depends on an animation completing\n"),

    # --------------------------------------------------------------- design
    ("design: define colour, type and spacing tokens", ["type:feature", "area:design", "P0"],
     "## Context\n\nOne system shared by the lab and the landing page, so marketing is not a separate codebase.\n\n"
     "## Acceptance\n\n"
     "- Tokens declared once and consumed by both\n"
     "- No raw colour value appears in a component\n"
     "- Contrast of every token pair documented and passing\n"),

    ("design: build the landing page", ["type:feature", "area:design", "P1"],
     "## Acceptance\n\n"
     "- Says what the tool is in one sentence, above the fold\n"
     "- Shows a real screenshot or plan diff, not a mockup\n"
     "- Ships once the lab has something worth landing people on\n"
     "- Passes the same contrast and keyboard gates as the lab\n"),

    ("design: diagram the two-engine architecture", ["type:feature", "area:design", "P2"],
     "## Acceptance\n\n"
     "- Generated from the same data as the code, not drawn by hand\n"
     "- Stays correct when the architecture changes\n"),

    ("design: motion that explains rather than decorates", ["type:feature", "area:design", "P2"],
     "## Acceptance\n\n"
     "- Every animation shows a state change or a movement of data\n"
     "- Nothing animates purely for effect\n"
     "- All of it disabled under reduce motion\n"),

    ("design: code block styling for both SQL dialects", ["type:feature", "area:design", "P2"],
     "## Acceptance\n\n"
     "- Readable at 375 wide without horizontal scroll\n"
     "- Copy button works with a keyboard\n"),

    ("design: empty and error states across lab and landing", ["type:feature", "area:design", "P2"],
     "## Acceptance\n\n"
     "- Every surface has a designed empty state\n"
     "- Every surface has a designed error state\n"
     "- Neither shows a raw exception\n"),

    ("design: print stylesheet for the plan diff view", ["type:chore", "area:design", "P3"],
     "## Context\n\nPeople will print this. Someone comparing two query plans wants it on paper.\n\n"
     "## Acceptance\n\n"
     "- Print view is readable with no dark sections and no clipped tables\n"),

    # ---------------------------------------------------------- CI and ship
    ("infra: CI runs Go tests with coverage gate", ["type:chore", "area:infra", "P0"],
     "## Acceptance\n\n"
     "- Coverage reported and fails below 80 percent\n"
     "- The threshold is a config value, not a comment\n"),

    ("infra: CI runs web tests, lint, types and build", ["type:chore", "area:infra", "P0"],
     "## Acceptance\n\n"
     "- All four run on every push\n"
     "- Fresh clone reproduces a green run with one documented command\n"),

    ("infra: CI runs the bridge round trip test", ["type:chore", "area:infra", "P0"],
     "## Context\n\nThis is the acceptance criterion for the whole project. It must not be skippable.\n\n"
     "## Acceptance\n\n"
     "- One row moves each direction in under a second\n"
     "- Test cannot be skipped by a build flag\n"
     "- Flake rate over twenty runs is zero\n"),

    ("infra: dependency audit and secret scan in CI", ["type:chore", "area:infra", "P0"],
     "## Acceptance\n\n"
     "- No high or critical findings allowed\n"
     "- Secret scan finds nothing on a clean tree\n"
     "- Both fail the build rather than warn\n"),

    ("infra: run the test suite across the release matrix", ["type:chore", "area:infra", "P2"],
     "## Acceptance\n\n\n"
     "- Linux and macOS, arm64 and amd64\n"
     "- Each combination reports separately, one green does not mask a red\n"),

    ("infra: publish a release with checksums", ["type:chore", "area:infra", "P2"],
     "## Acceptance\n\n"
     "- Binaries and checksums attached to a tagged release\n"
     "- Release notes generated from commits, edited by hand afterwards\n"),

    # ---------------------------------------------------------------- docs
    ("docs: explain the single writer limit with a runnable example", ["type:docs", "area:docs", "P0"],
     "## Acceptance\n\n"
     "- Reader can paste one command and see the failure\n"
     "- Then paste a second command and see WAL change it\n"
     "- Both commands ship in the repo\n"),

    ("docs: publish the driver comparison with measured numbers", ["type:docs", "area:docs", "P0"],
     "## Context\n\nThe spike produces data. This turns it into a decision someone else can check.\n\n"
     "## Acceptance\n\n"
     "- Every number has the command that produced it\n"
     "- Losing option still documented\n"
     "- Re-running the benchmark reproduces the table\n"),

    ("docs: document the bridge protocol as a wire format", ["type:docs", "area:docs", "P1"],
     "## Acceptance\n\n"
     "- Entry types, ordering guarantee and cursor semantics specified\n"
     "- Someone could implement a second client from the document alone\n"
     "- Spec matches the implementation, checked by a test\n"),

    ("docs: write a getting started that reaches a result quickly", ["type:docs", "area:docs", "P0"],
     "## Acceptance\n\n\n"
     "- Clone to first query in five commands or fewer\n"
     "- Every command tested on a clean machine by someone who wrote none of it\n"),

    ("docs: record the four known traps as a single page", ["type:docs", "area:docs", "P1"],
     "## Context\n\nforeign keys off by default, replication slots filling disk, session compiled out, license traps.\n\n"
     "## Acceptance\n\n"
     "- Each trap states the symptom, the cause and the fix\n"
     "- Written from the failures we actually hit, not from theory\n"),

    ("docs: document the API contract source of truth", ["type:docs", "area:docs", "P1"],
     "## Acceptance\n\n"
     "- Explains where the contract lives and how it is generated\n"
     "- Contributor can regenerate types without asking anyone\n"),

    ("docs: write an architecture decision record index", ["type:docs", "area:docs", "P2"],
     "## Acceptance\n\n\n"
     "- Every ADR listed with title, date and status\n"
     "- Superseded records marked, not deleted\n"),

    ("docs: publish examples that run in CI", ["type:docs", "area:docs", "P2"],
     "## Acceptance\n\n"
     "- Every example in docs is executed by CI\n"
     "- A broken example fails the build\n"
     "- No example is allowed to rot\n"),

    # ------------------------------------------------------- meta and review
    ("research: survey local first sync engines and what we can learn", ["type:research", "area:bridge", "P2"],
     "## Context\n\nElectric, PowerSync, Zero, sqlite-sync all solve a version of our problem. We should know the shape before we build.\n\n"
     "## Acceptance\n\n\n"
     "- One write-up comparing their conflict models\n"
     "- Each claim has a source and a date\n"
     "- Ends with what we will borrow and what we will refuse, and why\n"),

    ("research: measure query plan differences on a real corpus", ["type:research", "area:lab", "P1"],
     "## Acceptance\n\n"
     "- Corpus committed and public\n"
     "- Results for both engines on the same corpus\n"
     "- Anomalies reported even where they are inconvenient\n"),

    ("chore: sweep stale branches and document the rule", ["type:chore", "area:infra", "P1"],
     "## Acceptance\n\n"
     "- Branches deleted after merge, local and remote\n"
     "- A stale branch sweep runs at the end of each cycle\n"
     "- Rule written down so nobody has to remember\n"),
]


def existing_titles():
    out = subprocess.run(
        ["gh", "issue", "list", "--repo", REPO, "--state", "all", "--limit", "500",
         "--json", "title"],
        capture_output=True, text=True, check=True).stdout
    return {i["title"] for i in json.loads(out)}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    titles = [b[0] for b in BACKLOG]
    dupes = {t for t in titles if titles.count(t) > 1}
    if dupes:
        print("duplicate titles in BACKLOG:", file=sys.stderr)
        for d in dupes:
            print("  " + d, file=sys.stderr)
        return 1

    print(f"{len(BACKLOG)} issues defined across "
          f"{len({l for b in BACKLOG for l in b[1] if l.startswith('area:')})} areas")

    have = existing_titles() if not args.dry_run else set()
    created = skipped = 0

    for title, labels, body in BACKLOG:
        if title in have:
            skipped += 1
            continue
        if args.dry_run:
            print(f"  would create  [{','.join(labels)}] {title}")
            created += 1
            continue
        cmd = ["gh", "issue", "create", "--repo", REPO, "--title", title, "--body", body]
        for l in labels:
            cmd += ["--label", l]
        r = subprocess.run(cmd, capture_output=True, text=True)
        if r.returncode != 0:
            print(f"  FAILED  {title}\n    {r.stderr.strip()[:200]}", file=sys.stderr)
            return 1
        created += 1
        print(f"  created  {title}")

    print(f"\ncreated {created}, skipped {skipped}")
    return 0


if __name__ == "__main__":
    sys.exit(main())