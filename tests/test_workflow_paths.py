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
FILTERED = ["ci", "lint", "codeql", "security"]


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
    """Ordered evaluation: the LAST pattern that matches decides."""
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
