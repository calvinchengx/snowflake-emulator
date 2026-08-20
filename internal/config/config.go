package config

import "os"

type Config struct {
	Addr      string
	DataDir   string
	PublicURL string
	DuckDB    string // empty = not attached; ":memory:" or a file path
	StageDir  string
	// StageClientDir is the stage directory AS THE CLIENT SEES IT, and it
	// exists because PUT is the one operation where the bytes are written by
	// the driver rather than by this process. The upload response names a
	// directory and the driver copies into it, so when the emulator runs in a
	// container and the client on the host, the path this process knows
	// (/stages) is not a path the client can write to. Empty means the two
	// agree, which is true for a host binary and for a client in the same
	// container.
	StageClientDir string
	PolarisURL     string
}

func FromEnv() Config {
	c := Config{
		Addr:           getenv("SNOWFLAKE_ADDR", "127.0.0.1:8448"),
		DataDir:        getenv("SNOWFLAKE_DATA_DIR", "./data"),
		PublicURL:      getenv("SNOWFLAKE_PUBLIC_URL", "http://127.0.0.1:8448"),
		DuckDB:         os.Getenv("SNOWFLAKE_DUCKDB_PATH"),
		StageDir:       getenv("SNOWFLAKE_STAGE_DIR", "./stages"),
		StageClientDir: os.Getenv("SNOWFLAKE_STAGE_CLIENT_DIR"),
		PolarisURL:     os.Getenv("SNOWFLAKE_POLARIS_URL"),
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
