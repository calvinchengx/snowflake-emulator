package server

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/calvinchengx/snowflake-emulator/internal/engine"
)

// Streams: the change a table has seen since something last read it. A task
// graph driven by a stream reacts to arriving data instead of to a clock,
// which is the shape a bronze -> silver pipeline actually wants.
//
// APPEND-ONLY, AND IT PROVES IT RATHER THAN ASSUMING IT. DuckDB keeps no
// change log, so this is built on `rowid`: the stream remembers the first
// rowid it has not yet shown, and everything at or after it is new. That is
// exactly right for a table only ever inserted into, and wrong the moment a
// row before that point is updated or deleted.
//
// So the stream also remembers a checksum -- sum(hash(row)) over the rows it
// has already accounted for -- and REFUSES TO BE READ if that checksum has
// moved. A stream that quietly missed an UPDATE would be the worst kind of
// answer: a pipeline built on it would drop changes and report success. The
// refusal names what happened instead.

type stream struct {
	Name   string
	Table  string
	Offset int64
	Guard  string
}

var (
	reCreateStream  = regexp.MustCompile(`(?i)^CREATE\s+(?:OR\s+REPLACE\s+)?STREAM\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z0-9_$."]+)\s+ON\s+TABLE\s+([A-Za-z0-9_$."]+)\s*(.*)$`)
	reDropStream    = regexp.MustCompile(`(?i)^DROP\s+STREAM\s+(?:IF\s+EXISTS\s+)?([A-Za-z0-9_$."]+)`)
	reShowStreams   = regexp.MustCompile(`(?i)^SHOW\s+(?:TERSE\s+)?STREAMS\b`)
	reInitialRows   = regexp.MustCompile(`(?i)SHOW_INITIAL_ROWS\s*=\s*TRUE`)
	reStreamHasData = regexp.MustCompile(`(?i)SYSTEM\$STREAM_HAS_DATA\s*\(\s*'([^']+)'\s*\)`)
	reIsDML         = regexp.MustCompile(`(?i)^(INSERT|UPDATE|DELETE|MERGE|CREATE\s+(?:OR\s+REPLACE\s+)?TABLE)\b`)
)

func streamKey(name string) string { return strings.Trim(strings.ToUpper(name), `"`) }

// scalar runs a one-value query and returns it as text.
func (s *Server) scalar(sql string) (string, error) {
	res, err := engine.Exec(s.Cfg.DuckDB, sql)
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 || res.Rows[0][0] == nil {
		return "", nil
	}
	return *res.Rows[0][0], nil
}

// mark reads where a table currently stands: the first unseen rowid, and the
// checksum of everything before it.
func (s *Server) mark(table string, offset int64) (int64, string, error) {
	if offset < 0 {
		next, err := s.scalar(fmt.Sprintf("SELECT coalesce(max(rowid) + 1, 0) FROM %s", table))
		if err != nil {
			return 0, "", err
		}
		n, _ := strconv.ParseInt(strings.TrimSpace(next), 10, 64)
		offset = n
	}
	guard, err := s.scalar(fmt.Sprintf(
		"SELECT coalesce(sum(hash(t))::VARCHAR, '0') FROM %s t WHERE rowid < %d", table, offset))
	if err != nil {
		return 0, "", err
	}
	return offset, guard, nil
}

func (s *Server) handleStreamSQL(w http.ResponseWriter, sqlText string) bool {
	trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sqlText), ";"))

	if m := reCreateStream.FindStringSubmatch(trimmed); m != nil {
		table := strings.Trim(m[2], `"`)
		offset := int64(-1)
		if reInitialRows.MatchString(m[3]) {
			offset = 0
		}
		off, guard, err := s.mark(table, offset)
		if err != nil {
			writeFail(w, http.StatusOK, "002003", err.Error())
			return true
		}
		st := &stream{Name: strings.Trim(m[1], `"`), Table: table, Offset: off, Guard: guard}
		s.mu.Lock()
		if s.streams == nil {
			s.streams = map[string]*stream{}
		}
		s.streams[streamKey(st.Name)] = st
		s.mu.Unlock()
		writeQueryOK(w, []string{"status"},
			[][]string{{fmt.Sprintf("Stream %s successfully created.", st.Name)}}, "duckdb")
		return true
	}

	if m := reDropStream.FindStringSubmatch(trimmed); m != nil {
		s.mu.Lock()
		delete(s.streams, streamKey(m[1]))
		s.mu.Unlock()
		writeQueryOK(w, []string{"status"},
			[][]string{{fmt.Sprintf("%s successfully dropped.", m[1])}}, "duckdb")
		return true
	}

	if reShowStreams.MatchString(trimmed) {
		s.mu.Lock()
		rows := make([][]string, 0, len(s.streams))
		for _, st := range s.streams {
			rows = append(rows, []string{st.Name, st.Table, "DEFAULT", strconv.FormatInt(st.Offset, 10)})
		}
		s.mu.Unlock()
		sort.Slice(rows, func(i, j int) bool { return rows[i][0] < rows[j][0] })
		writeQueryOK(w, []string{"name", "table_name", "mode", "offset"}, rows, "duckdb")
		return true
	}
	return false
}

