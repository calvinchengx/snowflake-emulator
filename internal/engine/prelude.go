package engine

// Prelude teaches DuckDB the Snowflake scalar functions this emulator claims.
//
// WHY MACROS RATHER THAN REWRITING. A regex that turns NVL(a,b) into
// COALESCE(a,b) has to find the matching close paren through nested calls,
// string literals and commas inside them, and it gets that wrong on exactly
// the expressions worth having. A macro is parsed by DuckDB, so the argument
// boundaries are DuckDB's problem and the mapping is one line each.
//
// PREPENDED TO EVERY STATEMENT rather than installed once. `SNOWFLAKE_DUCKDB_
// PATH=:memory:` is a documented mode, and every Exec is a separate CLI
// process, so a database installed into at startup would be gone by the next
// call. Measured: the CLI runs the whole script and prints rows only for the
// statement that produced them, so a prelude in front of a CREATE TABLE still
// prints nothing and a Parser Error in the real statement still reaches
// stderr with its own text.
//
// ONLY WHAT REAL SNOWFLAKE HAS. That is the boundary this file is on the
// wrong side of the moment it adds a convenience Snowflake does not offer:
// a consumer that works here and fails on the real thing is worse than one
// that fails in both places, because it fails later and somewhere else.
const Prelude = `
CREATE OR REPLACE MACRO nvl(a, b) AS coalesce(a, b);
CREATE OR REPLACE MACRO nvl2(a, b, c) AS CASE WHEN a IS NOT NULL THEN b ELSE c END;
CREATE OR REPLACE MACRO ifnull(a, b) AS coalesce(a, b);
CREATE OR REPLACE MACRO zeroifnull(a) AS coalesce(a, 0);
CREATE OR REPLACE MACRO nullifzero(a) AS nullif(a, 0);
CREATE OR REPLACE MACRO iff(c, a, b) AS CASE WHEN c THEN a ELSE b END;
CREATE OR REPLACE MACRO to_date(x) AS CAST(x AS DATE);
CREATE OR REPLACE MACRO to_timestamp(x) AS CAST(x AS TIMESTAMP);
CREATE OR REPLACE MACRO to_varchar(x) AS CAST(x AS VARCHAR);
CREATE OR REPLACE MACRO to_char(x) AS CAST(x AS VARCHAR);
CREATE OR REPLACE MACRO to_boolean(x) AS CAST(x AS BOOLEAN);
CREATE OR REPLACE MACRO to_double(x) AS CAST(x AS DOUBLE);
CREATE OR REPLACE MACRO parse_json(x) AS CAST(x AS JSON);
CREATE OR REPLACE MACRO array_size(x) AS len(x);
CREATE OR REPLACE MACRO charindex(needle, haystack) AS position(needle IN haystack);
CREATE OR REPLACE MACRO is_null_value(x) AS x IS NULL;
`

// preludeFor returns the statements to run in front of sql. DESCRIBE carries
// it too: a query is only describable if its functions resolve.
func preludeFor(sql string) string {
	return Prelude + "\n" + sql
}
