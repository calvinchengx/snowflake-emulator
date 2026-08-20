# Roadmap

What is not implemented is listed in [parity](parity.md), generated from a run
against the image. This page is about which of those absences are next, and
why.

## Next

**`MERGE`.** Ordinary Snowflake, and what an incremental dbt model emits. No
model in the Contoso product needs it today, which is why it has waited.

**`OBJECT_CONSTRUCT`.** The one common semi-structured constructor still
missing.

**`REMOVE`.** The counterpart to `PUT`, which shipped. A stage you can write
and never clear grows for the life of the container.

**`COPY INTO` from a prefix.** `INFER_SCHEMA` describes a whole feed now, but
`COPY INTO` still resolves one name, so a paged feed is a loop in the caller.
The two ought to take the same reference.

**Stored procedures.** `CALL` and `SYSTEM$WAIT` are refused. Listed here rather
than left unclassified: this page previously named neither these nor `REMOVE`
in either section, which for a project whose whole claim is that a gap can be
planned around is the documentation version of the defect it guards against.

## Done since this page was written

**Typed loading, via `INFER_SCHEMA`.** The entry that used to head this list.
`COPY INTO` fills a table that already exists, so with nothing to ask, both
Contoso Snowflake leaves grew their own sampler -- read some rows, classify
each cell, widen, emit DDL. Two copies of a type-inference heuristic living in
the product because the warehouse would not answer the question. It answers now,
and the answer carries Snowflake's own type names, which meant teaching the
emulator to accept them in DDL as well: `CREATE TABLE t (a NUMBER(38,0))` was
refused by its own inference until that landed. Measured, not assumed -- see
`docs/06-stages-and-copy.md`.

## Deliberately not planned

**`DATEADD` with month, quarter or year.** Not effort — a limit. Every DuckDB
spelling widens a `DATE` to a `TIMESTAMP`, and SQL unifies a `CASE` to one
type, so no rewrite can return the type Snowflake returns. Answering with the
wrong type is worse than refusing.

**Time Travel and `CLONE`.** They need a versioned store; DuckDB has none.

**`GRANT` and roles.** An access model that is accepted and not enforced is
worse than one that is absent, because a consumer would test against it.

**Snowflake SQL compatibility as a claim.** The result names `dialect: duckdb`
and always will. What this project promises is that a statement a real account
accepts is either answered or refused **by name** — never quietly ignored.

## The rule this project keeps relearning

Three separate defects this year were the same shape: something reported
success while doing nothing. A CLI's exit code trusted, a stream that would
have missed an update, a `COPY INTO` that loaded no rows and said `ok`. Each
was found by measuring rather than by reading, and each is now guarded by a
test that fails on the old behaviour.

If you are adding to this emulator: make the failure honest first, then
implement. The order matters — until refusals were honest, eighteen missing
constructs were invisible, including several nobody had thought to ask about.
