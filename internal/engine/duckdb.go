package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Result is one statement's rows. Dialect is always duckdb when this engine ran.
//
// Rows holds *string so that SQL NULL can survive as nil rather than becoming
// some spelling of the word. Types carries DuckDB's own type name per column
// -- "INTEGER", "DECIMAL(19,4)" -- and is what lets the server answer a
// Snowflake rowtype instead of calling everything text.
type Result struct {
	Columns  []string
	Types    []string
	Rows     [][]*string
	Dialect  string
	RowCount int
}

func MissingAttachError() error {
	return fmt.Errorf("no engine attached: set SNOWFLAKE_DUCKDB_PATH to :memory: or a DuckDB file")
}

func Exec(duckdbPath, sql string) (Result, error) {
	out, err := run(duckdbPath, sql)
	if err != nil {
		return Result{}, err
	}
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		// NO ROWS IS NOT NO COLUMNS. duckdb prints nothing at all for a SELECT
		// that matched nothing, exactly as it does for a CREATE TABLE, so the
		// bytes cannot tell them apart -- and the server renders a result with
		// no columns as `status: ok`, one row. An empty table therefore
		// answered SELECT * with a single row saying "ok", which a client
		// counts as data.
		//
		// DESCRIBE separates them: it answers for a query whose result is
		// empty, and refuses for a statement that has no result at all.
		if describable(sql) {
			if cols, types, ok := describeShape(duckdbPath, sql); ok {
				return Result{Dialect: "duckdb", Columns: cols, Types: types, Rows: [][]*string{}}, nil
			}
		}
		return Result{Dialect: "duckdb", Columns: []string{}}, nil
	}
	cols, rows, perr := decodeRows(out)
	if perr != nil {
		// Not the JSON we expected. Hand back what duckdb said rather than
		// pretending it parsed, which is how a diagnostic reaches a caller.
		raw := string(out)
		return Result{Dialect: "duckdb", Columns: []string{"result"}, Types: []string{"VARCHAR"},
			Rows: [][]*string{{&raw}}, RowCount: 1}, nil
	}
	if len(cols) == 0 {
		return Result{Dialect: "duckdb"}, nil
	}
	return Result{
		Columns:  cols,
		Types:    columnTypes(duckdbPath, sql, cols, rows),
		Rows:     rows,
		Dialect:  "duckdb",
		RowCount: len(rows),
	}, nil
}

func run(duckdbPath, sql string) ([]byte, error) {
	if strings.TrimSpace(duckdbPath) == "" {
		return nil, MissingAttachError()
	}
	bin, err := exec.LookPath("duckdb")
	if err != nil {
		return nil, fmt.Errorf("duckdb binary not on PATH (SNOWFLAKE_DUCKDB_PATH=%s): %w", duckdbPath, err)
	}
	args := []string{"-json", "-c", preludeFor(sql)}
	if duckdbPath != ":memory:" {
		args = append([]string{duckdbPath}, args...)
	}
	cmd := exec.Command(bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = os.Environ()
	runErr := cmd.Run()
	if err := failed(stderr.Bytes(), runErr); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

// failed decides whether duckdb refused, WITHOUT TRUSTING ITS EXIT CODE.
//
// The pinned duckdb CLI (v1.2.2, the zip this image installs) exits 0 on a SQL
// error. It writes the diagnosis to stderr and stops, so `cmd.Run()` returns
// nil, stdout is empty, and a caller that believed the status reported an
// empty result -- which this server renders as `status: ok`. Measured in the
// shipped container:
//
//	duckdb -json -c "THIS IS NOT SQL"   ->  exit 0, stderr "Parser Error: ..."
//
// so `THIS IS NOT SQL AT ALL` came back over the API as success:true with
// rowset [["ok"]]. A silent 200, which is the one thing this emulator's
// doctrine forbids -- and it hid EVERY unsupported statement behind a pass:
// CREATE TASK, time travel, TO_DATE, all "fine".
//
// It is version-dependent, which is what made it survive: the same binary
// built for a developer's machine (homebrew, newer) exits 1, so a probe run
// on the host answered honestly while the released image did not. Two builds
// were compared and the difference was credited to the code.
//
// stderr is therefore the signal. duckdb writes nothing there for a statement
// that worked, including DDL and a SELECT matching no rows -- both of which
// produce empty stdout too, and are exactly what an exit code could not tell
// apart from a refusal.
func failed(stderr []byte, runErr error) error {
	msg := strings.TrimSpace(string(stderr))
	if msg != "" {
		return fmt.Errorf("duckdb: %s", msg)
	}
	if runErr != nil {
		return fmt.Errorf("duckdb: %s", runErr.Error())
	}
	return nil
}

// decodeRows reads duckdb's `-json` array while KEEPING THE COLUMN ORDER, and
// keeps every scalar as the text duckdb wrote.
//
// Both halves replace a `[]map[string]any` that had four defects at once, all
// of them the map's and all measured against a running emulator:
//
//   - ORDER. `for k := range parsed[0]` is Go map iteration, which is
//     randomised per run. `SELECT 1 AS alpha, 2 AS bravo, 3 AS charlie,
//     4 AS delta` came back in four different column orders across twelve
//     identical requests. A caller reading by name never noticed; a caller
//     reading data[0][0] -- which is how the platform reads its own gold
//     aggregates -- silently got another column's number. Worse, this file
//     itself read `DESCRIBE` positionally, so DESCRIBE TABLE silver_orders
//     answered ['order_id','INTEGER'] twice in eight tries and
//     ['<nil>','<nil>'] or ['INTEGER','YES'] the rest.
//
//   - PRECISION. json.Unmarshal into `any` makes every number a float64, so
//     9223372036854775807 went out as 9.223372036854776e+18. UseNumber keeps
//     the digits duckdb printed.
//
//   - NULL. fmt.Sprint(nil) is "<nil>", and that string was the answer to
//     SELECT NULL. A nil *string here becomes JSON null on the wire.
//
//   - TYPE. A map says nothing about types; see columnTypes.
func decodeRows(b []byte) ([]string, [][]*string, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if t, err := dec.Token(); err != nil || t != json.Delim('[') {
		return nil, nil, fmt.Errorf("expected a JSON array")
	}
	var cols []string
	var rows [][]*string
	for dec.More() {
		keys, vals, err := decodeObject(dec)
		if err != nil {
			return nil, nil, err
		}
		if cols == nil {
			cols = keys
		}
		row := make([]*string, len(cols))
		for i, c := range cols {
			if v, ok := vals[c]; ok {
				row[i] = v
			}
		}
		rows = append(rows, row)
	}
	return cols, rows, nil
}

func decodeObject(dec *json.Decoder) ([]string, map[string]*string, error) {
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, nil, fmt.Errorf("expected a JSON object")
	}
	var keys []string
	vals := map[string]*string{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		key, ok := kt.(string)
		if !ok {
			return nil, nil, fmt.Errorf("expected an object key")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, nil, err
		}
		keys = append(keys, key)
		vals[key] = scalar(raw)
	}
	if _, err := dec.Token(); err != nil { // closing '}'
		return nil, nil, err
	}
	return keys, vals, nil
}

// scalar renders one JSON value as the text Snowflake's wire format carries.
// nil means SQL NULL. Nested values keep their JSON spelling, which is what a
// STRUCT or LIST column looks like to a client that asked for text.
func scalar(raw json.RawMessage) *string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "null" {
		return nil
	}
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return &s
		}
	}
	return &trimmed
}

