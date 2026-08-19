package server

import "testing"

func TestRewriteTransientAndCurrent(t *testing.T) {
	sess := session{Database: "TEST_DB", Schema: "PUBLIC", Warehouse: "wh1"}
	out, _, special := rewriteSQL("create or replace transient table one as select 1 as id", sess)
	if special {
		t.Fatal("not special")
	}
	if out != "CREATE OR REPLACE TABLE one as select 1 as id" {
		t.Fatalf("got %q", out)
	}
	out, extra, special := rewriteSQL("USE WAREHOUSE e2e_wh", sess)
	if !special || extra != "use_warehouse" || out != "e2e_wh" {
		t.Fatalf("use warehouse: %q %q %v", out, extra, special)
	}
}

func TestExtractSQLNested(t *testing.T) {
	s := extractSQL([]byte(`{"data":{"sqlText":"SELECT 1"}}`))
	if s != "SELECT 1" {
		t.Fatalf("got %q", s)
	}
}

func TestDatediffPartBecomesAString(t *testing.T) {
	// Snowflake takes the part as a bare keyword; DuckDB reads that as a
	// column and answers `Binder Error: Referenced column "day" not found`.
	cases := map[string]string{
		"SELECT DATEDIFF(day, a, b)":    "SELECT date_diff('day', a, b)",
		"select datediff(month, a, b)":  "select date_diff('month', a, b)",
		"SELECT DATEDIFF( year , a, b)": "SELECT date_diff('year', a, b)",
		"SELECT DATEDIFF(day,a,b)":      "SELECT date_diff('day',a,b)",
	}
	for in, want := range cases {
		if got := rewriteDateParts(in); got != want {
			t.Errorf("%q ->\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestDatediffLeavesTheOtherArgumentsAlone(t *testing.T) {
	// Only the first argument is touched. Nested calls and commas inside
	// string literals in arguments two and three are DuckDB's to parse --
	// which is the whole reason the rest of the shim is macros rather than
	// regexes.
	in := "SELECT DATEDIFF(day, TO_DATE(x), coalesce(y, TO_DATE('2026-01-01')))"
	want := "SELECT date_diff('day', TO_DATE(x), coalesce(y, TO_DATE('2026-01-01')))"
	if got := rewriteDateParts(in); got != want {
		t.Errorf("\n got %q\nwant %q", got, want)
	}
}

func TestNothingElseNamedDatediffIsTouched(t *testing.T) {
	for _, in := range []string{
		"SELECT my_datediff_helper(day, a, b)",
		"SELECT 'DATEDIFF(day, a, b)' AS literal_text",
	} {
		if got := rewriteDateParts(in); got != in {
			t.Errorf("%q was rewritten to %q", in, got)
		}
	}
}
