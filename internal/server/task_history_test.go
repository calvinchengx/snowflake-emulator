package server

import (
	"strings"
	"testing"
	"time"
)

func historySQL(t *testing.T, s *Server, sql string) string {
	t.Helper()
	out, err := s.expandTaskHistory(sql)
	if err != nil {
		t.Fatalf("expand failed: %v", err)
	}
	return out
}

func TestAnEmptyHistoryIsZeroRowsAndNotAParseError(t *testing.T) {
	// The first poll usually happens before the first run. A consumer must
	// get an empty answer, not a syntax error for a table that is not there.
	s := &Server{}
	got := historySQL(t, s, "SELECT NAME FROM TABLE(INFORMATION_SCHEMA.TASK_HISTORY())")
	if !strings.Contains(got, "WHERE 1 = 0") {
		t.Fatalf("empty history did not become an empty typed relation: %q", got)
	}
	for _, col := range historyColumns {
		if !strings.Contains(got, `"`+col+`"`) {
			t.Errorf("empty relation is missing column %s: %q", col, got)
		}
	}
}

func TestTheRewriteLeavesTheRestOfTheQueryAlone(t *testing.T) {
	// The point of rewriting to a relation rather than answering a canned
	// shape: WHERE, ORDER BY and LIMIT belong to the engine. A consumer's
	// first `WHERE NAME = ...` must not be a parser error for a query real
	// Snowflake answers.
	s := &Server{}
	s.record(taskRun{Name: "T_ROOT", State: "SUCCEEDED", ScheduledFrom: "EXECUTE TASK"})
	got := historySQL(t, s,
		"SELECT NAME, STATE FROM TABLE(INFORMATION_SCHEMA.TASK_HISTORY()) "+
			"WHERE STATE = 'FAILED' ORDER BY COMPLETED_TIME DESC LIMIT 1")
	for _, want := range []string{
		"SELECT NAME, STATE FROM",
		"WHERE STATE = 'FAILED'",
		"ORDER BY COMPLETED_TIME DESC LIMIT 1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the rewrite ate %q: %q", want, got)
		}
	}
}

func TestNewestRunComesFirst(t *testing.T) {
	s := &Server{}
	s.record(taskRun{Name: "T_A", State: "SUCCEEDED"})
	s.record(taskRun{Name: "T_B", State: "FAILED"})
	got := historySQL(t, s, "SELECT * FROM TABLE(INFORMATION_SCHEMA.TASK_HISTORY())")
	if strings.Index(got, "T_B") > strings.Index(got, "T_A") {
		t.Errorf("newest run must come first, got %q", got)
	}
}

func TestTaskNameAndResultLimitFilter(t *testing.T) {
	s := &Server{}
	s.record(taskRun{Name: "T_A", State: "SUCCEEDED"})
	s.record(taskRun{Name: "T_B", State: "SUCCEEDED"})
	s.record(taskRun{Name: "T_A", State: "FAILED"})

	only := historySQL(t, s, "SELECT * FROM TABLE(INFORMATION_SCHEMA.TASK_HISTORY(TASK_NAME => 'T_A'))")
	if strings.Contains(only, "T_B") {
		t.Errorf("TASK_NAME did not filter: %q", only)
	}
	if strings.Count(only, "'T_A'") != 2 {
		t.Errorf("TASK_NAME dropped a run of the named task: %q", only)
	}

	one := historySQL(t, s, "SELECT * FROM TABLE(INFORMATION_SCHEMA.TASK_HISTORY(RESULT_LIMIT => 1))")
	// Newest first, so the single row is T_A's FAILED run.
	if !strings.Contains(one, "FAILED") || strings.Contains(one, "'T_B'") {
		t.Errorf("RESULT_LIMIT => 1 must keep only the newest run: %q", one)
	}
}

func TestAnArgumentWeCannotHonourIsRefusedByName(t *testing.T) {
	// Silently dropping a time range would answer a question about the last
	// hour with the whole history and look right doing it.
	s := &Server{}
	_, err := s.expandTaskHistory(
		"SELECT * FROM TABLE(INFORMATION_SCHEMA.TASK_HISTORY(SCHEDULED_TIME_RANGE_START => 'x'))")
	if err == nil {
		t.Fatal("an unsupported TASK_HISTORY argument was accepted")
	}
	if !strings.Contains(err.Error(), "SCHEDULED_TIME_RANGE_START") {
		t.Errorf("the refusal must name the argument, got %q", err)
	}
}

func TestASkippedRunHasNoStartAndNoCompletion(t *testing.T) {
	// It never began. A zero time rendered as 0001-01-01 is a timestamp a
	// consumer can sort on and compare, which is worse than absent.
	s := &Server{}
	s.record(taskRun{
		Name: "T_LEAF", State: "SKIPPED",
		ScheduledTime: time.Now().UTC(), ErrorMessage: "upstream task T_ROOT failed",
	})
	got := historySQL(t, s, "SELECT * FROM TABLE(INFORMATION_SCHEMA.TASK_HISTORY())")
	if strings.Count(got, "CAST(NULL AS TIMESTAMP)") != 2 {
		t.Errorf("a skipped run must carry no start and no completion: %q", got)
	}
	if strings.Contains(got, "0001-01-01") {
		t.Errorf("a zero time reached the relation: %q", got)
	}
}

func TestHistoryIsBounded(t *testing.T) {
	s := &Server{}
	for i := 0; i < maxRuns+50; i++ {
		s.record(taskRun{Name: "T", State: "SUCCEEDED"})
	}
	if len(s.runs) != maxRuns {
		t.Errorf("history holds %d runs, want it bounded at %d", len(s.runs), maxRuns)
	}
}

func TestNoReferenceMeansNoRewrite(t *testing.T) {
	s := &Server{}
	const plain = "SELECT 1"
	if got := historySQL(t, s, plain); got != plain {
		t.Errorf("a statement with no TASK_HISTORY was rewritten: %q", got)
	}
}
