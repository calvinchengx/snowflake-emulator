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
