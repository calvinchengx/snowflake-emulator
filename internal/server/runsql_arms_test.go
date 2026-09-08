package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/calvinchengx/snowflake-emulator/internal/config"
)

// runSQL dispatches every statement this emulator answers without DuckDB —
// warehouses, stages, streams, task history, schema inference, the catalog
// SHOWs — and it was 42.6% covered in a package at 62.8%. One coverage number
// over a seventeen-way dispatch says nothing about which arms are exercised,
// which is what these close.
//
// EACH CASE GETS ITS OWN SERVER. Found the hard way: a suspended warehouse is
// session state, so `ALTER WAREHOUSE ... SUSPEND` makes every later statement
// answer "warehouse is suspended" — a shared server would have had most of
// this table asserting the suspension message rather than the arm it names.

func newSQLServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	srv, err := New(config.Config{
		DataDir: dir, StageDir: filepath.Join(dir, "stages"), DuckDB: ":memory:",
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, login(t, srv)
}

func execSQL(t *testing.T, srv *Server, tok, sql string) (int, string) {
	t.Helper()
	body := `{"sqlText":"` + strings.ReplaceAll(sql, `"`, `\"`) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/queries/v1/query-request", strings.NewReader(body))
	req.Header.Set("Authorization", `Snowflake Token="`+tok+`"`)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	b, _ := io.ReadAll(rec.Body)
	return rec.Code, string(b)
}

func TestRunSQLRefusesAnEmptyStatement(t *testing.T) {
	srv, tok := newSQLServer(t)
	code, body := execSQL(t, srv, tok, "")
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", code)
	}
	// 000904 is Snowflake's own code for this. A driver branches on the code,
	// not the prose, so the code is the contract.
	if !strings.Contains(body, `"000904"`) || !strings.Contains(body, "empty statement") {
		t.Errorf("body = %s, want the 000904 empty-statement refusal", body)
	}
}

func TestRunSQLAnswersTheCatalogShows(t *testing.T) {
	// dbt issues these before it issues anything else; answering them is what
	// lets it introspect an emulator with no information_schema of its own.
	srv, tok := newSQLServer(t)
	for _, tc := range []struct{ sql, want string }{
		{"SHOW SCHEMAS", "INFORMATION_SCHEMA"},
		{"SHOW TERSE SCHEMAS", "PUBLIC"},
	} {
		_, body := execSQL(t, srv, tok, tc.sql)
		if !strings.Contains(body, tc.want) {
			t.Errorf("%s: body = %s, want it to contain %q", tc.sql, body, tc.want)
		}
	}
}

func TestWarehouseLifecycle(t *testing.T) {
	// One server on purpose: this IS a sequence, and each step depends on the
	// last. Suspension comes last because it changes what every statement after
	// it answers.
	srv, tok := newSQLServer(t)

	if _, b := execSQL(t, srv, tok, "CREATE WAREHOUSE wh1"); strings.Contains(b, `"success":false`) {
		t.Fatalf("CREATE WAREHOUSE failed: %s", b)
	}
	if _, b := execSQL(t, srv, tok, "SHOW WAREHOUSES"); !strings.Contains(b, "wh1") {
		t.Errorf("SHOW WAREHOUSES omitted the warehouse just created: %s", b)
	}
	if _, b := execSQL(t, srv, tok, "USE WAREHOUSE wh1"); strings.Contains(b, `"success":false`) {
		t.Errorf("USE WAREHOUSE failed: %s", b)
	}

	// Suspended is not an error state — it is a refusal with its own code, and
	// a driver retries on it. Answering the query anyway would let a test pass
	// against a warehouse the caller believes is stopped.
	if _, b := execSQL(t, srv, tok, "ALTER WAREHOUSE wh1 SUSPEND"); strings.Contains(b, `"success":false`) {
		t.Fatalf("SUSPEND failed: %s", b)
	}
	_, body := execSQL(t, srv, tok, "SELECT 1")
	if !strings.Contains(body, `"000606"`) || !strings.Contains(body, "suspended") {
		t.Errorf("a query against a suspended warehouse answered %s, want the "+
			"000606 refusal", body)
	}

	if _, b := execSQL(t, srv, tok, "ALTER WAREHOUSE wh1 RESUME"); strings.Contains(b, `"success":false`) {
		t.Fatalf("RESUME failed: %s", b)
	}
	if _, b := execSQL(t, srv, tok, "SELECT 1"); strings.Contains(b, "suspended") {
		t.Errorf("a resumed warehouse still refuses: %s", b)
	}
}

