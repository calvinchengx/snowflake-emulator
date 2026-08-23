#!/usr/bin/env python3
"""The landing page's numbers are the parity table's numbers.

A HAND-WRITTEN NUMBER ON A FRONT PAGE ROTS SILENTLY. docs/parity.md is
regenerated from a real run and CI fails when it drifts; site/index.html is
written by a person and nothing watched it. Two counts of the same thing, one
of them measured -- the unmeasured one is wrong the first time a probe is
added, and the front page is where a reader forms their impression.

So the page carries `data-parity-answered`, `data-parity-refused` and
`data-parity-total` attributes, and this asserts they equal what the table
says. It also checks the refusals the page names are exactly the ones refused.
"""

from __future__ import annotations

import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
PARITY = ROOT / "docs" / "parity.md"
LANDING = ROOT / "site" / "index.html"


def parity_counts() -> tuple[int, int, list[str]]:
    """Count TABLE ROWS, not emoji.

    A loose `grep -c` also matches the prose above the tables that explains
    what the marks mean, which is how a count comes out plausible and wrong.
    """
    answered = refused = 0
    refused_names: list[str] = []
    for line in PARITY.read_text(encoding="utf-8").splitlines():
        stripped = line.rstrip()
        if not stripped.startswith("|") or not stripped.endswith("|"):
            continue
        if stripped.endswith("🟢 |"):
            answered += 1
        elif stripped.endswith("🔴 |"):
            refused += 1
            refused_names.append(stripped.split("|")[1].strip())
    return answered, refused, refused_names


def attr(html: str, name: str) -> int:
    m = re.search(rf"{name}[^>]*>(\d+)<", html)
    if not m:
        raise SystemExit(f"site/index.html has no element carrying {name}")
    return int(m.group(1))


def main() -> int:
    answered, refused, refused_names = parity_counts()
    if answered == 0:
        raise SystemExit("read no rows from docs/parity.md -- the check proved nothing")
    html = LANDING.read_text(encoding="utf-8")

    problems = []
    for name, want in (
        ("data-parity-answered", answered),
        ("data-parity-refused", refused),
        ("data-parity-total", answered + refused),
    ):
        got = attr(html, name)
        if got != want:
            problems.append(f"{name}: page says {got}, parity.md says {want}")

    # The page names the refusals; a stale name is a promise the emulator no
    # longer breaks, or a gap it now has and does not admit to.
    for name in refused_names:
        if name not in html:
            problems.append(f"parity.md refuses {name!r} and the page does not say so")

    if problems:
        print("site/index.html disagrees with docs/parity.md:", file=sys.stderr)
        for p in problems:
            print(f"  - {p}", file=sys.stderr)
        print("\n  docs/parity.md is generated from a real run. Fix the page.", file=sys.stderr)
        return 1

    print(f"landing page agrees with parity.md: {answered} answered, {refused} refused")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
