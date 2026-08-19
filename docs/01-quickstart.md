# Quickstart

```sh
make doctor          # Go, duckdb, docker on PATH
make build
SNOWFLAKE_DUCKDB_PATH=:memory: make run
```

The process prints its seeded PAT once and writes it to `data/admin.pat`. That
file is the password. `token=dev` and an empty password are 401 — this
emulator has no anonymous mode, because a consumer that authenticates against
nothing here will fail the first time it meets an account.

## The first statement

```sh
PAT=$(cat data/admin.pat)
curl -s -X POST http://127.0.0.1:8448/api/v2/statements \
  -H "Authorization: Bearer $PAT" -H 'Content-Type: application/json' \
  -d '{"statement":"SELECT 1 AS n","warehouse":"COMPUTE_WH"}'
```

```json
{"success": true,
 "data": {"rowtype": [{"name": "n", "type": "fixed", "precision": 38, "scale": 0}],
          "rowset": [["1"]],
          "dialect": "duckdb"}}
```

Two things in that answer are the point of the whole project.

`dialect: duckdb` says which engine ran the statement. This emulator does not
claim Snowflake SQL compatibility, and a result that hid the engine would let
a consumer believe otherwise.

`type: fixed` is the column's real type, not `text`. Every column answered
`text` until v0.1.4, and the cost was not cosmetic: dbt's own result schema
requires a number for a test's failure count, so a whole contract suite
reported `'0' is not of type 'integer'` — fifty-two tests with no verdict,
pass or fail, over data that was correct.

## What a refusal looks like

```sh
curl … -d '{"statement":"CREATE MATERIALIZED VIEW v AS SELECT 1"}'
```

```json
{"success": false, "code": "002001",
 "message": "duckdb: Parser Error: syntax error at or near \"MATERIALIZED\""}
```

A refusal names itself. That is the doctrine, and it was not always true: the
duckdb CLI this image pins **exits 0 after refusing a statement**, so until
v0.1.4 anything unparseable came back as `status: ok` — eighteen constructs,
silently. See [SQL surface](05-sql-surface.md).

## Next

- [Installation](02-installation.md) — the image, the binary, the wheel
- [Configuration](04-configuration.md) — every variable
- [Parity](parity.md) — what is answered and what is refused, measured
