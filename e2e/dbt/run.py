#!/usr/bin/env python3
"""Phase 4: unmodified dbt-snowflake one + two over the warehouse handle.

dbt is the writer, so dbt is not the witness. Its exit code only says it
believed it succeeded. After the run the emulator is stopped and a separate
duckdb binary opens the warehouse file directly, with nothing of ours in the
read path, and checks the models are really there holding the right rows.
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
HERE = Path(__file__).resolve().parent
HOST = "127.0.0.1"
PORT = 18483

# A schema name no part of this family uses. See the second dbt run below.
OTHER_SCHEMA = "DBT_CI"


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


def duckdb_rows(db: Path, sql: str) -> list[str]:
    """Read the warehouse file with the duckdb CLI. The emulator is stopped by
    the time this runs, so the engine that wrote is not the one confirming."""
    proc = subprocess.run(
        ["duckdb", str(db), "-noheader", "-list", "-c", sql],
        text=True, capture_output=True,
    )
    if proc.returncode != 0:
        raise SystemExit(f"duckdb {sql!r} failed: {proc.stderr.strip()}")
    return [line.strip() for line in proc.stdout.splitlines() if line.strip()]


def refused(pat: str) -> bool:
    """A bad credential must not reach the warehouse."""
    try:
        api(pat, "SELECT 1")
    except urllib.error.HTTPError:
        return True
    except urllib.error.URLError:
        return True
    return False


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

        def write_profile(schema: str) -> None:
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
                "      schema: " + schema + "\n"
                "      insecure_mode: true\n"
                "      threads: 1\n",
                encoding="utf-8",
            )

        write_profile("PUBLIC")
        if not refused("not-a-real-token"):
            raise SystemExit("a bad token reached the warehouse")
        print("   bad token refused")

        # uv run already has dbt on PATH when invoked via make
        env["DBT_PROFILES_DIR"] = str(data_dir)
        r = subprocess.run(
            ["dbt", "run", "--project-dir", str(HERE / "project"), "--profiles-dir", str(data_dir)],
            cwd=ROOT, env=env,
        )
        if r.returncode != 0:
            raise SystemExit("dbt run failed")

        # A SCHEMA THIS FAMILY DOES NOT USE, and that is the whole point of it.
        # Three-part names once resolved only for PUBLIC, GOLD, SILVER and
        # MAIN -- a whitelist whose middle three are this repository's own
        # medallion names -- so every test passed while `TEST_DB.BRONZE.t`
        # answered `Catalog with name TEST_DB does not exist!`. A probe that
        # only ever names a schema we happen to use cannot catch the next one
        # of those. DBT_CI is deliberately a name nothing here relies on.
        write_profile(OTHER_SCHEMA)
        r = subprocess.run(
            ["dbt", "run", "--project-dir", str(HERE / "project"), "--profiles-dir", str(data_dir),
             "--target", "emulator"],
            cwd=ROOT, env=env,
        )
        if r.returncode != 0:
            raise SystemExit(f"dbt run failed against schema {OTHER_SCHEMA}")

        # Stop the emulator before reading: DuckDB is single-writer, and a
        # confirmer that needed the writer alive would not be independent.
        proc.terminate()
        proc.wait(timeout=5)
        proc = None

        db = data_dir / "wh.duckdb"
        listed = duckdb_rows(
            db,
            "select table_name from information_schema.tables "
            "where table_schema = 'main' order by table_name",
        )
        for model in ("one", "two"):
            if model not in listed:
                raise SystemExit(f"dbt reported success but {model} is not in the warehouse: {listed}")

        # two selects from ref('one'), so its rows prove the dependency
        # resolved and both models really materialized.
        for model, want in (("one", ["1"]), ("two", ["1"])):
            got = duckdb_rows(db, f"select id from main.{model} order by id")
            if got != want:
                raise SystemExit(f"{model} holds {got}, want {want}")

        # THE SCHEMA IS PART OF THE CLAIM. Reading `main` alone would pass just
        # as happily if both runs had been flattened into one namespace, which
        # is exactly the defect that made TEST_DB.GOLD.orders and
        # TEST_DB.SILVER.orders a single table.
        other = duckdb_rows(
            db,
            "select table_name from information_schema.tables "
            f"where lower(table_schema) = '{OTHER_SCHEMA.lower()}' order by table_name",
        )
        for model in ("one", "two"):
            if model not in other:
                raise SystemExit(
                    f"{model} is not in schema {OTHER_SCHEMA}: {other} -- a non-default "
                    "schema was flattened away")
        print(f"   duckdb confirms {listed} with id=1, emulator stopped")
        print(f"   and {other} really live in {OTHER_SCHEMA}, not in main")
        print("e2e-dbt: dbt run one + two in PUBLIC and in " + OTHER_SCHEMA)
        return 0
    finally:
        if proc is not None:
            proc.terminate()
            proc.wait(timeout=5)


if __name__ == "__main__":
    raise SystemExit(main())
