# Configuration

Every knob is an environment variable. There is no config file, because a
config file is a second place for the truth to live.

| Variable | Default | What it does |
|---|---|---|
| `SNOWFLAKE_ADDR` | `127.0.0.1:8448` | listen address. `0.0.0.0:8448` in a container |
| `SNOWFLAKE_PUBLIC_URL` | `http://127.0.0.1:8448` | the URL handed to clients that ask |
| `SNOWFLAKE_DATA_DIR` | `./data` | where the seeded PAT is written |
| `SNOWFLAKE_DUCKDB_PATH` | *(unset)* | the warehouse file, or `:memory:` |
| `SNOWFLAKE_STAGE_DIR` | `./stages` | the internal stage's root |
| `SNOWFLAKE_POLARIS_URL` | *(unset)* | an Iceberg REST catalog to register into |

## The engine must be named

`SNOWFLAKE_DUCKDB_PATH` has **no default**, and that is deliberate. An
emulator that quietly attached an in-memory database would answer `SELECT 1`
and lose every table on the next statement, because each `Exec` is a separate
CLI process. Unset, SQL is refused with the variable's name in the message.

`:memory:` is supported and means what it says: no state survives a statement.
Use it for a login probe, not for a pipeline.

## The stage is a directory

`SNOWFLAKE_STAGE_DIR` is the user stage (`@~`). A named stage is a directory
inside it, created by `CREATE STAGE`. External stages — `s3://`, `azure://` —
are **refused by name** rather than served from local disk: an emulator that
answered them would work here and fail exactly where the credentials matter.

`PUT` is not implemented. It is a client-side upload protocol; put the bytes in
the directory instead.

## Identity

The PAT is minted on first start and written to `$SNOWFLAKE_DATA_DIR/admin.pat`,
printed once. Delete the file to mint a new one. `token=dev`, an empty
password, and any other credential are 401 — there is no anonymous mode, and
no way to configure one.
