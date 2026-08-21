package server

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// INFER_SCHEMA: what a bronze layer asks before it creates the table.
//
// WHY THIS ONE, AHEAD OF MERGE AND OBJECT_CONSTRUCT. Its absence was not
// theoretical -- it was being paid for in the consumers. `COPY INTO` fills a
// table that already exists, so something has to decide the column types
// first, and with no INFER_SCHEMA both Contoso Snowflake leaves grew their own
// sampler: read a few rows of the staged CSV, classify each cell, widen the
// set, emit DDL. Two copies of a type-inference heuristic, in the product,
// because the warehouse would not answer the question.
//
// What it replaces is worse than the duplication. Landing every column as text
// is not neutral: a date arrives as VARCHAR, silver does arithmetic on it, and
// the reader sees `No function matches the given name and argument types
// '+(VARCHAR, INTEGER)'` three layers from the COPY that decided it.
//
// IT IS A REAL RELATION, following TASK_HISTORY. The reference is rewritten
// into a query the engine runs, so WHERE, ORDER BY and a join against it are
// the engine's own. Answering only the exact shape `SELECT * FROM
// TABLE(INFER_SCHEMA(...))` would be less code and a trap.
//
// A NAMED FILE FORMAT IS REQUIRED, as in real Snowflake -- INFER_SCHEMA does
// not take an inline FILE_FORMAT there, and accepting one here would be a
// statement that works against this emulator and fails against an account.

var (
	reInferSchema = regexp.MustCompile(
		`(?is)TABLE\s*\(\s*(?:[A-Za-z0-9_$."]+\.)?INFER_SCHEMA\s*\(([^)]*)\)\s*\)`)
	reInferArg = regexp.MustCompile(`(?is)([A-Za-z_]+)\s*=>\s*('[^']*'|[A-Za-z0-9_$.]+)`)
)

// expandInferSchema rewrites an INFER_SCHEMA() reference into a relation the
// engine can query. It returns the SQL unchanged when there is no reference.
func (s *Server) expandInferSchema(sqlText string) (string, error) {
	m := reInferSchema.FindStringSubmatch(sqlText)
	if m == nil {
		return sqlText, nil
	}
	location, formatName, err := parseInferArgs(m[1])
	if err != nil {
		return "", err
	}
	rel, err := s.inferRelation(location, formatName)
	if err != nil {
		return "", err
	}
	return strings.Replace(sqlText, m[0], rel, 1), nil
}

// parseInferArgs reads the named arguments. One this emulator cannot honour is
// refused BY NAME rather than ignored: silently dropping MAX_RECORDS_PER_FILE
// would sample the whole feed and report a type the caller did not ask for.
func parseInferArgs(args string) (location, formatName string, err error) {
	found := reInferArg.FindAllStringSubmatch(args, -1)
	for _, m := range found {
		key := strings.ToUpper(m[1])
		val := strings.Trim(m[2], "'")
		switch key {
		case "LOCATION":
			location = val
		case "FILE_FORMAT":
			formatName = val
		default:
			return "", "", fmt.Errorf(
				"INFER_SCHEMA argument %s is not implemented: this emulator "+
					"supports LOCATION and FILE_FORMAT", key)
		}
	}
	if strings.TrimSpace(args) != "" && len(found) == 0 {
		return "", "", fmt.Errorf(
			"INFER_SCHEMA takes named arguments only, e.g. LOCATION => '@s/d/', got %q",
			strings.TrimSpace(args))
	}
	if location == "" {
		return "", "", fmt.Errorf("INFER_SCHEMA requires LOCATION => '@stage/prefix'")
	}
	if formatName == "" {
		// Snowflake requires it too. Guessing CSV here would answer a JSON
		// feed with one text column per line and look like an inference.
		return "", "", fmt.Errorf(
			"INFER_SCHEMA requires FILE_FORMAT => 'name' naming a format created " +
				"with CREATE FILE FORMAT")
	}
	return location, formatName, nil
}

