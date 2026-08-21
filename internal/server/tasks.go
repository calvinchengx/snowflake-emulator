package server

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/calvinchengx/snowflake-emulator/internal/engine"
)

// Tasks: Snowflake's own scheduler, and the thing the Tasks consumer is named
// for. A task holds a statement; a task graph runs statements in dependency
// order after a root.
//
// WHAT IS HERE: CREATE / ALTER / DROP / SHOW TASK, AFTER graphs, EXECUTE TASK
// -- which runs the named task and then everything downstream of it, as
// Snowflake does -- and a scheduler that actually fires a resumed root task on
// its interval.
//
// WHAT IS REFUSED, BY NAME: USING CRON, because a cron expression means
// specific wall-clock times and honouring the syntax while firing on an
// interval would be a schedule that silently is not the one asked for; and
// WHEN, because a predicate that is never evaluated turns a conditional task
// into an unconditional one.

type task struct {
	Name      string
	Warehouse string
	Schedule  time.Duration
	ScheduleT string // as written, for SHOW TASKS
	After     []string
	SQL       string
	Started   bool
	LastRun   time.Time
}

var (
	// THE OPTION CLAUSE IS OPTIONAL, and `CREATE TASK t AS <sql>` is the purest
	// manual task: no warehouse, no schedule, no predecessor, run only by
	// EXECUTE TASK. Snowflake accepts it -- a task that names no WAREHOUSE is a
	// SERVERLESS task there, which is a real thing and not an omission.
	//
	// This is the other half of the manual task (#39), and it was missed because
	// the parity probe written alongside that fix spelled it
	// `CREATE TASK p_manual WAREHOUSE = parity_wh AS SELECT 1`. The option
	// clause made the regex match, so the probe passed and the barest form still
	// fell through to duckdb -- which answers `Parser Error ... near "TASK"`,
	// naming neither the statement nor why. A probe that picks a comfortable
	// spelling measures the spelling.
	// `(?:(.*?)\s+)??` -- LAZY optional, and the second `?` is the fix.
	//
	// The properties clause is optional because a MANUAL TASK has none. It was
	// written `(?:(.*?)\s+)?`, which is optional but GREEDY: the group prefers
	// to participate rather than be skipped, so on a manual task it consumed
	// the body's own first `AS` and latched onto a later one.
	//
	//   CREATE TASK t AS CREATE OR REPLACE TABLE x AS SELECT 1 AS n
	//     stored body: `SELECT 1 AS n`      <- not the statement asked for
	//
	// The task then ran that, TASK_HISTORY said SUCCEEDED, EXECUTE TASK said
	// "1 task(s) in the graph", and table x did not exist. A wrong body that
	// SUCCEEDS is worse than one that fails: nothing anywhere says a different
	// statement ran. SHOW TASKS did show the truncated body, which is the only
	// reason it was findable at all.
	//
	// It bit ONLY manual tasks -- with `WAREHOUSE = w` present the first `AS`
	// is already the right one, which is why every earlier probe of a CTAS
	// body passed and this survived a previous attempt at the same fix. And it
	// is the worst possible pairing: a manual task is the shape a pipeline
	// triggered by hand wants, and `CREATE TABLE ... AS SELECT` is what a dbt
	// model compiles to.
	reCreateTask = regexp.MustCompile(`(?is)^CREATE\s+(?:OR\s+REPLACE\s+)?TASK\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z0-9_$."]+)\s+(?:(.*?)\s+)??AS\s+(.*)$`)
	reAlterTask  = regexp.MustCompile(`(?i)^ALTER\s+TASK\s+(?:IF\s+EXISTS\s+)?([A-Za-z0-9_$."]+)\s+(RESUME|SUSPEND)\b`)
	reDropTask   = regexp.MustCompile(`(?i)^DROP\s+TASK\s+(?:IF\s+EXISTS\s+)?([A-Za-z0-9_$."]+)`)
	reShowTasks  = regexp.MustCompile(`(?i)^SHOW\s+(?:TERSE\s+)?TASKS\b`)
	reExecTask   = regexp.MustCompile(`(?i)^EXECUTE\s+TASK\s+([A-Za-z0-9_$."]+)`)

	reTaskWarehouse = regexp.MustCompile(`(?i)WAREHOUSE\s*=\s*'?([A-Za-z0-9_$."]+)'?`)
	reTaskSchedule  = regexp.MustCompile(`(?i)SCHEDULE\s*=\s*'([^']*)'`)
	reTaskAfter     = regexp.MustCompile(`(?i)\bAFTER\s+([A-Za-z0-9_$.", ]+?)(?:\s+WHEN\b|\s*$)`)
	reTaskWhen      = regexp.MustCompile(`(?i)\bWHEN\b`)
	reEveryN        = regexp.MustCompile(`(?i)^\s*(\d+)\s+(MINUTE|MINUTES|SECOND|SECONDS|HOUR|HOURS)\s*$`)
)