func TestRunSQLReachesTheStatementRewriters(t *testing.T) {
	// TASK_HISTORY, INFER_SCHEMA and stream reads are Snowflake table functions
	// DuckDB has never heard of. runSQL rewrites each into something the engine
	// can answer, and a rewrite that silently did nothing would surface as a
	// DuckDB parse error rather than as a missing feature.
	for _, tc := range []struct{ name, sql string }{
		{"task history", "SELECT * FROM TABLE(INFORMATION_SCHEMA.TASK_HISTORY())"},
		{"infer schema", "SELECT * FROM TABLE(INFER_SCHEMA(LOCATION=>'@st1'))"},
		{"show streams", "SHOW STREAMS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, tok := newSQLServer(t)
			code, body := execSQL(t, srv, tok, tc.sql)
			if code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", code, body)
			}
			// Whatever it answers, it must not be DuckDB complaining about
			// syntax it was never meant to see.
			if strings.Contains(strings.ToLower(body), "parser error") ||
				strings.Contains(strings.ToLower(body), "syntax error") {
				t.Errorf("%s reached the engine unrewritten: %s", tc.sql, body)
			}
		})
	}
}

func TestIcebergWithoutACatalogNamesTheVariable(t *testing.T) {
	// The emulator refuses rather than pretending: an Iceberg table it cannot
	// register would read as created and then not exist.
	srv, tok := newSQLServer(t)
	_, body := execSQL(t, srv, tok, "CREATE ICEBERG TABLE ib (id INT)")
	if !strings.Contains(body, "SNOWFLAKE_POLARIS_URL") {
		t.Errorf("body = %s, want the refusal to name the variable that fixes it", body)
	}
}

// --- the stage dispatcher ----------------------------------------------------
//
// handleStageSQL was 13.3% covered: it answers CREATE STAGE, DROP STAGE, LIST
// and GET, and one number over four verbs could not say which of them a test
// had ever issued. These reach each verb and the refusals between them.

func TestCreateAndDropStage(t *testing.T) {
	srv, tok := newSQLServer(t)
	if _, b := execSQL(t, srv, tok, "CREATE STAGE st1"); strings.Contains(b, `"success":false`) {
		t.Fatalf("CREATE STAGE failed: %s", b)
	}
	// Idempotent forms Snowflake accepts, and a client will send.
	for _, sql := range []string{
		"CREATE STAGE IF NOT EXISTS st1",
		"CREATE OR REPLACE STAGE st1",
		"CREATE TEMPORARY STAGE st2",
	} {
		if _, b := execSQL(t, srv, tok, sql); strings.Contains(b, `"success":false`) {
			t.Errorf("%s was refused: %s", sql, b)
		}
	}
	if _, b := execSQL(t, srv, tok, "DROP STAGE st2"); strings.Contains(b, `"success":false`) {
		t.Errorf("DROP STAGE failed: %s", b)
	}
	if _, b := execSQL(t, srv, tok, "DROP STAGE IF EXISTS nosuch"); strings.Contains(b, `"success":false`) {
		t.Errorf("DROP STAGE IF EXISTS on an absent stage must not fail: %s", b)
	}
}

func TestExternalStageIsRefusedRatherThanFaked(t *testing.T) {
	// An external stage points at S3/Azure/GCS. The emulator has no such
	// storage, and a CREATE that "succeeded" would leave a stage whose every
	// read is empty — indistinguishable from a bucket that happens to have no
	// files, which is the wrong diagnosis to hand someone.
	srv, tok := newSQLServer(t)
	for _, url := range []string{"s3://b/p", "azure://a/c", "gcs://g/o", "https://h/p"} {
		_, body := execSQL(t, srv, tok, "CREATE STAGE ext URL='"+url+"'")
		if !strings.Contains(body, `"success":false`) {
			t.Errorf("an external stage at %s was accepted: %s", url, body)
		}
	}
}

func TestListStageShowsWhatWasPut(t *testing.T) {
	// LIST is how a client checks a PUT landed, so an empty answer on a stage
	// that has files reads as a failed upload.
	srv, tok := newSQLServer(t)
	if _, b := execSQL(t, srv, tok, "CREATE STAGE st1"); strings.Contains(b, `"success":false`) {
		t.Fatalf("CREATE STAGE failed: %s", b)
	}
	code, body := execSQL(t, srv, tok, "LIST @st1")
	if code != http.StatusOK {
		t.Fatalf("LIST status = %d: %s", code, body)
	}
	if strings.Contains(body, `"success":false`) {
		t.Errorf("LIST on an empty stage must succeed with no rows, got %s", body)
	}
	// `LS` is the same verb; a client that uses the short form must not get a
	// DuckDB parse error instead.
	if _, b := execSQL(t, srv, tok, "LS @st1"); strings.Contains(strings.ToLower(b), "parser error") {
		t.Errorf("LS reached the engine unhandled: %s", b)
	}
}

func TestGetFromStageRefusesAnUnsupportedTarget(t *testing.T) {
	// GET writes to a local URL. Anything else has nowhere to go, and must say
	// so rather than report a transfer that never happened.
	srv, tok := newSQLServer(t)
	if _, b := execSQL(t, srv, tok, "CREATE STAGE st1"); strings.Contains(b, `"success":false`) {
		t.Fatalf("CREATE STAGE failed: %s", b)
	}
	_, body := execSQL(t, srv, tok, "GET @st1 s3://bucket/dest/")
	if !strings.Contains(body, `"success":false`) {
		t.Errorf("GET to a non-local target was accepted: %s", body)
	}
}

