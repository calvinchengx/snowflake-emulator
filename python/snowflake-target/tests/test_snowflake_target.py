from snowflake_target import Target, TargetError


def test_emulator_defaults(monkeypatch):
    for k in list(__import__("os").environ):
        if k.startswith("SNOWFLAKE_"):
            monkeypatch.delenv(k, raising=False)
    t = Target("emulator")
    assert t.is_emulator
    assert t.host.endswith(":8448")
    assert t.tls_verify is False
    assert t.seed_secrets_allowed is True


def test_real_rejects_localhost(monkeypatch):
    monkeypatch.setenv("SNOWFLAKE_HOST", "http://127.0.0.1:8448")
    monkeypatch.setenv("SNOWFLAKE_WAREHOUSE", "wh")
    monkeypatch.setenv("SNOWFLAKE_PASSWORD", "x")
    try:
        Target("real")
        raise AssertionError("localhost must fail")
    except TargetError as e:
        assert "localhost" in str(e).lower() or "127.0.0.1" in str(e)
