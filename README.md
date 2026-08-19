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

## Docs

[Doctrine](docs/00-doctrine.md) · [Quickstart](docs/01-quickstart.md) ·
[Installation](docs/02-installation.md) · [Architecture](docs/03-architecture.md) ·
[Configuration](docs/04-configuration.md)

**Reference** — [SQL surface](docs/05-sql-surface.md) ·
[Stages and COPY INTO](docs/06-stages-and-copy.md) ·
[VARIANT and the colon path](docs/07-semi-structured.md) ·
[Tasks and streams](docs/08-tasks-and-streams.md) · [Clients](docs/09-clients.md)

**The project** — [Testing](docs/10-testing.md) ·
[Family integration](docs/11-family-integration.md) · [Roadmap](docs/12-roadmap.md) ·
[Parity](docs/parity.md), measured against the image rather than written.

Apache-2.0.

## Related projects

`snowflake-emulator` is **not** part of the Azure emulator family and is
deliberately absent from its compose: Snowflake is not an Azure service. It is a
peer project sharing the same approach — a local account emulator that official
connectors point at.

Its Azure counterparts are [**azure-emulators**](https://github.com/calvinchengx/azure-emulators) (entra, Key Vault, ARM,
Fabric, API Management and Databricks); its consumer is
[`contoso-snowflake-platform`](https://github.com/calvinchengx/contoso-snowflake-platform).
