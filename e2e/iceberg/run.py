#!/usr/bin/env python3
"""Phase 3: CREATE ICEBERG TABLE listed on Iceberg REST; missing URL names the env."""

from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
HOST = "127.0.0.1"
PORT = 18482


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


def api(port: int, pat: str, sql: str) -> dict:
    req = urllib.request.Request(
        f"http://{HOST}:{port}/api/v2/statements",
        data=json.dumps({"statement": sql}).encode(),
        headers={"Authorization": f"Bearer {pat}", "Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req) as resp:
        return json.loads(resp.read())


def main() -> int:
    data_dir = Path(tempfile.mkdtemp(prefix="sf-ice-"))
    bin_path = data_dir / "snowflake-emulator"
    subprocess.check_call(["go", "build", "-o", str(bin_path), "./cmd/snowflake-emulator"], cwd=ROOT)

    env = os.environ.copy()
    env["SNOWFLAKE_ADDR"] = f"{HOST}:{PORT}"
    env["SNOWFLAKE_DATA_DIR"] = str(data_dir)
    env["SNOWFLAKE_DUCKDB_PATH"] = str(data_dir / "wh.duckdb")
    env.pop("SNOWFLAKE_POLARIS_URL", None)
    proc = subprocess.Popen([str(bin_path)], cwd=ROOT, env=env)
    try:
        wait_http(f"http://{HOST}:{PORT}/health")
        pat = (data_dir / "admin.pat").read_text().strip()
        miss = api(PORT, pat, "CREATE ICEBERG TABLE events (id INT)")
        if miss.get("success") or "SNOWFLAKE_POLARIS_URL" not in json.dumps(miss):
            raise SystemExit(f"missing polaris must be named: {miss}")
    finally:
        proc.terminate()
        proc.wait(timeout=5)

    data_dir2 = Path(tempfile.mkdtemp(prefix="sf-ice2-"))
    env["SNOWFLAKE_DATA_DIR"] = str(data_dir2)
    env["SNOWFLAKE_ADDR"] = f"{HOST}:{PORT+1}"
    env["SNOWFLAKE_POLARIS_URL"] = "embedded"
    proc = subprocess.Popen([str(bin_path)], cwd=ROOT, env=env)
    try:
        wait_http(f"http://{HOST}:{PORT+1}/health")
        pat = (data_dir2 / "admin.pat").read_text().strip()
        created = api(PORT + 1, pat, "CREATE ICEBERG TABLE events (id INT)")
        if not created.get("success"):
            raise SystemExit(created)
        listed = json.loads(urllib.request.urlopen(f"http://{HOST}:{PORT+1}/iceberg/v1/namespaces/TEST_DB/tables").read())
        names = [i.get("name") for i in listed.get("identifiers", [])]
        if "events" not in names and "EVENTS" not in names:
            raise SystemExit(f"iceberg REST did not list events: {listed}")
        print("e2e-iceberg: REST listed events; missing URL named SNOWFLAKE_POLARIS_URL")
        return 0
    finally:
        proc.terminate()
        proc.wait(timeout=5)


if __name__ == "__main__":
    raise SystemExit(main())