// inferRelation resolves the location to files and builds the query.
func (s *Server) inferRelation(location, formatName string) (string, error) {
	if reExternal.MatchString(location) {
		return "", fmt.Errorf("INFER_SCHEMA over an external stage is not implemented: " +
			"this emulator serves internal stages from SNOWFLAKE_STAGE_DIR")
	}
	stage, prefix, err := splitStageRef(location)
	if err != nil {
		return "", err
	}
	dir, err := s.stageDir(stage)
	if err != nil {
		return "", err
	}
	format, err := s.namedFormat(formatName)
	if err != nil {
		return "", err
	}
	files, err := stageFiles(dir, prefix)
	if err != nil {
		return "", fmt.Errorf("INFER_SCHEMA over @%s/%s: %w", stage, prefix, err)
	}
	return inferSQL(files, dir, format), nil
}

// splitStageRef takes `@stage/some/prefix/` apart. A PREFIX IS THE POINT here,
// unlike COPY INTO which resolves one name: INFER_SCHEMA exists to describe a
// feed, and a feed is a directory of parts.
func splitStageRef(location string) (stage, prefix string, err error) {
	ref := strings.TrimSpace(strings.Trim(location, `'"`))
	if !strings.HasPrefix(ref, "@") {
		return "", "", fmt.Errorf(
			"INFER_SCHEMA LOCATION must name a stage, e.g. '@~/feed/', got %q", location)
	}
	ref = strings.TrimPrefix(ref, "@")
	if i := strings.Index(ref, "/"); i >= 0 {
		return ref[:i], strings.Trim(ref[i:], "/"), nil
	}
	return ref, "", nil
}

// stageFiles lists what the prefix names, newest-agnostic and sorted so the
// generated SQL is stable -- an unstable file order would make ORDER_ID and
// FILENAMES differ between two identical calls.
// stageFiles resolves a stage reference to the files it names.
//
// A STAGE REFERENCE IS A PREFIX, which is what Snowflake does: it matches every
// file whose path STARTS WITH the reference. That covers all three forms with
// one rule -- an exact file (`orders.csv`), a directory (`feed/`) and a partial
// name (`feed/part_`) -- and Snowflake draws no distinction between them
// either, so neither does this.
//
// COPY INTO used to REFUSE a prefix while INFER_SCHEMA resolved one, through
// this very function in its older directory-only form. Two statements
// disagreeing about what a stage reference means is worse than either answer:
// a consumer had to issue one COPY INTO per part file, and since a task body is
// a single statement, loading eight tables through Tasks meant thirty-odd
// chained tasks instead of eight -- a shape dictated by this emulator rather
// than by Snowflake.
//
// The .gz that AUTO_COMPRESS leaves needs no special case: `orders.csv` is a
// prefix of `orders.csv.gz`.
//
// Sorted, because loading several files must happen in an order that does not
// change between runs. Two identical runs that differ, with nothing saying why,
// is the failure this family spends its time hunting.
func stageFiles(dir, prefix string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		if strings.HasPrefix(filepath.ToSlash(rel), prefix) {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no file or prefix matching %q in the stage", prefix)
	}
	sort.Strings(out)
	return out, nil
}

// inferSQL builds the relation. Its shape is Snowflake's result: COLUMN_NAME,
// TYPE, NULLABLE, EXPRESSION, FILENAMES, ORDER_ID.
//
// TWO READS, DELIBERATELY. The union read gives the schema a COPY would
// actually produce; the per-file reads give FILENAMES its real meaning -- the
// files a column was found in. Returning the whole file list for every column
// would be right only when every file has the same columns, and wrong exactly
// when a caller most needs to know.
func inferSQL(files []string, dir string, f fileFormat) string {
	var per []string
	for _, p := range files {
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			rel = filepath.Base(p)
		}
		per = append(per, fmt.Sprintf(
			"SELECT %s AS fn, column_name FROM (DESCRIBE SELECT * FROM %s)",
			quote(filepath.ToSlash(rel)), f.readerCall([]string{p})))
	}
	// BOTH CTEs AT THE TOP OF THE DERIVED TABLE. Defining `per` inside a
	// nested FROM puts it out of scope for the correlated subquery below --
	// `Catalog Error: Table with name per does not exist`, which no assertion
	// about the generated TEXT can catch and the image reports on the first
	// call.
	return fmt.Sprintf(`(WITH u AS (SELECT * FROM (DESCRIBE SELECT * FROM %s)),
 per AS (%s)
 SELECT u.column_name AS "COLUMN_NAME", %s AS "TYPE",
 true AS "NULLABLE", NULL AS "EXPRESSION",
 (SELECT list(fn ORDER BY fn) FROM per WHERE per.column_name = u.column_name) AS "FILENAMES",
 (row_number() OVER ()) - 1 AS "ORDER_ID"
 FROM u)`,
		f.readerCall(files), strings.Join(per, " UNION ALL "), inferTypeCase)
}

