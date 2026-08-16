#!/usr/bin/env python3
"""Build the snowflake-target wheel and prove it installs from the artifact."""
from __future__ import annotations

import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
PKG = ROOT / "python" / "snowflake-target"
DIST = ROOT / "dist" / "target"


def main() -> int:
    version = os.environ.get("GITHUB_REF_NAME", "").lstrip("v") or "0.1.0"
    shutil.rmtree(DIST, ignore_errors=True)
    DIST.mkdir(parents=True)
    env = os.environ.copy()
    env["SETUPTOOLS_SCM_PRETEND_VERSION"] = version
    subprocess.check_call(
        [sys.executable, "-m", "pip", "wheel", "--no-deps", "-w", str(DIST), str(PKG)],
        env=env,
    )
    wheels = list(DIST.glob("snowflake_target-*.whl"))
    if not wheels:
        raise SystemExit(f"no snowflake_target wheel in {DIST}")
    work = Path(tempfile.mkdtemp(prefix="sf-target-wheel-"))
    subprocess.check_call(
        [sys.executable, "-m", "pip", "install", "--force-reinstall", str(wheels[0])],
        cwd=work,
    )
    probe = (
        "from snowflake_target import Target; "
        "t = Target('emulator'); "
        "assert t.host.endswith(':8448'); "
        "print('snowflake-target wheel ok', t.host)"
    )
    subprocess.check_call([sys.executable, "-c", probe], cwd=work)
    print(f"built {wheels[0].name}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
