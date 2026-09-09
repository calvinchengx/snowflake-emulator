package config

import "testing"

// FromEnv is the whole configuration surface and had no test at all: every
// value the emulator runs on comes through here, and a key read under the
// wrong name reads as "the operator did not set it" rather than as a mistake.
//
// The distinction the table below turns on is DEFAULTED vs OPTIONAL. Addr,
// DataDir, PublicURL and StageDir have working defaults because the emulator
// must start with no configuration at all. DuckDB, StageClientDir and
// PolarisURL do NOT: each empty value means a feature is off, and inventing a
// default for them would attach an engine, a client path or a catalog nobody
// asked for.

func TestFromEnvDefaultsWhereStartingMattersAndNotElsewhere(t *testing.T) {
	for _, k := range []string{
		"SNOWFLAKE_ADDR", "SNOWFLAKE_DATA_DIR", "SNOWFLAKE_PUBLIC_URL",
		"SNOWFLAKE_DUCKDB_PATH", "SNOWFLAKE_STAGE_DIR",
		"SNOWFLAKE_STAGE_CLIENT_DIR", "SNOWFLAKE_POLARIS_URL",
	} {
		t.Setenv(k, "")
	}
	c := FromEnv()
	for _, tc := range []struct{ name, got, want string }{
		{"Addr", c.Addr, "127.0.0.1:8448"},
		{"DataDir", c.DataDir, "./data"},
		{"PublicURL", c.PublicURL, "http://127.0.0.1:8448"},
		{"StageDir", c.StageDir, "./stages"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q with no environment, want the default %q",
				tc.name, tc.got, tc.want)
		}
	}
	// These three stay empty on purpose: empty is "off", and a default would
	// silently turn a feature on.
	for _, tc := range []struct{ name, got string }{
		{"DuckDB", c.DuckDB},
		{"StageClientDir", c.StageClientDir},
		{"PolarisURL", c.PolarisURL},
	} {
		if tc.got != "" {
			t.Errorf("%s = %q with no environment, want empty: a default here "+
				"enables a feature nobody configured", tc.name, tc.got)
		}
	}
}

func TestFromEnvReadsEveryKey(t *testing.T) {
	// One distinct value per key, so a field reading the WRONG variable shows
	// up as a swapped value rather than passing because both happened to match.
	env := map[string]string{
		"SNOWFLAKE_ADDR":             "0.0.0.0:9999",
		"SNOWFLAKE_DATA_DIR":         "/d/data",
		"SNOWFLAKE_PUBLIC_URL":       "https://public.example",
		"SNOWFLAKE_DUCKDB_PATH":      "/d/db.duckdb",
		"SNOWFLAKE_STAGE_DIR":        "/d/stages",
		"SNOWFLAKE_STAGE_CLIENT_DIR": "/host/stages",
		"SNOWFLAKE_POLARIS_URL":      "https://polaris.example",
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	c := FromEnv()
	for name, tc := range map[string]struct{ got, want string }{
		"Addr":           {c.Addr, env["SNOWFLAKE_ADDR"]},
		"DataDir":        {c.DataDir, env["SNOWFLAKE_DATA_DIR"]},
		"PublicURL":      {c.PublicURL, env["SNOWFLAKE_PUBLIC_URL"]},
		"DuckDB":         {c.DuckDB, env["SNOWFLAKE_DUCKDB_PATH"]},
		"StageDir":       {c.StageDir, env["SNOWFLAKE_STAGE_DIR"]},
		"StageClientDir": {c.StageClientDir, env["SNOWFLAKE_STAGE_CLIENT_DIR"]},
		"PolarisURL":     {c.PolarisURL, env["SNOWFLAKE_POLARIS_URL"]},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", name, tc.got, tc.want)
		}
	}
}

func TestGetenvTreatsEmptyAsUnset(t *testing.T) {
	// An exported-but-empty variable is how a compose file spells "I did not
	// set this". Honouring it literally would give the emulator an empty
	// listen address rather than its default.
	t.Setenv("SNOWFLAKE_TEST_KEY", "")
	if got := getenv("SNOWFLAKE_TEST_KEY", "fallback"); got != "fallback" {
		t.Errorf("getenv on an empty variable = %q, want the default", got)
	}
	t.Setenv("SNOWFLAKE_TEST_KEY", "set")
	if got := getenv("SNOWFLAKE_TEST_KEY", "fallback"); got != "set" {
		t.Errorf("getenv on a set variable = %q, want the value", got)
	}
}
