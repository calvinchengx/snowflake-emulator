package server

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var (
	reTransientOR  = regexp.MustCompile(`(?i)CREATE\s+OR\s+REPLACE\s+TRANSIENT\s+TABLE`)
	reTransient    = regexp.MustCompile(`(?i)CREATE\s+TRANSIENT\s+TABLE`)
	reTemp         = regexp.MustCompile(`(?i)CREATE\s+OR\s+REPLACE\s+TEMPORARY\s+TABLE`)
	reUseWH        = regexp.MustCompile(`(?i)^USE\s+WAREHOUSE\s+([A-Za-z0-9_]+)`)
	reUseDB        = regexp.MustCompile(`(?i)^USE\s+(?:DATABASE|SCHEMA)\s+`)
	reAlterSession = regexp.MustCompile(`(?i)^ALTER\s+SESSION\b`)
	reTxn          = regexp.MustCompile(`(?i)^(BEGIN|COMMIT|ROLLBACK|START\s+TRANSACTION)\b`)
	reCommentOn    = regexp.MustCompile(`(?i)^COMMENT\s+ON\b`)
	reShowTerse    = regexp.MustCompile(`(?i)^SHOW\s+TERSE\s+(OBJECTS|TABLES|VIEWS)\b`)
	reDescribe     = regexp.MustCompile(`(?i)^DESC(?:RIBE)?\s+TABLE\s+`)
	reCreateDB     = regexp.MustCompile(`(?i)^CREATE\s+(?:OR\s+REPLACE\s+)?DATABASE\b`)
	reCreateSchema = regexp.MustCompile(`(?i)^CREATE\s+(?:OR\s+REPLACE\s+)?SCHEMA\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:[A-Za-z0-9_]+\.)?([A-Za-z0-9_]+)`)
	// QUOTED IDENTIFIERS TOO, and they are not a rare spelling: dbt-snowflake
	// quotes every part when it drops an existing relation --
	// `drop table if exists "TEST_DB"."PUBLIC"."P_DBT_ONE" cascade` -- which is
	// what a SECOND run of the same models issues. The first run has nothing to
	// drop, so an unquoted-only rewrite passed every first run and failed every
	// one after it, with `Catalog "TEST_DB" does not exist!` naming a catalog
	// the caller never mentioned.
	//
	// The closing quote of the TABLE is deliberately left alone: stripping
	// `"TEST_DB"."PUBLIC".` from the above leaves `"P_DBT_ONE"`, still a
	// balanced quoted identifier. Consuming the opening quote as well would
	// leave `P_DBT_ONE"` and turn a name into a syntax error.
	// THE MIDDLE PART IS NOT A WHITELIST ANY MORE. It used to be
	// `(PUBLIC|GOLD|SILVER|MAIN)` -- four names, three of them this family's
	// own -- so `TEST_DB.BRONZE.t` fell straight through to the engine and
	// came back `Catalog with name TEST_DB does not exist!`. Which schema
	// names a project happens to use is not something its author would think
	// to check, and dbt-snowflake emits a three-part name for every model.
	//
	// Only the DATABASE is removed now, and only when it is the session's own.
	// Matching any leading identifier would eat the first part of an ordinary
	// struct access -- `v.customer.email` is `a.b.c` too -- and the session
	// database is never empty (it defaults to TEST_DB), so the narrow test
	// costs nothing.
	reThreePart = regexp.MustCompile(`(?i)("?\b[A-Za-z0-9_$]+\b"?)\.("?[A-Za-z0-9_$]+"?\.)`)
	// The optional leading group is what stops PUBLIC being stripped out of
	// ANOTHER database's name: with the session on TEST_DB, `OTHER_DB.PUBLIC.t`
	// must stay whole, or it becomes `OTHER_DB.t` -- a table in a catalog that
	// does not exist, which is a worse error than the honest one and points at
	// the wrong part of the name.
	rePublicDot = regexp.MustCompile(`(?i)("?[A-Za-z0-9_$]+"?\.)?("?\bPUBLIC\b"?\.)`)
)

