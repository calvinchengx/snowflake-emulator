package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Internal stages, file formats, and the COPY INTO that reads them.
//
// This is the half of Snowflake a bronze layer is made of: bytes land in a
// stage, a file format says how to read them, and COPY INTO parses them into
// a table. None of it existed here -- CREATE STAGE, LIST and CREATE FILE
// FORMAT were parser errors, and COPY INTO ignored both the stage name and
// any format, reading every file as headed CSV.
//
// EXTERNAL STAGES ARE STILL REFUSED, by name. `@` on an s3:// or azure://
// location is a different thing with credentials attached, and answering it
// from a local directory would be a lie a consumer only discovers in
// production.

var (
	reCreateStage = regexp.MustCompile(`(?i)^CREATE\s+(?:OR\s+REPLACE\s+)?(?:TEMPORARY\s+)?STAGE\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z0-9_$."]+)(.*)$`)
	reDropStage   = regexp.MustCompile(`(?i)^DROP\s+STAGE\s+(?:IF\s+EXISTS\s+)?([A-Za-z0-9_$."]+)`)
	reList        = regexp.MustCompile(`(?i)^(?:LIST|LS)\s+@([A-Za-z0-9_$~./]+)`)
	reCreateFmt   = regexp.MustCompile(`(?i)^CREATE\s+(?:OR\s+REPLACE\s+)?FILE\s+FORMAT\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z0-9_$."]+)\s*(.*)$`)
	reCopyInto    = regexp.MustCompile(`(?i)^COPY\s+INTO\s+([A-Za-z0-9_$.".]+)\s+FROM\s+'?@([A-Za-z0-9_$~]+)((?:/[^'\s]*)?)'?\s*(.*)$`)
	reExternal    = regexp.MustCompile(`(?i)@?['"]?(s3|azure|gcs|gs|https?)://`)
	reFmtInline   = regexp.MustCompile(`(?i)FILE_FORMAT\s*=\s*\(([^)]*)\)`)
	reFmtNamed    = regexp.MustCompile(`(?i)FILE_FORMAT\s*=\s*(?:\(\s*FORMAT_NAME\s*=\s*'?([A-Za-z0-9_$.]+)'?\s*\)|'?([A-Za-z0-9_$.]+)'?)`)
	reOption      = regexp.MustCompile(`(?i)([A-Za-z_]+)\s*=\s*('(?:[^']*)'|\([^)]*\)|[A-Za-z0-9_|]+)`)
)

// fileFormat is the subset of Snowflake's CSV/JSON/PARQUET options that maps
// onto something DuckDB can be told to do. An option Snowflake accepts and
// this cannot honour is REFUSED rather than ignored -- a silently dropped
// FIELD_DELIMITER parses the whole line into column one.
type fileFormat struct {
	Type       string
	SkipHeader int
	Delimiter  string
	Quote      string
	Escape     string
	NullIf     string
	hasNullIf  bool
}

func defaultFormat() fileFormat {
	// Snowflake's defaults, not this emulator's convenience: TYPE = CSV,
	// SKIP_HEADER = 0. A header row is DATA unless the format says otherwise,
	// which is why `COPY INTO nums FROM @~/nums.csv` on a file whose first
	// line reads `n` fails on an INTEGER column -- there and here.
	return fileFormat{Type: "CSV", Delimiter: ",", Quote: `"`}
}

func (s *Server) stageDir(name string) (string, error) {
	if name == "~" {
		return s.Cfg.StageDir, nil // the user stage
	}
	clean := strings.Trim(strings.ToUpper(name), `"`)
	dir := filepath.Join(s.Cfg.StageDir, clean)
	if _, err := os.Stat(dir); err != nil {
		return "", fmt.Errorf("stage %s does not exist", name)
	}
	return dir, nil
}

func (s *Server) handleStageSQL(w http.ResponseWriter, sqlText string) bool {
	trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sqlText), ";"))

	if m := reCreateStage.FindStringSubmatch(trimmed); m != nil {
		if reExternal.MatchString(m[2]) {
			writeFail(w, http.StatusOK, "001011",
				"external stages are not implemented: this emulator serves internal stages from SNOWFLAKE_STAGE_DIR")
			return true
		}
		name := strings.Trim(strings.ToUpper(m[1]), `"`)
		if err := os.MkdirAll(filepath.Join(s.Cfg.StageDir, name), 0o755); err != nil {
			writeFail(w, http.StatusOK, "001012", err.Error())
			return true
		}
		writeQueryOK(w, []string{"status"},
			[][]string{{fmt.Sprintf("Stage area %s successfully created.", name)}}, "duckdb")
		return true
	}

	if m := reDropStage.FindStringSubmatch(trimmed); m != nil {
		name := strings.Trim(strings.ToUpper(m[1]), `"`)
		if err := os.RemoveAll(filepath.Join(s.Cfg.StageDir, name)); err != nil {
			writeFail(w, http.StatusOK, "001012", err.Error())
			return true
		}
		writeQueryOK(w, []string{"status"},
			[][]string{{fmt.Sprintf("%s successfully dropped.", name)}}, "duckdb")
		return true
	}

	if m := reList.FindStringSubmatch(trimmed); m != nil {
		s.listStage(w, m[1])
		return true
	}

	if m := reCreateFmt.FindStringSubmatch(trimmed); m != nil {
		f, err := parseFormat(m[2], defaultFormat())
		if err != nil {
			writeFail(w, http.StatusOK, "001013", err.Error())
			return true
		}
		name := strings.Trim(strings.ToUpper(m[1]), `"`)
		s.mu.Lock()
		if s.formats == nil {
			s.formats = map[string]fileFormat{}
		}
		s.formats[name] = f
		s.mu.Unlock()
		writeQueryOK(w, []string{"status"},
			[][]string{{fmt.Sprintf("File format %s successfully created.", name)}}, "duckdb")
		return true
	}
	return false
}

