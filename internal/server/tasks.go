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
	reCreateTask = regexp.MustCompile(`(?is)^CREATE\s+(?:OR\s+REPLACE\s+)?TASK\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z0-9_$."]+)\s+(.*?)\s+AS\s+(.*)$`)
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
			[][]string{{fmt.Sprintf("Statement executed successfully.")}}, "duckdb")
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
		s.runTaskGraph(w, m[1])
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
	if t.Schedule == 0 && len(t.After) == 0 {
		return nil, fmt.Errorf("a task needs SCHEDULE or AFTER")
	}
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
func (s *Server) runTaskGraph(w http.ResponseWriter, name string) {
	order, err := s.graphFrom(taskKey(name))
	if err != nil {
		writeFail(w, http.StatusOK, "002003", err.Error())
		return
	}
	for _, t := range order {
		if _, err := engine.Exec(s.Cfg.DuckDB, t.SQL); err != nil {
			writeFail(w, http.StatusOK, "001021",
				fmt.Sprintf("task %s failed: %s", t.Name, err.Error()))
			return
		}
		s.mu.Lock()
		t.LastRun = time.Now().UTC()
		s.mu.Unlock()
	}
	writeQueryOK(w, []string{"status"},
		[][]string{{fmt.Sprintf("Task %s executed, %d task(s) in the graph.", name, len(order))}},
		"duckdb")
}

// graphFrom returns the root and its descendants in dependency order. A cycle
// is refused rather than run: Snowflake rejects one at creation, and running
// it here would not stop.
func (s *Server) graphFrom(root string) ([]*task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	start, ok := s.tasks[root]
	if !ok {
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
			for _, d := range order {
				if _, err := engine.Exec(s.Cfg.DuckDB, d.SQL); err != nil {
					break
				}
			}
		}
	}
}
