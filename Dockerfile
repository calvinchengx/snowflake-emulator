# Build: static Go binary + duckdb CLI (the named engine).
FROM golang:1.25 AS build
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
COPY --from=build /snowflake-emulator /usr/local/bin/snowflake-emulator
RUN mkdir /data /stages && chown 65532:65532 /data /stages
USER 65532:65532
ENV SNOWFLAKE_DATA_DIR=/data
ENV SNOWFLAKE_STAGE_DIR=/stages
ENV SNOWFLAKE_DUCKDB_PATH=/data/warehouse.duckdb
EXPOSE 8448
HEALTHCHECK --interval=10s --timeout=3s --retries=5 CMD ["/usr/local/bin/snowflake-emulator", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/snowflake-emulator"]
