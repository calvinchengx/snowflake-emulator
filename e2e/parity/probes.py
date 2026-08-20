"""What this emulator claims, as statements REAL SNOWFLAKE ACCEPTS.

Every entry here runs on Snowflake. That is the whole discipline: a probe is
not "something we support", it is "something a consumer may legitimately
write", and the emulator either answers it or is recorded as not answering it.
A probe Snowflake would reject does not belong here, because passing it would
prove nothing and failing it would be no gap.

`setup` runs first and its result is ignored -- it exists so a probe has a
table to talk about.
"""

SETUP = [
    "CREATE OR REPLACE TABLE p_t (id INT, m DECIMAL(19,4), d DATE, arr VARIANT)",
    "INSERT INTO p_t VALUES (1, 1.5, DATE '2026-01-01', '[{\"sku\":\"A\"}]')",
    "CREATE OR REPLACE TABLE p_json (id INT, customer VARIANT)",
]

# (area, feature, statement)
PROBES = [
    ("Identity", "Seeded PAT", "SELECT 1 AS ok"),
    ("Identity", "Session context functions",
     "SELECT current_warehouse(), current_database(), current_schema(), current_role()"),

    ("SQL", "SELECT, CTE, window", "WITH c AS (SELECT id, sum(m) OVER () AS s FROM p_t) SELECT * FROM c"),
    ("SQL", "QUALIFY", "SELECT id FROM p_t QUALIFY row_number() OVER (ORDER BY id) = 1"),
    ("SQL", "TRY_CAST", "SELECT TRY_CAST('x' AS INT) AS v"),
    ("SQL", "IFF / NVL / NVL2 / ZEROIFNULL", "SELECT IFF(1=1, NVL(NULL, 1), 2) AS a, NVL2(NULL,'x','y') AS b, ZEROIFNULL(NULL) AS c"),
    ("SQL", "TO_DATE / TO_VARCHAR / TO_TIMESTAMP", "SELECT TO_DATE('2026-01-01') AS a, TO_VARCHAR(1) AS b, TO_TIMESTAMP('2026-01-01') AS c"),
    ("SQL", "DATEDIFF", "SELECT DATEDIFF(day, DATE '2026-01-01', DATE '2026-02-01') AS v"),
    ("SQL", "DATEADD", "SELECT DATEADD(day, 1, DATE '2026-01-01') AS v"),
    ("SQL", "GENERATOR / SEQ4",
     "SELECT seq4() AS n FROM table(generator(rowcount => 3))"),
    ("SQL", "A date series, the way core's silver builds one",
     "SELECT DATEADD(day, seq4(), p_t.d) AS rate_date FROM p_t, "
     "table(generator(rowcount => 20000)) WHERE DATEADD(day, seq4(), p_t.d) <= p_t.d"),
    ("SQL", "DATEADD(month, ...) is refused",
     "SELECT DATEADD(month, 1, DATE '2026-01-01') AS v", "must_fail"),
    ("SQL", "LISTAGG / ARRAY_AGG", "SELECT LISTAGG(id, ',') AS a, ARRAY_AGG(id) AS b FROM p_t"),
    ("SQL", "MERGE", "MERGE INTO p_t t USING (SELECT 1 AS id) s ON t.id = s.id WHEN MATCHED THEN UPDATE SET t.id = 1"),
    ("SQL", "Decimal keeps its scale", "SELECT m FROM p_t"),
    ("SQL", "NULL is null, not a word", "SELECT NULL AS v"),
    ("SQL", "Unparseable SQL is refused", "THIS IS NOT SQL AT ALL", "must_fail"),

    ("Semi-structured", "VARIANT columns", "CREATE OR REPLACE TABLE p_v (v VARIANT)"),
    ("Semi-structured", "Colon path", "SELECT arr:x FROM p_t"),
    ("Semi-structured", "Colon path with a cast", "SELECT customer:email::string FROM p_json"),
    ("Semi-structured", "LATERAL FLATTEN over VARIANT",
     "SELECT f.value:sku::string AS sku FROM p_t, LATERAL FLATTEN(input => p_t.arr) f"),
    ("Semi-structured", "LATERAL FLATTEN over an array",
     "SELECT f.value::int AS v, f.index FROM LATERAL FLATTEN(input => [10,20]) f"),
    ("Semi-structured", "ARRAY_GENERATE_RANGE",
     "SELECT f.value::int AS i FROM LATERAL FLATTEN(input => ARRAY_GENERATE_RANGE(0, 3)) f"),
    ("Semi-structured", "PARSE_JSON", "SELECT PARSE_JSON('{\"a\":1}') AS v"),
    ("Semi-structured", "OBJECT_CONSTRUCT", "SELECT OBJECT_CONSTRUCT('a', 1) AS v"),

    ("Stages", "CREATE STAGE", "CREATE STAGE p_stage"),
    ("Stages", "LIST", "LIST @~"),
    ("Stages", "CREATE FILE FORMAT", "CREATE FILE FORMAT p_ff TYPE = CSV SKIP_HEADER = 1"),
    ("Stages", "COPY INTO from an internal stage",
     "COPY INTO p_nums FROM @~/parity.csv FILE_FORMAT = (TYPE = CSV, SKIP_HEADER = 1)"),
    ("Stages", "COPY INTO with a named format",
     "COPY INTO p_nums FROM @~/parity.csv FILE_FORMAT = (FORMAT_NAME = p_ff)"),
    ("Stages", "COPY INTO JSON", "COPY INTO p_json FROM @~/parity.json FILE_FORMAT = (TYPE = JSON)"),
    ("Stages", "External stages are refused", "CREATE STAGE p_ext URL = 's3://b/p'", "must_fail"),
    ("Stages", "An unsupported format option is refused",
     "COPY INTO p_nums FROM @~/parity.csv FILE_FORMAT = (TYPE = CSV, RECORD_DELIMITER = '|')", "must_fail"),
    ("Stages", "A prefix is refused",
     "COPY INTO p_nums FROM '@~/parity_feed/' FILE_FORMAT = (TYPE = CSV, SKIP_HEADER = 1)",
     "must_fail"),
    ("Stages", "PUT", "PUT file:///tmp/parity.csv @~"),
    ("Stages", "REMOVE", "REMOVE @~/parity.csv"),
    ("Stages", "INFER_SCHEMA", "SELECT * FROM TABLE(INFER_SCHEMA(LOCATION => '@~/parity.csv'))"),

    ("Catalog", "SHOW TABLES", "SHOW TABLES"),
    ("Catalog", "DESCRIBE TABLE", "DESCRIBE TABLE p_t"),
    ("Catalog", "information_schema.columns",
     "SELECT column_name FROM information_schema.columns WHERE table_name = 'p_t'"),
    ("Catalog", "SHOW FUNCTIONS", "SHOW USER FUNCTIONS"),
    ("Catalog", "CREATE SCHEMA", "CREATE SCHEMA IF NOT EXISTS p_schema"),
    ("Catalog", "Time Travel", "SELECT * FROM p_t AT(OFFSET => -60)"),
    ("Catalog", "CLONE", "CREATE TABLE p_clone CLONE p_t"),
    ("Catalog", "GRANT / roles", "GRANT SELECT ON TABLE p_t TO ROLE p_role"),

    ("Orchestration", "CREATE TASK", "CREATE TASK p_task SCHEDULE = '1 minute' AS SELECT 1"),
    ("Orchestration", "Task graphs (AFTER)", "CREATE TASK p_task_child AFTER p_task AS SELECT 2"),
    ("Orchestration", "ALTER TASK RESUME / SUSPEND", "ALTER TASK p_task SUSPEND"),
    ("Orchestration", "SHOW TASKS", "SHOW TASKS"),
    ("Orchestration", "EXECUTE TASK", "EXECUTE TASK p_task"),
    ("Orchestration", "DROP TASK", "DROP TASK p_task_child"),
    ("Orchestration", "USING CRON is refused",
     "CREATE TASK p_cron SCHEDULE = 'USING CRON 0 9 * * * UTC' AS SELECT 1", "must_fail"),
    # A MANUAL TASK: neither SCHEDULE nor AFTER, run only by EXECUTE TASK. This
    # probe used to assert the opposite -- that such a task is REFUSED -- which
    # is what a wrong belief looks like once it has been written down twice, in
    # the code and in the thing that measures the code. Snowflake requires a
    # schedule only for a task that must start itself.
    ("Orchestration", "A manual task (no SCHEDULE, no AFTER)",
     "CREATE TASK p_manual WAREHOUSE = parity_wh AS SELECT 1"),
    ("Orchestration", "EXECUTE TASK on a manual task", "EXECUTE TASK p_manual"),
    ("Orchestration", "CREATE STREAM", "CREATE STREAM p_stream ON TABLE p_t"),
    ("Orchestration", "Reading a stream", "SELECT count(*) FROM p_stream"),
    ("Orchestration", "SYSTEM$STREAM_HAS_DATA", "SELECT SYSTEM$STREAM_HAS_DATA('p_stream') AS has"),
    ("Orchestration", "SHOW STREAMS", "SHOW STREAMS"),
    ("Orchestration", "DROP STREAM", "DROP STREAM p_stream"),
    ("Orchestration", "Stored procedures", "CALL system$wait(1)"),

    ("Warehouses", "CREATE / SHOW / SUSPEND", "SHOW WAREHOUSES"),
]

