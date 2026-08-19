package engine

import (
	"strings"
	"testing"
)

// The JSON duckdb actually prints for
//
//	SELECT 1 AS alpha, 2 AS bravo, 3 AS charlie, 4 AS delta
const fourColumns = `[{"alpha":1,"bravo":2,"charlie":3,"delta":4}]`

func TestColumnsKeepTheOrderDuckdbPrinted(t *testing.T) {
	// Not "once". The defect this replaces was Go map iteration, which is
	// randomised PER RANGE -- one pass could easily come out in order by
	// luck. Twelve identical requests against the old build returned four
	// different orders; a test that decoded once would have passed on three
	// runs in four and proved nothing.
	for i := 0; i < 200; i++ {
		cols, rows, err := decodeRows([]byte(fourColumns))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		want := []string{"alpha", "bravo", "charlie", "delta"}
		if strings.Join(cols, ",") != strings.Join(want, ",") {
			t.Fatalf("pass %d: columns %v, want %v", i, cols, want)
		}
		for j, v := range []string{"1", "2", "3", "4"} {
			if got := deref(rows[0][j]); got != v {
				t.Fatalf("pass %d: column %d is %q, want %q -- values must "+
					"travel with their names", i, j, got, v)
			}
		}
	}
}

func TestNullIsNullAndNotTheWordForIt(t *testing.T) {
	// fmt.Sprint(nil) is "<nil>", and that string was the answer to
	// SELECT NULL: a not-null contract would have seen five characters of
	// text where the engine meant nothing at all.
	_, rows, err := decodeRows([]byte(`[{"a":null,"b":1}]`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rows[0][0] != nil {
		t.Fatalf("NULL decoded as %q; a nil *string is what becomes JSON null", *rows[0][0])
	}
	if deref(rows[0][1]) != "1" {
		t.Fatalf("the non-null neighbour is %q", deref(rows[0][1]))
	}
}

func TestBigIntegersKeepEveryDigit(t *testing.T) {
	// json.Unmarshal into `any` makes every number a float64, and a float64
	// cannot hold this one: it went out as 9.223372036854776e+18. Measured
	// through the running emulator, not imagined.
	const max = "9223372036854775807"
	_, rows, err := decodeRows([]byte(`[{"big":` + max + `}]`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := deref(rows[0][0]); got != max {
		t.Fatalf("BIGINT became %q, want %q", got, max)
	}
}

func TestDecimalsKeepTheirScale(t *testing.T) {
	// duckdb prints DECIMAL as a JSON string precisely so the scale survives.
	_, rows, err := decodeRows([]byte(`[{"m":"1.5000"}]`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := deref(rows[0][0]); got != "1.5000" {
		t.Fatalf("DECIMAL became %q, want 1.5000", got)
	}
}

func TestInferTypesReadsTheWholeColumn(t *testing.T) {
	// A column whose first row is NULL says nothing about the rest, and one
	// that opens with an integer can still hold a fraction further down.
	// Reading row zero only is how a DOUBLE column gets called an integer.
	cases := []struct {
		name string
		vals []*string
		want string
	}{
		{"integers", ptrs("1", "2", "3"), "BIGINT"},
		{"widens to double", ptrs("1", "2.5"), "DOUBLE"},
		{"stays double", ptrs("2.5", "1"), "DOUBLE"},
		{"leading null", []*string{nil, str("7")}, "BIGINT"},
		{"all null", []*string{nil, nil}, "VARCHAR"},
		{"booleans", ptrs("true", "false"), "BOOLEAN"},
		{"text", ptrs("hi", "there"), "VARCHAR"},
		{"mixed falls back", ptrs("1", "hi"), "VARCHAR"},
		{"dates are text", ptrs("2026-01-01"), "VARCHAR"},
		{"exponent", ptrs("1e5"), "DOUBLE"},
		{"negative", ptrs("-3"), "BIGINT"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows := make([][]*string, len(c.vals))
			for i, v := range c.vals {
				rows[i] = []*string{v}
			}
			got := inferTypes([]string{"c"}, rows)
			if got[0] != c.want {
				t.Fatalf("inferred %s, want %s", got[0], c.want)
			}
		})
	}
}

func TestOnlyDescribableStatementsAreDescribed(t *testing.T) {
	// DESCRIBE is what separates DECIMAL(19,4) from a VARCHAR holding
	// "1.5000". It is also invalid in front of most statements, so asking
	// for it indiscriminately would spend a duckdb round trip per call to
	// be told so.
	for _, s := range []string{"SELECT 1", "  select 1", "WITH t AS (SELECT 1) SELECT * FROM t", "(SELECT 1)"} {
		if !describable(s) {
			t.Fatalf("%q should be describable", s)
		}
	}
	for _, s := range []string{"SHOW TABLES", "DESCRIBE t", "CREATE TABLE t (a INT)", "INSERT INTO t VALUES (1)", ""} {
		if describable(s) {
			t.Fatalf("%q should not be described", s)
		}
	}
}

func TestGarbageJSONIsHandedBackNotSwallowed(t *testing.T) {
	if _, _, err := decodeRows([]byte("not json")); err == nil {
		t.Fatal("decodeRows accepted something that is not a JSON array")
	}
}

func deref(s *string) string {
	if s == nil {
		return "<null>"
	}
	return *s
}

func str(s string) *string { return &s }

func ptrs(ss ...string) []*string {
	out := make([]*string, len(ss))
	for i := range ss {
		v := ss[i]
		out[i] = &v
	}
	return out
}

func TestASqlErrorIsAnErrorEvenWhenDuckdbExitsZero(t *testing.T) {
	// The whole defect. duckdb v1.2.2 -- the version this image pins -- exits
	// 0 after refusing a statement, so the exit code says the run was fine
	// while stderr says it was not. Believing the code turned every
	// unsupported statement into `status: ok`: CREATE TASK, time travel and
	// TO_DATE all reported success in the shipped container.
	err := failed([]byte(`Parser Error: syntax error at or near "THIS"`), nil)
	if err == nil {
		t.Fatal("a Parser Error on stderr with exit 0 must still be an error")
	}
	if !strings.Contains(err.Error(), "Parser Error") {
		t.Fatalf("the diagnosis must reach the caller, got %q", err)
	}
}

func TestSilenceIsSuccessEvenWhenNothingCameBack(t *testing.T) {
	// The other half, and the reason an exit code cannot do this job: DDL and
	// a SELECT matching no rows BOTH write empty stdout and empty stderr.
	// Treating "no output" as failure would fail every CREATE TABLE.
	if err := failed(nil, nil); err != nil {
		t.Fatalf("empty stderr is a statement that worked, got %v", err)
	}
	if err := failed([]byte("   \n"), nil); err != nil {
		t.Fatalf("whitespace on stderr is not a diagnosis, got %v", err)
	}
}

func TestAProcessFailureStillSurfacesWhenStderrIsEmpty(t *testing.T) {
	// Binary missing, killed, out of memory: no SQL diagnosis, but not a
	// success either.
	err := failed(nil, errTest)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("a process failure must surface, got %v", err)
	}
}

var errTest = errBoom{}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }
