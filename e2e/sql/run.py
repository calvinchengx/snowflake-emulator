#!/usr/bin/env python3
"""Phase 2 witness: warehouse handle + SELECT 1 with dialect duckdb + COPY INTO."""

from __future__ import annotations

import json
import os
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
HOST = "127.0.0.1"
PORT = 18481


def wait_http(url: str, timeout: float = 30.0) -> None:
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        try:
            urllib.request.urlopen(url, timeout=1)
            return
        except (urllib.error.URLError, TimeoutError, ConnectionError) as exc:
            last = exc
            time.sleep(0.1)
    raise SystemExit(f"never healthy: {last}")


def api(pat: str, sql: str) -> dict:
    req = urllib.request.Request(
        f"http://{HOST}:{PORT}/api/v2/statements",
        data=json.dumps({"statement": sql, "warehouse": "e2e_wh"}).encode(),
        headers={"Authorization": f"Bearer {pat}", "Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req) as resp:
        return json.loads(resp.read())


def main() -> int:
    import snowflake.connector

    data_dir = Path(tempfile.mkdtemp(prefix="sf-sql-"))
    stage = data_dir / "stages"
    stage.mkdir()
    (stage / "nums.csv").write_text("n\n1\n2\n", encoding="utf-8")
    bin_path = data_dir / "snowflake-emulator"
    subprocess.check_call(["go", "build", "-o", str(bin_path), "./cmd/snowflake-emulator"], cwd=ROOT)
    env = os.environ.copy()
    env["SNOWFLAKE_ADDR"] = f"{HOST}:{PORT}"
    env["SNOWFLAKE_DATA_DIR"] = str(data_dir)
    env["SNOWFLAKE_DUCKDB_PATH"] = str(data_dir / "wh.duckdb")
    env["SNOWFLAKE_STAGE_DIR"] = str(stage)
    proc = subprocess.Popen([str(bin_path)], cwd=ROOT, env=env)
    try:
        wait_http(f"http://{HOST}:{PORT}/health")
        pat = (data_dir / "admin.pat").read_text().strip()
        created = api(pat, "CREATE WAREHOUSE e2e_wh")
        if not created.get("success"):
            raise SystemExit(created)
        one = api(pat, "SELECT 1 AS n")
        if not one.get("success"):
            raise SystemExit(one)
        dialect = (one.get("data") or {}).get("dialect")
        if dialect != "duckdb":
            raise SystemExit(f"dialect {dialect!r} want duckdb")
        ctx = snowflake.connector.connect(
            account="test",
            user="admin",
            password=pat,
            host=HOST,
            port=PORT,
            protocol="http",
            warehouse="e2e_wh",
            insecure_mode=True,
        )
        cur = ctx.cursor()
        cur.execute("SELECT 1 AS n")
        row = cur.fetchone()
        if row is None:
            raise SystemExit("no row")
        api(pat, "CREATE TABLE nums (n INTEGER)")
        # SKIP_HEADER = 1 is now REQUIRED and that is the fix, not a chore.
        # nums.csv opens with a line reading `n`, and Snowflake's default CSV
        # format treats it as data -- so this used to pass only because the
        # emulator hardcoded a header skip no consumer had asked for. Against
        # the real thing the same statement fails on an INTEGER column.
        copy = api(
            pat,
            "COPY INTO nums FROM @~/nums.csv "
            "FILE_FORMAT = (TYPE = CSV, SKIP_HEADER = 1)",
        )
        if not copy.get("success"):
            raise SystemExit(copy)
        print("e2e-sql: SELECT 1 dialect=duckdb; COPY INTO ok")
        return 0
    finally:
        proc.terminate()
        proc.wait(timeout=5)


if __name__ == "__main__":
    raise SystemExit(main())
