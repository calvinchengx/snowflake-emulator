package server

import (
	"fmt"
	"regexp"
	"strings"
)

// DATEADD, GENERATOR and SEQ4: the three things core's silver reaches for on
// Snowflake that this emulator did not have.
//
// All three are real Snowflake, which is the only reason they are here. The
// boundary this file sits on is that the emulator owes what Snowflake offers,
// not what a model happens to want -- and these are both.

var (
	// DATEADD(part, n, d). The part is a bare keyword, so this cannot be a
	// macro: DuckDB reads `day` as a column and says so.
	reDateAdd = regexp.MustCompile(`(?i)\bDATEADD\s*\(\s*([A-Za-z_]+)\s*,`)

	// TABLE(GENERATOR(ROWCOUNT => n)) and the SEQ4()/SEQ8() that reads it.
	reGenerator = regexp.MustCompile(`(?i)\bTABLE\s*\(\s*GENERATOR\s*\(\s*ROWCOUNT\s*=>\s*(\d+)\s*\)\s*\)`)
	reSeq       = regexp.MustCompile(`(?i)\bSEQ[48]\s*\(\s*\)`)
)

const seqColumn = "seq4_col"

// rewriteGenerator turns Snowflake's row generator into a DuckDB range.
//
//	TABLE(GENERATOR(ROWCOUNT => 20000))  ->  (SELECT unnest(range(20000))::INTEGER AS seq4_col)
//	SEQ4()                               ->  seq4_col
//
// INTEGER and not BIGINT, deliberately: `DATE + BIGINT` is an error in DuckDB
// while `DATE + INTEGER` is a DATE, and the generator exists here precisely so
// a caller can offset a date by the row number.
func rewriteGenerator(sql string) string {
	if !reGenerator.MatchString(sql) {
		return sql
	}
	out := outsideLiterals(sql, func(s string) string {
		s = reGenerator.ReplaceAllString(s,
			fmt.Sprintf("(SELECT unnest(range($1))::INTEGER AS %s)", seqColumn))
		return reSeq.ReplaceAllLiteralString(s, seqColumn)
	})
	return out
}

// rewriteDateAdd turns DATEADD into arithmetic DuckDB agrees with, keeping the
// type Snowflake would return.
//
//	DATEADD(day, n, d)     ->  (d + (n)::INTEGER)        DATE stays a DATE
//	DATEADD(week, n, d)    ->  (d + ((n) * 7)::INTEGER)
//	DATEADD(hour, n, d)    ->  (d + INTERVAL (n) hour)   a time part is a timestamp either way
//
// MONTH, QUARTER AND YEAR ARE REFUSED, and the reason is the same one that
// kept DATEADD out of this emulator until now: every DuckDB spelling of them
// widens a DATE to a TIMESTAMP, and a CASE cannot return two types, so the
// answer would carry the wrong type for a DATE argument. Day and week avoid it
// only because integer addition to a DATE is defined and stays a DATE.
//
// A TIMESTAMP argument to the day form is an error here where Snowflake would
// answer -- DuckDB has no TIMESTAMP + INTEGER. An honest error beats a
// silently wrong type, and parity.md records it.
func rewriteDateAdd(sql string) (string, error) {
	var refused string
	out := outsideLiterals(sql, func(s string) string {
		return reDateAdd.ReplaceAllStringFunc(s, func(m string) string {
			part := strings.ToUpper(strings.TrimSpace(reDateAdd.FindStringSubmatch(m)[1]))
			switch part {
			case "DAY", "D", "DD", "DAYS", "DAYOFMONTH":
				return "date_add_days("
			case "WEEK", "W", "WK", "WEEKS", "WEEKOFYEAR":
				return "date_add_weeks("
			case "HOUR", "H", "HH", "HOURS":
				return "date_add_hours("
			case "MINUTE", "M", "MI", "MINUTES":
				return "date_add_minutes("
			case "SECOND", "S", "SS", "SECONDS":
				return "date_add_seconds("
			default:
				refused = part
				return m
			}
		})
	})
	if refused != "" {
		return "", fmt.Errorf(
			"DATEADD(%s, ...) is not implemented: every DuckDB spelling of a month, "+
				"quarter or year offset widens a DATE to a TIMESTAMP, and answering with "+
				"the wrong type is worse than not answering. DAY, WEEK, HOUR, MINUTE and "+
				"SECOND are available", strings.ToLower(refused))
	}
	return out, nil
}
