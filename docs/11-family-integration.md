# Where this sits in the family

A peer of [databricks-emulator](https://github.com/calvinchengx/databricks-emulator)
and [fabric-emulator](https://github.com/calvinchengx/fabric-emulator), rather
than a member of the azure-emulators stack. Same doctrine, different vendor:
terminate the public API, attach a real engine, refuse the rest by name.

## The consumer

`snowflake-platform-tasks` stands up this emulator with the Contoso vendors and
runs the shared data product against it — bronze by `COPY INTO` from the
internal stage, silver and gold by dbt over models that live in
[contoso-data-product](https://github.com/calvinchengx/contoso-data-product) and
are shared with the Fabric and Databricks cells.

That sharing is what makes the boundary in
[the SQL surface](05-sql-surface.md) matter in practice. The product's silver
carries three constructs that were written in Spark's spelling; they are behind
dialect macros now, and the Snowflake branch emits `DATEADD`, `TABLE(GENERATOR
(...))` with `SEQ4()`, and `LATERAL FLATTEN` — which is why this emulator
answers all three. The emulator owes what **Snowflake** offers; the product
owes SQL a real account would accept.

## Releases reach consumers only when they are released

A change on `main` is invisible to every consumer until it is tagged, the image
is published, and the consumer's pin moves. This is not a formality — it has
bitten this family more than once, in both directions:

- a fix that existed on `main` for two releases while consumers ran without it
- a probe run against a host build, reporting behaviour the published image did
  not have

So: tag `v*`, let the release workflow publish the image and the
`snowflake-target` wheel, then bump `SNOWFLAKE_EMULATOR_VERSION` **and** the
wheel URL in the consumer, and relock. Both come from the same release.

The dispatch that would tell the consumer automatically needs a repository
secret this project does not have; until it does, the bump is a hand-written
commit and the consumer's scheduled run verifies whatever is pinned.
