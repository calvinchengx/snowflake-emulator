package server

import "testing"

func TestTemporalValuesTakeTheClientsWireFormat(t *testing.T) {
	// The three numbers are not chosen: they are what the connector's own
	// converters read back as the value duckdb started with. 20455 days is
	// 2026-01-02; 1767322645 seconds is 2026-01-02 02:57:25; 11045 seconds
	// after midnight is 03:04:05.
	for _, tc := range []struct{ kind, duck, want string }{
		{"date", "2026-01-02", "20455"},
		{"date", "1970-01-01", "0"},
		{"timestamp_ntz", "2026-01-02 02:57:25", "1767322645.000000"},
		{"timestamp_ntz", "2026-01-02 02:57:25.123456", "1767322645.123456"},
		{"time", "03:04:05", "11045.000000"},
		{"time", "03:04:05.5", "11045.500000"},
	} {
		if got := renderCell(tc.kind, tc.duck); got != tc.want {
			t.Errorf("renderCell(%s, %q) = %q, want %q", tc.kind, tc.duck, got, tc.want)
		}
	}
}

func TestADateBeforeTheEpochGoesNegative(t *testing.T) {
	// Truncating toward zero would put 1969-12-31 on the epoch itself, which
	// is a real wrong date rather than a failure.
	if got := renderCell("date", "1969-12-31"); got != "-1" {
		t.Errorf("1969-12-31 rendered %q, want -1", got)
	}
}

func TestAnUnparseableTemporalIsLeftAlone(t *testing.T) {
	// Inventing an epoch for something unrecognised puts a confident wrong
	// date in front of a consumer.
	for _, kind := range []string{"date", "time", "timestamp_ntz"} {
		if got := renderCell(kind, "not a date"); got != "not a date" {
			t.Errorf("renderCell(%s, ...) = %q, want it untouched", kind, got)
		}
	}
}

func TestTemporalColumnsDeclareTheScaleTheyCarry(t *testing.T) {
	// The connector splits seconds from the fraction using the scale in the
	// row type. A fractional value declared scale 0 reads back wrong.
	for _, duck := range []string{"TIMESTAMP", "TIME", "TIMESTAMPTZ"} {
		if _, _, scale := snowflakeType(duck); scale != temporalScale {
			t.Errorf("%s declares scale %d, want %d", duck, scale, temporalScale)
		}
	}
	if _, _, scale := snowflakeType("DATE"); scale != 0 {
		t.Errorf("DATE declares scale %d, want 0: it is a whole number of days", scale)
	}
}
