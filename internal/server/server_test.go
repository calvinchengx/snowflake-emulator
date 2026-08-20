package server

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/calvinchengx/snowflake-emulator/internal/config"
)

func TestLoginRejectsDevAndEmpty(t *testing.T) {
	dir := t.TempDir()
	srv, err := New(config.Config{DataDir: dir, StageDir: filepath.Join(dir, "stages")})
	if err != nil {
		t.Fatal(err)
	}
	for _, pass := range []string{"", "dev", "nope"} {
		body := `{"data":{"LOGIN_NAME":"admin","PASSWORD":"` + pass + `"}}`
		req := httptest.NewRequest(http.MethodPost, "/session/v1/login-request", strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("password %q: status %d", pass, rec.Code)
		}
	}
}

func TestLoginSeededPAT(t *testing.T) {
	dir := t.TempDir()
	srv, err := New(config.Config{DataDir: dir, StageDir: filepath.Join(dir, "stages")})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"data":{"LOGIN_NAME":"admin","PASSWORD":"` + srv.PAT + `"}}`
	req := httptest.NewRequest(http.MethodPost, "/session/v1/login-request", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["success"] != true {
		t.Fatal(out)
	}
}

func TestLoginGzipPAT(t *testing.T) {
	dir := t.TempDir()
	srv, err := New(config.Config{DataDir: dir, StageDir: filepath.Join(dir, "stages")})
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte(`{"data":{"LOGIN_NAME":"admin","PASSWORD":"` + srv.PAT + `"}}`)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(plain)
	_ = zw.Close()
	req := httptest.NewRequest(http.MethodPost, "/session/v1/login-request", bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Encoding", "gzip")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
}

func TestQueryNamesMissingEngine(t *testing.T) {
	dir := t.TempDir()
	srv, err := New(config.Config{DataDir: dir, StageDir: filepath.Join(dir, "stages")})
	if err != nil {
		t.Fatal(err)
	}
	tok := login(t, srv)
	req := httptest.NewRequest(http.MethodPost, "/queries/v1/query-request", strings.NewReader(`{"sqlText":"SELECT 1"}`))
	req.Header.Set("Authorization", `Snowflake Token="`+tok+`"`)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	b, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(b), "SNOWFLAKE_DUCKDB_PATH") {
		t.Fatalf("expected attach name, got %s", b)
	}
}

func TestIcebergNamesMissingPolaris(t *testing.T) {
	dir := t.TempDir()
	srv, err := New(config.Config{DataDir: dir, StageDir: filepath.Join(dir, "stages"), DuckDB: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	tok := login(t, srv)
	req := httptest.NewRequest(http.MethodPost, "/queries/v1/query-request", strings.NewReader(`{"sqlText":"CREATE ICEBERG TABLE t (id INT)"}`))
	req.Header.Set("Authorization", `Snowflake Token="`+tok+`"`)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	b, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(b), "SNOWFLAKE_POLARIS_URL") {
		t.Fatalf("expected polaris name, got %s", b)
	}
}

func TestAdminPatFile(t *testing.T) {
	dir := t.TempDir()
	srv, err := New(config.Config{DataDir: dir, StageDir: filepath.Join(dir, "stages")})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "admin.pat"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != srv.PAT {
		t.Fatal("pat file mismatch")
	}
}

func login(t *testing.T, srv *Server) string {
	t.Helper()
	body := `{"data":{"LOGIN_NAME":"admin","PASSWORD":"` + srv.PAT + `"}}`
	req := httptest.NewRequest(http.MethodPost, "/session/v1/login-request", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var out struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Data.Token == "" {
		t.Fatal(rec.Body.String())
	}
	return out.Data.Token
}

func TestSnowflakeTypeCarriesPrecisionAndScale(t *testing.T) {
	// The whole point. A client converts VALUES BY TYPE, so "text" for every
	// column is why 52 gold contracts answered `'0' is not of type 'integer'`
	// instead of pass or fail.
	cases := []struct {
		duck             string
		kind             string
		precision, scale int
	}{
		{"INTEGER", "fixed", 38, 0},
		{"BIGINT", "fixed", 38, 0},
		{"HUGEINT", "fixed", 38, 0},
		// The one that has already cost this family a money column on
		// another engine: scale must survive, or decimal(19,4) is a float.
		{"DECIMAL(19,4)", "fixed", 19, 4},
		{"DECIMAL(38,0)", "fixed", 38, 0},
		{"NUMERIC(10,2)", "fixed", 10, 2},
		{"DOUBLE", "real", 0, 0},
		{"FLOAT", "real", 0, 0},
		{"BOOLEAN", "boolean", 0, 0},
		{"DATE", "date", 0, 0},
		// The scale is not decoration: the connector splits seconds from the
		// fraction using it, so a fractional value declared scale 0 reads
		// back with the fraction lost.
		{"TIMESTAMP", "timestamp_ntz", 0, temporalScale},
		{"TIME", "time", 0, temporalScale},
		{"VARCHAR", "text", 0, 0},
		{"VARCHAR(50)", "text", 0, 0},
		{"BLOB", "binary", 0, 0},
		// Unknown is text rather than a guess: a wrong type changes results,
		// and text is the one answer that cannot.
		{"STRUCT(a INT)", "text", 0, 0},
		{"", "text", 0, 0},
	}
	for _, c := range cases {
		kind, p, s := snowflakeType(c.duck)
		if kind != c.kind || p != c.precision || s != c.scale {
			t.Errorf("%s -> (%s,%d,%d), want (%s,%d,%d)",
				c.duck, kind, p, s, c.kind, c.precision, c.scale)
		}
	}
}

func TestColumnAtFindsByNameNotPosition(t *testing.T) {
	cols := []string{"column_name", "column_type", "null", "key", "default", "extra"}
	if got := columnAt(cols, "column_type"); got != 1 {
		t.Fatalf("column_type at %d", got)
	}
	if got := columnAt(cols, "COLUMN_NAME"); got != 0 {
		t.Fatalf("case-insensitive lookup failed: %d", got)
	}
	if got := columnAt(cols, "nope"); got != -1 {
		t.Fatalf("a missing column must be -1, got %d", got)
	}
	// -1 must read as empty rather than panic: a caller that asked for a
	// column the engine did not return should get nothing, not a crash.
	if got := cell([]*string{nil}, -1); got != "" {
		t.Fatalf("cell(-1) = %q", got)
	}
}

func TestShowObjectsReportsTheNameSnowflakeWouldReport(t *testing.T) {
	// An unquoted CREATE TABLE silver_customers makes SILVER_CUSTOMERS on
	// Snowflake. DuckDB keeps the case it was given, and this listing is how a
	// client learns what exists -- so dbt-snowflake searched for
	// TEST_DB.PUBLIC.SILVER_CUSTOMERS, found "silver_customers", and REFUSED
	// TO GUESS: `dbt found an approximate match ... Please delete or rename
	// it`. Every model that rebuilt an existing table failed to compile.
	if got := strings.ToUpper("silver_customers"); got != "SILVER_CUSTOMERS" {
		t.Fatalf("the case this listing must report: %q", got)
	}
}
