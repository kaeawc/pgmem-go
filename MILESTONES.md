# pgmem-go — milestones

Each milestone is an end-to-end slice. We do not build a layer in
isolation and then "wire it up later" — every milestone produces
something a test author could (in principle) call.

## M0 — wire protocol echo

**Goal:** `pgxpool.New(ctx, srv.DSN())` succeeds. A `SELECT 1` returns
the literal value 1.

- `postgres/wire`: startup, trust auth, simple-query for `SELECT 1`,
  Sync/ReadyForQuery loop.
- `postgres/server.go`: `Start(t)` listens on a free TCP port,
  registers cleanup, returns DSN.
- No parser. `SELECT 1` is recognized by string match and returns a
  hardcoded result row.
- Stub `pg_type` responses sufficient for pgx's startup queries.

**Done when:** a Go test opens a pgxpool, runs `SELECT 1`, gets `1`,
closes cleanly, no goroutine leaks.

## M1 — hardcoded catalog, hardcoded query

**Goal:** `SELECT id, name FROM users` works against a single
hand-built table, with no parser. The README calls this out as the
target first slice.

- `catalog`: in-memory `Schema` with one hardcoded `users(id int, name text)`.
- `storage`: simplest possible `Table` (slice of rows, RWMutex).
- `ir`: `Scan` and `Project` nodes.
- `exec`: scan operator and project operator.
- `postgres/parse`: a `Parse` function that, for the literal string
  `SELECT id, name FROM users`, hand-builds the IR. Other strings
  return "unsupported."
- Extended-query protocol (Parse/Bind/Describe/Execute), because pgx
  uses it by default.

**Done when:** a sqlc-generated `GetUsers` function returns the
seeded rows.

## M2 — real parser, real DDL

**Goal:** `CREATE TABLE` + `INSERT` + `SELECT … WHERE … ORDER BY …
LIMIT` from arbitrary user SQL.

- `postgres/parse`: pure-Go parser integration; AST→IR translation for
  the statement set above.
- `ir`: `Filter`, `Sort`, `Limit`, `Insert`, `Values`.
- `exec`: matching operators.
- `types`: int4, int8, text, bool, timestamptz end-to-end including
  binary wire format.
- Parameter binding (`$N`) with type inference from Bind.

**Done when:** a fresh schema can be created, populated, and queried by
a sqlc-generated test suite covering the four operations above.

## M3 — transactions and constraints

- `BEGIN`/`COMMIT`/`ROLLBACK`, `SAVEPOINT`/`RELEASE`/`ROLLBACK TO`.
- COW snapshot per tx, write-write conflict detection on commit.
- Primary key, NOT NULL, UNIQUE enforcement (via btree index on the
  unique columns).
- FOREIGN KEY with `ON DELETE CASCADE`/`SET NULL`/`RESTRICT`.
- CHECK constraints (expression evaluator reuse).

**Done when:** a sqlc test suite that uses transactions and relies on
constraint violations (duplicate-key errors with correct SQLSTATE
`23505`) passes.

## M4 — RETURNING, UPDATE, DELETE, JOINs

- `INSERT … RETURNING`, `UPDATE … RETURNING`, `DELETE … RETURNING`.
- `INNER JOIN`, `LEFT JOIN`, `CROSS JOIN`. Nested-loop only.
- Subqueries in `WHERE` (scalar and `IN`).

## M5 — types coverage

- `numeric`, `uuid`, `bytea`, `jsonb`, arrays of supported scalars.
- `SERIAL`/`BIGSERIAL`, identity columns, sequence machinery.
- `gen_random_uuid()`, `now()`, `coalesce`, `nullif`, basic `jsonb_*`.

## M6 — virtual catalog hardening

- `pg_catalog` and `information_schema` queries exercised by popular
  Go ORMs (gorm, ent) succeed even if the ORMs themselves aren't
  the target audience — they share the same introspection patterns
  as pgx and break in the same ways.

## M7 — opt-in cgo parser

- `cgo_pgquery` build tag; `pg_query_go` integration emitting the
  same `ir.Node` tree.
- Parser-conformance test runs both and asserts identical IR.

## Post-v0 (architected for, not built)

- `LISTEN`/`NOTIFY`.
- Real MVCC with version chains.
- Index-driven scans (currently uniqueness-only).
- MySQL sibling sharing `ir`/`storage`/`exec`.

## Rule of thumb for milestone discipline

Each milestone closes with a passing sqlc-style test that demonstrates
the new capability against a real `pgxpool`. If we can't write that
test, the milestone isn't done — and the slice was probably mis-cut.