func rewriteSQL(sql string, sess session) (string, string, bool, error) {
	trimmed := strings.TrimSpace(sql)
	upper := strings.ToUpper(trimmed)

	if m := reUseWH.FindStringSubmatch(trimmed); m != nil {
		return m[1], "use_warehouse", true, nil
	}
	if reUseDB.MatchString(trimmed) || reAlterSession.MatchString(trimmed) || reTxn.MatchString(trimmed) || reCommentOn.MatchString(trimmed) || reCreateDB.MatchString(trimmed) {
		return "SELECT 'ok' AS status", "", false, nil
	}
	if reShowTerse.MatchString(trimmed) {
		return "SHOW TABLES", "", false, nil
	}
	out := reTransientOR.ReplaceAllString(trimmed, "CREATE OR REPLACE TABLE")
	out = reTransient.ReplaceAllString(out, "CREATE TABLE")
	out = reTemp.ReplaceAllString(out, "CREATE OR REPLACE TABLE")
	out = reDescribe.ReplaceAllString(out, "DESCRIBE ")
	if strings.HasPrefix(upper, "CREATE") && strings.Contains(upper, "SCHEMA") {
		if m := reCreateSchema.FindStringSubmatch(out); m != nil {
			name := m[1]
			if strings.EqualFold(name, "PUBLIC") || strings.EqualFold(name, "MAIN") {
				return "SELECT 'ok' AS status", "", false, nil
			}
			out = fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", name)
		}
	}
	out = stripDatabaseQualifier(out, sess.Database)
	out = rewriteCurrentFns(out, sess)
	out = rewriteDateParts(out)
	out = rewriteVariantTypes(out)
	out = rewriteSnowflakeTypes(out)
	out = rewriteColonPaths(out)
	out = rewriteGenerator(out)
	added, aerr := rewriteDateAdd(out)
	if aerr != nil {
		return "", "", false, aerr
	}
	out = added
	flat, err := rewriteFlatten(out)
	if err != nil {
		return "", "", false, err
	}
	return flat, "", false, nil
}

func rewriteCurrentFns(sql string, sess session) string {
	wh := sess.Warehouse
	if wh == "" {
		wh = "COMPUTE_WH"
	}
	db := sess.Database
	if db == "" {
		db = "TEST_DB"
	}
	sch := sess.Schema
	if sch == "" {
		sch = "PUBLIC"
	}
	repl := []struct{ re, val string }{
		{`(?i)current_warehouse\s*\(\s*\)`, "'" + wh + "'"},
		{`(?i)current_database\s*\(\s*\)`, "'" + db + "'"},
		{`(?i)current_schema\s*\(\s*\)`, "'" + sch + "'"},
		{`(?i)current_role\s*\(\s*\)`, "'ACCOUNTADMIN'"},
		{`(?i)current_version\s*\(\s*\)`, "'8.0.0-emulator'"},
		{`(?i)current_account\s*\(\s*\)`, "'test'"},
		{`(?i)current_user\s*\(\s*\)`, "'ADMIN'"},
	}
	out := sql
	for _, r := range repl {
		out = regexp.MustCompile(r.re).ReplaceAllString(out, r.val)
	}
	return out
}

