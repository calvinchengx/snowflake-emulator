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
