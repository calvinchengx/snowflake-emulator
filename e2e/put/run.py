#!/usr/bin/env python3
"""PUT through the driver's own file transfer agent, then COPY INTO the bytes.

WHY THIS SUITE EXISTS, and why the unit tests are not enough. The Go tests
assert the upload response matches what I read in the drivers' source. That is
my reading of the contract, checked against itself. This runs the real thing:
snowflake-connector-python recognises PUT before sending it, asks the server
where the bytes go, and uploads them through the same file transfer agent it
uses against a real account. Nothing here writes into the stage directory --
if the driver does not do it, no bytes arrive and COPY INTO says so.

THE FILE IS NEVER PLACED BY THIS SCRIPT. It is written to a source directory
that is not the stage, so a stage file can only get there by upload. An
earlier draft seeded the stage and would have passed against a server that
answered PUT with anything at all.
"""

from __future__ import annotations

import gzip
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
PORT = 18483


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
        data=json.dumps({"statement": sql, "warehouse": "put_wh"}).encode(),
        headers={"Authorization": f"Bearer {pat}", "Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req) as resp:
        return json.loads(resp.read())


def main() -> int:
    import snowflake.connector

    data_dir = Path(tempfile.mkdtemp(prefix="sf-put-"))
    stage = data_dir / "stages"
    stage.mkdir()
    # NOT the stage. The only route from here to there is the driver.
    src = data_dir / "outside"
    src.mkdir()
    (src / "orders.csv").write_text("id,amount\n1,10.50\n2,20.25\n3,30.00\n", encoding="utf-8")

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
        if not api(pat, "CREATE WAREHOUSE put_wh").get("success"):
            raise SystemExit("no warehouse")

        ctx = snowflake.connector.connect(
            account="test", user="admin", password=pat,
            host=HOST, port=PORT, protocol="http",
            warehouse="put_wh", insecure_mode=True,
        )
        cur = ctx.cursor()

        # The stage is empty. Proving it, because everything below is only
        # evidence if the bytes were not already there.
        before = sorted(p.name for p in stage.iterdir())
        if before:
            raise SystemExit(f"the stage was not empty before the upload: {before}")

        cur.execute(f"PUT file://{src / 'orders.csv'} @~")
        uploaded = cur.fetchall()

        # AUTO_COMPRESS defaults to TRUE on a real account, so the driver
        # gzips and the stage holds orders.csv.gz. Asserting the NAME rather
        # than a count: a stage with one file of the wrong name is the same
        # cardinality and a different fact.
        landed = sorted(p.name for p in stage.iterdir())
        if landed != ["orders.csv.gz"]:
            raise SystemExit(f"stage holds {landed}, want ['orders.csv.gz']")
        with gzip.open(stage / "orders.csv.gz", "rt", encoding="utf-8") as fh:
            head = fh.readline().strip()
        if head != "id,amount":
            raise SystemExit(f"uploaded bytes read {head!r}, want 'id,amount'")

        # And the other half: a consumer names orders.csv, as they would on a
        # real account, where the prefix matches the compressed file.
        api(pat, "CREATE TABLE orders (id INTEGER, amount DECIMAL(10,2))")
        copied = api(
            pat,
            "COPY INTO orders FROM @~/orders.csv "
            "FILE_FORMAT = (TYPE = CSV, SKIP_HEADER = 1)",
        )
        if not copied.get("success"):
            raise SystemExit(copied)

        cur.execute("SELECT COUNT(*), SUM(amount) FROM orders")
        measured = cur.fetchone()
        if measured is None:
            raise SystemExit("COPY INTO reported success and the table is unreadable")
        rows, total = measured
        if int(rows) != 3 or float(total) != 60.75:
            raise SystemExit(f"COPY INTO landed {rows} rows summing {total}, want 3 and 60.75")

        # AND BACK OUT AGAIN, through the driver's own file transfer agent.
        #
        # GET is not decoration: EXECUTE DBT PROJECT leaves dbt's
        # `run_results.json` in the stage, and that file is how a pipeline
        # learns WHICH tests ran rather than which exist. Without a way to read
        # a stage file back, a consumer is left publishing contract names
        # nothing evaluated -- so this asserts the round trip, not the response.
        back = data_dir / "back"
        back.mkdir()
        cur.execute(f"GET @~/orders.csv file://{back}")
        fetched = cur.fetchall()
        if not fetched or fetched[0][2] != "DOWNLOADED":
            raise SystemExit(f"GET did not download: {fetched}")
        landed_back = sorted(p.name for p in back.iterdir())
        if landed_back != ["orders.csv.gz"]:
            raise SystemExit(f"GET landed {landed_back}, want ['orders.csv.gz']")
        # THE BYTES, not the filename. A GET that creates an empty file with the
        # right name reports DOWNLOADED just as loudly.
        with gzip.open(back / "orders.csv.gz", "rt") as fh:
            round_tripped = fh.read()
        if round_tripped != (src / "orders.csv").read_text(encoding="utf-8"):
            raise SystemExit(
                f"GET returned different bytes than PUT sent: {round_tripped!r}"
            )

        print(f"e2e-put: driver uploaded {uploaded!r}, and GET returned the same bytes")
        print(f"e2e-put: stage holds {landed}; COPY INTO landed {rows} rows summing {total}")
        return 0
    finally:
        proc.terminate()
        proc.wait(timeout=5)


if __name__ == "__main__":
    raise SystemExit(main())
