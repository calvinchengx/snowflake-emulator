#!/usr/bin/env python3
"""Measure what the emulator answers, and write docs/parity.md from it.

WHY THIS EXISTS. parity.md was hand-written, and on 2026-08-19 it was found to
be wrong in both directions at once: it marked Streams, Tasks and Time Travel
as not implemented while the emulator was silently answering `status: ok` to
all three, and it said nothing at all about the eighteen other constructs real
Snowflake accepts and this refused. A hand-maintained claim drifts from the
code the moment either changes, and nothing fails when it does.

So the document is generated. `--check` regenerates it and fails if the file
on disk disagrees, which is what makes the claim testable rather than
aspirational.

IT RUNS AGAINST THE CONTAINER, not a host build, and that is load-bearing.
The duckdb CLI pinned in the Dockerfile (v1.2.2) EXITS 0 AFTER REFUSING a
statement; a newer one on a developer's machine exits 1. Probing a host build
therefore reports honest failures that the shipped image does not give, which
is exactly how the silent-200 defect survived a day of measurement. If this
probe is ever pointed at a host binary, it will describe an emulator nobody
runs.
"""

from __future__ import annotations

import json
import re
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(Path(__file__).resolve().parent))

from probes import CAVEATS, PROBES, SETUP, WITNESSES  # noqa: E402

DOC = ROOT / "docs" / "parity.md"
MANIFEST = ROOT / "docs" / "witnesses.json"
PORT = 18488
IMAGE = "snowflake-emulator:parity"
NAME = "sf-parity-probe"


def sh(*args: str, **kw) -> subprocess.CompletedProcess:
    return subprocess.run(args, check=True, capture_output=True, text=True, **kw)


