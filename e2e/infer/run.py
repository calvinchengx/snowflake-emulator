#!/usr/bin/env python3
"""INFER_SCHEMA describes a staged file, and COPY INTO agrees with it.

THE IMPLEMENTATION IS NOT MINE. INFER_SCHEMA landed in #45 while this was
being written, and this suite passed against it unchanged -- which is worth
recording, because two people arriving independently at NUMBER(38,0) and
TIMESTAMP_NTZ is better evidence for those names than either of us alone.

WHY THE SECOND HALF IS THE TEST. A schema description that nothing checks is a
plausible-looking list of types. What makes it worth having is that a table
BUILT from the description then loads the file it described -- so this suite
reads the types back, writes the CREATE TABLE from them, loads the file into
it, and reads the values. If INFER_SCHEMA said DATE where the loader wanted
TEXT, the COPY INTO fails and this suite goes red.

That is also the thing the Contoso Snowflake leaf does by hand today, in
`_csv_types`: it computes bronze DDL for files it staged moments earlier.
"""

from __future__ import annotations

import os
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
HOST = "127.0.0.1"
PORT = 18489


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


def main() -> int:
    import snowflake.connector

    data_dir = Path(tempfile.mkdtemp(prefix="sf-infer-"))
    stage = data_dir / "stages"
    stage.mkdir()
    (stage / "orders.csv").write_text(
        "id,amount,ordered_on,is_cancelled\n"
        "1,10.50,2026-01-02,true\n"
        "2,20.25,2026-03-04,false\n",
        encoding="utf-8",
    )

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
        ctx = snowflake.connector.connect(
            account="test", user="admin", password=pat,
            host=HOST, port=PORT, protocol="http", insecure_mode=True,
        )
        cur = ctx.cursor()
        cur.execute("CREATE WAREHOUSE infer_wh")
        cur.execute("CREATE FILE FORMAT csv_h TYPE = CSV SKIP_HEADER = 1")

        cur.execute(
            "SELECT COLUMN_NAME, TYPE, ORDER_ID FROM "
            "TABLE(INFER_SCHEMA(LOCATION => '@~/orders.csv', FILE_FORMAT => 'csv_h')) "
            "ORDER BY ORDER_ID"
        )
        described = cur.fetchall()
        want = [
            ("id", "NUMBER(38,0)", 0),
            ("amount", "FLOAT", 1),
            ("ordered_on", "DATE", 2),
            ("is_cancelled", "BOOLEAN", 3),
        ]
        if described != want:
            raise SystemExit(f"INFER_SCHEMA said {described!r}, want {want!r}")

        # THE HALF THAT MAKES IT EVIDENCE: build the table from what it said,
        # then load the file it described into that table.
        ddl = ", ".join(f'"{name}" {typ}' for name, typ, _ in described)
        cur.execute(f"CREATE TABLE orders ({ddl})")
        cur.execute(
            "COPY INTO orders FROM '@~/orders.csv' FILE_FORMAT = (FORMAT_NAME = csv_h)"
        )
        cur.execute("SELECT COUNT(*), SUM(amount) FROM orders")
        measured = cur.fetchone()
        if measured is None or int(measured[0]) != 2 or float(measured[1]) != 30.75:
            raise SystemExit(f"the inferred table loaded {measured!r}, want 2 rows summing 30.75")

        # And the types survived the round trip rather than all being text.
        cur.execute("SELECT ordered_on, is_cancelled FROM orders ORDER BY id LIMIT 1")
        first = cur.fetchone()
        if first is None:
            raise SystemExit("no row back from the inferred table")
        if str(first[0]) != "2026-01-02" or first[1] is not True:
            raise SystemExit(f"types did not survive: {first!r}")

        print(f"e2e-infer: described {described}")
        print(f"e2e-infer: the inferred table loaded {measured[0]} rows summing {measured[1]}")
        return 0
    finally:
        proc.terminate()
        proc.wait(timeout=5)


if __name__ == "__main__":
    raise SystemExit(main())
