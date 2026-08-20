package server

import "testing"

func TestABooleanIsRenderedAsTheConnectorReadsIt(t *testing.T) {
	// snowflake-connector-python: `lambda value: value in ("1", "TRUE")`.
	// duckdb writes `true`, which is neither, so every boolean -- including a
	// literal TRUE -- came back False. Measured through the connector before
	// this existed, not deduced.
	for _, tc := range []struct{ duck, want string }{
		{"true", "TRUE"},
		{"false", "FALSE"},
		{"TRUE", "TRUE"},
		{"1", "TRUE"},
		{"0", "FALSE"},
	} {
		if got := renderCell("boolean", tc.duck); got != tc.want {
			t.Errorf("renderCell(boolean, %q) = %q, want %q", tc.duck, got, tc.want)
		}
	}
}

func TestOnlyBooleansAreRewritten(t *testing.T) {
	// A text column holding the word "true" is a string and must stay one.
	// Rewriting by VALUE rather than by TYPE would corrupt real data, which is
	// a worse defect than the one being fixed.
	for _, kind := range []any{"text", "fixed", "real", "timestamp_ntz", nil} {
		if got := renderCell(kind, "true"); got != "true" {
			t.Errorf("renderCell(%v, \"true\") = %q, want it untouched", kind, got)
		}
	}
}

func TestAValueWeCannotParseIsLeftAlone(t *testing.T) {
	// Inventing an answer for something unrecognised is how the original
	// defect reads: confident and wrong.
	if got := renderCell("boolean", "maybe"); got != "maybe" {
		t.Errorf("renderCell(boolean, \"maybe\") = %q, want it untouched", got)
	}
}
