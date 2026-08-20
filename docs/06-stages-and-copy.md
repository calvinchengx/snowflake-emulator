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
emulator resolves one name and its `.gz` spelling, which is **narrower**.

A prefix naming several files loads all of them on a real account. Here it is
**refused by name**, rather than half-implemented as "the first one":

```
COPY INTO t FROM @~/feed/
  →  COPY INTO from a prefix is not implemented (a trailing slash): name one
     file. Snowflake loads every file under a prefix; this resolves one name
     and the .gz that AUTO_COMPRESS leaves
```

A trailing slash, a directory, a glob and the bare stage are all refused this
way. It used to come back as duckdb's own words instead:

```
duckdb: IO Error: No files found that match the pattern "/stages/feed"
```

which is a refusal and not a silence, so nothing was ever loaded wrongly. But
it names a path inside the container and a duckdb concept, and leaves the
reader to work out that the FEATURE is missing rather than the file. A
consumer did work that out, and wrote the loop that names each part with a
comment explaining why.

## An option that cannot be honoured is refused

Silently dropping `FIELD_DELIMITER` reads every line into column one and
reports success. So `RECORD_DELIMITER`, `SKIP_HEADER` greater than one, and
`TYPE = XML` say so by name.

Carried through: `SKIP_HEADER`, `FIELD_DELIMITER`,
`FIELD_OPTIONALLY_ENCLOSED_BY`, `ESCAPE`, `NULL_IF`, and `TYPE` for CSV, JSON
and PARQUET.

## `INFER_SCHEMA` answers what the feed holds

`COPY INTO` fills a table that already **exists**, so something has to decide
the column types first. Landing everything as text is not the neutral choice it
looks like: a date column arrives as `TEXT`, silver does date arithmetic on it,
and the reader gets `No function matches the given name and argument types
'+(VARCHAR, INTEGER)'` three layers away from the `COPY` that decided it.

```sql
CREATE FILE FORMAT my_csv TYPE = CSV SKIP_HEADER = 1;

SELECT "COLUMN_NAME", "TYPE"
FROM TABLE(INFER_SCHEMA(LOCATION => '@~/feed/', FILE_FORMAT => 'my_csv'))
ORDER BY "ORDER_ID";
```

```
order_id   | NUMBER(38,0)
order_date | DATE
amount     | FLOAT
ok         | BOOLEAN
```

Four things about it are worth knowing:

**A prefix is the ordinary form**, unlike `COPY INTO`, which resolves one name.
A feed is a directory of parts and describing it is the whole point.

**Files that disagree are unioned by name.** `FILENAMES` is per column — a
column only the second file carries lists only that file. Returning the whole
list for every column would be right only when the files agree, and wrong
exactly when a caller most needs to know.

**`FILE_FORMAT` is required**, as it is in a real account. Guessing CSV would
read a JSON feed as one text column per line and call it an inference.

**It is a relation, not one blessed sentence.** `WHERE`, `ORDER BY` and joins
over the result are the engine's own.

## Snowflake's type names in DDL

`INFER_SCHEMA` reports the names an **account** reports, so a `CREATE TABLE`
built from its answer is one you could run against real Snowflake. That means
this emulator has to accept them too: `NUMBER`, `NUMBER(p,s)`, `TIMESTAMP_NTZ`,
`TIMESTAMP_LTZ` and `TIMESTAMP_TZ` are mapped to the engine's spellings in DDL,
alongside `VARIANT`, `OBJECT` and `ARRAY`. Bare `NUMBER` becomes
`DECIMAL(38,0)` — Snowflake's default, not the engine's `(18,3)`.

`DESCRIBE TABLE` still reports the **engine's** names, and deliberately. This
family's `money_is_never_stored_as_float` contract accepts only `decimal` and
`numeric` prefixes, so renaming what `DESCRIBE` reports would fail all 52 gold
contracts. The two statements answer in different vocabularies; that is
recorded rather than reconciled.

## External stages

Refused by name. `s3://` and `azure://` are different things with credentials
attached, and serving one from a local directory would work here and fail
exactly where the credentials matter.

## Known limits

- `PUT` is a client-side upload protocol and is not implemented — put the bytes
  in the directory.
- `REMOVE` is refused.
- `INFER_SCHEMA` describes a prefix; `COPY INTO` still loads one named
  file at a time, so a paged feed is a loop in the caller.
- `LIST @~` walks the whole stage directory, so a named stage created inside it
  appears in the user stage's listing.
