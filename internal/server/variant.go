package server

import (
	"regexp"
	"strings"
)

// VARIANT and the colon path, which is how Snowflake reads semi-structured
// data -- and therefore how a bronze layer reads a vendor's JSON export.
//
//	CREATE TABLE raw (v VARIANT)
//	SELECT v:id::int, v:customer.email::string, v:lines[0].sku FROM raw
//
// DuckDB spells the type JSON and the access json_extract. The translation is
// mechanical; what it is not is optional. `COPY INTO ... TYPE = JSON` already
// worked, so the bytes could land -- and then nothing could read them in the
// spelling a Snowflake consumer would actually write.

var (
	// VARIANT, OBJECT and ARRAY are Snowflake's semi-structured types. DuckDB
	// has one JSON type that covers all three; the distinction Snowflake draws
	// is about what a value may hold, not about storage.
	reVariantType = regexp.MustCompile(`(?i)\b(VARIANT|OBJECT|ARRAY)\b(\s*(?:,|\)|$))`)

	// A cast straight after a path: v:a::string. Snowflake hands back the
	// value itself there, not its JSON spelling, so "a@x.com" must not arrive
	// with its quotes still on.
	// The type may carry precision: `v:amount::decimal(19,4)`. Without the
	// parenthesised part the cast closed early and `(19,4)` was left dangling
	// after it -- `CAST(... AS decimal)(19,4)` -- which the engine reported as
	// `syntax error at or near "("`, a message that points at the arguments
	// rather than at the cast that lost them.
	reTrailingCast = regexp.MustCompile(`^::\s*([A-Za-z_][A-Za-z0-9_]*(?:\s*\([^)]*\))?)`)
)

// rewriteVariantTypes maps Snowflake's semi-structured column types onto
// DuckDB's JSON, in DDL only -- the word `variant` in a string or a column
// name is left alone by the literal-skipping caller.
func rewriteVariantTypes(sql string) string {
	upper := strings.ToUpper(strings.TrimSpace(sql))
	if !strings.HasPrefix(upper, "CREATE") && !strings.HasPrefix(upper, "ALTER") {
		return sql
	}
	return outsideLiterals(sql, func(s string) string {
		return reVariantType.ReplaceAllString(s, "JSON$2")
	})
}

// rewriteColonPaths turns Snowflake's colon access into DuckDB's json_extract.
//
//	v:id            ->  json_extract(v, '$.id')
//	v:a.b           ->  json_extract(v, '$.a.b')
//	v:lines[0].sku  ->  json_extract(v, '$.lines[0].sku')
//	v:id::int       ->  CAST(json_extract_string(v, '$.id') AS int)
//
// The cast case is the one that matters and the one a pattern gets wrong.
// json_extract returns JSON, so a string comes back wearing its quotes and
// `v:email::string` would yield "a@x.com" INCLUDING them -- a value that
// compares unequal to itself across engines. json_extract_string unwraps it.
//
// `::` is Snowflake's cast operator and is never a path, so a colon that is
// part of one is skipped rather than parsed.
func rewriteColonPaths(sql string) string {
	var out strings.Builder
	i, inLiteral := 0, false
	for i < len(sql) {
		c := sql[i]
		if c == '\'' {
			inLiteral = !inLiteral
			out.WriteByte(c)
			i++
			continue
		}
		if inLiteral || c != ':' || (i+1 < len(sql) && sql[i+1] == ':') {
			out.WriteByte(c)
			i++
			continue
		}
		base, baseAt := trailingIdentifier(out.String())
		if base == "" {
			out.WriteByte(c)
			i++
			continue
		}
		path, next := readPath(sql, i+1)
		if path == "" {
			out.WriteByte(c)
			i++
			continue
		}
		body := out.String()[:baseAt]
		fn, cast := "json_extract", ""
		if m := reTrailingCast.FindStringSubmatch(sql[next:]); m != nil {
			fn, cast = "json_extract_string", m[1]
			next += len(m[0])
		}
		expr := fn + "(" + base + ", '$." + path + "')"
		if cast != "" {
			expr = "CAST(" + expr + " AS " + cast + ")"
		}
		out.Reset()
		out.WriteString(body)
		out.WriteString(expr)
		i = next
	}
	return out.String()
}

// trailingIdentifier reads the column reference immediately before a colon --
// `v`, `raw.v`, `"V"` -- and reports where it starts.
func trailingIdentifier(s string) (string, int) {
	end := len(s)
	for end > 0 && (s[end-1] == ' ' || s[end-1] == '\t') {
		return "", 0 // whitespace before the colon: not a path
	}
	start := end
	for start > 0 {
		c := s[start-1]
		if c == '_' || c == '$' || c == '.' || c == '"' ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			start--
			continue
		}
		break
	}
	if start == end {
		return "", 0
	}
	return s[start:end], start
}

// readPath reads the dotted and subscripted path after the colon and returns
// it in JSONPath spelling with the leading `$.` left to the caller.
func readPath(s string, from int) (string, int) {
	i := from
	var b strings.Builder
	for i < len(s) {
		c := s[i]
		switch {
		case c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9'):
			b.WriteByte(c)
			i++
		case c == '.' && i+1 < len(s) && isPathStart(s[i+1]):
			b.WriteByte('.')
			i++
		case c == '[':
			j := i
			for j < len(s) && s[j] != ']' {
				j++
			}
			if j >= len(s) {
				return b.String(), i
			}
			b.WriteString(strings.ReplaceAll(s[i:j+1], `"`, ""))
			i = j + 1
		default:
			return b.String(), i
		}
	}
	return b.String(), i
}

func isPathStart(c byte) bool {
	return c == '_' || c == '"' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
