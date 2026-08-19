# VARIANT and the colon path

How Snowflake reads semi-structured data, and therefore how a bronze layer
reads a vendor's JSON export.

```sql
CREATE TABLE raw_web (id INT, customer VARIANT, lines VARIANT);
COPY INTO raw_web FROM @~/web.json FILE_FORMAT = (TYPE = JSON);

SELECT id, customer:email::string FROM raw_web;

SELECT r.id, f.value:sku::string, f.value:qty::int
FROM raw_web r, LATERAL FLATTEN(input => r.lines) f;
```

`VARIANT`, `OBJECT` and `ARRAY` all map to DuckDB's `JSON`. Snowflake's
distinction between them is about what a value may hold, not about storage.
The mapping applies in DDL only — `SELECT variant FROM t` is a column
reference.

## The cast is the part that matters

`json_extract` returns JSON, so a string comes back **wearing its quotes**.
Without special handling, `v:email::string` would be `"a@x.com"` *including*
them — a value that compares unequal to itself across engines, and the kind of
defect no row count notices. A cast straight after a path routes through
`json_extract_string`, which unwraps it.

`::` is Snowflake's cast operator and never a path, so a colon belonging to one
is skipped. Colons inside string literals and comments are skipped too.

## FLATTEN

`LATERAL FLATTEN(input => x)` yields `value` and `index`, 0-based, restarting
per input row — checked against a two-row table whose arrays differ in length,
rather than read off the documentation.

It accepts both a VARIANT and a native array, because the expression goes
through `json_extract(to_json(x), '$[*]')`: the identity on JSON, a converter
for a list. Values arrive as JSON, exactly as Snowflake hands back VARIANT, and
casts work through them.

`OUTER`, `RECURSIVE`, `PATH` and `MODE` are **refused by name**. Each changes
which rows come back — `OUTER` keeps the input row when the array is empty,
`RECURSIVE` descends — so accepting and ignoring them would return a different
result set and say nothing about it.

`SEQ`, `KEY`, `PATH` and `THIS` are not produced.

## Not implemented

`OBJECT_CONSTRUCT`. `PARSE_JSON` and `ARRAY_GENERATE_RANGE` are.
