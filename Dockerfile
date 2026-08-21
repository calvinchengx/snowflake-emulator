# Build: static Go binary + duckdb CLI (the named engine).
FROM golang:1.26 AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /snowflake-emulator ./cmd/snowflake-emulator

FROM debian:bookworm-slim
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl unzip \
    && arch="$TARGETARCH" \
    && if [ "$arch" = "arm64" ]; then duck=linux-aarch64; else duck=linux-amd64; fi \
    && curl -fsSL -o /tmp/duckdb.zip "https://github.com/duckdb/duckdb/releases/download/v1.2.2/duckdb_cli-${duck}.zip" \
    && unzip -o /tmp/duckdb.zip -d /usr/local/bin \
    && chmod +x /usr/local/bin/duckdb \
    && rm -rf /var/lib/apt/lists /tmp/duckdb.zip
# dbt, server-side, because EXECUTE DBT PROJECT is a Snowflake statement:
# `dbt run` happens INSIDE the account, not on a client. The emulator runs it
# the same way it runs duckdb -- a CLI on argv, in this image -- which is the
# execution model this architecture already has, rather than a second service.
#
# FROM THIS REPOSITORY'S OWN LOCK, not a loose pin. `uv sync --frozen` with the
# `dbt` group installs exactly the dbt that e2e/dbt tests with, so the dbt an
# EXECUTE DBT PROJECT runs is the dbt CI measured. A bare `pip install
# dbt-snowflake` would resolve independently and could differ -- the lock names
# dbt-core 2.0.0b1, a prerelease pip would not choose on its own.
COPY --from=ghcr.io/astral-sh/uv:0.12.3 /uv /usr/local/bin/uv
COPY pyproject.toml uv.lock ./
ENV UV_PYTHON_INSTALL_DIR=/opt/python UV_PROJECT_ENVIRONMENT=/opt/dbt
RUN uv sync --frozen --no-dev --group dbt \
    && rm -rf /root/.cache /tmp/* pyproject.toml uv.lock
ENV PATH="/opt/dbt/bin:$PATH"

COPY --from=build /snowflake-emulator /usr/local/bin/snowflake-emulator
RUN mkdir /data /stages && chown 65532:65532 /data /stages
USER 65532:65532
ENV SNOWFLAKE_DATA_DIR=/data
ENV SNOWFLAKE_STAGE_DIR=/stages
ENV SNOWFLAKE_DUCKDB_PATH=/data/warehouse.duckdb
EXPOSE 8448
HEALTHCHECK --interval=10s --timeout=3s --retries=5 CMD ["/usr/local/bin/snowflake-emulator", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/snowflake-emulator"]
