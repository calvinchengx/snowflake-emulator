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
type Result struct {
	Columns  []string
	Rows     [][]string
	Dialect  string
	RowCount int
}

func MissingAttachError() error {
	return fmt.Errorf("no engine attached: set SNOWFLAKE_DUCKDB_PATH to :memory: or a DuckDB file")
}

func Exec(duckdbPath, sql string) (Result, error) {
	if strings.TrimSpace(duckdbPath) == "" {
		return Result{}, MissingAttachError()
	}
	bin, err := exec.LookPath("duckdb")
	if err != nil {
		return Result{}, fmt.Errorf("duckdb binary not on PATH (SNOWFLAKE_DUCKDB_PATH=%s): %w", duckdbPath, err)
	}
	args := []string{"-json", "-c", sql}
	if duckdbPath != ":memory:" {
		args = append([]string{duckdbPath}, args...)
	}
	cmd := exec.Command(bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return Result{}, fmt.Errorf("duckdb: %s", msg)
	}
	out := bytes.TrimSpace(stdout.Bytes())
	if len(out) == 0 {
		return Result{Dialect: "duckdb", Columns: []string{}, Rows: nil}, nil
	}
	var parsed []map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		return Result{Dialect: "duckdb", Columns: []string{"result"}, Rows: [][]string{{string(out)}}}, nil
	}
	if len(parsed) == 0 {
		return Result{Dialect: "duckdb"}, nil
	}
	cols := make([]string, 0, len(parsed[0]))
	for k := range parsed[0] {
		cols = append(cols, k)
	}
	rows := make([][]string, 0, len(parsed))
	for _, rec := range parsed {
		row := make([]string, len(cols))
		for i, c := range cols {
			row[i] = fmt.Sprint(rec[c])
		}
		rows = append(rows, row)
	}
	return Result{Columns: cols, Rows: rows, Dialect: "duckdb", RowCount: len(rows)}, nil
}