// inferTypeCase maps duckdb's type names onto the ones an account would report.
// A type this does not know is passed through rather than guessed at: a wrong
// name here becomes a wrong column in someone's CREATE TABLE.
//
// THE SECOND MAPPING IN THIS PACKAGE, and the duplication is forced rather
// than sloppy. `snowflakeType` in server.go maps the same duckdb types onto
// the WIRE vocabulary a result's metadata uses -- `fixed`, `real`, `text` --
// while this one produces the SQL names that go in a CREATE TABLE. They answer
// different questions and neither can be written in terms of the other,
// because this one has to run inside the engine, over DESCRIBE's output. Keep
// their coverage in step: a duckdb type one of them knows and the other does
// not is a column that arrives with a type nobody can act on.
const inferTypeCase = `CASE
 WHEN u.column_type IN ('BIGINT','INTEGER','SMALLINT','TINYINT','HUGEINT',
                        'UBIGINT','UINTEGER','USMALLINT','UTINYINT') THEN 'NUMBER(38,0)'
 WHEN starts_with(u.column_type,'DECIMAL') THEN 'NUMBER' || substr(u.column_type, 8)
 WHEN u.column_type IN ('DOUBLE','FLOAT','REAL') THEN 'FLOAT'
 WHEN u.column_type = 'VARCHAR' THEN 'TEXT'
 WHEN u.column_type = 'BOOLEAN' THEN 'BOOLEAN'
 WHEN u.column_type = 'DATE' THEN 'DATE'
 WHEN u.column_type = 'TIME' THEN 'TIME'
 WHEN starts_with(u.column_type,'TIMESTAMP') THEN 'TIMESTAMP_NTZ'
 WHEN u.column_type = 'BLOB' THEN 'BINARY'
 WHEN ends_with(u.column_type,'[]') THEN 'ARRAY'
 WHEN starts_with(u.column_type,'STRUCT') OR starts_with(u.column_type,'MAP') THEN 'OBJECT'
 WHEN u.column_type = 'JSON' THEN 'VARIANT'
 ELSE u.column_type END`

// readerCall is the duckdb reader for this format over these files.
//
// UNION_BY_NAME IS LOAD-BEARING, not a flourish. Without it duckdb takes the
// first file's columns and DROPS any the later files add -- measured on the
// pinned build: two files, one with an extra column, and the extra column
// simply is not in the result. That is this project's recurring defect shape
// (success reported, work not done) wearing a different hat.
func (f fileFormat) readerCall(files []string) string {
	list := make([]string, 0, len(files))
	for _, p := range files {
		list = append(list, quote(filepath.ToSlash(p)))
	}
	arr := "[" + strings.Join(list, ",") + "]"
	switch f.Type {
	case "JSON":
		return fmt.Sprintf("read_json(%s, union_by_name=true)", arr)
	case "PARQUET":
		return fmt.Sprintf("read_parquet(%s, union_by_name=true)", arr)
	}
	args := []string{arr, fmt.Sprintf("header=%t", f.SkipHeader == 1), "union_by_name=true"}
	if f.Delimiter != "" {
		args = append(args, "delim="+quote(f.Delimiter))
	}
	if f.Quote != "" {
		args = append(args, "quote="+quote(f.Quote))
	}
	if f.Escape != "" {
		args = append(args, "escape="+quote(f.Escape))
	}
	if f.hasNullIf {
		args = append(args, "nullstr="+quote(f.NullIf))
	}
	return fmt.Sprintf("read_csv(%s)", strings.Join(args, ", "))
}
