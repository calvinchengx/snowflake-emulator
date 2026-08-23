"""The path filters that decide whether CI runs at all.

A WRONG FILTER FAILS SILENTLY, which is why it is worth a test. A workflow
that does not run reports nothing -- no red, no job, just a pull request that
looks reviewed. That is the same shape as every defect this repository keeps
finding, and it is worse here because the thing not running IS the check.

The rule implemented below is GitHub's documented one: patterns are evaluated
in order, a matching negative pattern after a positive match excludes the
path, and a matching positive pattern after a negative match includes it
again.
"""

from __future__ import annotations

import fnmatch
import pathlib

import yaml

ROOT = pathlib.Path(__file__).resolve().parent.parent
FILTERED = ["ci", "lint", "codeql"]

# `security` is deliberately NOT in that list. gitleaks scans for secrets, and a
# README with a real token pasted into an example is docs by path and a
# credential by content -- so filtering prose out of the secret scan skips it on
# exactly the commits most likely to carry one. It used to carry the same list
# as the other three, which is what this pair of tests now prevents.
UNFILTERED = ["security"]


def _on(name: str) -> dict:
    doc = yaml.safe_load((ROOT / ".github" / "workflows" / f"{name}.yml").read_text())
    # `on:` is YAML's boolean true unless quoted, which is why this is not
    # simply doc["on"].
    return doc[True] if True in doc else doc["on"]


def _patterns(name: str, event: str) -> list[str]:
    return list((_on(name)[event] or {}).get("paths", []))


def _matches(pattern: str, path: str) -> bool:
    """One glob, GitHub-style: `**` spans separators, `*` does not."""
    if "**" in pattern:
        return fnmatch.fnmatch(path, pattern.replace("**", "*"))
    if "/" in pattern:
        return fnmatch.fnmatch(path, pattern)
    return fnmatch.fnmatch(path, pattern) and "/" not in path


def runs_for(patterns: list[str], path: str) -> bool:
    """Ordered evaluation: the LAST pattern that matches decides.

    No patterns at all means no filter, which means the workflow runs for
    every path. Reading an empty list as "runs for nothing" is backwards, and
    it is the reading that would let an unfiltered workflow look skipped.
    """
    if not patterns:
        return True
    verdict = False
    for pat in patterns:
        if pat.startswith("!"):
            if _matches(pat[1:], path):
                verdict = False
        elif _matches(pat, path):
            verdict = True
    return verdict


def test_every_filtered_workflow_carries_the_same_list():
    """Four copies, because Actions will not resolve an anchor across files.

    Copies drift. If one workflow starts running on a README while the others
    do not, the saving is gone and the reason is invisible.
    """
    lists = {
        (name, event): _patterns(name, event)
        for name in FILTERED
        for event in ("push", "pull_request")
    }
    first = next(iter(lists.values()))
    assert first, "the filter is missing entirely"
    for key, got in lists.items():
        assert got == first, f"{key} has drifted from the others:\n{got}\n{first}"


def test_prose_alone_does_not_run_the_heavy_jobs():
    for path in [
        "README.md",
        "LICENSE",
        "docs/05-sql-surface.md",
        "docs/12-roadmap.md",
        "docs/index.md",
        "website/astro.config.mjs",
        "website/src/content/docs/index.mdx",
        # The landing page: guarded by check_landing.py in docs-site.yml,
        # which the same change triggers.
        "site/index.html",
    ]:
        for name in FILTERED:
            for event in ("push", "pull_request"):
                assert not runs_for(_patterns(name, event), path), (
                    f"{name}/{event} still runs for prose-only change {path}"
                )


def test_the_generated_parity_files_are_not_prose():
    """The exception the whole filter turns on.

    `docs/parity.md` and `docs/witnesses.json` are written by
    e2e/parity/run.py, and the `parity` job fails when they disagree with what
    the emulator answers. If they were exempt as "docs", a hand-edited parity
    table would merge with nothing run against it -- this repository's central
    claim about itself, quietly untrue.
    """
    for path in ("docs/parity.md", "docs/witnesses.json"):
        for name in FILTERED:
            for event in ("push", "pull_request"):
                assert runs_for(_patterns(name, event), path), (
                    f"{name}/{event} would skip {path}, which is generated and guarded"
                )


def test_code_still_runs_everything():
    for path in [
        "internal/server/rewrite.go",
        "cmd/snowflake-emulator/main.go",
        "go.mod",
        "Dockerfile",
        "e2e/parity/probes.py",
        "pyproject.toml",
        ".github/workflows/ci.yml",
        "Makefile",
        "versions.env",
        # Not anticipated by any rule: the default has to be "run".
        "some_new_top_level_file",
        "internal/newpkg/thing.go",
    ]:
        for name in FILTERED:
            for event in ("push", "pull_request"):
                assert runs_for(_patterns(name, event), path), (
                    f"{name}/{event} would skip {path}"
                )


def test_the_docs_site_still_builds_for_prose():
    """Skipping the heavy jobs must not leave a docs change unchecked.

    docs-site.yml has its own filter and is deliberately NOT in FILTERED: a
    prose pull request still gets the site built, which is the check that
    actually bears on it.
    """
    patterns = _patterns("docs-site", "pull_request")
    for path in ("docs/05-sql-surface.md", "website/astro.config.mjs"):
        assert runs_for(patterns, path), f"docs-site would skip {path}"


def test_every_network_download_retries():
    """A transient reset must not fail a pull request about something else.

    `curl: (35) Recv failure: Connection reset by peer` failed the dbt job on a
    dependabot PR that only bumped an action SHA. The download is repeated in
    eight jobs, so the chance of one run hitting it is eight times the chance
    of one job doing so, and the resulting red says nothing about the change
    under review.
    """
    ci = (ROOT / ".github" / "workflows" / "ci.yml").read_text(encoding="utf-8")
    downloads = [
        line for line in ci.splitlines()
        if "curl" in line and "duckdb" in line or "duckdb/releases/download" in line
    ]
    assert downloads, "found no duckdb download at all -- the check proved nothing"

    # The retry flags sit on the `curl` line; the URL is continued onto its own.
    curls = [line for line in ci.splitlines() if line.strip().startswith("curl")]
    assert curls, "found no curl invocation"
    for line in curls:
        assert "--retry" in line, f"a download without retries: {line.strip()}"


def test_the_secret_scan_is_not_filtered():
    """The one workflow that must see a prose-only commit.

    A docs-only change is a perfectly good way to leak a secret. Filtering
    `**.md` out of gitleaks would skip the scan on exactly the commits most
    likely to carry a pasted token, and the weekly cron would not notice until
    the credential had been public for days.

    Asserted as the ABSENCE of a filter rather than as a pattern list, because
    the failure being guarded against is someone adding one.
    """
    for name in UNFILTERED:
        for event in ("push", "pull_request"):
            assert not _patterns(name, event), (
                f"{name}/{event} has a path filter; the secret scan must see every commit"
            )
            assert runs_for(_patterns(name, event), "README.md"), (
                f"{name}/{event} would skip a README, where a pasted token lands"
            )
