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

## Related projects

`snowflake-emulator` is **not** part of the Azure emulator family and is
deliberately absent from its compose: Snowflake is not an Azure service. It is a
peer project sharing the same approach — a local account emulator that official
connectors point at.

Its Azure counterparts are [**azure-emulators**](https://github.com/calvinchengx/azure-emulators) (entra, Key Vault, ARM,
Fabric, API Management and Databricks); its consumer is
[`contoso-snowflake-platform`](https://github.com/calvinchengx/contoso-snowflake-platform).
