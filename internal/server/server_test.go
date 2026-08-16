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
