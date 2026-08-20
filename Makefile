# Everyday verbs. Python e2e uses uv + pyproject.toml / uv.lock.

ifeq ($(OS),Windows_NT)
  SHELL := sh.exe
  .SHELLFLAGS := -c
endif

UV ?= uv
PY ?= $(shell if command -v uv >/dev/null 2>&1; then echo "uv run --frozen --no-sync python"; \
	else for c in python3 python py; do if "$$c" -c '' >/dev/null 2>&1; then echo "$$c"; break; fi; done; fi)

.PHONY: help doctor build run test e2e-sdk e2e-sql e2e-dbt e2e-iceberg e2e-snowflake-target clean witnesses

help:
	@grep -hE '^[a-z0-9-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk 'BEGIN{FS=":.*?## "}{printf "  %-22s %s\n", $$1, $$2}'

doctor: ## Check the toolchain
	@command -v go >/dev/null || { echo "go is required" >&2; exit 1; }
	@go version
	@command -v $(UV) >/dev/null || { echo "uv is required for e2e" >&2; exit 1; }

build: ## Compile the binary
	go build -o snowflake-emulator ./cmd/snowflake-emulator

run: build ## Serve HTTP (SNOWFLAKE_DUCKDB_PATH=:memory: to attach DuckDB)
	./snowflake-emulator

test: ## go build, vet and unit tests
	go build ./... && go vet ./... && go test ./...

e2e-sdk: ## Official connectors: auth + SELECT 1 names the missing attach
	@command -v $(UV) >/dev/null || { echo "uv is required" >&2; exit 1; }
	$(UV) run --frozen --group sdk python e2e/sdk/run.py

e2e-sql: ## Official connector SELECT 1 on a warehouse handle with DuckDB named
	@command -v $(UV) >/dev/null || { echo "uv is required" >&2; exit 1; }
	@command -v duckdb >/dev/null || { echo "duckdb is required on PATH" >&2; exit 1; }
	$(UV) run --frozen --group sql python e2e/sql/run.py

e2e-dbt: ## Unmodified dbt-snowflake one + two
	@command -v $(UV) >/dev/null || { echo "uv is required" >&2; exit 1; }
	@command -v duckdb >/dev/null || { echo "duckdb is required on PATH" >&2; exit 1; }
	$(UV) run --frozen --group dbt python e2e/dbt/run.py

e2e-put: ## The driver's own file transfer agent uploads, then COPY INTO reads
	@command -v $(UV) >/dev/null || { echo "uv is required" >&2; exit 1; }
	$(UV) run --frozen --group sql python e2e/put/run.py

e2e-iceberg: ## Iceberg REST lists a table created through the connector
	@command -v $(UV) >/dev/null || { echo "uv is required" >&2; exit 1; }
	@command -v duckdb >/dev/null || { echo "duckdb is required on PATH" >&2; exit 1; }
	$(UV) run --frozen --group sql python e2e/iceberg/run.py

e2e-snowflake-target: ## snowflake-target resolver + SELECT 1
	@command -v $(UV) >/dev/null || { echo "uv is required" >&2; exit 1; }
	@command -v duckdb >/dev/null || { echo "duckdb is required on PATH" >&2; exit 1; }
	$(UV) run --frozen --group target pytest python/snowflake-target/tests -q
	$(UV) run --frozen --group target python e2e/target/run.py

witnesses: ## Verify docs/witnesses.json
	@test -n "$(PY)" || { echo "no working python found" >&2; exit 1; }
	$(PY) scripts/check_witnesses.py

clean:
	rm -f snowflake-emulator snowflake-emulator.exe
	rm -rf data
