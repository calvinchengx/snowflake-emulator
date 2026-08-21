package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/calvinchengx/snowflake-emulator/internal/config"
)

// ARGS carries dbt's own command line, and the argument a medallion needs most
// carries JSON: `run --vars {"a": "b"}`. strings.Fields hands dbt a fragment
// per key and it fails on something that looks nothing like the cause.
func TestDbtArgsRespectQuotes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"run", []string{"run"}},
		{"  test  ", []string{"test"}},
		{`run --vars '{"a": "b", "c": "d"}'`, []string{"run", "--vars", `{"a": "b", "c": "d"}`}},
		{`run --vars "{\"a\": 1}"`, []string{"run", "--vars", `{\a\: 1}`}},
		{"run --select my_model --target prod", []string{"run", "--select", "my_model", "--target", "prod"}},
		// An empty quoted argument is an argument, not nothing: dropping it
		// would shift every flag after it onto the wrong value.
		{"run --select ''", []string{"run", "--select", ""}},
	} {
		if got := dbtArgFields(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q\n  got  %#v\n  want %#v", tc.in, got, tc.want)
		}
	}
}

// ENV_VARS is how a dbt project on Snowflake gets an env_var() answered, and
// the keys are constrained: UPPERCASE and DBT_-prefixed. Accepting one dbt will
// never see is the silent kind of wrong -- env_var() falls to its default and
// the models read the wrong thing without anything failing.
//
// Driven through the HANDLER, not by restating the rule: a test that spells the
// condition out again passes whatever the server does.
func TestEnvVarsKeysAreRefusedByName(t *testing.T) {
	dir := t.TempDir()
	stage := filepath.Join(dir, "stages")
	if err := os.MkdirAll(filepath.Join(stage, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv, err := New(config.Config{DataDir: dir, StageDir: stage, DuckDB: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	tok := login(t, srv)

	create := func(envVars string) map[string]any {
		payload, _ := json.Marshal(map[string]any{
			"sqlText": "CREATE DBT PROJECT p FROM '@~/proj' ENV_VARS = (" + envVars + ")",
		})
		req := httptest.NewRequest(http.MethodPost, "/queries/v1/query-request", bytes.NewReader(payload))
		req.Header.Set("Authorization", `Snowflake Token="`+tok+`"`)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}

	for _, bad := range []string{`bronze_schema = 'x'`, `DBT_lower = 'x'`, `SCHEMA = 'x'`} {
		out := create(bad)
		if ok, _ := out["success"].(bool); ok {
			t.Errorf("ENV_VARS (%s) was accepted; dbt would never see it", bad)
			continue
		}
		if msg, _ := out["message"].(string); !strings.Contains(msg, "DBT_") {
			t.Errorf("the refusal for (%s) does not say what a key must look like: %q", bad, msg)
		}
	}
	if out := create(`DBT_BRONZE_SCHEMA = 'PUBLIC'`); !out["success"].(bool) {
		t.Fatalf("a legal ENV_VARS key was refused: %v", out["message"])
	}
}
