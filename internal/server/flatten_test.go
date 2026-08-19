package server

import (
	"strings"
	"testing"
)

func TestFlattenBecomesUnnest(t *testing.T) {
	got, err := rewriteFlatten("SELECT f.value FROM LATERAL FLATTEN(input => t.arr) f")
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT f.value FROM LATERAL (SELECT unnest(json_extract(to_json(t.arr), '$[*]')) AS value, " +
		"unnest(range(len(json_extract(to_json(t.arr), '$[*]')))) AS index) f"
	if got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
}

func TestFlattenKeepsNestedCallsWhole(t *testing.T) {
	// The reason this is a scanner and not a pattern. A regex reaching for
	// the closing paren stops at the first one it meets, which is inside the
	// argument on every expression worth flattening.
	in := "SELECT f.value FROM LATERAL FLATTEN(input => split(coalesce(x, 'a,b'), ',')) f"
	got, err := rewriteFlatten(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "to_json(split(coalesce(x, 'a,b'), ','))") {
		t.Fatalf("the argument did not survive: %q", got)
	}
	if strings.Count(got, "(") != strings.Count(got, ")") {
		t.Fatalf("unbalanced parentheses: %q", got)
	}
}

func TestParenthesesInsideStringLiteralsDoNotCount(t *testing.T) {
	in := "SELECT f.value FROM LATERAL FLATTEN(input => split(x, ')')) f"
	got, err := rewriteFlatten(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "to_json(split(x, ')'))") {
		t.Fatalf("a paren inside a literal ended the argument early: %q", got)
	}
}

func TestTableFlattenLosesItsWrapper(t *testing.T) {
	got, err := rewriteFlatten("SELECT * FROM TABLE(FLATTEN(input => a)) f")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got, "(") != strings.Count(got, ")") {
		t.Fatalf("TABLE( left a stray paren: %q", got)
	}
	if strings.Contains(strings.ToUpper(got), "TABLE(") {
		t.Fatalf("TABLE( was not consumed: %q", got)
	}
}

func TestPositionalInputIsAccepted(t *testing.T) {
	got, err := rewriteFlatten("SELECT * FROM LATERAL FLATTEN(a.b) f")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "to_json(a.b)") {
		t.Fatalf("positional input not read: %q", got)
	}
}

func TestArgumentsThatChangeTheRowsAreRefused(t *testing.T) {
	// OUTER keeps the input row when the array is empty and RECURSIVE
	// descends. Ignoring either returns a different set of rows and says
	// nothing, which is the failure this repository keeps finding.
	for _, in := range []string{
		"SELECT * FROM LATERAL FLATTEN(input => a, outer => true) f",
		"SELECT * FROM LATERAL FLATTEN(input => a, recursive => true) f",
		"SELECT * FROM LATERAL FLATTEN(input => a, path => 'b') f",
		"SELECT * FROM LATERAL FLATTEN(input => a, mode => 'array') f",
	} {
		if _, err := rewriteFlatten(in); err == nil {
			t.Errorf("%q was accepted", in)
		}
	}
}

func TestTwoFlattensInOneStatement(t *testing.T) {
	got, err := rewriteFlatten(
		"SELECT 1 FROM LATERAL FLATTEN(input => a) f, LATERAL FLATTEN(input => b) g")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got, "unnest(") != 4 { // value and index, twice
		t.Fatalf("expected both to be rewritten: %q", got)
	}
}

func TestSqlWithoutFlattenIsUntouched(t *testing.T) {
	in := "SELECT flattened FROM t WHERE note = 'FLATTEN'"
	got, err := rewriteFlatten(in)
	if err != nil {
		t.Fatal(err)
	}
	if got != in {
		t.Fatalf("rewrote %q to %q", in, got)
	}
}

func TestFlattenInsideAStringLiteralIsNotRewritten(t *testing.T) {
	// The same trap DATEDIFF fell into: a literal that happens to contain the
	// call. Rewriting it changes the string and unbalances the quotes.
	in := "SELECT 'FLATTEN(input => a)' AS note FROM t"
	got, err := rewriteFlatten(in)
	if err != nil {
		t.Fatal(err)
	}
	if got != in {
		t.Fatalf("rewrote inside a literal:\n got %q\nwant %q", got, in)
	}
}
