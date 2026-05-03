# pgmem-go — design

Answers to the six design questions in the README, plus the implicit
seventh: how the pieces talk to each other.

## 1. Parser strategy

**Default: pure-Go parser (`auxten/postgresql-parser`). Opt-in cgo path
(`pganalyze/pg_query_go`) behind a `cgo_pgquery` build tag.**

The README treats "pure Go, no cgo by default" as a hard requirement, so
this is barely a choice — it's a consequence. What's worth defending is
the *layering*: both parsers emit their own AST, neither of which we want
leaking through the codebase. Both implementations live under
`postgres/parse/` and produce the same `ir.Node` tree. Switching parsers
is a build-tag flip, not a rewrite.

The pure-Go parser will mis-parse some Postgres syntax. We accept that as
the cost of the no-cgo guarantee, and document the cgo path as the
escape hatch for users whose schemas hit those gaps.

## 2. IR shape

**Yes, a thin IR — a logical plan tree, not relational algebra.**

Nodes: `Scan`, `Filter`, `Project`, `Join`, `Aggregate`, `Sort`, `Limit`,
`Insert`, `Update`, `Delete`, `Values`, plus `Expr` for scalar
expressions. No optimizer, no rewrite rules — `parse` builds it,
`exec` walks it.

Reasons to pay the abstraction cost on day one:

- Two parsers (pure-Go, cgo) need a common output. Without an IR we'd
  fork the executor.
- The catalog (`pg_catalog`, `information_schema`) is implemented as
  IR-backed views. Without an IR those become a second code path.
- The MySQL-sibling argument from the README is real but secondary —
  the parser-swap argument alone justifies it.

Reasons it's *thin*: no cost-based planning, no statistics, no
rewrite-to-canonical-form. Operators map almost 1:1 to AST nodes.

## 3. Storage model

**v0: copy-on-write tables behind `sync.RWMutex`. Transactions get a
snapshot on `BEGIN`; writes copy-on-modify into a per-tx overlay; commit
applies the overlay under the table write lock.**

This gives us:

- Snapshot read isolation for free (the tx holds its snapshot pointer).
- No version chains, no vacuum, no xmin/xmax bookkeeping.
- Trivially correct rollback: drop the overlay.

It does **not** give us:

- True serializable isolation. Concurrent write-write conflicts on the
  same row are detected at commit (overlay vs current head) and the
  later tx errors. Read-skew across tables is possible. We document
  this as a known fidelity gap; sqlc-style test code rarely exercises
  it.

Migration path to real MVCC, when we need it: add `xmin`/`xmax` to row
storage, replace snapshot-COW with version chains, keep the executor's
table-iterator API unchanged. The exec layer never sees snapshots
directly — it sees an `storage.Txn` handle.

## 4. Catalog implementation

**Virtual views computed from schema metadata.**

`pg_catalog.pg_type`, `pg_catalog.pg_class`, `information_schema.columns`
— each is a struct that implements the same row-iterator interface as a
real table. They read from the live `catalog.Schema` on each query.

Cost: more code per catalog table.

Benefit: zero drift. When `CREATE TABLE` adds an entry to the schema, it
is *immediately* visible in `pg_catalog.pg_class` because there's no
materialized copy to update. The alternative (materialize on startup,
rebuild on DDL) sounds simpler until the first bug where a catalog
lookup returns stale rows.

We start with the handful of catalog tables pgx queries on startup
(`pg_type`, `pg_range`, a few `information_schema` views) and add more
as failing tests demand.

## 5. Type coercion

**Implement only the casts sqlc-generated code actually emits. Maintain
a `TYPES.md` listing what's supported and what's known-missing.**

The full PG cast lattice is a research project. sqlc emits a narrow,
predictable set: literal-to-column-type at parameter binding, numeric
widening in arithmetic, text↔varchar interchange. We implement that
slice end-to-end and reject (with a clear error) anything outside it.

This is a deliberate fidelity sacrifice — the README's "fidelity over
performance" doesn't mean "fidelity over scope." We pick the scope (sqlc
output) and aim for fidelity *within* it.

## 6. Concurrency model

- One goroutine per client connection (pgproto3's natural shape).
- Storage layer: `sync.RWMutex` per table.
- Writes inside a transaction land in a per-tx overlay (no lock held
  between statements).
- `COMMIT` acquires the table write locks in a deterministic order
  (table OID ascending, to prevent deadlock), checks the overlay
  against current head for write-write conflicts, and applies.
- `LISTEN`/`NOTIFY` (post-v0) routes through a per-server channel
  registry — does not touch the storage path.

No global write lock. Tests inside one process must be able to run
parallel transactions against independent tables without serializing.

## Cross-cutting: clock and randomness

`Server` takes an optional `clock.Clock` (default: real wall clock) and
`rand.Source` (default: crypto/rand). `now()`, `current_timestamp`,
`gen_random_uuid()` route through these. This is the README's "time
travel" requirement — exposed as `srv.SetNow(time.Time)` for test
ergonomics.

## Cross-cutting: error model

Wire-protocol errors must look like real PG errors (SQLSTATE + message)
because pgx pattern-matches on them. We maintain a small
`postgres/wire/errors.go` mapping internal error types to SQLSTATE
codes. Internal errors (executor panics, type-system bugs) map to
`XX000 internal_error` with the Go error chained for test debugging.

## What this design explicitly does *not* commit to

- Query planner. Operators run in the order the IR specifies.
- Cost model, statistics, ANALYZE.
- Index selection. Indexes exist for uniqueness enforcement; the
  executor never reads from them for SELECT in v0.
- Parallel query, vectorized execution, JIT.

These are non-goals per the README and we should resist the urge to
add scaffolding for them.
