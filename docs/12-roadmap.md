# Roadmap

What is not implemented is listed in [parity](parity.md), generated from a run
against the image. This page is about which of those absences are next, and
why.

## Next

**`MERGE`.** Ordinary Snowflake, and what an incremental dbt model emits. No
model in the Contoso product needs it today, which is why it has waited.

**`OBJECT_CONSTRUCT`.** The one common semi-structured constructor still
missing.

**Typed CSV loading.** `COPY INTO` from a CSV lands every column as text,
where Spark's reader infers. A consumer whose bronze arrives as CSV must cast
in silver, and a date column that is really a string is the kind of difference
that surfaces three layers later.

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