func extractSQL(raw []byte) string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return strings.TrimSpace(string(raw))
	}
	for _, k := range []string{"sqlText", "sql", "statement"} {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	if data, ok := m["data"].(map[string]any); ok {
		for _, k := range []string{"sqlText", "sql", "statement"} {
			if s, ok := data[k].(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	return ""
}

var (
	// DATEDIFF(part, a, b) takes the part as a BARE KEYWORD in Snowflake.
	// DuckDB parses that as a column reference and says so -- `Binder Error:
	// Referenced column "day" not found` -- which is why this one cannot be a
	// macro like the rest: the argument does not survive being an expression.
	// Quoting it is the whole translation; DuckDB's date_diff takes the part
	// as a string and returns the same integer count Snowflake does.
	reDateDiff = regexp.MustCompile(`(?i)\bDATEDIFF\s*\(\s*([A-Za-z_]+)\s*,`)
)

// rewriteDateParts turns Snowflake's DATEDIFF into DuckDB's date_diff.
//
//	DATEDIFF(day, a, b)  ->  date_diff('day', a, b)
//
// Only the first argument is touched; the rest is DuckDB's to parse, so a
// nested call or a comma inside a string later in the call is not this
// function's problem.
//
// DATEADD IS DELIBERATELY NOT HERE, and the reason is a type. Snowflake's
// DATEADD returns a DATE when it is given one and the part is day or coarser;
// every DuckDB spelling reachable from a macro -- d + to_days(n) and the rest
// -- returns TIMESTAMP, and a CASE over the part cannot return two types.
// Shipping it would put a timestamp where a consumer's model declares a date,
// which is the same class of defect as answering `text` for a decimal: right
// value, wrong type, invisible to any row check. It needs the argument
// boundaries parsed so the part can stay a keyword in `d + INTERVAL (n) day`,
// and that is worth its own change rather than a plausible one bolted here.
func rewriteDateParts(sql string) string {
	return outsideLiterals(sql, func(s string) string {
		return reDateDiff.ReplaceAllString(s, "date_diff('$1',")
	})
}

// outsideLiterals applies f to the parts of sql that are not inside a single
// quoted string.
//
// Without it this rewrite reaches into text it has no business touching:
// `SELECT 'DATEDIFF(day, a, b)' AS note` came out as
// `SELECT 'date_diff('day', a, b)' AS note`, which is not the same string and
// is not even valid SQL any more -- the quote count changed. A model carrying
// that spelling in a comment string, or a WHERE clause matching on it, would
// have been corrupted quietly.
//
// ” is SQL's escape for a quote inside a literal, and it needs no special
// case here: it reads as a literal closing and another opening immediately,
// which leaves the same regions inside and outside.
func outsideLiterals(sql string, f func(string) string) string {
	var out strings.Builder
	start := 0
	for _, r := range codeRegions(sql) {
		out.WriteString(sql[start:r.from])
		out.WriteString(f(sql[r.from:r.to]))
		start = r.to
	}
	out.WriteString(sql[start:])
	return out.String()
}

type region struct{ from, to int }

// codeRegions returns the spans of sql that are neither a quoted string nor a
// comment.
//
// COMMENTS ARE THE HALF THIS ORIGINALLY MISSED, and it cost a whole model. A
// `--` comment containing an apostrophe -- "the rate in force on Saturday is
// Friday's" -- flipped the scanner into thinking a string literal had opened,
// so every rewrite after that point in the statement silently did not happen.
// The generator, FLATTEN, DATEDIFF and the colon path all stopped, and DuckDB
// met `table(generator(rowcount => 20000))` raw and said `syntax error at or
// near "table"`. One apostrophe in one comment, and the engine looked like it
// had lost a feature it has.
//
// So a comment is skipped as a unit, and a quote inside one is prose.
func codeRegions(sql string) []region {
	var out []region
	start, i := 0, 0
	for i < len(sql) {
		switch {
		case sql[i] == '\'':
			out = append(out, region{start, i})
			i++
			for i < len(sql) && sql[i] != '\'' {
				i++
			}
			i++ // the closing quote, or past the end when unterminated
			start = i
		case sql[i] == '-' && i+1 < len(sql) && sql[i+1] == '-':
			out = append(out, region{start, i})
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			start = i
		case sql[i] == '/' && i+1 < len(sql) && sql[i+1] == '*':
			out = append(out, region{start, i})
			i += 2
			for i+1 < len(sql) && (sql[i] != '*' || sql[i+1] != '/') {
				i++
			}
			i = min(i+2, len(sql))
			start = i
		default:
			i++
		}
	}
	if start <= len(sql) {
		out = append(out, region{start, len(sql)})
	}
	return out
}

// stripDatabaseQualifier turns `db.schema.table` into `schema.table`, and then
// the DEFAULT schema into nothing at all.
//
// WHAT CHANGED AND WHY IT MATTERS MORE THAN THE WHITELIST. Removing the whole
// `db.schema.` prefix flattened every schema into duckdb's default one, so
// TEST_DB.SILVER.orders and TEST_DB.GOLD.orders were ONE TABLE. Measured on
// the published image: creating both and then selecting from the silver one
// returned the gold row, with no error anywhere. Two schemas, one table, a
// wrong answer reported as success -- which is the defect shape this
// repository keeps meeting, and it was hiding underneath a narrower bug.
//
// It also made a name disagree with itself: `TEST_DB.GOLD.dual` was created in
// the default schema, so `SELECT FROM GOLD.dual` afterwards could not find it.
// Keeping the schema makes the two spellings the same table, as they are on an
// account.
//
// PUBLIC STILL BECOMES THE DEFAULT SCHEMA, deliberately. Snowflake's default
// schema is PUBLIC and duckdb's is main; treating them as the same concept is
// what keeps every table that already exists where it is. A schema that is not
// the default now resolves to a real schema of that name -- which does move
// tables created under GOLD or SILVER, and is the point.
//
// OUTSIDE LITERALS, which the old form was not: `SELECT 'TEST_DB.PUBLIC.orders'`
// came back as `orders`. A rewrite that edits data rather than SQL is wrong
// however good its intentions, and a string is data.
func stripDatabaseQualifier(sql, db string) string {
	return outsideLiterals(sql, func(s string) string {
		if db != "" {
			s = reThreePart.ReplaceAllStringFunc(s, func(m string) string {
				parts := reThreePart.FindStringSubmatch(m)
				if !strings.EqualFold(unquoteIdent(parts[1]), db) {
					return m
				}
				return parts[2]
			})
		}
		return rePublicDot.ReplaceAllStringFunc(s, func(m string) string {
			if parts := rePublicDot.FindStringSubmatch(m); parts[1] != "" {
				return m // qualified by something else: not ours to touch
			}
			return ""
		})
	})
}

func unquoteIdent(s string) string { return strings.Trim(strings.TrimSpace(s), `"`) }
