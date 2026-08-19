package server

import (
	"strings"
	"testing"
)

func TestRewriteTransientAndCurrent(t *testing.T) {
	sess := session{Database: "TEST_DB", Schema: "PUBLIC", Warehouse: "wh1"}
	out, _, special, _ := rewriteSQL("create or replace transient table one as select 1 as id", sess)
	if special {
		t.Fatal("not special")
	}
	if out != "CREATE OR REPLACE TABLE one as select 1 as id" {
		t.Fatalf("got %q", out)
	}
	out, extra, special, _ := rewriteSQL("USE WAREHOUSE e2e_wh", sess)
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

func TestAnApostropheInACommentDoesNotStopEveryLaterRewrite(t *testing.T) {
	// THE ONE THAT COST A MODEL. A `--` comment containing "Friday's" opened a
	// string literal as far as the scanner was concerned, so every rewrite
	// after it in the statement silently did not happen -- and DuckDB met
	// `table(generator(...))` raw and reported a syntax error for a feature
	// this emulator has.
	sql := "-- the rate in force on Saturday is Friday's\n" +
		"SELECT DATEDIFF(day, a, b) FROM t"
	got := rewriteDateParts(sql)
	if !strings.Contains(got, "date_diff('day', a, b)") {
		t.Fatalf("the rewrite stopped at the comment:\n%s", got)
	}
	if !strings.Contains(got, "Friday's") {
		t.Fatalf("the comment was altered: %q", got)
	}
}

func TestCommentsAreNotRewrittenEither(t *testing.T) {
	sql := "-- DATEDIFF(day, a, b) is what this does\nSELECT 1"
	if got := rewriteDateParts(sql); got != sql {
		t.Fatalf("rewrote inside a comment:\n%s", got)
	}
	block := "/* DATEDIFF(day, a, b) */ SELECT DATEDIFF(day, a, b)"
	got := rewriteDateParts(block)
	if strings.Count(got, "date_diff") != 1 {
		t.Fatalf("a block comment was rewritten: %q", got)
	}
}

func TestStringLiteralsStillWin(t *testing.T) {
	sql := "SELECT 'DATEDIFF(day, a, b)' AS note, DATEDIFF(day, a, b)"
	got := rewriteDateParts(sql)
	if strings.Count(got, "date_diff") != 1 {
		t.Fatalf("expected exactly the unquoted call to move: %q", got)
	}
	if !strings.Contains(got, "'DATEDIFF(day, a, b)'") {
		t.Fatalf("the literal changed: %q", got)
	}
}

func TestAnUnterminatedLiteralDoesNotEatTheRest(t *testing.T) {
	// Left for the engine to reject, but it must not panic or loop.
	_ = rewriteDateParts("SELECT 'oops, DATEDIFF(day, a, b)")
}
