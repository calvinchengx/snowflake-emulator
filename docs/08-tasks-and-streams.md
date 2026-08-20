# Tasks and streams

Snowflake's own scheduler, and the change a table has seen. Together they are
how a Snowflake pipeline runs without an external orchestrator.

## Task graphs

```sql
CREATE TASK t_root SCHEDULE = '1 MINUTE' AS INSERT INTO log VALUES ('root');
CREATE TASK t_mid  AFTER t_root AS INSERT INTO log VALUES ('mid');
CREATE TASK t_leaf AFTER t_mid  AS INSERT INTO log VALUES ('leaf');

EXECUTE TASK t_root;   -- Task t_root executed, 3 task(s) in the graph.
SELECT step FROM log;  -- root, mid, leaf
```

`EXECUTE TASK` runs the named task and everything downstream of it, as
Snowflake does. Dependency order is enforced rather than hoped for, and a cycle
is **refused rather than run** — Snowflake rejects one at creation, and running
it here would not stop.

`ALTER TASK … RESUME | SUSPEND`, `SHOW TASKS` and `DROP TASK` do what they say.

### The schedule actually fires

`SCHEDULE = '<n> SECOND | MINUTE | HOUR'`, and a resumed root task runs on its
interval. Storing an interval and never acting on it would be a task that
reports created, reports started, and never runs — a consumer would wait
forever with nothing to read. Measured: 0 rows before `RESUME`, 4 after four
seconds at one per second, and `SUSPEND` stops it.

`USING CRON` is **refused by name**. A cron expression means specific
wall-clock times, and firing on an interval instead would be a schedule that is
not the one asked for.

`WHEN` is refused for the mirror-image reason: a predicate that is never
evaluated turns a conditional task into an unconditional one.

A task with **neither `SCHEDULE` nor `AFTER`** is a **manual task**: created
suspended, never picked up by the scheduler, and run on demand by
`EXECUTE TASK`. That is what a pipeline driven by an orchestrator wants, and
what `CREATE TASK … AS EXECUTE DBT PROJECT` looks like when something else owns
the schedule.

This was refused until v0.1.7, and the reason given here — *"it could never
run"* — was **false**. Snowflake requires a schedule only for a task that must
START ITSELF. Refusing made a consumer invent a `SCHEDULE` it did not want,
which is the opposite of what an honest refusal is for: this emulator may refuse
what is genuinely absent upstream, never what is merely absent here.

## `TASK_HISTORY`, and why the rest of this page was not enough

```sql
SELECT NAME, STATE, ERROR_MESSAGE
FROM TABLE(INFORMATION_SCHEMA.TASK_HISTORY(TASK_NAME => 'T_ROOT'))
ORDER BY COMPLETED_TIME DESC LIMIT 1;
```

Everything above this section was green before this existed, and a consumer
still could not drive a pipeline from it. A driver could create a graph, resume
it, fire it with `EXECUTE TASK` — and never learn whether it worked. **That is
the reason the Snowflake Tasks cell in this family is named for an orchestrator
it does not use**: not missing tasks, missing answers.

It is a real relation, not a canned reply, so `WHERE`, `ORDER BY`, `LIMIT`,
joins and aggregates over it are the engine's own:

| column | |
|---|---|
| `NAME` | the task, uppercased as Snowflake reports an unquoted identifier |
| `STATE` | `SUCCEEDED`, `FAILED` or `SKIPPED` |
| `QUERY_TEXT` | the statement the task holds |
| `ERROR_MESSAGE` | why it failed, or which predecessor failed for a skip |
| `SCHEDULED_TIME` | when the graph run began |
| `QUERY_START_TIME` | when this task began, `NULL` for a skip |
| `COMPLETED_TIME` | when it finished, `NULL` for a skip |
| `SCHEDULED_FROM` | `EXECUTE TASK` or `SCHEDULE` |

`TASK_NAME` and `RESULT_LIMIT` are honoured. Any other argument is **refused by
name**, because silently dropping `SCHEDULED_TIME_RANGE_START` would answer a
question about the last hour with the whole history and look right doing it.

**This column list is a subset, and the missing ones are absent rather than
`NULL`.** A column that is always `NULL` reads as "this run had no value"
instead of "this emulator does not track it".

### A failure is written down, and so is what never ran

When a task in a graph fails, everything downstream of it is recorded
`SKIPPED`, naming the task that failed. Leaving those out would read as "not
started yet" to anything polling, which is how a driver waits forever on a
graph that already gave up. A skipped run carries no start and no completion,
because it never began.

### The scheduler writes to it too

`EXECUTE TASK` reported a failure to its caller. The scheduler **swallowed**
one: it broke out of the loop, logged nothing, stored nothing, so a resumed
root task failing every minute was indistinguishable from one succeeding every
minute. Both paths now run through the same recorded code, because a history
the unattended path does not write to is the one that matters least.

The history keeps the last 1000 runs. A consumer polling for a run it started
will always find it; one auditing a long history will not.

## Streams

```sql
CREATE STREAM s_src ON TABLE src;
INSERT INTO src VALUES (2, 'b');

SELECT SYSTEM$STREAM_HAS_DATA('s_src');      -- TRUE
SELECT id, v, "METADATA$ACTION" FROM s_src;  -- 2, b, INSERT
SELECT count(*) FROM s_src;                  -- still 1: a SELECT does not consume
INSERT INTO sink SELECT id, v FROM s_src;    -- DML does
SELECT count(*) FROM s_src;                  -- 0
```

### Append-only, and it proves it

DuckDB keeps no change log, so a stream remembers the first `rowid` it has not
shown — exactly right for a table only inserted into, and wrong the moment a
row before that point is updated or deleted.

So it also remembers a checksum of the rows it has already accounted for, and
**refuses to be read** if that checksum moves:

```
stream s_src cannot be read: rows in src before its offset have been updated
or deleted, and this emulator tracks appends only.
```

A stream that quietly missed an `UPDATE` is the worst answer available: a
pipeline built on it would drop changes and report success. `METADATA$ACTION`
is always `INSERT` for the same reason.

`TASK_HISTORY()` and stored procedures are not implemented.
