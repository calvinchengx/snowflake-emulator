# Stages and COPY INTO

This is the half of Snowflake a bronze layer is made of: bytes land in a
stage, a file format says how to read them, `COPY INTO` parses them into a
table.

```sql
CREATE STAGE landing;
LIST @~;
CREATE FILE FORMAT csv_with_header TYPE = CSV SKIP_HEADER = 1;

COPY INTO orders FROM @~/orders.csv FILE_FORMAT = (FORMAT_NAME = csv_with_header);
COPY INTO raw    FROM @~/web.json   FILE_FORMAT = (TYPE = JSON);
```

The stage is a directory (`SNOWFLAKE_STAGE_DIR`); a named stage is a directory
inside it.

## The default format is Snowflake's, not a convenient one

`TYPE = CSV`, `SKIP_HEADER = 0`. **A header row is data unless the format says
otherwise.**

This emulator used to hardcode a header skip, so

```sql
COPY INTO nums FROM @~/nums.csv          -- nums.csv opens with a line reading `n`
```

succeeded here and would fail against a real account on an `INTEGER` column.
Measured both ways:

```
default        →  duckdb: Conversion Error: CSV Error on Line: 1
SKIP_HEADER=1  →  [['1'], ['2']]
```

That is the worst shape of infidelity to leave in place, because the consumer
does not find out here. It finds out in production.

## `PUT` puts the driver back in the loop

```sql
PUT file:///tmp/orders.csv @~;
COPY INTO orders FROM @~/orders.csv FILE_FORMAT = (TYPE = CSV, SKIP_HEADER = 1);
```

This was missing for a long time, and the cost was not the statement. Without
it a consumer cannot get a file into a stage the way a real consumer does, so
every pipeline built here reached for the stage **directory** instead: write
the bytes to a shared mount, let `COPY INTO` find them. That code does not move
to a real account, where no such directory exists. The emulator's convenience
had become the consumer's architecture, and nothing failed to say so.

`PUT` is Snowflake's own client-side protocol. The driver recognises the
statement before sending it, asks where the bytes go, and uploads them itself.
The answer names a location type, and both drivers this emulator is witnessed
against implement `LOCAL_FS` beside S3, Azure and GCS. So the answer is
`LOCAL_FS` and the stage directory, and the **driver's own file transfer agent**
does the copying, over the same code path it uses against a real account.

`AUTO_COMPRESS` defaults to `TRUE`, as it does there, so

```
PUT file:///tmp/orders.csv @~   →   the stage holds orders.csv.gz
```

Answering `FALSE` would be more convenient here and would teach a consumer the
wrong stage contents. Because real Snowflake resolves a stage path by prefix,
`COPY INTO ... FROM @~/orders.csv` finds that compressed file there; this
emulator resolves one name and its `.gz` spelling, which is **narrower**. A
prefix naming several files loads all of them on a real account and is not
implemented here, rather than being half-implemented as "the first one".

## An option that cannot be honoured is refused

Silently dropping `FIELD_DELIMITER` reads every line into column one and
reports success. So `RECORD_DELIMITER`, `SKIP_HEADER` greater than one, and
`TYPE = XML` say so by name.

Carried through: `SKIP_HEADER`, `FIELD_DELIMITER`,
`FIELD_OPTIONALLY_ENCLOSED_BY`, `ESCAPE`, `NULL_IF`, and `TYPE` for CSV, JSON
and PARQUET.

## External stages

Refused by name. `s3://` and `azure://` are different things with credentials
attached, and serving one from a local directory would work here and fail
exactly where the credentials matter.

## Known limits

- `PUT` is a client-side upload protocol and is not implemented — put the bytes
  in the directory.
- `REMOVE` and `INFER_SCHEMA` are refused.
- `LIST @~` walks the whole stage directory, so a named stage created inside it
  appears in the user stage's listing.
