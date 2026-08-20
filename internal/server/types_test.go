package server

import (
	"strings"
	"testing"
)

func TestSnowflakeScalarTypesReachDuckdb(t *testing.T) {
	// Each of these is REJECTED by the pinned duckdb as it stands -- measured,
	// not assumed -- so each one left unmapped is a CREATE TABLE a consumer
	// writes from INFER_SCHEMA's own answer and this emulator refuses.
	for _, tc := range []struct{ in, want string }{
		{"CREATE TABLE t (a NUMBER(38,0))", "DECIMAL(38,0)"},
		{"CREATE TABLE t (a NUMBER(19,4))", "DECIMAL(19,4)"},
		{"CREATE TABLE t (a NUMBER(10))", "DECIMAL(10)"},
		{"CREATE TABLE t (a NUMBER)", "DECIMAL(38,0)"},
		{"CREATE TABLE t (a TIMESTAMP_NTZ)", "TIMESTAMP"},
		{"CREATE TABLE t (a TIMESTAMP_NTZ(9))", "TIMESTAMP(9)"},
		{"CREATE TABLE t (a TIMESTAMP_LTZ)", "TIMESTAMPTZ"},
		{"CREATE TABLE t (a TIMESTAMP_TZ(6))", "TIMESTAMPTZ"},
	} {
		if got := rewriteSnowflakeTypes(tc.in); !strings.Contains(got, tc.want) {
			t.Errorf("%s -> %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestBareNumberIsThirtyEightZeroNotDuckdbsDefault(t *testing.T) {
	// Snowflake's bare NUMBER is NUMBER(38,0); duckdb's bare DECIMAL is
	// (18,3). Leaving it bare would silently give an id column three decimal
	// places and a smaller range than the consumer asked for.
	got := rewriteSnowflakeTypes("CREATE TABLE t (id NUMBER)")
	if strings.Contains(got, "DECIMAL)") || !strings.Contains(got, "DECIMAL(38,0)") {
		t.Fatalf("bare NUMBER must become DECIMAL(38,0), got %s", got)
	}
}

func TestTypesAreRewrittenInDDLOnly(t *testing.T) {
	// `number` is an ordinary word. A SELECT that mentions it, or a string
	// containing it, must be left exactly alone.
	for _, sql := range []string{
		"SELECT number FROM t",
		"SELECT 'a NUMBER(38,0) column' AS note",
	} {
		if got := rewriteSnowflakeTypes(sql); got != sql {
			t.Errorf("non-DDL was rewritten: %s -> %s", sql, got)
		}
	}
	// Inside DDL, a literal is still a literal.
	const ddl = "CREATE TABLE t (a NUMBER, note VARCHAR DEFAULT 'NUMBER(1)')"
	got := rewriteSnowflakeTypes(ddl)
	if !strings.Contains(got, "'NUMBER(1)'") {
		t.Errorf("a string literal was rewritten: %s", got)
	}
	if !strings.Contains(got, "a DECIMAL(38,0)") {
		t.Errorf("the column type was not rewritten: %s", got)
	}
}
