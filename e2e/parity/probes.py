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
    "CREATE OR REPLACE TABLE p_prefix (n INT)",
    # For the task-body probes below. The stream is created BEFORE the rows are
    # inserted, so it genuinely owes them -- a stream over an unchanged table
    # would let the task probe pass by inserting nothing.
    "CREATE OR REPLACE TABLE p_copy_out (n INT)",
    "CREATE OR REPLACE TABLE p_src2 (id INT)",
    "CREATE OR REPLACE STREAM p_stream2 ON TABLE p_src2",
    "INSERT INTO p_src2 VALUES (1),(2)",
    "CREATE OR REPLACE TABLE p_stream_out (id INT)",
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
    # parity_feed holds two parts of two rows each. Loading the PREFIX must
    # take both, so the count is what the probe turns on -- naming one file
    # would load two rows and pass a probe that only checked for success.
    ("Stages", "A prefix loads every file under it",
     "COPY INTO p_prefix FROM '@~/parity_feed/' FILE_FORMAT = (TYPE = CSV, SKIP_HEADER = 1)"),
    ("Stages", "The prefix really loaded both parts",
     "SELECT CASE WHEN count(*) = 4 THEN 0 ELSE "
     "CAST('the prefix loaded the wrong number of rows' AS INT) END AS ok FROM p_prefix"),
    ("Stages", "PUT", "PUT file:///tmp/parity.csv @~"),
    ("Stages", "REMOVE", "REMOVE @~/parity.csv"),
    # FILE_FORMAT IS REQUIRED, and the old probe here omitted it -- so the
    # statement this file was measuring is one a real account rejects, which
    # makes a red row unreadable: it could mean the feature is missing or the
    # probe is wrong. Both forms are measured now.
    ("Stages", "INFER_SCHEMA",
     "SELECT * FROM TABLE(INFER_SCHEMA(LOCATION => '@~/parity_feed/', FILE_FORMAT => 'p_ff'))"),
    ("Stages", "INFER_SCHEMA composes (WHERE over the result)",
     "SELECT \"COLUMN_NAME\" FROM TABLE(INFER_SCHEMA(LOCATION => '@~/parity_feed/', "
     "FILE_FORMAT => 'p_ff')) WHERE \"TYPE\" <> 'TEXT'"),
    ("Stages", "INFER_SCHEMA without FILE_FORMAT is refused",
     "SELECT * FROM TABLE(INFER_SCHEMA(LOCATION => '@~/parity_feed/'))", "must_fail"),
    ("SQL", "Snowflake scalar type names in DDL",
     "CREATE OR REPLACE TABLE p_types (a NUMBER(38,0), b NUMBER, c TIMESTAMP_NTZ, d TIMESTAMP_LTZ)"),

    ("Tasks and streams", "TASK_HISTORY",
     "SELECT NAME, STATE FROM TABLE(INFORMATION_SCHEMA.TASK_HISTORY(RESULT_LIMIT => 10))"),

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
    # NO OPTION CLAUSE AT ALL -- the barest manual task Snowflake accepts, where
    # naming no WAREHOUSE means a serverless task. Spelled without options
    # DELIBERATELY: the first version of this probe wrote
    # `... WAREHOUSE = parity_wh AS SELECT 1`, which made the regex match and
    # passed while the barest form still fell through to duckdb. A probe that
    # picks a comfortable spelling measures the spelling.
    ("Orchestration", "A manual task (no SCHEDULE, no AFTER)",
     "CREATE TASK p_manual AS SELECT 1"),
    ("Orchestration", "EXECUTE TASK on a manual task", "EXECUTE TASK p_manual"),
    # A MANUAL TASK RUNS THE BODY IT WAS GIVEN, and the third probe is the one
    # that bites. Every probe above is judged on whether the STATEMENT
    # succeeded, which is one level away from whether anything happened: the
    # two above passed for months while a manual task whose body contained
    # ` AS ` stored a TRUNCATED body, ran that, and reported SUCCEEDED.
    #
    # `CREATE TASK t AS CREATE OR REPLACE TABLE x AS SELECT 1 AS n` kept only
    # `SELECT 1 AS n`. Both statements still succeed, so nothing here could see
    # it. Selecting from the table the body was supposed to create cannot pass
    # unless the right statement ran -- and a CTAS is what a dbt model compiles
    # to, so this is the body the Tasks consumer is actually made of.
    ("Orchestration", "A manual task with a CTAS body",
     "CREATE TASK p_body AS CREATE OR REPLACE TABLE p_body_out AS SELECT 1 AS n"),
    ("Orchestration", "EXECUTE TASK runs the CTAS", "EXECUTE TASK p_body"),
    # THE COUNT IS ASSERTED, not merely selected. Every probe here is judged on
    # whether the STATEMENT succeeded, and `SELECT count(*) FROM t` succeeds
    # just as well on nought rows -- so a probe named "actually ran" that only
    # counts proves the TABLE exists, not that the work happened. The CASE
    # casts a sentence to INT when the count is wrong, which duckdb refuses by
    # name, so a wrong number fails the probe and says what it was. (A division
    # by zero was the first idea and does NOT work: duckdb answers Infinity.)
    ("Orchestration", "The CTAS body actually ran",
     "SELECT CASE WHEN count(*) = 1 THEN 0 ELSE "
     "CAST('the CTAS body did not run' AS INT) END AS ok FROM p_body_out"),
    # A TASK BODY IS A SNOWFLAKE STATEMENT, not a duckdb one. The body used to
    # go straight to the engine, so a stream reference was never expanded and
    # COPY INTO was never rewritten -- both work outside a task, so the same
    # text meant two different things depending on who ran it. Stream-driven
    # CDC and stage loading are the two shapes a medallion is built from.
    ("Orchestration", "A task body loads from a stage",
     "CREATE TASK p_task_copy AS COPY INTO p_copy_out FROM '@~/parity.csv' "
     "FILE_FORMAT = (TYPE = CSV SKIP_HEADER = 1)"),
    ("Orchestration", "EXECUTE TASK runs the COPY INTO", "EXECUTE TASK p_task_copy"),
    ("Orchestration", "The task's COPY INTO actually loaded",
     "SELECT CASE WHEN count(*) = 2 THEN 0 ELSE "
     "CAST('the task loaded the wrong number of rows' AS INT) END AS ok FROM p_copy_out"),
    ("Orchestration", "A task body reads a stream",
     "CREATE TASK p_task_stream AS INSERT INTO p_stream_out SELECT id FROM p_stream2"),
    ("Orchestration", "EXECUTE TASK runs the stream read", "EXECUTE TASK p_task_stream"),
    # dbt PROJECTS ON SNOWFLAKE. dbt runs INSIDE the account here, which is what
    # lets a task graph chain run and test with AFTER -- Snowflake's own
    # orchestration example. See dbtproject.go for why a failing dbt run fails
    # the QUERY rather than returning Success = FALSE.
    ("Orchestration", "CREATE DBT PROJECT", "CREATE DBT PROJECT p_proj FROM '@~/parity_dbt'"),
    ("Orchestration", "SHOW DBT PROJECTS", "SHOW DBT PROJECTS"),
    ("Orchestration", "EXECUTE DBT PROJECT", "EXECUTE DBT PROJECT p_proj ARGS='run'"),
    ("Orchestration", "dbt really built the models",
     "SELECT CASE WHEN count(*) = 1 THEN 0 ELSE "
     "CAST('dbt reported success and built nothing' AS INT) END AS ok FROM p_dbt_two"),
    ("Orchestration", "A dbt failure fails the QUERY",
     "EXECUTE DBT PROJECT p_proj ARGS='build'", "must_fail"),
    ("Orchestration", "A dbt task with no warehouse is refused",
     "CREATE TASK p_dbt_nowh AS EXECUTE DBT PROJECT p_proj ARGS='run'", "must_fail"),
    ("Orchestration", "A task body runs EXECUTE DBT PROJECT",
     "CREATE TASK p_dbt_task WAREHOUSE = parity_wh AS EXECUTE DBT PROJECT p_proj ARGS='run'"),
    ("Orchestration", "EXECUTE TASK runs the dbt project", "EXECUTE TASK p_dbt_task"),
    ("Orchestration", "DROP DBT PROJECT", "DROP DBT PROJECT p_proj"),

    ("Orchestration", "The task's stream read actually inserted",
     "SELECT CASE WHEN count(*) = 2 THEN 0 ELSE "
     "CAST('the task inserted the wrong number of rows' AS INT) END AS ok FROM p_stream_out"),
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
    "EXECUTE DBT PROJECT": (
        "dbt runs in THIS image, on argv, the same way duckdb does -- no second "
        "service and no network hop between the statement and what executes it. "
        "The profile is generated under the name the PROJECT declares, so a "
        "project runs here without being edited to say an emulator-specific one."
    ),
    "A dbt failure fails the QUERY": (
        "`build` is not one of run, test or deps, so it is refused by name. "
        "Snowflake made dbt errors query failures in October 2025 precisely so "
        "tasks could handle them: a failed run that returned Success = FALSE "
        "from a SUCCESSFUL statement let a task graph run its downstream nodes "
        "anyway."
    ),
    "dbt really built the models": (
        "Read back from the model dbt was asked to build, so this cannot pass "
        "on a run that reported success and built nothing."
    ),
    "The task's COPY INTO actually loaded": (
        "Counted from the table the task loaded into. The body used to go "
        "straight to duckdb, which answers COPY INTO with a syntax error at "
        "INTO -- while the identical statement outside a task loaded fine."
    ),
    "The task's stream read actually inserted": (
        "Counted from the table the task inserted into. The body used to go "
        "straight to duckdb, which has no table for a stream name at all."
    ),
    "A manual task (no SCHEDULE, no AFTER)": (
        "The body is the whole statement after the task's own AS, including a "
        "body carrying its own AS -- a CREATE TABLE ... AS SELECT, which is "
        "what a dbt model compiles to. It was not: the optional properties "
        "group was greedy, so a task with no WAREHOUSE swallowed the body's "
        "first AS, stored a truncated statement, ran that, and reported "
        "SUCCEEDED."
    ),
    "The CTAS body actually ran": (
        "Selected back from the table the body creates, so this cannot pass on "
        "a task that succeeded while running a different statement. Every other "
        "probe here is judged on whether the STATEMENT succeeded, which is one "
        "level away from whether anything happened."
    ),
    "INFER_SCHEMA": (
        "TYPE reports the names an ACCOUNT reports -- NUMBER(38,0), "
        "TIMESTAMP_NTZ -- so a CREATE TABLE built from it is portable. "
        "DESCRIBE TABLE still reports the ENGINE's names (DECIMAL(38,0)), and "
        "deliberately: the family's `money_is_never_stored_as_float` contract "
        "accepts only `decimal` and `numeric` prefixes, so renaming what "
        "DESCRIBE reports would fail 52 gold contracts. The two statements "
        "answer in different vocabularies and that is recorded rather than "
        "reconciled."
    ),
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
        "Its runs, and the runs of everything downstream, are readable through TASK_HISTORY()."
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
    "A prefix loads every file under it": (
        "A stage reference matches every file whose path STARTS WITH it, which "
        "is Snowflake's rule and covers a directory, a partial name and an "
        "exact file with one behaviour. It was REFUSED by name until a "
        "consumer needed it: a task body is a single statement, so one COPY "
        "INTO per part file turns an eight-table bronze into thirty-odd chained "
        "tasks -- the emulator deciding the shape of a pipeline. The `.gz` that "
        "AUTO_COMPRESS leaves needs no special case, since the uncompressed "
        "name is a prefix of the compressed one."
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
    "The task's COPY INTO actually loaded": ["ci:parity", "go:TestATaskBodyIsASnowflakeStatement"],
    "The task's stream read actually inserted": ["ci:parity", "go:TestATaskBodyIsASnowflakeStatement"],
    "A manual task (no SCHEDULE, no AFTER)": ["ci:parity", "go:TestATaskWithNoOptionClauseAtAll"],
    "The CTAS body actually ran": ["ci:parity", "go:TestATaskWithNoOptionClauseAtAll"],
    "Seeded PAT": ["ci:e2e-sdk", "go:TestLoginRejectsDevAndEmpty"],
    "SELECT, CTE, window": ["ci:e2e-sql"],
    "COPY INTO from an internal stage": ["ci:e2e-sql"],
    "COPY INTO with a named format": ["ci:e2e-sql"],
    # The Go tests prove it is a relation and that it unions by name. NEITHER
    # proves the loop the feature exists for: that a CREATE TABLE built from
    # the answer then LOADS the file it described. e2e-infer does.
    "INFER_SCHEMA": ["go:TestInferSchemaIsARelationNotOneBlessedSentence",
                     "go:TestInferSchemaUnionsByNameOrItSilentlyDropsColumns",
                     "ci:e2e-infer"],
    "INFER_SCHEMA composes (WHERE over the result)":
        ["go:TestInferSchemaIsARelationNotOneBlessedSentence"],
    "INFER_SCHEMA without FILE_FORMAT is refused":
        ["go:TestInferSchemaRefusesWhatItCannotHonour"],
    "Snowflake scalar type names in DDL":
        ["go:TestSnowflakeScalarTypesReachDuckdb",
         "go:TestBareNumberIsThirtyEightZeroNotDuckdbsDefault"],
    "CREATE / SHOW / SUSPEND": ["ci:e2e-sql"],
    # ci:parity proves it is REFUSED. The claim is that it is refused BY NAME,
    # and only the Go test reads the message, so the row cites both. A witness
    # that does not check the thing the row claims is the assertion one level
    # off from the fact.
    "A prefix loads every file under it": ["ci:parity", "go:TestAPrefixResolvesToEveryFileUnderIt"],
    "The prefix really loaded both parts": ["ci:parity", "go:TestAPrefixResolvesToEveryFileUnderIt"],
    "PUT": ["ci:e2e-put", "go:TestPutAnswersTheContractBothDriversRead"],
    # ci:parity proves the function ANSWERS. What a driver needs is that it
    # answers CORRECTLY about a failure, and only the e2e runs a graph that
    # fails and reads the FAILED and SKIPPED rows back.
    "TASK_HISTORY": ["ci:e2e-tasks", "go:TestASkippedRunHasNoStartAndNoCompletion"],
}