// columnTypes names each column's DuckDB type.
//
// DESCRIBE, for the statements that have a describable shape, because it is
// the only source that can tell DECIMAL(19,4) from a VARCHAR holding
// "1.5000" -- duckdb's `-json` writes both as JSON strings, and this family
// has already lost a money column once to a type that was merely plausible.
// Everything else infers from what the values are, which is exact for the
// numbers, booleans and nulls that JSON distinguishes.
func columnTypes(duckdbPath, sql string, cols []string, rows [][]*string) []string {
	types := inferTypes(cols, rows)
	if !describable(sql) {
		return types
	}
	dcols, dtypes, ok := describeShape(duckdbPath, sql)
	if !ok {
		return types
	}
	byName := map[string]string{}
	for i, c := range dcols {
		byName[c] = dtypes[i]
	}
	for i, c := range cols {
		if t, ok := byName[c]; ok {
			types[i] = t
		}
	}
	return types
}

// describeShape asks duckdb for a query's columns and their types without
// running it. ok is false when the statement has no describable shape.
func describeShape(duckdbPath, sql string) ([]string, []string, bool) {
	out, err := run(duckdbPath, "DESCRIBE "+sql)
	if err != nil {
		return nil, nil, false
	}
	dcols, drows, err := decodeRows(bytes.TrimSpace(out))
	if err != nil {
		return nil, nil, false
	}
	name, typ := indexOf(dcols, "column_name"), indexOf(dcols, "column_type")
	if name < 0 || typ < 0 {
		return nil, nil, false
	}
	cols := make([]string, 0, len(drows))
	types := make([]string, 0, len(drows))
	for _, r := range drows {
		if name < len(r) && typ < len(r) && r[name] != nil && r[typ] != nil {
			cols = append(cols, *r[name])
			types = append(types, *r[typ])
		}
	}
	if len(cols) == 0 {
		return nil, nil, false
	}
	return cols, types, true
}

func describable(sql string) bool {
	s := strings.ToUpper(strings.TrimSpace(sql))
	return strings.HasPrefix(s, "SELECT") || strings.HasPrefix(s, "WITH") || strings.HasPrefix(s, "(")
}

// inferTypes reads the WHOLE column rather than its first row. A column whose
// first value is NULL says nothing about the rest, and one that starts with an
// integer can still hold a fraction further down.
func inferTypes(cols []string, rows [][]*string) []string {
	types := make([]string, len(cols))
	for i := range cols {
		kind := ""
		for _, r := range rows {
			if i >= len(r) || r[i] == nil {
				continue
			}
			k := kindOf(*r[i])
			switch {
			case kind == "":
				kind = k
			case kind == k:
			case kind == "BIGINT" && k == "DOUBLE":
				kind = "DOUBLE"
			case kind == "DOUBLE" && k == "BIGINT":
			default:
				kind = "VARCHAR"
			}
		}
		if kind == "" {
			kind = "VARCHAR"
		}
		types[i] = kind
	}
	return types
}

func kindOf(v string) string {
	switch v {
	case "true", "false":
		return "BOOLEAN"
	}
	if v == "" {
		return "VARCHAR"
	}
	seenDigit, seenDot, seenExp := false, false, false
	for i, c := range v {
		switch {
		case c >= '0' && c <= '9':
			seenDigit = true
		case (c == '-' || c == '+') && (i == 0 || v[i-1] == 'e' || v[i-1] == 'E'):
		case c == '.' && !seenDot && !seenExp:
			seenDot = true
		case (c == 'e' || c == 'E') && seenDigit && !seenExp:
			seenExp = true
		default:
			return "VARCHAR"
		}
	}
	if !seenDigit {
		return "VARCHAR"
	}
	if seenDot || seenExp {
		return "DOUBLE"
	}
	return "BIGINT"
}

func indexOf(ss []string, want string) int {
	for i, s := range ss {
		if strings.EqualFold(s, want) {
			return i
		}
	}
	return -1
}
