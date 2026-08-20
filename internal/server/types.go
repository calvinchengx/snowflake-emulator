package server

import (
	"regexp"
	"strings"
)

// Snowflake's SCALAR type names in DDL, which is the other half of what
// `rewriteVariantTypes` does for the semi-structured ones.
//
// WHY IT SURFACED NOW. INFER_SCHEMA reports the types an ACCOUNT would report
// -- NUMBER(38,0), TIMESTAMP_NTZ -- because reporting duckdb's spelling would
// hand a consumer a CREATE TABLE that only works here. But the emulator then
// refused its own answer: `CREATE TABLE t (a NUMBER(38,0))` came back as
// `Catalog Error: Type with name NUMBER does not exist!`, so the loop the
// feature exists for -- infer, create, load -- did not close. Measured against
// the pinned build, not assumed: NUMBER, TIMESTAMP_NTZ, TIMESTAMP_LTZ and
// TIMESTAMP_TZ are rejected, while NUMERIC, DECIMAL, STRING, TEXT, BINARY,
// FLOAT, BOOLEAN, DATE, TIME and TIMESTAMP are all accepted as they stand and
// are therefore left alone.
//
// DDL ONLY, and outside string literals -- a column called `number` or a row
// containing the word is not a type.

var (
	// NUMBER is Snowflake's canonical numeric type and the one INFER_SCHEMA
	// names. Bare NUMBER means NUMBER(38,0) there, so the default is written
	// out rather than left to duckdb's DECIMAL, whose bare form is (18,3).
	reNumberType = regexp.MustCompile(`(?i)\bNUMBER\b(\s*\(\s*\d+\s*(?:,\s*\d+\s*)?\))?`)

	// The three timestamp flavours. DuckDB has TIMESTAMP (no zone) and
	// TIMESTAMPTZ, which is the same distinction Snowflake draws between NTZ
	// and LTZ/TZ. The precision, if given, is carried across.
	reTimestampNTZ = regexp.MustCompile(`(?i)\bTIMESTAMP_NTZ\b(\s*\(\s*\d+\s*\))?`)
	reTimestampTZ  = regexp.MustCompile(`(?i)\bTIMESTAMP_(?:LTZ|TZ)\b(\s*\(\s*\d+\s*\))?`)
)

// rewriteSnowflakeTypes maps the scalar type names duckdb does not know.
func rewriteSnowflakeTypes(sql string) string {
	upper := strings.ToUpper(strings.TrimSpace(sql))
	if !strings.HasPrefix(upper, "CREATE") && !strings.HasPrefix(upper, "ALTER") {
		return sql
	}
	return outsideLiterals(sql, func(s string) string {
		s = reNumberType.ReplaceAllStringFunc(s, func(m string) string {
			if i := strings.IndexByte(m, '('); i >= 0 {
				return "DECIMAL" + m[i:]
			}
			return "DECIMAL(38,0)"
		})
		s = reTimestampNTZ.ReplaceAllString(s, "TIMESTAMP$1")
		// TIMESTAMPTZ takes no precision in duckdb, so a precision given here
		// is dropped rather than passed through as a syntax error.
		s = reTimestampTZ.ReplaceAllString(s, "TIMESTAMPTZ")
		return s
	})
}
