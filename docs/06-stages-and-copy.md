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
