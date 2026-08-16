package config

import "os"

type Config struct {
	Addr      string
	DataDir   string
	PublicURL string
	DuckDB    string // empty = not attached; ":memory:" or a file path
	StageDir  string
	PolarisURL string
}

func FromEnv() Config {
	c := Config{
		Addr:       getenv("SNOWFLAKE_ADDR", "127.0.0.1:8448"),
		DataDir:    getenv("SNOWFLAKE_DATA_DIR", "./data"),
		PublicURL:  getenv("SNOWFLAKE_PUBLIC_URL", "http://127.0.0.1:8448"),
		DuckDB:     os.Getenv("SNOWFLAKE_DUCKDB_PATH"),
		StageDir:   getenv("SNOWFLAKE_STAGE_DIR", "./stages"),
		PolarisURL: os.Getenv("SNOWFLAKE_POLARIS_URL"),
	}
	return c
}

func getenv(k, d string) string {
	v := os.Getenv(k)
	if v == "" {
		return d
	}
	return v
}
