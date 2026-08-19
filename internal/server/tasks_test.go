package server

import (
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

func TestATaskNeedsAScheduleOrAPredecessor(t *testing.T) {
	// Snowflake requires one. A task with neither can never run, and storing
	// it would be a task that reports created and does nothing forever.
	if _, err := parseTask("orphan", "WAREHOUSE = wh", "SELECT 1"); err == nil {
		t.Fatal("a task with neither SCHEDULE nor AFTER was accepted")
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
