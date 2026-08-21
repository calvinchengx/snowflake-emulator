package server

import (
	"bytes"
	"encoding/json"
	"github.com/calvinchengx/snowflake-emulator/internal/config"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskParsesItsOptions(t *testing.T) {
	got, err := parseTask("load_bronze",
		`WAREHOUSE = compute_wh SCHEDULE = '5 MINUTE'`,
		"INSERT INTO bronze SELECT * FROM raw")
	if err != nil {
		t.Fatal(err)
	}
	if got.Warehouse != "compute_wh" || got.ScheduleT != "5 MINUTE" || got.Schedule.Minutes() != 5 {
		t.Fatalf("%+v", got)
	}
	if got.SQL != "INSERT INTO bronze SELECT * FROM raw" {
		t.Fatalf("body was %q", got.SQL)
	}
}

func TestAfterMakesAGraph(t *testing.T) {
	got, err := parseTask("silver", `AFTER bronze_a, bronze_b`, "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.After) != 2 || got.After[0] != "bronze_a" || got.After[1] != "bronze_b" {
		t.Fatalf("predecessors were %v", got.After)
	}
}

func TestATaskWithNoOptionClauseAtAll(t *testing.T) {
	// `CREATE TASK t AS <sql>` -- no warehouse, no schedule, no predecessor.
	// Snowflake accepts it: a task naming no WAREHOUSE is a SERVERLESS task
	// there, a real thing rather than an omission.
	//
	// THE OTHER HALF OF #39, and missed by the probe written alongside it. That
	// probe spelled the manual task `CREATE TASK p_manual WAREHOUSE = parity_wh
	// AS SELECT 1`; the option clause made the regex match, so it passed while
	// the barest form still fell through to duckdb and came back
	// `Parser Error ... near "TASK"` -- naming neither the statement nor why.
	// A probe that picks a comfortable spelling measures the spelling.
	for _, sql := range []string{
		"CREATE TASK t AS SELECT 1",
		"CREATE OR REPLACE TASK t AS SELECT 1",
		"CREATE TASK IF NOT EXISTS t AS SELECT 1",
	} {
		m := reCreateTask.FindStringSubmatch(sql)
		if m == nil {
			t.Fatalf("%q did not parse as a task, so it falls through to duckdb", sql)
		}
		if m[1] != "t" || strings.TrimSpace(m[3]) != "SELECT 1" {
			t.Fatalf("%q parsed as name=%q body=%q", sql, m[1], m[3])
		}
		got, err := parseTask(m[1], m[2], m[3])
		if err != nil {
			t.Fatalf("%q: %v", sql, err)
		}
		if got.Warehouse != "" || got.Schedule != 0 || len(got.After) != 0 {
			t.Fatalf("%q: %+v", sql, got)
		}
	}

	// A BODY THAT CONTAINS ` AS `, which every case above carefully avoided.
	//
	// The cases above all use `SELECT 1`. That body has no `AS` in it, so it
	// passed against a regex that mis-split on one -- this test named the trap
	// ("a probe that picks a comfortable spelling measures the spelling") and
	// then picked a comfortable BODY, which is the same mistake one level in.
	//
	// Measured before the fix: `CREATE TASK t AS CREATE OR REPLACE TABLE x AS
	// SELECT 1 AS n` stored `SELECT 1 AS n`, ran it, and reported SUCCEEDED
	// with no table x. A CTAS is what a dbt model compiles to, so this is the
	// body the Tasks consumer is made of.
	for _, tc := range []struct{ sql, body string }{
		{"CREATE TASK t AS CREATE OR REPLACE TABLE x AS SELECT 1 AS n",
			"CREATE OR REPLACE TABLE x AS SELECT 1 AS n"},
		{"CREATE TASK t AS SELECT 1 AS n",
			"SELECT 1 AS n"},
		{"CREATE OR REPLACE TASK t AS CREATE TABLE y AS SELECT a AS b FROM z",
			"CREATE TABLE y AS SELECT a AS b FROM z"},
		// The option clause must still win when it is present, even though the
		// body also carries an AS.
		{"CREATE TASK t WAREHOUSE = wh AS CREATE TABLE x AS SELECT 1 AS n",
			"CREATE TABLE x AS SELECT 1 AS n"},
		{"CREATE TASK t AFTER a AS CREATE TABLE x AS SELECT 1 AS n",
			"CREATE TABLE x AS SELECT 1 AS n"},
	} {
		m := reCreateTask.FindStringSubmatch(tc.sql)
		if m == nil {
			t.Fatalf("%q did not parse as a task", tc.sql)
		}
		if got := strings.TrimSpace(m[3]); got != tc.body {
			t.Fatalf("%q\n  stored body %q\n  want        %q\n"+
				"a task that runs a body it was not given, and succeeds, says nothing anywhere",
				tc.sql, got, tc.body)
		}
	}

	// The option clause still parses when it IS there -- relaxing the regex must
	// not make `WAREHOUSE = wh` part of the body.
	m := reCreateTask.FindStringSubmatch("CREATE TASK t WAREHOUSE = wh AS SELECT 1")
	if m == nil {
		t.Fatal("a task WITH options stopped parsing")
	}
	got, err := parseTask(m[1], m[2], m[3])
	if err != nil || got.Warehouse != "wh" || strings.TrimSpace(got.SQL) != "SELECT 1" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestAManualTaskIsAcceptedAndNeverSelfFires(t *testing.T) {
	// THIS TEST USED TO ASSERT THE OPPOSITE, and it was wrong.
	//
	// It said "Snowflake requires one. A task with neither can never run" --
	// confident, plausible, and false. Snowflake requires a schedule only for a
	// task that must START ITSELF. A task created with neither is a MANUAL TASK:
	// valid, created suspended, and run on demand by `EXECUTE TASK`.
	//
	// That is the shape a pipeline driven by an orchestrator wants, and the
	// shape `CREATE TASK ... AS EXECUTE DBT PROJECT` takes when something else
	// owns the schedule. Refusing it made a consumer invent a SCHEDULE it did
	// not want, which is the opposite of what an honest refusal is for: an
	// emulator may refuse what is genuinely absent upstream, never what is
	// merely absent here.
	got, err := parseTask("manual", "WAREHOUSE = wh", "SELECT 1")
	if err != nil {
		t.Fatalf("a manual task was refused: %v", err)
	}
	if got.Schedule != 0 || len(got.After) != 0 {
		t.Fatalf("%+v: a manual task carries neither a schedule nor a predecessor", got)
	}

	// AND IT MUST NOT FIRE ITSELF, which is the half the old refusal was
	// protecting. The scheduler picks up only tasks with a positive interval, so
	// a manual task is invisible to it however long it is resumed.
	s := &Server{tasks: map[string]*task{"MANUAL": got}}
	got.Started = true
	var due int
	for _, x := range s.tasks {
		if x.Started && x.Schedule > 0 && len(x.After) == 0 {
			due++
		}
	}
	if due != 0 {
		t.Fatal("a resumed manual task was treated as due; it runs only on EXECUTE TASK")
	}

	// It is still reachable BY NAME, so EXECUTE TASK can run it.
	order, err := s.graphFrom("MANUAL")
	if err != nil || len(order) != 1 || order[0].Name != "manual" {
		t.Fatalf("EXECUTE TASK could not reach the manual task: %v %v", order, err)
	}
}

func TestCronAndWhenAreRefusedByName(t *testing.T) {
	// Both change WHEN or WHETHER the statement runs. Accepting the syntax and
	// ignoring the meaning is the silent-success failure this repository keeps
	// finding: a cron task would fire on the wrong times, a WHEN task would
	// fire when it should not.
	if _, err := parseTask("t", `SCHEDULE = 'USING CRON 0 9 * * * UTC'`, "SELECT 1"); err == nil {
		t.Error("USING CRON was accepted")
	}
	if _, err := parseTask("t", `AFTER a WHEN SYSTEM$STREAM_HAS_DATA('s')`, "SELECT 1"); err == nil {
		t.Error("WHEN was accepted")
	}
}

func TestAnIntervalWeCannotParseIsRefused(t *testing.T) {
	for _, spec := range []string{"every monday", "5 FORTNIGHTS", ""} {
		if _, err := everyInterval(spec); err == nil {
			t.Errorf("%q was accepted as a schedule", spec)
		}
	}
	for spec, want := range map[string]float64{"30 SECOND": 30, "2 MINUTES": 120, "1 HOUR": 3600} {
		d, err := everyInterval(spec)
		if err != nil {
			t.Errorf("%q: %v", spec, err)
		} else if d.Seconds() != want {
			t.Errorf("%q -> %v, want %vs", spec, d, want)
		}
	}
}

func TestGraphRunsRootThenDependents(t *testing.T) {
	s := &Server{tasks: map[string]*task{}}
	s.tasks["ROOT"] = &task{Name: "root", SQL: "SELECT 1", Schedule: 1}
	s.tasks["MID"] = &task{Name: "mid", SQL: "SELECT 2", After: []string{"root"}}
	s.tasks["LEAF"] = &task{Name: "leaf", SQL: "SELECT 3", After: []string{"mid"}}
	s.tasks["OTHER"] = &task{Name: "other", SQL: "SELECT 4", Schedule: 1}

	order, err := s.graphFrom("ROOT")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, x := range order {
		names = append(names, x.Name)
	}
	if strings.Join(names, ",") != "root,mid,leaf" {
		t.Fatalf("order was %v; a dependent must not run before what it is AFTER, "+
			"and a task in another graph must not run at all", names)
	}
}

func TestACycleIsRefusedRatherThanRun(t *testing.T) {
	s := &Server{tasks: map[string]*task{}}
	s.tasks["A"] = &task{Name: "a", SQL: "SELECT 1", After: []string{"b"}}
	s.tasks["B"] = &task{Name: "b", SQL: "SELECT 2", After: []string{"a"}}
	if _, err := s.graphFrom("A"); err == nil {
		t.Fatal("a cycle was accepted; running it would not stop")
	}
}

// A TASK BODY IS A SNOWFLAKE STATEMENT, NOT A DUCKDB ONE.
//
// runOrder used to hand the body to engine.Exec directly, so everything this
// emulator does to a statement before the engine sees it was skipped inside a
// task: a stream reference was never expanded into the rows it owes, and
// COPY INTO was never rewritten against the internal stage. Both work outside
// a task, which is the whole point -- the same text meant two different things
// depending on who ran it.
//
// Asserted as PAIRS, and on the EFFECT. A failure inside a task proves nothing
// unless the same statement succeeds outside one, and "EXECUTE TASK succeeded"
// proves nothing unless the rows are there: this repository has already been
// bitten by a task that reported SUCCEEDED while running a different statement.
func TestATaskBodyIsASnowflakeStatement(t *testing.T) {
	dir := t.TempDir()
	srv, err := New(config.Config{
		DataDir:  dir,
		StageDir: filepath.Join(dir, "stages"),
		// A FILE, not :memory:. Each engine.Exec spawns its own duckdb, so an
		// in-memory database would give every statement a fresh, empty one and
		// the test would measure nothing.
		DuckDB: filepath.Join(dir, "wh.duckdb"),
	})
	if err != nil {
		t.Fatal(err)
	}
	tok := login(t, srv)

	run := func(t *testing.T, sqlText string) map[string]any {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{"sqlText": sqlText})
		req := httptest.NewRequest(http.MethodPost, "/queries/v1/query-request", bytes.NewReader(payload))
		req.Header.Set("Authorization", `Snowflake Token="`+tok+`"`)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s: %v", sqlText, err)
		}
		return out
	}
	mustRun := func(t *testing.T, sqlText string) {
		t.Helper()
		out := run(t, sqlText)
		if ok, _ := out["success"].(bool); !ok {
			t.Fatalf("%s\n  failed: %v", sqlText, out["message"])
		}
	}
	// count reads the first cell back, so an assertion is about ROWS rather
	// than about a statement having succeeded.
	count := func(t *testing.T, table string) string {
		t.Helper()
		out := run(t, "SELECT count(*) AS n FROM "+table)
		if ok, _ := out["success"].(bool); !ok {
			t.Fatalf("reading %s back: %v", table, out["message"])
		}
		data, _ := out["data"].(map[string]any)
		rows, _ := data["rowset"].([]any)
		if len(rows) == 0 {
			t.Fatalf("no rows counting %s: %v", table, data)
		}
		first, _ := rows[0].([]any)
		if len(first) == 0 {
			t.Fatalf("empty row counting %s: %v", table, data)
		}
		s, _ := first[0].(string)
		return s
	}

	t.Run("a stream reference expands inside a task", func(t *testing.T) {
		mustRun(t, "CREATE OR REPLACE TABLE src (id INT)")
		mustRun(t, "INSERT INTO src VALUES (1),(2)")
		mustRun(t, "CREATE OR REPLACE STREAM st ON TABLE src")
		mustRun(t, "INSERT INTO src VALUES (3),(4)")
		mustRun(t, "CREATE OR REPLACE TABLE sink (id INT)")

		// Outside a task first: without this the failure below is unattributable.
		mustRun(t, "INSERT INTO sink SELECT id FROM st")
		if got := count(t, "sink"); got != "2" {
			t.Fatalf("the stream owed 2 rows outside a task, got %s", got)
		}

		mustRun(t, "INSERT INTO src VALUES (5),(6)")
		mustRun(t, "CREATE OR REPLACE TABLE sink2 (id INT)")
		mustRun(t, "CREATE OR REPLACE TASK t_stream AS INSERT INTO sink2 SELECT id FROM st")
		mustRun(t, "EXECUTE TASK t_stream")
		if got := count(t, "sink2"); got != "2" {
			t.Fatalf("the task read the stream and wrote %s rows, want 2 -- "+
				"a task body that never expanded the stream cannot see it at all", got)
		}
	})

	t.Run("COPY INTO rewrites inside a task", func(t *testing.T) {
		stageDir := filepath.Join(dir, "stages")
		if err := os.MkdirAll(stageDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// Written straight into the stage directory: PUT is the driver's job
		// and this test is about the task body, not the upload.
		if err := os.WriteFile(filepath.Join(stageDir, "o.csv"),
			[]byte("id,amount\n1,10.50\n2,20.25\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		const format = "FILE_FORMAT = (TYPE = CSV SKIP_HEADER = 1)"

		mustRun(t, "CREATE OR REPLACE TABLE loaded (id INT, amount DECIMAL(19,4))")
		mustRun(t, "COPY INTO loaded FROM '@~/o.csv' "+format)
		if got := count(t, "loaded"); got != "2" {
			t.Fatalf("COPY INTO loaded %s rows outside a task, want 2", got)
		}

		mustRun(t, "CREATE OR REPLACE TABLE loaded2 (id INT, amount DECIMAL(19,4))")
		mustRun(t, "CREATE OR REPLACE TASK t_copy AS COPY INTO loaded2 FROM '@~/o.csv' "+format)
		mustRun(t, "EXECUTE TASK t_copy")
		if got := count(t, "loaded2"); got != "2" {
			t.Fatalf("the task's COPY INTO loaded %s rows, want 2 -- a body handed "+
				"straight to duckdb fails on INTO and loads nothing", got)
		}
	})
}
