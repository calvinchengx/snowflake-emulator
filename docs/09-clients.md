# Clients

Every client here is the **unmodified** official one, pointed at localhost. A
client patched to work against an emulator proves nothing about the account it
is meant to stand in for.

## snowflake-connector-python and gosnowflake

Login with the seeded PAT as the password, `protocol: http`, `insecure_mode`.
Both are exercised by `e2e/sdk`, which is a CI job rather than a claim.

## dbt-snowflake

Runs unmodified against the warehouse endpoint. The profile is ordinary:

```yaml
type: snowflake
account: "{{ env_var('SNOWFLAKE_ACCOUNT') }}"
password: "{{ env_var('SNOWFLAKE_PASSWORD') }}"   # data/admin.pat
host: "{{ env_var('SNOWFLAKE_HOST') }}"
port: "{{ env_var('SNOWFLAKE_PORT') | as_number }}"
protocol: http
insecure_mode: true
```

**dbt is the writer, so it is not the witness.** `e2e/dbt` runs a model, stops
the emulator, and opens the warehouse file with a separate `duckdb` binary to
confirm the rows are there. An engine confirming its own write is not evidence.

dbt needs more of the surface than a SELECT does — it lists relations and
user-defined functions before it runs anything, and expects each result to
carry its columns. `SHOW FUNCTIONS` returns the five columns dbt selects with
zero rows, because "no UDFs" is an answer and an empty result with no columns
is not.

## Dates, times and timestamps are numbers on the wire

Not `2026-01-02`. The client's converters read a **number**:

| type | wire value | |
|---|---|---|
| `DATE` | `20455` | days since the epoch |
| `TIMESTAMP_NTZ` | `1767322645.123456` | seconds, with the fraction the scale declares |
| `TIME` | `11045.500000` | seconds since midnight |

Until this was fixed, all three were **unreadable**. Not refused and not wrong:
a `SELECT` of a `DATE` raised `252005: Failed to convert: DATE::2026-01-02,
Error: invalid literal for int()`. A consumer could not read a date out of this
emulator through the client it is meant to be used with.

It survived because the pipelines built on this do their work in SQL and read
back counts and sums. Nothing selected a date, so nothing failed, and the
capability was never claimed as working because nobody had asked.

The scale in the row type is load-bearing: the connector splits the seconds
from the fraction with it, so a fractional value declared scale 0 comes back
with the fraction dropped.

## A boolean is spelled the way the client reads it

`TRUE` and `FALSE`, not duckdb's `true` and `false`, because the client is the
one that has to understand it. snowflake-connector-python converts a boolean
column with

```python
lambda value: value in ("1", "TRUE")
```

so duckdb's spelling answered **false for every boolean**, a literal `TRUE`
included. That is a wrong answer rather than a refusal, and nothing anywhere
failed while it was happening: the JSON this emulator sent was self-consistent,
and only the client disagreed. It is the reason the test for it asserts through
the connector rather than reading the response.

gosnowflake has no boolean arm at all in the JSON result format and hands the
raw string through, so it reads what it is given either way.

## snowflake-target

The Python client published with each release, pinning the emulator's contract
and the `SNOWFLAKE_TARGET=emulator|real` toggle. The toggle is what lets a
consumer run the same code against an account, and `e2e/target` exercises both
paths.

**The wheel and the image come from the same release.** A consumer bumps both
together and relocks; a workspace binary and a client that disagree about the
contract is the one mismatch a consumer repository exists to notice.