def api(pat: str, sql: str) -> dict:
    req = urllib.request.Request(
        f"http://127.0.0.1:{PORT}/api/v2/statements",
        data=json.dumps({"statement": sql, "warehouse": "parity_wh", "database": "TEST_DB"}).encode(),
        headers={"Authorization": f"Bearer {pat}", "Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.loads(resp.read())


def wait_healthy(timeout: float = 60.0) -> None:
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        try:
            urllib.request.urlopen(f"http://127.0.0.1:{PORT}/health", timeout=1)
            return
        except (urllib.error.URLError, TimeoutError, ConnectionError) as exc:
            last = exc
            time.sleep(0.2)
    raise SystemExit(f"emulator never became healthy: {last}")


def measure() -> list[tuple[str, str, bool, str]]:
    stage = Path(tempfile.mkdtemp(prefix="sf-parity-"))
    (stage / "parity.csv").write_text("n\n1\n2\n", encoding="utf-8")
    (stage / "parity.json").write_text('{"id":1,"customer":{"email":"a@x.com"}}\n', encoding="utf-8")
    # A PAGED FEED, because "a prefix is refused" is only a claim worth making
    # when the prefix HAS files under it. Refusing an empty directory would
    # pass against an emulator that merely failed to find anything, which is
    # the weaker statement and the one already covered by the missing-file
    # case.
    feed = stage / "parity_feed"
    feed.mkdir()
    for part in ("part_0.csv", "part_1.csv"):
        (feed / part).write_text("n\n1\n2\n", encoding="utf-8")
    feed.chmod(0o777)

    # A dbt PROJECT in the stage, because EXECUTE DBT PROJECT reads one from
    # there. Two models and a ref, so `run` has something to build and the
    # probe that reads the result back is reading dbt's work rather than a
    # constant. The profile name is deliberately NOT one this emulator could
    # special-case: dbt resolves `profile:` from dbt_project.yml, and a
    # generated profile filed under any other key would send dbt looking for
    # one that was never written -- the defect databricks-emulator#68 was.
    proj = stage / "parity_dbt"
    (proj / "models").mkdir(parents=True)
    (proj / "dbt_project.yml").write_text(
        "name: parity_dbt\n"
        "version: '1.0.0'\n"
        "config-version: 2\n"
        "profile: a_name_the_emulator_does_not_know\n"
        "model-paths: ['models']\n"
        "flags:\n"
        "  send_anonymous_usage_stats: false\n",
        encoding="utf-8",
    )
    (proj / "models" / "p_dbt_one.sql").write_text("select 1 as n\n", encoding="utf-8")
    (proj / "models" / "p_dbt_two.sql").write_text(
        "select n from {{ ref('p_dbt_one') }}\n", encoding="utf-8")
    for child in proj.rglob("*"):
        child.chmod(0o777)
    proj.chmod(0o777)
    stage.chmod(0o777)

    sh("docker", "build", "-t", IMAGE, ".", cwd=ROOT)
    subprocess.run(["docker", "rm", "-f", NAME], capture_output=True)
    sh("docker", "run", "-d", "--name", NAME,
       "-e", "SNOWFLAKE_ADDR=0.0.0.0:8448",
       "-e", "SNOWFLAKE_DUCKDB_PATH=/data/w.duckdb",
       "-e", "SNOWFLAKE_STAGE_DIR=/stages",
       "-v", f"{stage}:/stages",
       "-p", f"{PORT}:8448", IMAGE)
    try:
        wait_healthy()
        pat = sh("docker", "exec", NAME, "cat", "/data/admin.pat").stdout.strip()
        api(pat, "CREATE WAREHOUSE parity_wh")
        api(pat, "CREATE TABLE p_nums (n INTEGER)")
        for stmt in SETUP:
            api(pat, stmt)
        out = []
        for probe in PROBES:
            area, feature, stmt = probe[0], probe[1], probe[2]
            must_fail = len(probe) > 3 and probe[3] == "must_fail"
            res = api(pat, stmt)
            ok = bool(res.get("success"))
            note = "" if ok else (res.get("message") or "").split("\n")[0]
            if must_fail:
                # A refusal IS the feature. Answering would be the defect.
                out.append((area, feature, not ok, "" if not ok else "answered when it should refuse"))
            else:
                out.append((area, feature, ok, note))
        return out
    finally:
        subprocess.run(["docker", "rm", "-f", NAME], capture_output=True)


def key_for(feature: str) -> str:
    """The same slug scripts/check_witnesses.py derives, so the two agree."""
    text = re.sub(r"[*`_]", "", feature)
    text = re.sub(r"[^a-z0-9]+", "-", text.lower())
    return text.strip("-")


def cell(text: str) -> str:
    """A pipe inside a cell ends the cell. The engine's own error text and a
    caveat naming `'<n> SECOND | MINUTE | HOUR'` both carry one, and an
    unescaped pipe silently splits the row into extra columns -- which moved
    the status mark and left the table rendering as nonsense."""
    return text.replace("|", "\\|")


def render(rows: list[tuple[str, str, bool, str]]) -> str:
    lines = [
        "# Parity",
        "",
        "<!-- GENERATED by e2e/parity/run.py. Do not edit: `run.py --check` fails",
        "     if this file disagrees with what the emulator actually answers. -->",
        "",
        "Every row below is a statement **real Snowflake accepts**, run against the",
        "image this repository builds. 🟢 means the emulator answered it; 🔴 means it",
        "refused, by name, with the reason recorded.",
        "",
        "A refusal is not a bug in itself -- this family would rather fail honestly",
        "than answer wrongly. A 🔴 with a reason is a gap someone can plan around; a",
        "silent success is one nobody can see.",
        "",
        "Every 🟢 names a witness in `docs/witnesses.json`, which is generated from",
        "the same run.",
        "",
    ]
    areas: dict[str, list] = {}
    for area, feature, ok, note in rows:
        areas.setdefault(area, []).append((feature, ok, note))
    for area, items in areas.items():
        lines += [f"## {area}", "", "| Feature | Detail | Status |", "|---|---|---|"]
        for feature, ok, note in items:
            detail = CAVEATS.get(feature, "")
            if not ok:
                detail = note if not detail else f"{note} — {detail}"
            lines.append(f"| {cell(feature)} | {cell(detail)} | {'🟢' if ok else '🔴'} |")
        lines.append("")
    green = sum(1 for _, _, ok, _ in rows if ok)
    lines += [f"_{green} of {len(rows)} answered._", ""]
    return "\n".join(lines)


def manifest_for(rows: list[tuple[str, str, bool, str]]) -> str:
    out = {}
    for area, feature, ok, _ in rows:
        if not ok:
            continue
        out[key_for(feature)] = {
            "section": area,
            "claim": feature,
            "witnesses": WITNESSES.get(feature, ["ci:parity"]),
        }
    return json.dumps(out, indent=2, ensure_ascii=False) + "\n"


def main() -> int:
    check = "--check" in sys.argv
    rows = measure()
    doc, manifest = render(rows), manifest_for(rows)
    if check:
        for path, want in ((DOC, doc), (MANIFEST, manifest)):
            if not path.exists() or path.read_text(encoding="utf-8") != want:
                print(f"{path.relative_to(ROOT)} does not match what the emulator answers.")
                print("Run: uv run --group sql python e2e/parity/run.py")
                return 1
        print("parity: the documents match the emulator")
        return 0
    DOC.write_text(doc, encoding="utf-8")
    MANIFEST.write_text(manifest, encoding="utf-8")
    print(f"parity: wrote {DOC.relative_to(ROOT)} and {MANIFEST.relative_to(ROOT)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
