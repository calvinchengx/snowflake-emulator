package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/calvinchengx/snowflake-emulator/internal/config"
)

// Streams and tasks are the two stateful surfaces this emulator offers, and
// both were thin: handleTaskSQL 36.2%, streamHasData 0%.
//
// THEY NEED A FILE-BACKED DUCKDB, which is why they were hard to reach. With
// `:memory:` every connection gets its OWN database, so a table created by one
// statement does not exist for the next -- the probe that found this saw
// CREATE TABLE succeed and the very next INSERT report "Table with name t does
// not exist". Nothing stateful can be asserted on the in-memory server, and the
// existing helper uses it.
func newStatefulServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	srv, err := New(config.Config{
		DataDir:  dir,
		StageDir: filepath.Join(dir, "stages"),
		DuckDB:   filepath.Join(dir, "db.duckdb"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, login(t, srv)
}

func mustSQL(t *testing.T, srv *Server, tok, sql string) string {
	t.Helper()
	_, body := execSQL(t, srv, tok, sql)
	if strings.Contains(body, `"success":false`) {
		t.Fatalf("%s failed: %s", sql, body)
	}
	return body
}

// TestStreamTracksChangeOnItsTable is the contract a stream exists for: it
// reports whether its table has changed since the stream was read.
//
// SYSTEM$STREAM_HAS_DATA is what dbt's incremental materialisation branches on,
// so a stream that always answered FALSE would make every incremental run a
// no-op -- reporting success while processing nothing, which is the failure
// mode this whole emulator is built to expose rather than reproduce.
func TestStreamTracksChangeOnItsTable(t *testing.T) {
	srv, tok := newStatefulServer(t)
	mustSQL(t, srv, tok, "CREATE TABLE t (id INT)")
	mustSQL(t, srv, tok, "CREATE STREAM st ON TABLE t")

	if body := mustSQL(t, srv, tok, "SELECT SYSTEM$STREAM_HAS_DATA('st')"); !strings.Contains(body, "FALSE") {
		t.Errorf("a fresh stream on an unchanged table reports %s, want FALSE", body)
	}
	mustSQL(t, srv, tok, "INSERT INTO t VALUES (1)")
	if body := mustSQL(t, srv, tok, "SELECT SYSTEM$STREAM_HAS_DATA('st')"); !strings.Contains(body, "TRUE") {
		t.Errorf("after an INSERT the stream reports %s, want TRUE", body)
	}
}

func TestStreamLifecycle(t *testing.T) {
	srv, tok := newStatefulServer(t)
	mustSQL(t, srv, tok, "CREATE TABLE t (id INT)")
	mustSQL(t, srv, tok, "CREATE STREAM st ON TABLE t")
	// SHOW_INITIAL_ROWS is a distinct mode: the stream starts holding the
	// table's existing rows rather than only what arrives after it.
	mustSQL(t, srv, tok, "CREATE STREAM st2 ON TABLE t SHOW_INITIAL_ROWS = TRUE")

	body := mustSQL(t, srv, tok, "SHOW STREAMS")
	for _, want := range []string{"st", "st2", "t"} {
		if !strings.Contains(body, `"`+want+`"`) {
			t.Errorf("SHOW STREAMS omitted %q: %s", want, body)
		}
	}
	mustSQL(t, srv, tok, "DROP STREAM st2")
	if body := mustSQL(t, srv, tok, "SHOW STREAMS"); strings.Contains(body, `"st2"`) {
		t.Errorf("a dropped stream is still listed: %s", body)
	}
	// IF EXISTS on an absent stream is a no-op, not a failure — a teardown
	// script runs it whether or not the stream was ever created.
	mustSQL(t, srv, tok, "DROP STREAM IF EXISTS nosuch")
}

// TestTaskLifecycleAndExecution covers handleTaskSQL's five verbs and the
// execution path underneath them.
//
// A task is CREATED SUSPENDED in Snowflake, and that is not a detail: a task
// that began running on creation would fire on a schedule nobody asked for.
func TestTaskLifecycleAndExecution(t *testing.T) {
	srv, tok := newStatefulServer(t)
	mustSQL(t, srv, tok, "CREATE TABLE t (id INT)")
	mustSQL(t, srv, tok, "CREATE TASK tk WAREHOUSE = 'wh' SCHEDULE = '1 MINUTE' AS INSERT INTO t VALUES (2)")

	body := mustSQL(t, srv, tok, "SHOW TASKS")
	if !strings.Contains(body, "suspended") {
		t.Errorf("a newly created task is not suspended: %s", body)
	}
	for _, want := range []string{"tk", "wh", "1 MINUTE"} {
		if !strings.Contains(body, want) {
			t.Errorf("SHOW TASKS omitted %q: %s", want, body)
		}
	}

	mustSQL(t, srv, tok, "ALTER TASK tk RESUME")
	// EXECUTE TASK runs the body NOW, regardless of schedule. If it did not,
	// the row below would be absent and the task would look like it ran.
	mustSQL(t, srv, tok, "EXECUTE TASK tk")
	if body := mustSQL(t, srv, tok, "SELECT id FROM t"); !strings.Contains(body, "2") {
		t.Errorf("EXECUTE TASK reported success without running its body: %s", body)
	}

	mustSQL(t, srv, tok, "ALTER TASK tk SUSPEND")
	mustSQL(t, srv, tok, "DROP TASK tk")
	if body := mustSQL(t, srv, tok, "SHOW TASKS"); strings.Contains(body, `"tk"`) {
		t.Errorf("a dropped task is still listed: %s", body)
	}
}

// TestTaskGraphRunsChildrenAfterTheirParent covers runTaskGraph and runOrder:
// AFTER makes a DAG, and the order is the whole point. A child that ran first
// would read the table before its parent wrote it — and still report success.
func TestTaskGraphRunsChildrenAfterTheirParent(t *testing.T) {
	srv, tok := newStatefulServer(t)
	mustSQL(t, srv, tok, "CREATE TABLE t (id INT)")
	mustSQL(t, srv, tok, "CREATE TASK root WAREHOUSE = 'wh' SCHEDULE = '1 MINUTE' AS INSERT INTO t VALUES (1)")
	mustSQL(t, srv, tok, "CREATE TASK child AFTER root AS INSERT INTO t SELECT COUNT(*) + 100 FROM t")
	mustSQL(t, srv, tok, "ALTER TASK root RESUME")
	mustSQL(t, srv, tok, "ALTER TASK child RESUME")
	mustSQL(t, srv, tok, "EXECUTE TASK root")

	// The child counts what the root inserted, so its row proves the ORDER,
	// not merely that both ran: a child running first would have counted 0.
	body := mustSQL(t, srv, tok, "SELECT id FROM t ORDER BY id")
	if !strings.Contains(body, "101") {
		t.Errorf("the child did not observe its parent's row; rows = %s", body)
	}
}

func TestTaskSQLRefusals(t *testing.T) {
	srv, tok := newStatefulServer(t)
	// A task whose body cannot run must fail loudly. Snowflake reports the
	// task's own failure, and a silent success here would leave a scheduled
	// task reporting green while writing nothing.
	mustSQL(t, srv, tok, "CREATE TASK bad WAREHOUSE = 'wh' SCHEDULE = '1 MINUTE' AS INSERT INTO nosuchtable VALUES (1)")
	mustSQL(t, srv, tok, "ALTER TASK bad RESUME")
	if _, body := execSQL(t, srv, tok, "EXECUTE TASK bad"); !strings.Contains(body, `"success":false`) {
		t.Errorf("a task whose body fails reported success: %s", body)
	}
	// IF EXISTS forms are teardown-safe.
	mustSQL(t, srv, tok, "DROP TASK IF EXISTS nosuch")
}

// --- the catalog SHOWs -------------------------------------------------------
//
// handleCatalogSQL was 16.0% covered. dbt issues these BEFORE anything else --
// it introspects the warehouse to decide what to build -- so an arm that
// answers wrongly does not fail loudly, it makes dbt plan against a catalog
// that is not there. SHOW OBJECTS in particular has to reflect real tables,
// which needs the stateful server.

func TestCatalogShowsReflectRealObjects(t *testing.T) {
	srv, tok := newStatefulServer(t)
	mustSQL(t, srv, tok, "CREATE TABLE customers (id INT, name TEXT)")
	mustSQL(t, srv, tok, "CREATE TABLE orders (id INT)")

	t.Run("schemas", func(t *testing.T) {
		body := mustSQL(t, srv, tok, "SHOW SCHEMAS")
		// PUBLIC is Snowflake's default and the one dbt qualifies against;
		// INFORMATION_SCHEMA is what it introspects through.
		for _, want := range []string{"PUBLIC", "INFORMATION_SCHEMA"} {
			if !strings.Contains(body, want) {
				t.Errorf("SHOW SCHEMAS omitted %s: %s", want, body)
			}
		}
	})

	// UPPERCASE IS THE CONTRACT, not an accident of this assertion. Snowflake
	// normalises an unquoted identifier to upper case, so a client that created
	// `customers` finds `CUSTOMERS` in the catalog and must match on that.
	// Answering the lower-case form would make dbt's existence check miss the
	// table it just created and plan to create it again.
	t.Run("objects lists the tables that exist, upper-cased", func(t *testing.T) {
		body := mustSQL(t, srv, tok, "SHOW OBJECTS")
		for _, want := range []string{"CUSTOMERS", "ORDERS"} {
			if !strings.Contains(body, `"`+want+`"`) {
				t.Errorf("SHOW OBJECTS omitted %q, so dbt would plan to create a "+
					"table that already exists: %s", want, body)
			}
		}
	})

	t.Run("tables is the same verb", func(t *testing.T) {
		if body := mustSQL(t, srv, tok, "SHOW TABLES"); !strings.Contains(body, `"CUSTOMERS"`) {
			t.Errorf("SHOW TABLES omitted a table SHOW OBJECTS lists: %s", body)
		}
	})

	t.Run("terse forms are accepted", func(t *testing.T) {
		// dbt sends TERSE. An unhandled TERSE would reach DuckDB and fail on
		// syntax Snowflake defines and DuckDB does not.
		for _, sql := range []string{"SHOW TERSE SCHEMAS", "SHOW TERSE OBJECTS", "SHOW TERSE TABLES"} {
			if body := mustSQL(t, srv, tok, sql); strings.Contains(strings.ToLower(body), "parser error") {
				t.Errorf("%s reached the engine unhandled: %s", sql, body)
			}
		}
	})

	t.Run("functions answers with no rows, not an error", func(t *testing.T) {
		// The emulator defines no UDFs. The honest answer is an empty result
		// with the columns dbt selects — a failure here stops introspection.
		for _, sql := range []string{"SHOW FUNCTIONS", "SHOW USER FUNCTIONS", "SHOW TERSE FUNCTIONS"} {
			body := mustSQL(t, srv, tok, sql)
			if !strings.Contains(body, "is_builtin") {
				t.Errorf("%s did not return the column set dbt selects: %s", sql, body)
			}
		}
	})

	t.Run("describe table names its columns", func(t *testing.T) {
		body := mustSQL(t, srv, tok, "DESCRIBE TABLE customers")
		for _, want := range []string{"id", "name"} {
			if !strings.Contains(body, want) {
				t.Errorf("DESCRIBE TABLE omitted column %q: %s", want, body)
			}
		}
		// DESC is the short form and the one clients actually send.
		if body := mustSQL(t, srv, tok, "DESC TABLE customers"); !strings.Contains(body, "id") {
			t.Errorf("DESC TABLE differs from DESCRIBE TABLE: %s", body)
		}
	})
}

// --- file formats ------------------------------------------------------------
//
// CREATE FILE FORMAT was an entire verb with no test: `handleStageSQL` matched
// it, parsed it and stored it, and nothing asserted any of that. A file format
// is what a COPY INTO reads its CSV through, so a mis-parsed delimiter or an
// ignored SKIP_HEADER does not fail — it loads the header row as data, or
// splits columns in the wrong place, and the table looks populated.

func TestCreateFileFormatParsesItsOptions(t *testing.T) {
	srv, tok := newStatefulServer(t)
	for _, sql := range []string{
		"CREATE FILE FORMAT ff_csv TYPE = 'CSV'",
		"CREATE OR REPLACE FILE FORMAT ff_csv TYPE = 'CSV' FIELD_DELIMITER = '|' SKIP_HEADER = 1",
		"CREATE FILE FORMAT IF NOT EXISTS ff_json TYPE = 'JSON'",
		"CREATE FILE FORMAT ff_pq TYPE = 'PARQUET'",
		"CREATE FILE FORMAT ff_full TYPE = 'CSV' FIELD_OPTIONALLY_ENCLOSED_BY = '\"' ESCAPE = '\\\\' NULL_IF = ('NULL')",
	} {
		if _, body := execSQL(t, srv, tok, sql); strings.Contains(body, `"success":false`) {
			t.Errorf("%s was refused: %s", sql, body)
		}
	}
}

func TestFileFormatRefusesAnUnparseableOption(t *testing.T) {
	// SKIP_HEADER is a count. A non-numeric value that parsed as zero would
	// silently load the header row as data — a wrong table rather than an
	// error, and one nobody notices until a downstream cast fails.
	srv, tok := newStatefulServer(t)
	_, body := execSQL(t, srv, tok, "CREATE FILE FORMAT bad TYPE = 'CSV' SKIP_HEADER = notanumber")
	if !strings.Contains(body, `"success":false`) {
		t.Errorf("a non-numeric SKIP_HEADER was accepted: %s", body)
	}
	if !strings.Contains(body, "SKIP_HEADER") {
		t.Errorf("the refusal does not name the option that was wrong: %s", body)
	}
}

func TestListStageSortsAndRelativisesPaths(t *testing.T) {
	// LIST returns paths RELATIVE to the stage, sorted. A client diffs this
	// listing to decide what to upload; absolute paths or an unstable order
	// make every run look like everything changed.
	srv, tok := newStatefulServer(t)
	mustSQL(t, srv, tok, "CREATE STAGE st1")

	dir := stageDirFor(t, srv, "st1")
	writeStageFile(t, dir, "b.csv", "2\n")
	writeStageFile(t, dir, "a.csv", "1\n")
	writeStageFile(t, dir, filepath.Join("nested", "c.csv"), "3\n")

	body := mustSQL(t, srv, tok, "LIST @st1")
	for _, want := range []string{"a.csv", "b.csv", "nested/c.csv"} {
		if !strings.Contains(body, want) {
			t.Errorf("LIST omitted %q: %s", want, body)
		}
	}
	if strings.Contains(body, dir) {
		t.Errorf("LIST leaked the emulator's own absolute path: %s", body)
	}
	if ia, ib := strings.Index(body, "a.csv"), strings.Index(body, "b.csv"); ia > ib {
		t.Errorf("LIST is not sorted; a client diffing it sees spurious changes: %s", body)
	}
}

func stageDirFor(t *testing.T, srv *Server, stage string) string {
	t.Helper()
	d, err := srv.stageDir(stage)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func writeStageFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
