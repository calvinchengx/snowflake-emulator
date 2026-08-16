# Parity

## Legend

| Grade | Meaning |
|---|---|
| 🟢 **Real** | Unmodified official client, real engine or store. |
| 🔴 **Not implemented** | Honest failure naming what is missing. Never a silent 200. |

## Identity

| Feature | Emulator | Type |
|---|---|---|
| Seeded PAT | Written to `data/admin.pat` on first start. `token=dev` and empty password are 401. | 🟢 Real |
| Any-password-accepted | | 🔴 Not implemented |

## SQL

| Feature | Emulator | Type |
|---|---|---|
| SQL warehouses | Session handle. CREATE / SHOW / ALTER SUSPEND\|RESUME. SQL runs on DuckDB. Result `dialect` is `duckdb`. | 🟢 Real |
| Snowflake SQL compatibility | | 🔴 Not implemented |
| Time Travel | | 🔴 Not implemented |
| Streams / Tasks | | 🔴 Not implemented |
| Cortex | | 🔴 Not implemented |
| Internal stage COPY INTO | Files under `SNOWFLAKE_STAGE_DIR`. | 🟢 Real |
| External cloud stages | | 🔴 Not implemented |

## Catalog

| Feature | Emulator | Type |
|---|---|---|
| Iceberg REST list | After `SNOWFLAKE_POLARIS_URL` is set, `CREATE ICEBERG TABLE` is listed at `/iceberg/v1/`. Missing URL names `SNOWFLAKE_POLARIS_URL`. | 🟢 Real |
| Horizon / Time Travel catalog | | 🔴 Not implemented |

## Clients

| Feature | Emulator | Type |
|---|---|---|
| snowflake-connector-python / gosnowflake | Login + SELECT 1. | 🟢 Real |
| dbt-snowflake warehouse run | Unmodified `dbt-snowflake` `dbt run` of `one` and `two`. | 🟢 Real |
| snowflake-target toggle | `SNOWFLAKE_TARGET=emulator\|real`. | 🟢 Real |
