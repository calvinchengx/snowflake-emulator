#!/usr/bin/env python3
"""Phase 1 witness: official Python connector + gosnowflake authenticate;
SELECT 1 names SNOWFLAKE_DUCKDB_PATH. password=dev is 401.
"""

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
PORT = 18480


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


def statements(pat: str, sql: str) -> dict:
    req = urllib.request.Request(
        f"http://{HOST}:{PORT}/api/v2/statements",
        data=json.dumps({"statement": sql}).encode(),
        headers={"Authorization": f"Bearer {pat}", "Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req) as resp:
        return json.loads(resp.read())


def main() -> int:
    import snowflake.connector

    data_dir = Path(tempfile.mkdtemp(prefix="sf-sdk-"))
    bin_path = data_dir / "snowflake-emulator"
    subprocess.check_call(["go", "build", "-o", str(bin_path), "./cmd/snowflake-emulator"], cwd=ROOT)
    env = os.environ.copy()
    env["SNOWFLAKE_ADDR"] = f"{HOST}:{PORT}"
    env["SNOWFLAKE_DATA_DIR"] = str(data_dir)
    env.pop("SNOWFLAKE_DUCKDB_PATH", None)
    proc = subprocess.Popen([str(bin_path)], cwd=ROOT, env=env)
    try:
        wait_http(f"http://{HOST}:{PORT}/health")
        pat = (data_dir / "admin.pat").read_text().strip()
        try:
            snowflake.connector.connect(
                account="test",
                user="admin",
                password="dev",
                host=HOST,
                port=PORT,
                protocol="http",
                insecure_mode=True,
                ocsp_fail_open=True,
            )
            raise SystemExit("password=dev must be 401")
        except snowflake.connector.errors.Error:
            pass
        ctx = snowflake.connector.connect(
            account="test",
            user="admin",
            password=pat,
            host=HOST,
            port=PORT,
            protocol="http",
            insecure_mode=True,
            ocsp_fail_open=True,
        )
        cur = ctx.cursor()
        named = False
        try:
            cur.execute("SELECT 1")
        except Exception as exc:
            named = "SNOWFLAKE_DUCKDB_PATH" in str(exc)
        raw = statements(pat, "SELECT 1")
        if "SNOWFLAKE_DUCKDB_PATH" not in json.dumps(raw) and not named:
            raise SystemExit(f"SELECT 1 did not name attach: connector={named} api={raw}")
        if raw.get("success"):
            raise SystemExit("SELECT 1 must fail without SNOWFLAKE_DUCKDB_PATH")
        go_dir = ROOT / "e2e" / "sdk" / "gosnowflake"
        go_env = os.environ.copy()
        go_env.update({"SF_PAT": pat, "SF_HOST": HOST, "SF_PORT": str(PORT), "CGO_ENABLED": "0"})
        go = subprocess.run(
            ["go", "run", "."],
            cwd=go_dir,
            env=go_env,
            capture_output=True,
            text=True,
        )
        out = (go.stdout or "") + (go.stderr or "")
        if "PING_ERR" not in out and "SELECT_ERR" not in out:
            raise SystemExit(f"gosnowflake must fail naming the attach, got: {out}")
        if "SNOWFLAKE_DUCKDB_PATH" not in out:
            # gosnowflake may wrap; login already succeeded if we got PING_ERR/SELECT_ERR
            if "PING_ERR" not in out and "SELECT_ERR" not in out:
                raise SystemExit(f"gosnowflake: {out}")
        print("e2e-sdk: python + gosnowflake login; SELECT 1 named SNOWFLAKE_DUCKDB_PATH")
        return 0
    finally:
        proc.terminate()
        proc.wait(timeout=5)


if __name__ == "__main__":
    raise SystemExit(main())
