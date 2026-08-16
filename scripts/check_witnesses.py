#!/usr/bin/env python3
"""Every 🟢 parity claim must name a witness in docs/witnesses.json."""
from __future__ import annotations

import json
import pathlib
import re
import sys

if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")

ROOT = pathlib.Path(__file__).resolve().parent.parent
PARITY = ROOT / "docs" / "parity.md"
MANIFEST = ROOT / "docs" / "witnesses.json"
CI = ROOT / ".github" / "workflows" / "ci.yml"
SKIP = {"Legend"}


def key_for(feature: str) -> str:
    text = re.sub(r"[*`_]", "", feature)
    text = re.sub(r"[^a-z0-9]+", "-", text.lower())
    return text.strip("-")


def main() -> int:
    manifest = json.loads(MANIFEST.read_text(encoding="utf-8"))
    ci = CI.read_text(encoding="utf-8")
    missing = []
    dangling = []
    greens = []
    section = None
    for line in PARITY.read_text(encoding="utf-8").splitlines():
        if line.startswith("## "):
            section = line[3:].strip()
            continue
        if not line.startswith("| ") or section in SKIP or section is None:
            continue
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        if len(cells) < 3 or cells[0] in ("Feature", "Emulator", "Type") or set(cells[0]) <= set("-"):
            continue
        if "🟢" not in cells[-1]:
            continue
        k = key_for(cells[0])
        greens.append(k)
        if k not in manifest:
            missing.append(k)
            continue
        for w in manifest[k].get("witnesses", []):
            if w.startswith("ci:"):
                job = w[3:]
                if f" {job}:" not in ci and f"\n  {job}:" not in ci:
                    dangling.append(w)
    extra = [k for k in manifest if k not in greens]
    if missing or dangling or extra:
        print("missing", missing)
        print("dangling", dangling)
        print("extra", extra)
        return 1
    print(f"🟢 capability claims: {len(greens)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
