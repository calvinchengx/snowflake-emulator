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
	reThreePart    = regexp.MustCompile(`(?i)\b[A-Za-z0-9_]+\.(PUBLIC|GOLD|SILVER|MAIN)\.`)
	rePublicDot    = regexp.MustCompile(`(?i)\bPUBLIC\.`)
)

func rewriteSQL(sql string, sess session) (string, string, bool) {
	trimmed := strings.TrimSpace(sql)
	upper := strings.ToUpper(trimmed)

	if m := reUseWH.FindStringSubmatch(trimmed); m != nil {
		return m[1], "use_warehouse", true
	}
	if reUseDB.MatchString(trimmed) || reAlterSession.MatchString(trimmed) || reTxn.MatchString(trimmed) || reCommentOn.MatchString(trimmed) || reCreateDB.MatchString(trimmed) {
		return "SELECT 'ok' AS status", "", false
	}
	if reShowTerse.MatchString(trimmed) {
		return "SHOW TABLES", "", false
	}
	out := reTransientOR.ReplaceAllString(trimmed, "CREATE OR REPLACE TABLE")
	out = reTransient.ReplaceAllString(out, "CREATE TABLE")
	out = reTemp.ReplaceAllString(out, "CREATE OR REPLACE TABLE")
	out = reDescribe.ReplaceAllString(out, "DESCRIBE ")
	if strings.HasPrefix(upper, "CREATE") && strings.Contains(upper, "SCHEMA") {
		if m := reCreateSchema.FindStringSubmatch(out); m != nil {
			name := m[1]
			if strings.EqualFold(name, "PUBLIC") || strings.EqualFold(name, "MAIN") {
				return "SELECT 'ok' AS status", "", false
			}
			out = fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", name)
		}
	}
	out = reThreePart.ReplaceAllString(out, "")
	out = rePublicDot.ReplaceAllString(out, "")
	out = rewriteCurrentFns(out, sess)
	out = rewriteDateParts(out)
	return out, "", false
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
	start, inLiteral := 0, false
	for i := 0; i < len(sql); i++ {
		if sql[i] != '\'' {
			continue
		}
		if inLiteral {
			out.WriteString(sql[start : i+1])
		} else {
			out.WriteString(f(sql[start:i]))
			out.WriteByte('\'')
		}
		start, inLiteral = i+1, !inLiteral
	}
	if inLiteral {
		out.WriteString(sql[start:]) // unterminated: leave it for the engine to reject
	} else {
		out.WriteString(f(sql[start:]))
	}
	return out.String()
}
