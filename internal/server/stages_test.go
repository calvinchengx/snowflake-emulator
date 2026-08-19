package server

import (
	"strings"
	"testing"
)

func TestDefaultFormatIsSnowflakesNotOurs(t *testing.T) {
	// The header row is DATA unless the format says otherwise. This emulator
	// used to hardcode a header skip, so `COPY INTO nums FROM @~/nums.csv` on
	// a file opening with `n` succeeded here and would fail on the real thing
	// with an INTEGER column -- the worst shape of infidelity, because the
	// consumer only finds out in production.
	opts := defaultFormat().duckdbOptions()
	if !strings.Contains(opts, "HEADER false") {
		t.Fatalf("default must not skip a header, got %q", opts)
	}
	if !strings.Contains(opts, "FORMAT CSV") {
		t.Fatalf("Snowflake's default TYPE is CSV, got %q", opts)
	}
}

func TestSkipHeaderOneMeansHeaderTrue(t *testing.T) {
	f, err := parseFormat("TYPE = CSV, SKIP_HEADER = 1", defaultFormat())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.duckdbOptions(), "HEADER true") {
		t.Fatalf("SKIP_HEADER = 1 must skip one row, got %q", f.duckdbOptions())
	}
}

func TestAnOptionWeCannotHonourIsRefused(t *testing.T) {
	// Silently dropping FIELD_DELIMITER reads every line into column one and
	// reports success, which is the class of failure this repository keeps
	// finding. Refusing names the gap instead.
	for _, opts := range []string{
		"TYPE = XML",
		"TYPE = CSV, SKIP_HEADER = 3",
		"TYPE = CSV, RECORD_DELIMITER = '|'",
	} {
		if _, err := parseFormat(opts, defaultFormat()); err == nil {
			t.Errorf("%q was accepted; an option we cannot honour must be refused", opts)
		}
	}
}

func TestOptionsWeDoHonourReachDuckdb(t *testing.T) {
	f, err := parseFormat(
		`TYPE = CSV, SKIP_HEADER = 1, FIELD_DELIMITER = '|', FIELD_OPTIONALLY_ENCLOSED_BY = '"', NULL_IF = ('')`,
		defaultFormat())
	if err != nil {
		t.Fatal(err)
	}
	got := f.duckdbOptions()
	for _, want := range []string{"HEADER true", "DELIMITER '|'", `QUOTE '"'`, "NULLSTR ''"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestJsonAndParquetAreTheirOwnFormats(t *testing.T) {
	for typ, want := range map[string]string{"JSON": "(FORMAT JSON)", "PARQUET": "(FORMAT PARQUET)"} {
		f, err := parseFormat("TYPE = "+typ, defaultFormat())
		if err != nil {
			t.Fatal(err)
		}
		if f.duckdbOptions() != want {
			t.Errorf("%s -> %q, want %q", typ, f.duckdbOptions(), want)
		}
	}
}

func TestExternalStagesAreRefusedByName(t *testing.T) {
	// Serving an s3:// stage out of a local directory would work here and
	// fail wherever the credentials actually matter.
	for _, s := range []string{
		"CREATE STAGE ext URL = 's3://bucket/path'",
		"COPY INTO t FROM 's3://bucket/x.csv'",
		"CREATE STAGE ext URL = 'azure://acct.blob.core.windows.net/c'",
	} {
		if !reExternal.MatchString(s) {
			t.Errorf("%q was not recognised as external", s)
		}
	}
	if reExternal.MatchString("COPY INTO t FROM @~/x.csv") {
		t.Error("an internal stage must not be mistaken for an external one")
	}
}