# Features whose value is correct but whose TYPE or shape diverges from
# Snowflake in a way a consumer can see. Recorded here so a green probe is not
# read as full fidelity.
CAVEATS = {
    "LATERAL FLATTEN over VARIANT": (
        "value and index only. SEQ, KEY, PATH and THIS are not produced, and "
        "OUTER, RECURSIVE, PATH and MODE are refused by name because each "
        "changes which rows come back."
    ),
    "CREATE TASK": (
        "SCHEDULE takes '<n> SECOND | MINUTE | HOUR'. USING CRON is refused by "
        "name -- a cron expression means specific wall-clock times, and firing "
        "on an interval instead would be a schedule that is not the one asked "
        "for. WHEN is refused for the same reason in the other direction: a "
        "predicate that is never evaluated makes a conditional task "
        "unconditional."
    ),
    "EXECUTE TASK": (
        "Runs the named task and everything downstream of it, as Snowflake "
        "does. A resumed root task also fires on its own interval. "
        "TASK_HISTORY() is not implemented."
    ),
    "CREATE STREAM": (
        "APPEND-ONLY, and it proves it rather than assuming it. DuckDB keeps no "
        "change log, so the stream remembers the first rowid it has not shown "
        "and a checksum of the rows it has. If a row before that point is "
        "updated or deleted the stream REFUSES TO BE READ, naming what "
        "happened -- Snowflake would report those as DELETE and INSERT rows, "
        "and answering without them would silently drop the change. "
        "METADATA$ACTION is always INSERT for the same reason."
    ),
    "DATEADD": (
        "DAY, WEEK, HOUR, MINUTE and SECOND. MONTH, QUARTER and YEAR are "
        "refused: every DuckDB spelling of them widens a DATE to a TIMESTAMP, "
        "and a CASE cannot return two types, so the answer would carry the "
        "wrong type for a DATE argument. A TIMESTAMP given to the day form is "
        "an error here where Snowflake would answer."
    ),
    "A prefix is refused": (
        "Real Snowflake resolves a stage reference by prefix and loads EVERY "
        "file under it, which is the ordinary way to load a paged feed. This "
        "resolves one name and the `.gz` AUTO_COMPRESS leaves, so a prefix is "
        "refused BY NAME. It used to come back as duckdb's own words about a "
        "path inside the container, which left the reader to work out that the "
        "feature was missing rather than the file."
    ),
    "PUT": (
        "The driver uploads the bytes itself, as it does against a real "
        "account: the answer names LOCAL_FS and the stage directory, and the "
        "connector's file transfer agent does the copying. AUTO_COMPRESS "
        "defaults to TRUE, so the stage holds `<name>.gz`. Set "
        "SNOWFLAKE_STAGE_CLIENT_DIR when the client sees the stage at a "
        "different path than the server does."
    ),
    "LIST": (
        "Walks the whole stage directory, so a named stage created inside it "
        "appears in the user stage's listing."
    ),
}


# Richer evidence than the parity probe itself, where a suite already proves
# the claim end to end with a real client. Everything not named here is
# witnessed by the parity job, which is a real witness: it runs every
# statement against the built image and fails if the answer changes.
WITNESSES = {
    "Seeded PAT": ["ci:e2e-sdk", "go:TestLoginRejectsDevAndEmpty"],
    "SELECT, CTE, window": ["ci:e2e-sql"],
    "COPY INTO from an internal stage": ["ci:e2e-sql"],
    "COPY INTO with a named format": ["ci:e2e-sql"],
    "CREATE / SHOW / SUSPEND": ["ci:e2e-sql"],
    # ci:parity proves it is REFUSED. The claim is that it is refused BY NAME,
    # and only the Go test reads the message, so the row cites both. A witness
    # that does not check the thing the row claims is the assertion one level
    # off from the fact.
    "A prefix is refused": ["ci:parity", "go:TestAPrefixIsRefusedByNameRatherThanByDuckdb"],
    "PUT": ["ci:e2e-put", "go:TestPutAnswersTheContractBothDriversRead"],
}
