# snowflake-emulator

Local Snowflake **account** emulator. Official connectors point at localhost.
SQL runs on **DuckDB** and the result names `dialect: duckdb`. Time Travel,
Streams, Cortex, and “Snowflake SQL compatible” are not implemented.

```sh
make doctor
make test
SNOWFLAKE_DUCKDB_PATH=:memory: make run
```

`data/admin.pat` is the seeded password. `token=dev` is 401.

See [docs/00-doctrine.md](docs/00-doctrine.md) and [docs/parity.md](docs/parity.md).

Apache-2.0.
