package server

import (
	"fmt"
	"regexp"
	"strings"
)

// LATERAL FLATTEN, which is how Snowflake explodes an array into rows and so
// how a silver layer unpacks one. DuckDB spells the same thing UNNEST.
//
//	LATERAL FLATTEN(input => t.arr) f
//	  ->  LATERAL (SELECT unnest(t.arr) AS value, unnest(range(len(t.arr))) AS index) f
//
// The index is 0-based in both, and both restart it per input row -- checked
// against duckdb rather than assumed, on a two-row table whose arrays have
// different lengths.
//
// A SCANNER, NOT A PATTERN. The argument is an arbitrary expression: it can
// carry nested calls, commas and parentheses inside string literals. A regex
// for the closing paren gets those wrong on precisely the expressions worth
// flattening, which is the same reason the scalar functions are macros.

var reFlattenStart = regexp.MustCompile(`(?i)\b(LATERAL\s+|TABLE\s*\(\s*)?FLATTEN\s*\(`)

// rewriteFlatten turns every FLATTEN call in sql into DuckDB's UNNEST form.
// The error names an argument Snowflake accepts and this does not implement,
// rather than dropping it -- OUTER and RECURSIVE change which rows come back.
func rewriteFlatten(sql string) (string, error) {
	from := 0
	for {
		loc := reFlattenStart.FindStringSubmatchIndex(sql[from:])
		if loc == nil {
			return sql, nil
		}
		for i := range loc {
			if loc[i] >= 0 {
				loc[i] += from
			}
		}
		// A literal that happens to spell the call is text, not a call. The
		// DATEDIFF rewrite fell into this and came out with an odd number of
		// quotes; here it would replace someone's note with SQL.
		if insideLiteral(sql, loc[0]) {
			from = loc[0] + 1
			continue
		}
		open := loc[1] - 1 // the '(' the match ends on
		close, ok := matchParen(sql, open)
		if !ok {
			return sql, fmt.Errorf("FLATTEN( is not closed")
		}
		input, err := flattenInput(sql[open+1 : close])
		if err != nil {
			return sql, err
		}
		// A `TABLE(` prefix brings its own closing paren; consume it so the
		// replacement is not left wrapped in a stray one.
		end := close + 1
		prefix := ""
		if loc[2] >= 0 {
			lead := strings.ToUpper(strings.TrimSpace(sql[loc[2]:loc[3]]))
			if strings.HasPrefix(lead, "LATERAL") {
				prefix = "LATERAL "
			} else if after, ok := skipCloseParen(sql, end); ok {
				end = after
			}
		}
		// json_extract(to_json(X), '$[*]') rather than unnest(X) directly,
		// because FLATTEN's argument is a VARIANT and this emulator spells
		// VARIANT as DuckDB's JSON -- on which unnest refuses outright
		// (`Binder Error: UNNEST not supported here`). to_json is the identity
		// on JSON and converts a native LIST, so one spelling serves both, and
		// the values come back as JSON exactly as Snowflake hands back VARIANT.
		// Casts still work through it: `f.value::int` and `f.value:sku::string`
		// were both checked against the container.
		elems := fmt.Sprintf("json_extract(to_json(%s), '$[*]')", input)
		replacement := fmt.Sprintf(
			"%s(SELECT unnest(%s) AS value, unnest(range(len(%s))) AS index)",
			prefix, elems, elems)
		sql = sql[:loc[0]] + replacement + sql[end:]
		from = loc[0] + len(replacement)
	}
}

// flattenInput reads FLATTEN's argument list. Snowflake names its arguments,
// and INPUT is the only one whose behaviour this reproduces.
func flattenInput(args string) (string, error) {
	trimmed := strings.TrimSpace(args)
	for _, unsupported := range []string{"OUTER", "RECURSIVE", "PATH", "MODE"} {
		if regexp.MustCompile(`(?i)\b` + unsupported + `\s*=>`).MatchString(trimmed) {
			return "", fmt.Errorf("FLATTEN argument %s is not implemented: "+
				"it changes which rows come back, so it cannot be ignored", unsupported)
		}
	}
	if i := regexp.MustCompile(`(?i)^INPUT\s*=>\s*`).FindStringIndex(trimmed); i != nil {
		return strings.TrimSpace(trimmed[i[1]:]), nil
	}
	// Snowflake allows the first argument positionally.
	if trimmed == "" {
		return "", fmt.Errorf("FLATTEN needs an input")
	}
	return trimmed, nil
}

// matchParen returns the index of the ')' closing the '(' at open, ignoring
// parentheses inside single-quoted strings.
func matchParen(s string, open int) (int, bool) {
	depth, inLiteral := 0, false
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '\'':
			inLiteral = !inLiteral
		case '(':
			if !inLiteral {
				depth++
			}
		case ')':
			if !inLiteral {
				depth--
				if depth == 0 {
					return i, true
				}
			}
		}
	}
	return 0, false
}

// skipCloseParen consumes whitespace then one ')', for the TABLE( wrapper.
func skipCloseParen(s string, from int) (int, bool) {
	i := from
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n') {
		i++
	}
	if i < len(s) && s[i] == ')' {
		return i + 1, true
	}
	return from, false
}

// insideLiteral reports whether pos falls outside every code region -- that
// is, inside a string OR inside a comment. Both are text this rewrite has no
// business reaching into, and treating a comment's apostrophe as a quote is
// what made every later rewrite in a statement silently stop.
func insideLiteral(s string, pos int) bool {
	for _, r := range codeRegions(s) {
		if pos >= r.from && pos < r.to {
			return false
		}
	}
	return true
}
