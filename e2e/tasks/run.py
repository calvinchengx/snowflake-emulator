#!/usr/bin/env python3
"""What an orchestrator does: start a graph, then find out what it did.

THE POINT OF THIS SUITE. Tasks, graphs, schedules and EXECUTE TASK were all
green before TASK_HISTORY existed, and a consumer still could not drive a
pipeline from them, because nothing said what a run DID. So this does not test
that tasks run -- other things cover that. It tests that a driver can ASK, over
the connector, in ordinary SQL, and get an answer it can branch on.

Both outcomes are exercised, because only one of them is dangerous. A history
that records successes and forgets failures is worse than none: a driver polls,
sees no failure, and waits forever on a graph that already gave up.
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
PORT = 18485


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
        data=json.dumps({"statement": sql, "warehouse": "task_wh"}).encode(),
        headers={"Authorization": f"Bearer {pat}", "Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req) as resp:
            return json.loads(resp.read())
    except urllib.error.HTTPError as exc:
        return json.loads(exc.read())


def main() -> int:
    import snowflake.connector

    data_dir = Path(tempfile.mkdtemp(prefix="sf-tasks-"))
    bin_path = data_dir / "snowflake-emulator"
    subprocess.check_call(["go", "build", "-o", str(bin_path), "./cmd/snowflake-emulator"], cwd=ROOT)
    env = os.environ.copy()
    env["SNOWFLAKE_ADDR"] = f"{HOST}:{PORT}"
    env["SNOWFLAKE_DATA_DIR"] = str(data_dir)
    env["SNOWFLAKE_DUCKDB_PATH"] = str(data_dir / "wh.duckdb")
    env["SNOWFLAKE_STAGE_DIR"] = str(data_dir / "stages")
    (data_dir / "stages").mkdir()
    proc = subprocess.Popen([str(bin_path)], cwd=ROOT, env=env)
    try:
        wait_http(f"http://{HOST}:{PORT}/health")
        pat = (data_dir / "admin.pat").read_text().strip()
        if not api(pat, "CREATE WAREHOUSE task_wh").get("success"):
            raise SystemExit("no warehouse")

        ctx = snowflake.connector.connect(
            account="test", user="admin", password=pat,
            host=HOST, port=PORT, protocol="http",
            warehouse="task_wh", insecure_mode=True,
        )
        cur = ctx.cursor()

        # Before anything has run, a poll must answer zero rows. A driver's
        # first question is always asked too early.
        cur.execute("SELECT COUNT(*) FROM TABLE(INFORMATION_SCHEMA.TASK_HISTORY())")
        first = cur.fetchone()
        if first is None or int(first[0]) != 0:
            raise SystemExit(f"an empty history answered {first!r}, want 0 rows")

        cur.execute("CREATE TABLE log (step VARCHAR)")
        cur.execute("CREATE TASK t_root WAREHOUSE = task_wh AS INSERT INTO log VALUES ('root')")
        cur.execute("CREATE TASK t_mid AFTER t_root AS INSERT INTO log VALUES ('mid')")
        cur.execute("EXECUTE TASK t_root")

        cur.execute(
            "SELECT NAME, STATE, SCHEDULED_FROM FROM TABLE(INFORMATION_SCHEMA.TASK_HISTORY()) "
            "ORDER BY NAME"
        )
        good = cur.fetchall()
        if good != [("T_MID", "SUCCEEDED", "EXECUTE TASK"), ("T_ROOT", "SUCCEEDED", "EXECUTE TASK")]:
            raise SystemExit(f"history after a good run is {good!r}")

        # THE HALF THAT MATTERS. A graph whose middle fails: the failure is
        # recorded with its message, and what never ran says SKIPPED rather
        # than going unmentioned, which a poller would read as "not yet".
        cur.execute("CREATE TASK t_bad WAREHOUSE = task_wh AS INSERT INTO no_such_table VALUES (1)")
        cur.execute("CREATE TASK t_after_bad AFTER t_bad AS INSERT INTO log VALUES ('never')")
        api(pat, "EXECUTE TASK t_bad")  # expected to fail; the API call carries it

        # The columns themselves, not `IS NULL` over them. That is the
        # stronger assertion -- it reads the recorded values rather than a
        # server-side predicate about them -- and it sidesteps a defect this
        # suite found and does not own: a BOOLEAN comes back through the
        # connector as False whatever it is, because the emulator sends
        # duckdb's `true` where the connector maps `value in ("1", "TRUE")`.
        # Filed separately; asserting on booleans here would have been a test
        # that passes for the wrong reason.
        cur.execute(
            "SELECT NAME, STATE, ERROR_MESSAGE, QUERY_START_TIME, COMPLETED_TIME "
            "FROM TABLE(INFORMATION_SCHEMA.TASK_HISTORY(TASK_NAME => 'T_AFTER_BAD'))"
        )
        skipped = cur.fetchall()
        if len(skipped) != 1:
            raise SystemExit(f"the skipped task has {len(skipped)} runs, want 1")
        name, state, err_msg, started, completed = skipped[0]
        if (name, state) != ("T_AFTER_BAD", "SKIPPED"):
            raise SystemExit(f"the skipped task reads {name!r} {state!r}")
        if not err_msg or "T_BAD" not in err_msg.upper():
            raise SystemExit(f"a skipped run must name what failed upstream, got {err_msg!r}")
        if started is not None or completed is not None:
            raise SystemExit(
                f"a skipped run never began: start={started!r} completed={completed!r}")

        cur.execute(
            "SELECT STATE, ERROR_MESSAGE FROM TABLE(INFORMATION_SCHEMA.TASK_HISTORY(TASK_NAME => 'T_BAD'))"
        )
        failed = cur.fetchone()
        if failed is None or failed[0] != "FAILED" or not failed[1]:
            raise SystemExit(f"the failed task reads {failed!r}, want FAILED with a message")

        # And the thing a driver actually writes: newest run for one task.
        cur.execute(
            "SELECT STATE FROM TABLE(INFORMATION_SCHEMA.TASK_HISTORY(TASK_NAME => 'T_ROOT')) "
            "ORDER BY COMPLETED_TIME DESC LIMIT 1"
        )
        latest = cur.fetchone()
        if latest is None or latest[0] != "SUCCEEDED":
            raise SystemExit(f"the latest T_ROOT run reads {latest!r}")

        print(f"e2e-tasks: good run {good}")
        print(f"e2e-tasks: failure {failed[0]} with a message; downstream {skipped[0][1]}")
        return 0
    finally:
        proc.terminate()
        proc.wait(timeout=5)


if __name__ == "__main__":
    raise SystemExit(main())
