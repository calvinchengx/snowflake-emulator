package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/calvinchengx/snowflake-emulator/internal/config"
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

// A stage name that resolves outside the stage directory must be refused, and
// the case that matters is the one the grammar does not obviously admit.
//
// `[A-Za-z0-9_$."]+` contains no `/`, so `../../etc` truncates at the slash and
// looks harmless. `..` on its own matches whole, and `strings.ToUpper` cannot
// neutralise a name with no letters in it. The sink for DROP is os.RemoveAll,
// so the consequence is a recursive delete of the stage directory's parent.
func TestAStageNameCannotEscapeTheStageDirectory(t *testing.T) {
	root := t.TempDir()
	stages := filepath.Join(root, "stages")
	if err := os.MkdirAll(stages, 0o755); err != nil {
		t.Fatal(err)
	}
	s := &Server{Cfg: config.Config{StageDir: stages}}

	for _, name := range []string{"..", `".."`, "../../etc", "..\\..", "$.."} {
		// Through the same trim/upper the handlers apply, so the test drives
		// what the code actually sees rather than an idealised input.
		clean := strings.Trim(strings.ToUpper(name), `"`)
		got, err := s.stagePath(clean)
		if err != nil {
			continue // refused outright, which is one acceptable outcome
		}
		// The other is a path strictly INSIDE the stage directory. Note the
		// absence of an `|| got == stages` escape hatch: resolving to the root
		// is not containment, it is the worst case. DROP's sink is
		// os.RemoveAll, so a name landing on the root deletes every stage.
		// `$..` and, on Unix, `..\..` are ordinary filenames and land here.
		if !strings.HasPrefix(got, stages+string(filepath.Separator)) {
			t.Errorf("stagePath(%q) = %q, which is not inside %q", name, got, stages)
		}
	}

	// And the ordinary case must still work, or the refusal proves nothing.
	ok, err := s.stagePath("MYSTAGE")
	if err != nil {
		t.Fatalf("a normal stage name was refused: %v", err)
	}
	if ok != filepath.Join(stages, "MYSTAGE") {
		t.Fatalf("stagePath(MYSTAGE) = %q", ok)
	}
	// An empty name resolves to the stage directory itself, which for DROP
	// means os.RemoveAll over every stage. It is refused. The user stage is
	// reached through stageDir's explicit `~` case, not by this falling
	// through to the root.
	if got, err := s.stagePath(""); err == nil {
		t.Fatalf("stagePath(\"\") = %q, want a refusal", got)
	}
}

// The regex admits `..`, which is why the containment check exists rather than
// the grammar being trusted to prevent it. If this ever fails because the
// grammar tightened, stagePath is still correct -- but the reason for it has
// changed and the comment above it should say so.
func TestTheStageGrammarStillAdmitsDotDot(t *testing.T) {
	m := reDropStage.FindStringSubmatch("DROP STAGE ..")
	if m == nil {
		t.Skip("the grammar no longer admits `..`; stagePath is now belt and braces")
	}
	if got := strings.Trim(strings.ToUpper(m[1]), `"`); got != ".." {
		t.Fatalf("captured %q, want `..`", got)
	}
}
