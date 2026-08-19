package server

import (
	"strings"
	"testing"
)

func TestGeneratorBecomesARange(t *testing.T) {
	got := rewriteGenerator("SELECT seq4() FROM table(generator(rowcount => 20000))")
	if !strings.Contains(got, "unnest(range(20000))::INTEGER AS seq4_col") {
		t.Fatalf("generator: %q", got)
	}
	if strings.Contains(strings.ToUpper(got), "SEQ4()") {
		t.Fatalf("seq4() was not substituted: %q", got)
	}
}

func TestGeneratorYieldsIntegerNotBigint(t *testing.T) {
	// Load-bearing. `DATE + BIGINT` is an error in DuckDB while
	// `DATE + INTEGER` is a DATE, and the generator exists here precisely so a
	// caller can offset a date by the row number -- which is what core's
	// silver does to build its calendar.
	got := rewriteGenerator("SELECT seq4() FROM table(generator(rowcount => 5))")
	if !strings.Contains(got, "::INTEGER") {
		t.Fatalf("the generated column must be INTEGER: %q", got)
	}
}

func TestSqlWithoutAGeneratorIsUntouched(t *testing.T) {
	in := "SELECT * FROM t WHERE note = 'generator'"
	if got := rewriteGenerator(in); got != in {
		t.Fatalf("rewrote %q to %q", in, got)
	}
}

func TestDateaddKeepsTheTypeSnowflakeReturns(t *testing.T) {
	for in, want := range map[string]string{
		"SELECT DATEADD(day, 1, d)":    "date_add_days(",
		"SELECT DATEADD(week, 1, d)":   "date_add_weeks(",
		"SELECT DATEADD(hour, 1, d)":   "date_add_hours(",
		"SELECT DATEADD(minute, 1, d)": "date_add_minutes(",
		"SELECT DATEADD(second, 1, d)": "date_add_seconds(",
	} {
		got, err := rewriteDateAdd(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if !strings.Contains(got, want) {
			t.Errorf("%q -> %q, wanted %s", in, got, want)
		}
	}
}

func TestDateaddRefusesThePartsThatWouldChangeTheType(t *testing.T) {
	// MONTH, QUARTER and YEAR widen a DATE to a TIMESTAMP in every DuckDB
	// spelling, and a CASE cannot return two types. Answering with the wrong
	// type is the defect this repository spent the day removing; refusing
	// names it instead.
	for _, in := range []string{
		"SELECT DATEADD(month, 1, d)",
		"SELECT DATEADD(quarter, 1, d)",
		"SELECT DATEADD(year, 1, d)",
	} {
		if _, err := rewriteDateAdd(in); err == nil {
			t.Errorf("%q was accepted", in)
		}
	}
}

func TestDateaddInsideALiteralIsNotRewritten(t *testing.T) {
	in := "SELECT 'DATEADD(month, 1, d)' AS note"
	got, err := rewriteDateAdd(in)
	if err != nil {
		t.Fatalf("a literal was read as a call: %v", err)
	}
	if got != in {
		t.Fatalf("rewrote inside a literal: %q", got)
	}
}