func (s *Server) listStage(w http.ResponseWriter, ref string) {
	stage, sub, _ := strings.Cut(ref, "/")
	dir, err := s.stageDir(stage)
	if err != nil {
		writeFail(w, http.StatusOK, "001014", err.Error())
		return
	}
	base := filepath.Join(dir, filepath.FromSlash(sub))
	var rows [][]string
	_ = filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil //nolint:nilerr // a missing prefix lists nothing, as it does in Snowflake
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return nil
		}
		rows = append(rows, []string{
			stage + "/" + filepath.ToSlash(rel),
			strconv.FormatInt(info.Size(), 10),
			"", // md5 is not computed; the column exists because clients index it
			info.ModTime().UTC().Format("Mon, 2 Jan 2006 15:04:05 GMT"),
		})
		return nil
	})
	sort.Slice(rows, func(i, j int) bool { return rows[i][0] < rows[j][0] })
	writeQueryOK(w, []string{"name", "size", "md5", "last_modified"}, rows, "duckdb")
}

// parseFormat reads Snowflake's option list. An option that is present and
// cannot be honoured is an error: ignoring FIELD_DELIMITER would read every
// line into the first column and report success.
func parseFormat(opts string, base fileFormat) (fileFormat, error) {
	f := base
	for _, m := range reOption.FindAllStringSubmatch(opts, -1) {
		key := strings.ToUpper(m[1])
		val := strings.Trim(m[2], `'`)
		switch key {
		case "TYPE":
			t := strings.ToUpper(val)
			switch t {
			case "CSV", "JSON", "PARQUET":
				f.Type = t
			default:
				return f, fmt.Errorf("file format TYPE = %s is not implemented (CSV, JSON, PARQUET)", t)
			}
		case "SKIP_HEADER":
			n, err := strconv.Atoi(val)
			if err != nil {
				return f, fmt.Errorf("SKIP_HEADER = %s is not a number", val)
			}
			if n > 1 {
				return f, fmt.Errorf("SKIP_HEADER = %d is not implemented: DuckDB skips at most one header row", n)
			}
			f.SkipHeader = n
		case "FIELD_DELIMITER":
			f.Delimiter = val
		case "FIELD_OPTIONALLY_ENCLOSED_BY":
			f.Quote = val
		case "ESCAPE", "ESCAPE_UNENCLOSED_FIELD":
			f.Escape = val
		case "NULL_IF":
			f.NullIf = strings.Trim(strings.Trim(val, "()"), `'`)
			f.hasNullIf = true
		case "FORMAT_NAME", "COMPRESSION", "TRIM_SPACE", "EMPTY_FIELD_AS_NULL",
			"ERROR_ON_COLUMN_COUNT_MISMATCH", "REPLACE_INVALID_CHARACTERS", "ENCODING":
			// COMPRESSION is DuckDB's to detect from the extension; the rest
			// are accepted where DuckDB's behaviour already matches.
		default:
			return f, fmt.Errorf("file format option %s is not implemented", key)
		}
	}
	return f, nil
}

// duckdbOptions renders the format as DuckDB COPY options.
func (f fileFormat) duckdbOptions() string {
	switch f.Type {
	case "JSON":
		return "(FORMAT JSON)"
	case "PARQUET":
		return "(FORMAT PARQUET)"
	}
	parts := []string{"FORMAT CSV", fmt.Sprintf("HEADER %t", f.SkipHeader == 1)}
	if f.Delimiter != "" {
		parts = append(parts, fmt.Sprintf("DELIMITER %s", quote(f.Delimiter)))
	}
	if f.Quote != "" {
		parts = append(parts, fmt.Sprintf("QUOTE %s", quote(f.Quote)))
	}
	if f.Escape != "" {
		parts = append(parts, fmt.Sprintf("ESCAPE %s", quote(f.Escape)))
	}
	if f.hasNullIf {
		parts = append(parts, fmt.Sprintf("NULLSTR %s", quote(f.NullIf)))
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
