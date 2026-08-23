#!/usr/bin/env python3
"""Every internal link in the ASSEMBLED site resolves to a file that exists.

THE FAILURE THIS EXISTS FOR IS SILENT, and it happened while this script was
being written. Moving the docs from `/` to `/docs/` meant moving the site's
base path -- which lived in THREE files: website/astro.config.mjs,
scripts/sync-docs.mjs and scripts/parity-versions.mjs. Updating one of them
left a build that was green, a site that deployed, 32 pages that all rendered,
and 777 links pointing one directory above where the pages were.

A first version of this checker looked only at the landing page's four links
and passed. That is the shape of the mistake it is meant to catch, so it now
walks every HTML file in the tree that is actually served.
"""

from __future__ import annotations

import argparse
import pathlib
import re
import sys

HREF = re.compile(r'(?:href|src)="([^"]+)"')
EXTERNAL = ("http://", "https://", "//", "#", "data:", "mailto:", "javascript:")


def pages_prefix(root: pathlib.Path) -> str:
    """The path the site is served under, from the one place the base lives.

    Hard-coding `/snowflake-emulator/` here would make a FOURTH copy of the
    string whose duplication caused the bug this file guards.
    """
    base_mjs = root / "website" / "base.mjs"
    m = re.search(r"export const BASE = '([^']+)'", base_mjs.read_text(encoding="utf-8"))
    if not m:
        raise SystemExit(f"no BASE in {base_mjs}")
    first = [p for p in m.group(1).split("/") if p]
    return f"/{first[0]}/" if first else "/"


def resolve(site: pathlib.Path, page: pathlib.Path, href: str, prefix: str) -> bool:
    link = href.split("#", 1)[0].split("?", 1)[0]
    if not link:
        return True
    if link.startswith("/"):
        if link.startswith(prefix):
            link = link[len(prefix):]
        target = site / link.lstrip("/")
    else:
        target = page.parent / link
    for candidate in (target, target / "index.html", target.with_suffix(".html")):
        try:
            if candidate.is_file():
                return True
        except OSError:
            return False
    return False


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--site", required=True, type=pathlib.Path)
    ap.add_argument("--root", default=pathlib.Path(__file__).resolve().parent.parent,
                    type=pathlib.Path)
    args = ap.parse_args()

    if not (args.site / "index.html").is_file():
        raise SystemExit(f"no landing page at {args.site}/index.html -- was the site assembled?")

    prefix = pages_prefix(args.root)
    pages = sorted(args.site.rglob("*.html"))
    if not pages:
        raise SystemExit("no HTML in the assembled site -- the check proved nothing")

    checked = 0
    broken: list[tuple[str, str]] = []
    for page in pages:
        html = page.read_text(encoding="utf-8", errors="ignore")
        for href in HREF.findall(html):
            if href.startswith(EXTERNAL):
                continue
            checked += 1
            if not resolve(args.site, page, href, prefix):
                broken.append((str(page.relative_to(args.site)), href))

    if not checked:
        raise SystemExit("found no internal links at all -- the check proved nothing")

    if broken:
        print(f"{len(broken)} of {checked} internal links do not resolve:", file=sys.stderr)
        for page, href in broken[:25]:
            print(f"  {page} -> {href}", file=sys.stderr)
        if len(broken) > 25:
            print(f"  ... and {len(broken) - 25} more", file=sys.stderr)
        print("\n  If the docs moved, website/base.mjs moves with them.", file=sys.stderr)
        return 1

    print(f"{checked} internal links across {len(pages)} pages resolve")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
