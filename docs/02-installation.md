# Installation

## The container

```sh
docker run --rm \
  -e SNOWFLAKE_ADDR=0.0.0.0:8448 \
  -e SNOWFLAKE_DUCKDB_PATH=/data/warehouse.duckdb \
  -e SNOWFLAKE_STAGE_DIR=/stages \
  -v "$PWD/stages:/stages" \
  -p 8448:8448 \
  ghcr.io/calvinchengx/snowflake-emulator:0.1.4
```

`linux/amd64` and `linux/arm64`. The arm64 image arrived in v0.1.2; before it,
`make up` on Apple Silicon failed with `no matching manifest for
linux/arm64/v8`, and the reason was one word — the Dockerfile asked DuckDB for
`duckdb_cli-linux-arm64.zip`, which is a 404, where the project publishes
`duckdb_cli-linux-aarch64.zip`.

## The binary

```sh
go build -o snowflake-emulator ./cmd/snowflake-emulator
```

`duckdb` must be on PATH. **Which duckdb matters**, and this is not a
formality: the CLI the image pins (v1.2.2) exits 0 after refusing a statement,
while a newer build exits 1. An emulator built against one and probed against
the other reports honest failures the shipped image does not give — which is
exactly how a silent success on eighteen constructs survived a day of
measurement. `TestEveryDuckdbPinIsTheSameVersion` now fails if the Dockerfile
and CI ever name different versions.

## The client wheel

`snowflake-target` is published with each release and pins the emulator's
contract for Python consumers:

```toml
snowflake-target = { url = "https://github.com/calvinchengx/snowflake-emulator/releases/download/v0.1.4/snowflake_target-0.1.0-py3-none-any.whl" }
```

**The wheel and the image come from the same release.** A workspace binary and
a client that disagree about the contract is the one mismatch a consumer
repository exists to notice, so a consumer bumps both together and relocks.
