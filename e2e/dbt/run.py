#!/usr/bin/env python3
"""Phase 4: unmodified dbt-snowflake one + two over the warehouse handle."""

from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import time
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
HERE = Path(__file__).resolve().parent
HOST = "127.0.0.1"
PORT = 18483


def wait_http(url: str, timeout: float = 30.0) -> None:
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        try:
            urllib.request.urlopen(url, timeout=1)
            return
        except Exception as exc:
            last = exc
            time.sleep(0.1)
    raise SystemExit(f"never healthy: {last}")


def api(pat: str, sql: str) -> dict:
    req = urllib.request.Request(
        f"http://{HOST}:{PORT}/api/v2/statements",
        data=json.dumps({"statement": sql, "warehouse": "dbt_wh"}).encode(),
        headers={"Authorization": f"Bearer {pat}", "Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req) as resp:
        return json.loads(resp.read())


def main() -> int:
    data_dir = Path(tempfile.mkdtemp(prefix="sf-dbt-"))
    bin_path = data_dir / "snowflake-emulator"
    subprocess.check_call(["go", "build", "-o", str(bin_path), "./cmd/snowflake-emulator"], cwd=ROOT)
    env = os.environ.copy()
    env["SNOWFLAKE_ADDR"] = f"{HOST}:{PORT}"
    env["SNOWFLAKE_DATA_DIR"] = str(data_dir)
    env["SNOWFLAKE_DUCKDB_PATH"] = str(data_dir / "wh.duckdb")
    proc = subprocess.Popen([str(bin_path)], cwd=ROOT, env=env)
    try:
        wait_http(f"http://{HOST}:{PORT}/health")
        pat = (data_dir / "admin.pat").read_text().strip()
        api(pat, "CREATE WAREHOUSE dbt_wh")
        profiles = data_dir / "profiles.yml"
        profiles.write_text(
            "sf_e2e:\n"
            "  target: emulator\n"
            "  outputs:\n"
            "    emulator:\n"
            "      type: snowflake\n"
            "      account: test\n"
            "      user: admin\n"
            "      password: " + pat + "\n"
            "      host: 127.0.0.1\n"
            "      port: " + str(PORT) + "\n"
            "      protocol: http\n"
            "      warehouse: dbt_wh\n"
            "      database: TEST_DB\n"
            "      schema: PUBLIC\n"
            "      insecure_mode: true\n"
            "      threads: 1\n",
            encoding="utf-8",
        )
        cmd = [
            sys.executable, "-m", "dbt", "run",
            "--project-dir", str(HERE / "project"),
            "--profiles-dir", str(data_dir),
        ]
        # uv run already has dbt on PATH when invoked via make
        env["DBT_PROFILES_DIR"] = str(data_dir)
        r = subprocess.run(["dbt", "run", "--project-dir", str(HERE / "project"), "--profiles-dir", str(data_dir)], cwd=ROOT, env=env)
        if r.returncode != 0:
            raise SystemExit("dbt run failed")
        print("e2e-dbt: dbt run one + two ok")
        return 0
    finally:
        proc.terminate()
        proc.wait(timeout=5)


if __name__ == "__main__":
    raise SystemExit(main())
