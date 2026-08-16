"""One toggle between snowflake-emulator and a real Snowflake account."""

from __future__ import annotations

import os
from pathlib import Path


class TargetError(RuntimeError):
    pass


def _env(name, default=None):
    v = os.environ.get(name)
    return v if v not in (None, "") else default


class Target:
    def __init__(self, name):
        if name not in ("emulator", "real"):
            raise TargetError(f"SNOWFLAKE_TARGET must be emulator|real, got {name!r}")
        self.name = name
        if name == "emulator":
            self.host = _env("SNOWFLAKE_EMULATOR_URL", "http://127.0.0.1:8448").rstrip("/")
            self.account = _env("SNOWFLAKE_ACCOUNT", "test")
            self.warehouse = _env("SNOWFLAKE_WAREHOUSE", "COMPUTE_WH")
            self.database = _env("SNOWFLAKE_DATABASE", "TEST_DB")
            self.schema = _env("SNOWFLAKE_SCHEMA", "PUBLIC")
            self.tls_verify = False
            self.seed_secrets_allowed = True
            self.data_dir = Path(_env("SNOWFLAKE_DATA_DIR", "./data"))
        else:
            host = _env("SNOWFLAKE_HOST")
            if not host:
                raise TargetError("SNOWFLAKE_TARGET=real requires SNOWFLAKE_HOST")
            if "localhost" in host.lower() or "127.0.0.1" in host:
                raise TargetError(f"SNOWFLAKE_TARGET=real was given a localhost host ({host})")
            self.host = host.rstrip("/")
            self.account = _env("SNOWFLAKE_ACCOUNT")
            self.warehouse = _env("SNOWFLAKE_WAREHOUSE")
            if not self.warehouse:
                raise TargetError("SNOWFLAKE_TARGET=real requires SNOWFLAKE_WAREHOUSE")
            self.database = _env("SNOWFLAKE_DATABASE", "TEST_DB")
            self.schema = _env("SNOWFLAKE_SCHEMA", "PUBLIC")
            self.tls_verify = True
            self.seed_secrets_allowed = False
            self.data_dir = None
            if not _env("SNOWFLAKE_PASSWORD") and not _env("SNOWFLAKE_PRIVATE_KEY"):
                raise TargetError("SNOWFLAKE_TARGET=real needs SNOWFLAKE_PASSWORD or SNOWFLAKE_PRIVATE_KEY")

    @property
    def is_emulator(self):
        return self.name == "emulator"

    @property
    def password(self) -> str:
        p = _env("SNOWFLAKE_PASSWORD")
        if p:
            return p
        if not self.is_emulator:
            raise TargetError("SNOWFLAKE_TARGET=real needs SNOWFLAKE_PASSWORD")
        pat = (self.data_dir or Path("./data")) / "admin.pat"
        if not pat.is_file():
            raise TargetError(f"emulator PAT not found at {pat}")
        return pat.read_text(encoding="utf-8").strip()

    def warehouse(self, name: str):
        return type("Warehouse", (), {"id": name, "name": name})()

    def refuse_seed_secrets(self):
        if not self.seed_secrets_allowed:
            raise TargetError("seed_secrets is emulator-only")


_cached = None


def target(name=None, fresh=False):
    global _cached
    if name is None and not fresh and _cached is not None:
        return _cached
    t = Target(name or _env("SNOWFLAKE_TARGET", "emulator"))
    if name is None:
        _cached = t
    return t