func taskKey(name string) string { return strings.Trim(strings.ToUpper(name), `"`) }

func (s *Server) handleTaskSQL(w http.ResponseWriter, sqlText string) bool {
	trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sqlText), ";"))

	if m := reCreateTask.FindStringSubmatch(trimmed); m != nil {
		t, err := parseTask(m[1], m[2], m[3])
		if err != nil {
			writeFail(w, http.StatusOK, "001020", err.Error())
			return true
		}
		s.mu.Lock()
		if s.tasks == nil {
			s.tasks = map[string]*task{}
		}
		s.tasks[taskKey(t.Name)] = t
		s.mu.Unlock()
		writeQueryOK(w, []string{"status"},
			[][]string{{fmt.Sprintf("Task %s successfully created.", t.Name)}}, "duckdb")
		return true
	}

	if m := reAlterTask.FindStringSubmatch(trimmed); m != nil {
		s.mu.Lock()
		t, ok := s.tasks[taskKey(m[1])]
		if ok {
			t.Started = strings.EqualFold(m[2], "RESUME")
		}
		s.mu.Unlock()
		if !ok {
			writeFail(w, http.StatusOK, "002003", fmt.Sprintf("Task %s does not exist", m[1]))
			return true
		}
		writeQueryOK(w, []string{"status"},
			[][]string{{"Statement executed successfully."}}, "duckdb")
		return true
	}

	if m := reDropTask.FindStringSubmatch(trimmed); m != nil {
		s.mu.Lock()
		delete(s.tasks, taskKey(m[1]))
		s.mu.Unlock()
		writeQueryOK(w, []string{"status"},
			[][]string{{fmt.Sprintf("%s successfully dropped.", m[1])}}, "duckdb")
		return true
	}

	if reShowTasks.MatchString(trimmed) {
		s.mu.Lock()
		rows := make([][]string, 0, len(s.tasks))
		for _, t := range s.tasks {
			state := "suspended"
			if t.Started {
				state = "started"
			}
			rows = append(rows, []string{
				t.Name, t.Warehouse, t.ScheduleT, state,
				strings.Join(t.After, ", "), t.SQL,
			})
		}
		s.mu.Unlock()
		sort.Slice(rows, func(i, j int) bool { return rows[i][0] < rows[j][0] })
		writeQueryOK(w, []string{"name", "warehouse", "schedule", "state", "predecessors", "definition"},
			rows, "duckdb")
		return true
	}

	if m := reExecTask.FindStringSubmatch(trimmed); m != nil {
		s.runTaskGraph(w, m[1], "EXECUTE TASK")
		return true
	}
	return false
}

