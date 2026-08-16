package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"

	"github.com/calvinchengx/snowflake-emulator/internal/config"
	"github.com/calvinchengx/snowflake-emulator/internal/server"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("snowflake-emulator", version)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		cfg := config.FromEnv()
		resp, err := http.Get("http://" + cfg.Addr + "/health")
		if err != nil {
			log.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			log.Fatal(resp.Status)
		}
		return
	}
	cfg := config.FromEnv()
	srv, err := server.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	if srv.Fresh {
		fmt.Printf("seeded admin PAT written to %s/admin.pat (printed once)\n", cfg.DataDir)
		fmt.Printf("  PAT: %s\n", srv.PAT)
	}
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("snowflake-emulator %s listening on http://%s\n", version, cfg.Addr)
	if stringsEmpty(cfg.DuckDB) {
		fmt.Println("no engine: set SNOWFLAKE_DUCKDB_PATH=:memory: (or a file) and put duckdb on PATH")
	}
	log.Fatal(http.Serve(ln, srv))
}

func stringsEmpty(s string) bool { return s == "" }