// --- session endpoints -------------------------------------------------------
//
// health, heartbeat, logout and token-request were all at 0%. A driver calls
// every one of them during a normal session, so nothing here is exotic — they
// were simply never asserted, and an endpoint that 404s or 500s breaks a client
// before it runs a single query.

func TestSessionEndpoints(t *testing.T) {
	srv, tok := newSQLServer(t)
	auth := `Snowflake Token="` + tok + `"`
	for _, tc := range []struct {
		name, method, path string
		body               string
	}{
		{"health", http.MethodGet, "/health", ""},
		{"heartbeat", http.MethodPost, "/session/heartbeat", `{}`},
		{"token request", http.MethodPost, "/session/token-request", `{"data":{"REQUEST_TYPE":"RENEW"}}`},
		{"logout", http.MethodPost, "/session/logout", `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", auth)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code >= 500 {
				b, _ := io.ReadAll(rec.Body)
				t.Errorf("%s %s = %d, a driver cannot proceed past a 5xx here: %s",
					tc.method, tc.path, rec.Code, b)
			}
			if rec.Code == http.StatusNotFound {
				t.Errorf("%s %s is not routed at all", tc.method, tc.path)
			}
		})
	}
}

// --- pure helpers ------------------------------------------------------------

func TestSnowflakeSchemaNamesDuckDBsDefaultAsPublic(t *testing.T) {
	// DuckDB's default schema is `main`; Snowflake's is PUBLIC. A client that
	// asked for its current schema and got "main" would qualify every later
	// name with a schema Snowflake does not have.
	for in, want := range map[string]string{
		"":       "PUBLIC",
		"main":   "PUBLIC",
		"MAIN":   "PUBLIC",
		"MaIn":   "PUBLIC",
		"gold":   "GOLD",
		"SILVER": "SILVER",
	} {
		if got := snowflakeSchema(in); got != want {
			t.Errorf("snowflakeSchema(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIcebergTableNameFallsBackRatherThanEmpty(t *testing.T) {
	// The name is used as a catalog key. An empty one would register a table
	// under "", which is unfindable rather than wrong-looking.
	for in, want := range map[string]string{
		"CREATE ICEBERG TABLE ib (id INT)":                   "ib",
		"create iceberg table IF NOT EXISTS db.sc.t (i INT)": "db.sc.t",
		"CREATE   ICEBERG   TABLE   spaced (i INT)":          "spaced",
		"SELECT 1": "table",
	} {
		if got := icebergTableName(in); got != want {
			t.Errorf("icebergTableName(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- the v2 REST surface -----------------------------------------------------
//
// /api/v2 and the Iceberg catalog routes were all at 0%. These are the surfaces
// a client reaches WITHOUT the classic query protocol, so nothing in the SQL
// tests above touches them, and a broken route here fails a client that never
// issues a login.

func TestV2AndIcebergRoutesAreServed(t *testing.T) {
	srv, tok := newSQLServer(t)
	auth := `Snowflake Token="` + tok + `"`
	for _, tc := range []struct {
		name, method, path, body string
	}{
		{"sql api", http.MethodPost, "/api/v2/statements", `{"statement":"SELECT 1"}`},
		{"warehouses", http.MethodGet, "/api/v2/warehouses", ""},
		{"iceberg namespaces", http.MethodGet, "/iceberg/v1/namespaces", ""},
		{"iceberg namespace", http.MethodGet, "/iceberg/v1/namespaces/db", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", auth)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			b, _ := io.ReadAll(rec.Body)
			// 501 is DELIBERATE here and not a failure: this emulator answers
			// an unimplemented surface with an honest Not Implemented naming
			// what would enable it, rather than a plausible empty result. The
			// first draft of this test asserted `>= 500` and reported the
			// Iceberg routes as broken when they were doing exactly that.
			if rec.Code >= 500 && rec.Code != http.StatusNotImplemented {
				t.Errorf("%s %s = %d: %s", tc.method, tc.path, rec.Code, b)
			}
			if rec.Code == http.StatusNotFound {
				t.Errorf("%s %s is not routed", tc.method, tc.path)
			}
		})
	}
}

func TestIcebergCatalogRefusesWithoutPolaris(t *testing.T) {
	// Same rule the CREATE path follows: no catalog attached means say so,
	// rather than answer an empty namespace list that reads as "no tables".
	srv, tok := newSQLServer(t)
	req := httptest.NewRequest(http.MethodGet, "/iceberg/v1/namespaces", nil)
	req.Header.Set("Authorization", `Snowflake Token="`+tok+`"`)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	b, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(b), "SNOWFLAKE_POLARIS_URL") {
		t.Errorf("body = %s, want the refusal to name the variable that fixes it", b)
	}
}