func parseTask(name, opts, body string) (*task, error) {
	if reTaskWhen.MatchString(opts) {
		return nil, fmt.Errorf("WHEN is not implemented: a predicate that is never " +
			"evaluated turns a conditional task into an unconditional one")
	}
	t := &task{Name: strings.Trim(name, `"`), SQL: strings.TrimSpace(body)}
	if m := reTaskWarehouse.FindStringSubmatch(opts); m != nil {
		t.Warehouse = strings.Trim(m[1], `"`)
	}
	// A dbt task without a warehouse is refused AT CREATE, because Snowflake
	// requires one: "when you create a task that executes the EXECUTE DBT
	// PROJECT command, you must specify a user-managed warehouse". dbt connects
	// back as an ordinary client and a client needs a warehouse to run in.
	//
	// Refused here rather than discovered at run time, which is what happened
	// while this was built: the task ran, dbt could not reach a warehouse, and
	// the failure read `dbt run failed: 2 total | 1 error | 1 skipped` --
	// naming neither the warehouse nor the task's missing clause. A dispatch
	// spent to learn something the CREATE already showed.
	if t.Warehouse == "" && reExecDbtProject.MatchString(t.SQL) {
		return nil, fmt.Errorf("a task whose body is EXECUTE DBT PROJECT must name a "+
			"WAREHOUSE: dbt connects back as a client and has nowhere to run without "+
			"one (task %s)", t.Name)
	}
	if m := reTaskSchedule.FindStringSubmatch(opts); m != nil {
		t.ScheduleT = m[1]
		if strings.Contains(strings.ToUpper(m[1]), "CRON") {
			return nil, fmt.Errorf("SCHEDULE = 'USING CRON ...' is not implemented: " +
				"a cron expression names specific wall-clock times, and firing on an " +
				"interval instead would be a schedule that is not the one asked for")
		}
		d, err := everyInterval(m[1])
		if err != nil {
			return nil, err
		}
		t.Schedule = d
	}
	if m := reTaskAfter.FindStringSubmatch(opts); m != nil {
		for _, p := range strings.Split(m[1], ",") {
			if p = strings.Trim(strings.TrimSpace(p), `"`); p != "" {
				t.After = append(t.After, p)
			}
		}
	}
	// A TASK WITH NEITHER SCHEDULE NOR AFTER IS A MANUAL TASK, and it is legal.
	//
	// This used to be refused, with the reason "it could never run". That reason
	// was FALSE, and checking it against Snowflake's own documentation rather
	// than against intuition is what found it: Snowflake requires a schedule
	// only for a task that must START ITSELF. A task created without one is
	// valid, starts suspended like every other, and is run on demand by
	// `EXECUTE TASK` -- which is exactly the shape a pipeline triggered by an
	// orchestrator wants, and the shape `CREATE TASK ... AS EXECUTE DBT PROJECT`
	// takes when something else owns the schedule.
	//
	// Refusing was doctrine misapplied. An honest refusal is only honest when
	// the thing refused is genuinely absent upstream; refusing something real
	// Snowflake supports teaches a consumer to write SQL it does not need, and
	// the consumer here was made to invent a SCHEDULE it did not want.
	//
	// Nothing else has to change for it to be safe: the scheduler already fires
	// only tasks with `Schedule > 0`, so a manual task is never picked up by it.
	return t, nil
}

func everyInterval(spec string) (time.Duration, error) {
	m := reEveryN.FindStringSubmatch(spec)
	if m == nil {
		return 0, fmt.Errorf("SCHEDULE = '%s' is not implemented: expected '<n> MINUTE', "+
			"'<n> SECOND' or '<n> HOUR'", spec)
	}
	n, _ := strconv.Atoi(m[1])
	switch strings.ToUpper(m[2]) {
	case "SECOND", "SECONDS":
		return time.Duration(n) * time.Second, nil
	case "HOUR", "HOURS":
		return time.Duration(n) * time.Hour, nil
	default:
		return time.Duration(n) * time.Minute, nil
	}
}

// runTaskGraph runs the named task and then everything downstream of it, which
// is what EXECUTE TASK does on Snowflake: a root's run carries its graph.
func (s *Server) runTaskGraph(w http.ResponseWriter, name, from string) {
	order, err := s.graphFrom(taskKey(name))
	if err != nil {
		writeFail(w, http.StatusOK, "002003", err.Error())
		return
	}
	if err := s.runOrder(order, from); err != nil {
		writeFail(w, http.StatusOK, "001021", err.Error())
		return
	}
	writeQueryOK(w, []string{"status"},
		[][]string{{fmt.Sprintf("Task %s executed, %d task(s) in the graph.", name, len(order))}},
		"duckdb")
}

