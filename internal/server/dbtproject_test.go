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

	// ON EXECUTE, which is where Snowflake overrides a project's environment
	// for a run. It was on CREATE here, which no real account accepts.
	run := func(sqlText string) map[string]any {
		payload, _ := json.Marshal(map[string]any{"sqlText": sqlText})
		req := httptest.NewRequest(http.MethodPost, "/queries/v1/query-request", bytes.NewReader(payload))
		req.Header.Set("Authorization", `Snowflake Token="`+tok+`"`)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	execWith := func(envVars string) map[string]any {
		return run("EXECUTE DBT PROJECT p ARGS='run' ENV_VARS = (" + envVars + ")")
	}

	if out := run("CREATE DBT PROJECT p FROM '@~/proj'"); !out["success"].(bool) {
		t.Fatalf("creating the project failed: %v", out["message"])
	}
	// ENV_VARS on CREATE is refused, and says where it belongs -- storing it
	// there would have it ignored by the statement a caller actually runs.
	out := run("CREATE DBT PROJECT q FROM '@~/proj' ENV_VARS = (DBT_X = 'y')")
	if ok, _ := out["success"].(bool); ok {
		t.Error("ENV_VARS was accepted on CREATE DBT PROJECT")
	} else if msg, _ := out["message"].(string); !strings.Contains(msg, "EXECUTE DBT PROJECT") {
		t.Errorf("the refusal does not say where ENV_VARS belongs: %q", msg)
	}

	for _, bad := range []string{`bronze_schema = 'x'`, `DBT_lower = 'x'`, `SCHEMA = 'x'`} {
		out := execWith(bad)
		if ok, _ := out["success"].(bool); ok {
			t.Errorf("ENV_VARS (%s) was accepted; dbt would never see it", bad)
			continue
		}
		if msg, _ := out["message"].(string); !strings.Contains(msg, "DBT_") {
			t.Errorf("the refusal for (%s) does not say what a key must look like: %q", bad, msg)
		}
	}
	// A legal key gets past the check. The run itself needs dbt, which a unit
	// test has no business installing, so the assertion is that it did NOT fail
	// on the KEY -- the parity probe is what proves the value reaches dbt.
	if msg, _ := execWith(`DBT_BRONZE_SCHEMA = 'PUBLIC'`)["message"].(string); strings.Contains(msg, "ENV_VARS key") {
		t.Errorf("a legal ENV_VARS key was refused: %q", msg)
	}
}
