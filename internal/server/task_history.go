package server

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TASK_HISTORY: what an orchestrator reads to find out whether the graph it
// started actually finished.
//
// WHY THIS MATTERS MORE THAN IT LOOKS. Tasks, graphs, schedules and EXECUTE
// TASK were all here and green, and a consumer still could not drive a
// pipeline from them, because nothing said what a run DID. The Snowflake Tasks
// cell in this family is named for an orchestrator it does not use: its steps
// run from the host through `uv run`, and the reason is not that tasks were
// missing. It is that a driver could start a graph and never learn whether it
// succeeded.
//
// IT IS A REAL RELATION, not a canned answer. The reference is rewritten into
// a typed VALUES list and handed to the engine, so ORDER BY, WHERE, LIMIT,
// joins and aggregates over it are the engine's own and work the way they
// would anywhere. Answering only the exact shape `SELECT * FROM
// TABLE(INFORMATION_SCHEMA.TASK_HISTORY())` would have been less code and a
// trap: the first consumer to add `WHERE NAME = ...` would get a parser error
// for a query real Snowflake answers.
//
// THE COLUMN LIST IS A SUBSET, and deliberately a conservative one. Snowflake's
// TASK_HISTORY carries more than this. The columns here are the ones this
// emulator can fill from what it actually knows; the rest are OMITTED rather
// than returned as NULL, because a column that is always NULL reads as "this
// run had no value" instead of "this emulator does not track it". They are
// listed in docs/08-tasks-and-streams.md so the surface is discoverable
// without reading this file.

// taskRun is one task's one run. SKIPPED carries no start or completion,
// which is the honest shape: it never began.
type taskRun struct {
	Name          string
	State         string // SUCCEEDED | FAILED | SKIPPED
	QueryText     string
	ErrorMessage  string
	ScheduledTime time.Time
	StartTime     time.Time
	CompletedTime time.Time
	ScheduledFrom string // EXECUTE TASK | SCHEDULE
}

// maxRuns bounds the history. Snowflake keeps seven days; this keeps the last
// thousand runs, because an emulator that grows without limit for the life of
// a container is its own defect. Documented rather than silent: a consumer
// polling for a run it started will always find it, and one auditing a long
// history will not.
const maxRuns = 1000

var (
	reTaskHistory = regexp.MustCompile(
		`(?is)TABLE\s*\(\s*(?:[A-Za-z0-9_$."]+\.)?TASK_HISTORY\s*\(([^)]*)\)\s*\)`)
	reHistArg = regexp.MustCompile(`(?is)([A-Za-z_]+)\s*=>\s*('[^']*'|[A-Za-z0-9_]+)`)
)

// record appends a run. The caller holds no lock.
func (s *Server) record(r taskRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs = append(s.runs, r)
	if len(s.runs) > maxRuns {
		s.runs = s.runs[len(s.runs)-maxRuns:]
	}
}

// expandTaskHistory rewrites a TASK_HISTORY() reference into a relation the
// engine can query. It returns the SQL unchanged when there is no reference.
func (s *Server) expandTaskHistory(sqlText string) (string, error) {
	m := reTaskHistory.FindStringSubmatch(sqlText)
	if m == nil {
		return sqlText, nil
	}
	name, limit, err := parseHistoryArgs(m[1])
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	rows := make([]taskRun, 0, len(s.runs))
	// Newest first, which is the order a consumer polling for "did my run
	// finish" wants and the order Snowflake's RESULT_LIMIT cuts from.
	for i := len(s.runs) - 1; i >= 0; i-- {
		r := s.runs[i]
		if name != "" && !strings.EqualFold(r.Name, name) {
			continue
		}
		rows = append(rows, r)
		if limit > 0 && len(rows) == limit {
			break
		}
	}
	s.mu.Unlock()

	return strings.Replace(sqlText, m[0], historyRelation(rows), 1), nil
}

// parseHistoryArgs reads the named arguments. One this emulator cannot honour
// is refused BY NAME rather than ignored: silently dropping
// SCHEDULED_TIME_RANGE_START would answer a question about the last hour with
// the whole history and look right.
func parseHistoryArgs(args string) (name string, limit int, err error) {
	for _, m := range reHistArg.FindAllStringSubmatch(args, -1) {
		key := strings.ToUpper(m[1])
		val := strings.Trim(m[2], "'")
		switch key {
		case "TASK_NAME":
			name = strings.Trim(strings.ToUpper(val), `"`)
		case "RESULT_LIMIT":
			n, convErr := strconv.Atoi(val)
			if convErr != nil || n < 1 {
				return "", 0, fmt.Errorf("RESULT_LIMIT must be a positive integer, got %q", val)
			}
			limit = n
		default:
			return "", 0, fmt.Errorf(
				"TASK_HISTORY argument %s is not implemented: this emulator "+
					"supports TASK_NAME and RESULT_LIMIT", key)
		}
	}
	if strings.TrimSpace(args) != "" && len(reHistArg.FindAllString(args, -1)) == 0 {
		return "", 0, fmt.Errorf(
			"TASK_HISTORY takes named arguments only, e.g. TASK_NAME => 'T', got %q",
			strings.TrimSpace(args))
	}
	return name, limit, nil
}

var historyColumns = []string{
	"NAME", "STATE", "QUERY_TEXT", "ERROR_MESSAGE",
	"SCHEDULED_TIME", "QUERY_START_TIME", "COMPLETED_TIME", "SCHEDULED_FROM",
}

// historyRelation renders the runs as a typed relation. An empty history is a
// typed EMPTY relation rather than no relation at all: a consumer whose first
// poll happens before the first run must get zero rows, not a parse error.
func historyRelation(rows []taskRun) string {
	if len(rows) == 0 {
		cols := make([]string, len(historyColumns))
		for i, c := range historyColumns {
			typ := "VARCHAR"
			if strings.HasSuffix(c, "_TIME") {
				typ = "TIMESTAMP"
			}
			cols[i] = fmt.Sprintf(`CAST(NULL AS %s) AS "%s"`, typ, c)
		}
		return "(SELECT " + strings.Join(cols, ", ") + " WHERE 1 = 0)"
	}
	tuples := make([]string, 0, len(rows))
	for _, r := range rows {
		tuples = append(tuples, "("+strings.Join([]string{
			quote(r.Name), quote(r.State), quote(r.QueryText), nullableText(r.ErrorMessage),
			stamp(r.ScheduledTime), stamp(r.StartTime), stamp(r.CompletedTime),
			quote(r.ScheduledFrom),
		}, ", ")+")")
	}
	names := make([]string, len(historyColumns))
	for i, c := range historyColumns {
		names[i] = `"` + c + `"`
	}
	return fmt.Sprintf("(SELECT * FROM (VALUES %s) AS task_history(%s))",
		strings.Join(tuples, ", "), strings.Join(names, ", "))
}

// nullableText keeps the difference between "no error" and "an empty error".
func nullableText(s string) string {
	if s == "" {
		return "CAST(NULL AS VARCHAR)"
	}
	return quote(s)
}

// stamp renders a time, and a zero time as a typed NULL. A SKIPPED run never
// started, and 0001-01-01 would be a timestamp a consumer could sort on.
func stamp(t time.Time) string {
	if t.IsZero() {
		return "CAST(NULL AS TIMESTAMP)"
	}
	return "CAST(" + quote(t.UTC().Format("2006-01-02 15:04:05.000")) + " AS TIMESTAMP)"
}
