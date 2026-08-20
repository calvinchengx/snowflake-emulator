package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/calvinchengx/snowflake-emulator/internal/config"
)

// inferServer is a stage on disk with one named CSV format registered.
func inferServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	s := &Server{Cfg: config.Config{StageDir: dir}}
	f, err := parseFormat("TYPE = CSV, SKIP_HEADER = 1", defaultFormat())
	if err != nil {
		t.Fatal(err)
	}
	s.formats = map[string]fileFormat{"MY_CSV": f}
	return s, dir
}

func writeStage(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInferSchemaIsARelationNotOneBlessedSentence(t *testing.T) {
	// The reason this is a rewrite and not a canned answer: the first caller
	// to add a WHERE would otherwise get a parser error for a query real
	// Snowflake answers.
	s, dir := inferServer(t)
	writeStage(t, dir, "feed/a.csv", "id,amt\n1,2.5\n")

	sql := `SELECT * FROM TABLE(INFER_SCHEMA(LOCATION => '@~/feed/', FILE_FORMAT => 'MY_CSV'))
	         WHERE "COLUMN_NAME" = 'amt' ORDER BY "ORDER_ID"`
	got, err := s.expandInferSchema(sql)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToUpper(got), "INFER_SCHEMA") {
		t.Fatalf("the reference survived the rewrite: %s", got)
	}
	for _, want := range []string{`WHERE "COLUMN_NAME" = 'amt'`, `ORDER BY "ORDER_ID"`} {
		if !strings.Contains(got, want) {
			t.Errorf("the caller's own %q was lost: %s", want, got)
		}
	}
}

func TestInferSchemaUnionsByNameOrItSilentlyDropsColumns(t *testing.T) {
	// MEASURED on the pinned duckdb: without union_by_name a column that only
	// the second file has is simply absent from the result, with no error.
	// That is this repository's recurring defect -- success reported, work not
	// done -- so the flag is asserted rather than trusted.
	s, dir := inferServer(t)
	writeStage(t, dir, "feed/a.csv", "id\n1\n")
	writeStage(t, dir, "feed/b.csv", "id,extra\n2,x\n")

	got, err := s.expandInferSchema(
		`SELECT * FROM TABLE(INFER_SCHEMA(LOCATION => '@~/feed/', FILE_FORMAT => 'MY_CSV'))`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "union_by_name=true") {
		t.Fatalf("union_by_name is load-bearing and missing: %s", got)
	}
}

func TestInferSchemaReadsEveryFileUnderThePrefix(t *testing.T) {
	// A prefix is the ordinary form here, unlike COPY INTO which resolves one
	// name: INFER_SCHEMA exists to describe a feed, and a feed is a directory.
	s, dir := inferServer(t)
	writeStage(t, dir, "feed/part-0.csv", "id\n1\n")
	writeStage(t, dir, "feed/nested/part-1.csv", "id\n2\n")

	got, err := s.expandInferSchema(
		`SELECT * FROM TABLE(INFER_SCHEMA(LOCATION => '@~/feed/', FILE_FORMAT => 'MY_CSV'))`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"part-0.csv", "part-1.csv"} {
		if !strings.Contains(got, want) {
			t.Errorf("%s is under the prefix and was not read: %s", want, got)
		}
	}
}

func TestFilenamesArePerColumnNotTheWholeList(t *testing.T) {
	// Returning every file for every column is right only when the files agree
	// and wrong exactly when the caller most needs to know.
	s, dir := inferServer(t)
	writeStage(t, dir, "feed/a.csv", "id\n1\n")
	writeStage(t, dir, "feed/b.csv", "id,extra\n2,x\n")

	got, err := s.expandInferSchema(
		`SELECT * FROM TABLE(INFER_SCHEMA(LOCATION => '@~/feed/', FILE_FORMAT => 'MY_CSV'))`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "WHERE per.column_name = u.column_name") {
		t.Fatalf("FILENAMES is not attributed per column: %s", got)
	}
}

func TestInferSchemaRefusesWhatItCannotHonour(t *testing.T) {
	s, dir := inferServer(t)
	writeStage(t, dir, "feed/a.csv", "id\n1\n")

	for name, sql := range map[string]string{
		"no LOCATION":    `SELECT * FROM TABLE(INFER_SCHEMA(FILE_FORMAT => 'MY_CSV'))`,
		"no FILE_FORMAT": `SELECT * FROM TABLE(INFER_SCHEMA(LOCATION => '@~/feed/'))`,
		"an unknown argument": `SELECT * FROM TABLE(INFER_SCHEMA(LOCATION => '@~/feed/',
		    FILE_FORMAT => 'MY_CSV', MAX_RECORDS_PER_FILE => 10))`,
		"a format nobody created": `SELECT * FROM TABLE(INFER_SCHEMA(LOCATION => '@~/feed/',
		    FILE_FORMAT => 'NOPE'))`,
		"a prefix that matches nothing": `SELECT * FROM TABLE(INFER_SCHEMA(LOCATION => '@~/absent/',
		    FILE_FORMAT => 'MY_CSV'))`,
		"an external stage": `SELECT * FROM TABLE(INFER_SCHEMA(LOCATION => 's3://b/k',
		    FILE_FORMAT => 'MY_CSV'))`,
		"a location that is not a stage": `SELECT * FROM TABLE(INFER_SCHEMA(LOCATION => 'feed/',
		    FILE_FORMAT => 'MY_CSV'))`,
	} {
		if _, err := s.expandInferSchema(sql); err == nil {
			t.Errorf("%s was accepted; it must be refused by name", name)
		}
	}
}

func TestStatementsWithoutInferSchemaAreUntouched(t *testing.T) {
	s, _ := inferServer(t)
	const sql = `SELECT 1`
	got, err := s.expandInferSchema(sql)
	if err != nil {
		t.Fatal(err)
	}
	if got != sql {
		t.Fatalf("an unrelated statement was rewritten: %s", got)
	}
}

func TestJSONAndParquetUseTheirOwnReaders(t *testing.T) {
	// A JSON feed read as CSV yields one text column per line and looks like
	// an inference, which is worse than a refusal.
	for _, tc := range []struct{ typ, want string }{
		{"JSON", "read_json("},
		{"PARQUET", "read_parquet("},
	} {
		f, err := parseFormat("TYPE = "+tc.typ, defaultFormat())
		if err != nil {
			t.Fatal(err)
		}
		if got := f.readerCall([]string{"/x/a"}); !strings.Contains(got, tc.want) {
			t.Errorf("TYPE = %s must use %s, got %q", tc.typ, tc.want, got)
		}
	}
}
