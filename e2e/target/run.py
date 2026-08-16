#!/usr/bin/env python3
"""Phase 5: snowflake-target emulator mode + SQL API SELECT 1 naming duckdb."""

from __future__ import annotations

import json
import os
import subprocess
import tempfile
import time
import urllib.request
from pathlib import Path

from snowflake_target import Target, TargetError

ROOT = Path(__file__).resolve().parents[2]
HOST = "127.0.0.1"
PORT = 18484


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


def main() -> int:
    try:
        Target("real")
        raise SystemExit("real without host must fail")
    except TargetError:
        pass
    data_dir = Path(tempfile.mkdtemp(prefix="sf-tgt-"))
    bin_path = data_dir / "snowflake-emulator"
    subprocess.check_call(["go", "build", "-o", str(bin_path), "./cmd/snowflake-emulator"], cwd=ROOT)
    env = os.environ.copy()
    env["SNOWFLAKE_ADDR"] = f"{HOST}:{PORT}"
    env["SNOWFLAKE_DATA_DIR"] = str(data_dir)
    env["SNOWFLAKE_DUCKDB_PATH"] = str(data_dir / "wh.duckdb")
    proc = subprocess.Popen([str(bin_path)], cwd=ROOT, env=env)
    try:
        wait_http(f"http://{HOST}:{PORT}/health")
        os.environ["SNOWFLAKE_TARGET"] = "emulator"
        os.environ["SNOWFLAKE_EMULATOR_URL"] = f"http://{HOST}:{PORT}"
        os.environ["SNOWFLAKE_DATA_DIR"] = str(data_dir)
        t = Target("emulator")
        if t.tls_verify:
            raise SystemExit("emulator must not verify TLS")
        pat = t.password
        req = urllib.request.Request(
            f"{t.host}/api/v2/statements",
            data=json.dumps({"statement": "SELECT 1 AS n", "warehouse": "COMPUTE_WH"}).encode(),
            headers={"Authorization": f"Bearer {pat}", "Content-Type": "application/json"},
            method="POST",
        )
        with urllib.request.urlopen(req) as resp:
            body = json.loads(resp.read())
        dialect = (body.get("data") or {}).get("dialect")
        if dialect != "duckdb":
            raise SystemExit(f"want dialect duckdb, got {body}")
        print("e2e-snowflake-target: SELECT 1 dialect=duckdb")
        return 0
    finally:
        proc.terminate()
        proc.wait(timeout=5)


if __name__ == "__main__":
    raise SystemExit(main())
