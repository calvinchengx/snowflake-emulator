package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "github.com/snowflakedb/gosnowflake"
)

func main() {
	pat := os.Getenv("SF_PAT")
	host := os.Getenv("SF_HOST")
	port := os.Getenv("SF_PORT")
	if pat == "" || host == "" || port == "" {
		fmt.Fprintln(os.Stderr, "SF_PAT, SF_HOST, SF_PORT required")
		os.Exit(2)
	}
	dsn := fmt.Sprintf(
		"admin:%s@%s:%s/TEST_DB/PUBLIC?account=test&protocol=http&insecureMode=true&ocspFailOpen=true&validateDefaultParameters=false",
		pat, host, port,
	)
	db, err := sql.Open("snowflake", dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		fmt.Printf("PING_ERR %v\n", err)
		os.Exit(0)
	}
	row := db.QueryRow("SELECT 1")
	var n any
	if err := row.Scan(&n); err != nil {
		fmt.Printf("SELECT_ERR %v\n", err)
		os.Exit(0)
	}
	fmt.Printf("SELECT_OK %v\n", n)
}
