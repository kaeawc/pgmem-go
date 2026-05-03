# pgmem-go — architectural brief

This README is a **prompt**, not a finished design. Read it, ask sharp
questions, and produce a design doc + initial package layout in a follow-up.

## What we're building

An in-memory, Postgres-wire-compatible test database, implemented in Go,
optimized for **fidelity over performance**. The audience is Go developers
writing unit/integration tests against code that talks to Postgres via pgx
and sqlc.

A test author should be able to:

```go
srv, _ := pgmem.Start(t)         // boots in <100ms, picks a free port
defer srv.Stop()

pool, _ := pgxpool.New(ctx, srv.DSN())
// ... run sqlc-generated queries against pool, no Docker, no Postgres binary
```

The bar is "behaves enough like Postgres that sqlc-generated code passes,"
not "production database."

## Hard requirements

1. **Postgres wire protocol** — clients connect with stock pgx/pgxpool,
   unmodified DSN, no custom dialer.
2. **Pure Go, no cgo** by default. A cgo build tag for `pg_query_go` is
   acceptable as an opt-in for higher parser fidelity, but the default
   build must work on any Go target without a C toolchain.
3. **No external processes**. No Docker, no `postgres` binary on disk.
   Everything runs in-process.
4. **Per-test isolation**. `pgmem.Start(t)` returns a fresh database
   that's torn down with the test. No global state.
5. **Deterministic** where it matters: row ordering in `ORDER BY`, `now()`
   pluggable via a `clock.Clock` interface for time travel in tests.

## Scope (what sqlc users actually need)

Must work on day one:

- `SELECT … FROM … WHERE … ORDER BY … LIMIT/OFFSET` with `$N` placeholders
- `INSERT … RETURNING`, `UPDATE … RETURNING`, `DELETE … RETURNING`
- Transactions: BEGIN/COMMIT/ROLLBACK, `SAVEPOINT`, default isolation
- Common types: int2/4/8, text/varchar, bool, timestamptz, uuid, numeric,
  bytea, jsonb, arrays of the above
- `SERIAL`/`BIGSERIAL` and identity columns
- Primary keys, NOT NULL, UNIQUE, FOREIGN KEY (with cascades), CHECK
- Indexes: btree only is fine; used for correctness (uniqueness), not speed
- `pg_catalog.pg_type` and `information_schema` lookups — pgx and many ORMs
  query these on connect; even stub data must be correct enough not to crash
- `LISTEN`/`NOTIFY` — out of scope initially, but architect so it can land
  later without a rewrite

Explicit non-goals:

- Performance. Single-digit-MB datasets, sequential scans, no planner.
- Replication, streaming, logical decoding.
- Extensions (PostGIS, pgcrypto). A `now()`/`gen_random_uuid()` shim is fine.
- Stored procedures, triggers, rules.
- Multi-database / multi-schema parity at production scale (basic schema
  support yes, search_path edge cases no).

## Architecture seams

Design these as interfaces from day one. We may add a MySQL sibling later;
we don't want to relitigate the architecture when that happens.

```
pgmem-go/
  ir/         relational algebra, logical plan, expressions, dialect-neutral
  storage/    in-memory tables, indexes, MVCC or RWMutex-per-table (decide)
  exec/       operators: scan, filter, project, join, aggregate, sort
  types/      base type kit; PG-specific types in postgres/types
  catalog/    pg_catalog + information_schema as IR-backed views

  postgres/
    wire/     pgproto3 server: startup, auth (trust), simple+extended query
    parse/    SQL → IR (libpg_query via build tag, fallback parser otherwise)
    funcs/    PG builtin functions (now, coalesce, jsonb_*, etc.)
    types/    PG-specific type registrations
    server.go public Start/Stop API
```

The interesting design questions to answer in your follow-up:

1. **Parser strategy.** `pg_query_go` (cgo, perfect PG syntax) vs
   `auxten/postgresql-parser` (pure Go, extracted from CockroachDB,
   imperfect but adequate). My instinct: pure-Go parser as the default,
   `pg_query_go` behind a `cgo` build tag for fidelity-critical use cases.
   Justify your choice or push back.
2. **IR shape.** Do we need an IR at all if we only target one dialect on
   day one? Argument for: keeps the MySQL door open and forces clean
   parse/exec separation. Argument against: premature abstraction; the
   parser AST might be enough. Decide and defend.
3. **Storage model.** Copy-on-write maps + RWMutex per table is the
   simplest thing that could work. MVCC gives us realistic transaction
   isolation but is a lot more code. Pick one for v0; document the
   migration path to the other.
4. **Catalog implementation.** The `pg_catalog` tables are queried by pgx
   on every connection. Are they real in-memory tables populated at
   startup, or virtual views computed on demand from our schema metadata?
   The latter avoids drift but is more code.
5. **Type coercion.** PG's implicit cast rules are nontrivial. Do we
   implement the full lattice or just the casts sqlc-generated code
   actually emits? Start narrow, document what's missing.
6. **Concurrency model.** Each connection gets a goroutine; how do we
   serialize writes against the storage layer? Per-table locks? Global
   write lock? MVCC?

## Reference projects to study

- **pg-mem** (TypeScript, https://github.com/oguimbal/pg-mem) — the design
  we're copying: AST-walk interpreter, no planner, focus on test
  ergonomics. Read its architecture before designing ours.
- **CockroachDB testserver** — the closest existing Go option, but a real
  binary. Useful as a fidelity benchmark, not a code reference.
- **jackc/pgproto3** — wire-protocol library we'll depend on.
- **jackc/pgmock** — protocol-level fake; useful as a reference for how to
  drive pgproto3 as a server, not as a base to fork.
- **auxten/postgresql-parser** — pure-Go PG parser candidate.
- **pganalyze/pg_query_go** — cgo PG parser candidate.

## Out of scope for the brief reply

Don't write code yet. The follow-up should produce:

1. A `DESIGN.md` answering the six design questions above.
2. A package skeleton (empty files with package docs and exported
   interface signatures) reflecting the chosen architecture.
3. A `MILESTONES.md` listing the smallest end-to-end slice that gets a
   trivial sqlc-generated query (`SELECT id, name FROM users`) passing
   against a real pgxpool — and what comes after that.

The first working slice should be SELECT-only against a hand-built
catalog with one table, no parser at all (hardcode the AST). That proves
the wire protocol works end-to-end before we commit to a parser.