// runOrder runs a graph in dependency order and WRITES DOWN WHAT HAPPENED.
//
// Both callers share it on purpose. EXECUTE TASK reported a failure to the
// caller and the scheduler swallowed one entirely -- `break` out of the loop,
// nothing logged, nothing stored -- so a resumed root task that failed every
// minute was indistinguishable from one that succeeded every minute. That is
// the shape this repository keeps finding, and history is only worth having if
// the unattended path writes to it too.
// execTaskBody runs a task's statement the way the HTTP path runs one.
//
// IT USED TO CALL engine.Exec DIRECTLY, which made a task body a DUCKDB
// statement rather than a Snowflake one. Everything this emulator does to a
// statement before the engine sees it -- expanding a stream reference into the
// rows it owes, rewriting COPY INTO against an internal stage -- was skipped
// for exactly the statements Snowflake tasks are most used with:
//
//	CREATE TASK t AS INSERT INTO sink SELECT id FROM my_stream
//	  -> duckdb: Catalog Error: Table with name my_stream does not exist!
//	CREATE TASK t AS COPY INTO tbl FROM '@~/dir/'
//	  -> duckdb: Parser Error: syntax error at or near "INTO"
//
// Both statements work perfectly OUTSIDE a task, which is what made this worth
// fixing rather than documenting: the same text meant two different things
// depending on who ran it, and the task was the one place a consumer could not
// see why.
//
// Stream-driven CDC and stage loading are the two shapes a medallion pipeline
// is built from, so between them this covered most of what anyone would put in
// a task here.
func (s *Server) execTaskBody(t *task, sqlText string) (engine.Result, error) {
	// The same order the HTTP path uses. Streams first, so the engine never
	// sees a name it has no table for; the advance happens whatever the engine
	// then makes of it, exactly as the deferred advance does over there.
	expanded, err := s.expandStreams(sqlText)
	if err != nil {
		return engine.Result{}, err
	}
	if expanded != sqlText {
		defer s.advanceStreams(sqlText)
		sqlText = expanded
	}
	// EXECUTE DBT PROJECT is the reason Snowflake documents tasks for dbt at
	// all: `CREATE TASK build AS EXECUTE DBT PROJECT p ARGS='run'` chained by
	// AFTER to a `test` task is their own orchestration example. A task is
	// where this statement is MEANT to run, so a task body that could not
	// carry it would leave the feature reachable only by hand.
	//
	// A task has no session, so the generated profile takes the emulator's
	// defaults -- the same ones current_database() and current_schema() answer
	// with when a session set none.
	if m := reExecDbtProject.FindStringSubmatch(strings.TrimSpace(sqlText)); m != nil {
		// The rows go nowhere -- runOrder keeps a task's state, not its result
		// set -- so what matters here is the ERROR. A failing dbt run fails the
		// statement, which fails the task, which stops the graph and records
		// why in TASK_HISTORY. That chain is the whole reason Snowflake made
		// dbt errors query failures.
		// THE TASK'S OWN WAREHOUSE, which is why Snowflake requires one here:
		// "when you create a task that executes the EXECUTE DBT PROJECT
		// command, you must specify a user-managed warehouse". dbt connects
		// back as a client and a client needs a warehouse to run in.
		//
		// This passed session{} at first, which defaulted the profile to
		// COMPUTE_WH -- a warehouse no deployment here creates. The statement
		// worked when run directly and failed inside a task, for a reason the
		// error never named: `dbt run failed: 2 total | 1 error | 1 skipped`.
		// The parity probe caught it; the direct probe beside it was green.
		// A task body carries its own ENV_VARS, the same as the statement run
		// by hand -- that is where a project's environment is overridden for a
		// run, so a task that could not carry one could not build against
		// anything but the project's defaults.
		env, err := dbtEnvVars(m[2])
		if err != nil {
			return engine.Result{}, err
		}
		if _, _, err := s.execDbtProject(session{Warehouse: t.Warehouse}, dbtKey(m[1]), m[2], env); err != nil {
			return engine.Result{}, err
		}
		return engine.Result{Dialect: "duckdb"}, nil
	}
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(sqlText)), "COPY INTO") {
		rewritten, err := s.rewriteCopy(sqlText)
		if err != nil {
			return engine.Result{}, err
		}
		sqlText = rewritten
	}
	return engine.Exec(s.Cfg.DuckDB, sqlText)
}