// expandStreams rewrites a reference to a stream into the rows it owes, plus
// the METADATA$ columns Snowflake carries. It also answers
// SYSTEM$STREAM_HAS_DATA, which is how a task asks whether it has work.
func (s *Server) expandStreams(sqlText string) (string, error) {
	out := reStreamHasData.ReplaceAllStringFunc(sqlText, func(m string) string {
		name := reStreamHasData.FindStringSubmatch(m)[1]
		has, err := s.streamHasData(name)
		if err != nil {
			return "NULL"
		}
		return strconv.FormatBool(has)
	})

	s.mu.Lock()
	names := make([]string, 0, len(s.streams))
	for k := range s.streams {
		names = append(names, k)
	}
	s.mu.Unlock()
	// Longest first: a stream called ORDERS must not be substituted inside a
	// reference to ORDERS_WEB.
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })

	for _, key := range names {
		s.mu.Lock()
		st := s.streams[key]
		s.mu.Unlock()
		re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(st.Name) + `\b`)
		if !re.MatchString(out) {
			continue
		}
		if err := s.checkGuard(st); err != nil {
			return "", err
		}
		sub := fmt.Sprintf(
			`(SELECT t.*, 'INSERT' AS "METADATA$ACTION", false AS "METADATA$ISUPDATE", `+
				`t.rowid::VARCHAR AS "METADATA$ROW_ID" FROM %s t WHERE t.rowid >= %d)`,
			st.Table, st.Offset)
		// ReplaceAllLiteralString, not ReplaceAllString. The substitution
		// contains METADATA$ACTION and friends, and `$ACTION` in a Go
		// replacement is a capture-group reference -- it expanded to nothing,
		// so the columns vanished and the engine answered `Referenced column
		// "METADATA$ACTION" not found` for a column this very string defines.
		out = outsideLiterals(out, func(part string) string {
			return re.ReplaceAllLiteralString(part, sub)
		})
	}
	return out, nil
}

// checkGuard refuses a stream whose already-accounted rows have moved.
func (s *Server) checkGuard(st *stream) error {
	_, guard, err := s.mark(st.Table, st.Offset)
	if err != nil {
		return err
	}
	if guard != st.Guard {
		return fmt.Errorf(
			"stream %s cannot be read: rows in %s before its offset have been updated or "+
				"deleted, and this emulator tracks appends only. Snowflake would report those "+
				"as DELETE and INSERT rows; answering without them would silently drop the "+
				"change. Recreate the stream to start from here", st.Name, st.Table)
	}
	return nil
}

func (s *Server) streamHasData(name string) (bool, error) {
	s.mu.Lock()
	st, ok := s.streams[streamKey(name)]
	s.mu.Unlock()
	if !ok {
		return false, fmt.Errorf("Stream %s does not exist", name)
	}
	n, err := s.scalar(fmt.Sprintf("SELECT count(*) FROM %s WHERE rowid >= %d", st.Table, st.Offset))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(n) != "0", nil
}

// advanceStreams moves every stream a DML statement consumed, which is when
// Snowflake advances one: reading a stream in a SELECT shows the same rows
// again, consuming it in DML does not.
func (s *Server) advanceStreams(original string) {
	if !reIsDML.MatchString(strings.TrimSpace(original)) {
		return
	}
	s.mu.Lock()
	var touched []*stream
	for _, st := range s.streams {
		if regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(st.Name) + `\b`).MatchString(original) {
			touched = append(touched, st)
		}
	}
	s.mu.Unlock()
	for _, st := range touched {
		off, guard, err := s.mark(st.Table, -1)
		if err != nil {
			continue
		}
		s.mu.Lock()
		st.Offset, st.Guard = off, guard
		s.mu.Unlock()
	}
}
