# Doctrine

A Snowflake account emulator is possible on the same bet the family already
uses: terminate the public API, attach a real engine, leave the rest **not
implemented**.

DuckDB is the local engine. Statement results name `dialect: duckdb`. This
process does not claim Snowflake SQL compatibility.

## What this is

A sibling of databricks-emulator. Official `snowflake-connector-python` and
`gosnowflake` point at localhost. Identity is a seeded PAT
(`data/admin.pat`). `token=dev` and an empty password are 401.

## What this is not

- Snowflake SQL. Rewrites, if any, are documented as rewrites.
- Time Travel, Streams, Tasks, Cortex, Unistore, JS procedures.
- A fork of nnnkkk7/snowflake-emulator.

## Engine

`SNOWFLAKE_DUCKDB_PATH` (`:memory:` or a file) plus `duckdb` on PATH.
Without it, SQL fails naming `SNOWFLAKE_DUCKDB_PATH`.

Warehouses are session handles. Size is stored, not a performance claim.

## Iceberg

`SNOWFLAKE_POLARIS_URL` attaches an Iceberg REST catalog. Missing sidecar
fails naming that variable. Internal DuckDB tables are a different row.

## Proof

A row is green only when an unmodified official client drove the call.
See [parity.md](parity.md).