func (s *Server) runOrder(order []*task, from string) error {
	scheduled := time.Now().UTC()
	for i, t := range order {
		start := time.Now().UTC()
		if _, err := s.execTaskBody(t, t.SQL); err != nil {
			s.record(taskRun{
				Name: taskKey(t.Name), State: "FAILED", QueryText: t.SQL,
				ErrorMessage:  err.Error(),
				ScheduledTime: scheduled, StartTime: start,
				CompletedTime: time.Now().UTC(), ScheduledFrom: from,
			})
			// EVERYTHING DOWNSTREAM IS SKIPPED, AND IT SAYS SO. Those tasks do
			// not run on Snowflake either when a predecessor fails, and
			// leaving them out of the history would read as "not started yet"
			// to anything polling -- which is how a driver waits forever on a
			// graph that already gave up.
			for _, sk := range order[i+1:] {
				s.record(taskRun{
					Name: taskKey(sk.Name), State: "SKIPPED", QueryText: sk.SQL,
					ErrorMessage:  fmt.Sprintf("upstream task %s failed", t.Name),
					ScheduledTime: scheduled, ScheduledFrom: from,
				})
			}
			return fmt.Errorf("task %s failed: %s", t.Name, err.Error())
		}
		s.record(taskRun{
			Name: taskKey(t.Name), State: "SUCCEEDED", QueryText: t.SQL,
			ScheduledTime: scheduled, StartTime: start,
			CompletedTime: time.Now().UTC(), ScheduledFrom: from,
		})
		s.mu.Lock()
		t.LastRun = time.Now().UTC()
		s.mu.Unlock()
	}
	return nil
}

// graphFrom returns the root and its descendants in dependency order. A cycle
// is refused rather than run: Snowflake rejects one at creation, and running
// it here would not stop.
func (s *Server) graphFrom(root string) ([]*task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	start, ok := s.tasks[root]
	if !ok {
		// Wire format, as above: written by the caller as
		// writeFail(..., "002003", err.Error()), and the sibling literal at
		// the SHOW TASKS handler spells it the same way on purpose.
		//nolint:staticcheck // ST1005: Snowflake message, not Go idiom
		return nil, fmt.Errorf("Task %s does not exist", root)
	}
	children := map[string][]*task{}
	for _, t := range s.tasks {
		for _, p := range t.After {
			children[taskKey(p)] = append(children[taskKey(p)], t)
		}
	}
	var order []*task
	seen := map[string]bool{}
	var visit func(t *task, path map[string]bool) error
	visit = func(t *task, path map[string]bool) error {
		k := taskKey(t.Name)
		if path[k] {
			return fmt.Errorf("task graph has a cycle at %s", t.Name)
		}
		if seen[k] {
			return nil
		}
		seen[k] = true
		order = append(order, t)
		path[k] = true
		kids := children[k]
		sort.Slice(kids, func(i, j int) bool { return kids[i].Name < kids[j].Name })
		for _, c := range kids {
			if err := visit(c, path); err != nil {
				return err
			}
		}
		delete(path, k)
		return nil
	}
	if err := visit(start, map[string]bool{}); err != nil {
		return nil, err
	}
	return order, nil
}

// scheduler fires resumed root tasks on their interval. A task with AFTER is
// not a root and runs only as part of its root's graph, which is Snowflake's
// rule too.
func (s *Server) scheduler(tick time.Duration) {
	for range time.Tick(tick) {
		now := time.Now().UTC()
		s.mu.Lock()
		var due []*task
		for _, t := range s.tasks {
			if t.Started && t.Schedule > 0 && len(t.After) == 0 && now.Sub(t.LastRun) >= t.Schedule {
				t.LastRun = now
				due = append(due, t)
			}
		}
		s.mu.Unlock()
		for _, t := range due {
			order, err := s.graphFrom(taskKey(t.Name))
			if err != nil {
				continue
			}
			// The error is deliberately not propagated -- there is nobody to
			// return it to -- but it is no longer LOST: runOrder writes the
			// failure and the skips into the history, which is the only place
			// an unattended run can be seen from.
			_ = s.runOrder(order, "SCHEDULE")
		}
	}
}
