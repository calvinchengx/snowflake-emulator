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

	if m := rePut.FindStringSubmatch(trimmed); m != nil {
		s.handlePut(w, m)
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

// PUT: the one statement whose bytes this process does not write.
//
// A client-side upload protocol, and the reason it was worth implementing is
// not the statement itself. Without it a consumer cannot put a file into a
// stage the way a real consumer does, so every pipeline built against this
// emulator reached for the stage DIRECTORY instead -- writing bytes to a
// shared mount and letting COPY INTO find them. That code does not move to a
// real account, where no such directory exists. The emulator's convenience had
// become the consumer's architecture.
//
// HOW IT WORKS, and it is Snowflake's own mechanism rather than an invention:
// the driver recognises PUT before sending it, asks the server where to put
// the bytes, and uploads them itself. The answer names a location type. Both
// drivers this emulator is witnessed against -- gosnowflake and
// snowflake-connector-python -- implement LOCAL_FS alongside S3, Azure and
// GCS, so answering LOCAL_FS with the stage directory makes the CLIENT do a
// real upload over its real code path. What is exercised is the driver's file
// transfer agent, not a shortcut around it.
//
// THE PATH IS THE CLIENT'S, NOT OURS. The driver copies into the directory
// this response names, so the name has to mean something on the client's
// filesystem. It does for a host binary. It does not when the emulator is in a
// container and the client is not, which is what SNOWFLAKE_STAGE_CLIENT_DIR
// is for. Get it wrong and the bytes land where COPY INTO cannot see them --
// which fails loudly at COPY INTO, because that statement stats the file it
// was given rather than reporting a cheerful zero rows.
var (
	rePut       = regexp.MustCompile(`(?i)^PUT\s+'?([a-z0-9+.-]+://[^'\s]+)'?\s+'?@([A-Za-z0-9_$~][A-Za-z0-9_$~./]*)'?\s*(.*)$`)
	rePutOption = regexp.MustCompile(`(?i)([A-Za-z_]+)\s*=\s*('[^']*'|[A-Za-z0-9_]+)`)
)

// putOptions is the subset of PUT's option list that changes what the driver
// does. An option Snowflake accepts and this cannot honour is refused by name,
// the same rule the file formats keep: a silently dropped OVERWRITE would
// report an upload that did not replace what was there.
type putOptions struct {
	AutoCompress      bool
	Overwrite         bool
	Parallel          int
	SourceCompression string
}

func defaultPutOptions() putOptions {
	// Snowflake's defaults, not this emulator's convenience. AUTO_COMPRESS is
	// TRUE there, so `PUT file://orders.csv @~` lands `orders.csv.gz` in the
	// stage and a consumer who assumed otherwise is wrong on both systems.
	return putOptions{AutoCompress: true, Parallel: 4, SourceCompression: "AUTO_DETECT"}
}

func parsePutOptions(tail string) (putOptions, error) {
	o := defaultPutOptions()
	for _, m := range rePutOption.FindAllStringSubmatch(tail, -1) {
		key := strings.ToUpper(m[1])
		val := strings.Trim(m[2], "'")
		switch key {
		case "AUTO_COMPRESS":
			o.AutoCompress = strings.EqualFold(val, "TRUE")
		case "OVERWRITE":
			o.Overwrite = strings.EqualFold(val, "TRUE")
		case "PARALLEL":
			n, err := strconv.Atoi(val)
			if err != nil || n < 1 {
				return o, fmt.Errorf("PARALLEL must be a positive integer, got %q", val)
			}
			o.Parallel = n
		case "SOURCE_COMPRESSION":
			o.SourceCompression = strings.ToUpper(val)
		default:
			return o, fmt.Errorf("PUT option %s is not implemented", key)
		}
	}
	return o, nil
}

// handlePut answers the upload request. It writes no bytes: the driver does,
// after reading this.
func (s *Server) handlePut(w http.ResponseWriter, m []string) {
	src, ref, tail := m[1], m[2], m[3]

	if !strings.HasPrefix(strings.ToLower(src), "file://") {
		writeFail(w, http.StatusOK, "001011", fmt.Sprintf(
			"PUT from %s is not implemented: this emulator uploads from file:// only", src))
		return
	}
	opts, err := parsePutOptions(tail)
	if err != nil {
		writeFail(w, http.StatusOK, "001013", err.Error())
		return
	}

	stage, sub, _ := strings.Cut(ref, "/")
	dir, err := s.stageDir(stage)
	if err != nil {
		writeFail(w, http.StatusOK, "001012", err.Error())
		return
	}
	// 0777, NOT 0755, BECAUSE THE CLIENT WRITES THE BYTES AND IT IS NOT US.
	//
	// `PUT` is a two-party operation here: this server decides where the file
	// goes and answers with the path, and the driver's file transfer agent
	// writes it. When the two run as different users -- which is the ordinary
	// case for a containerised client against a containerised warehouse -- a
	// directory this process creates 0755 is one the client cannot write into.
	//
	// Measured rather than reasoned about, on the Airflow 3 cell where the
	// worker is uid 50000 and this server is 65532:
	//
	//     PermissionError: [Errno 13] Permission denied:
	//         '/stages/contoso_pos_customers/part-0001.csv.gz'
	//
	// wrapped by the driver as `253003: While putting file(s) there was an
	// error`, which names blob storage and gives no hint that a umask decided
	// it. The stage root already has to be world-writable for the same reason;
	// creating its subdirectories tighter than the root it sits in was the
	// inconsistency.
	//
	// MkdirAll APPLIES THE UMASK, so the mode is set explicitly afterwards --
	// a fresh directory under a 0022 umask would otherwise come out 0755
	// however it was requested, which is exactly how this was missed.
	dest := filepath.Join(dir, filepath.FromSlash(sub))
	if err := s.preparePutDir(dest); err != nil {
		writeFail(w, http.StatusOK, "001012", err.Error())
		return
	}

	// The directory the CLIENT will write into. It differs from `dest` only
	// when this process and the client see the stage at different paths.
	clientDest := dest
	if s.Cfg.StageClientDir != "" {
		rel, err := filepath.Rel(s.Cfg.StageDir, dest)
		if err != nil {
			writeFail(w, http.StatusOK, "001012", err.Error())
			return
		}
		clientDest = filepath.Join(s.Cfg.StageClientDir, rel)
	}

	writeUploadOK(w, strings.TrimPrefix(src, "file://"), clientDest, opts)
}

// writeUploadOK renders the response the drivers' file transfer agents read.
//
// THE FIELD LIST IS DERIVED FROM THE DRIVERS, not from documentation:
// gosnowflake's execResponseData/execResponseStageInfo and the Python
// connector's _parse_command, which errors without `command`, a list-valued
// `src_locations`, and a `stageInfo` carrying `locationType`, `location` and
// `creds`. `creds` is empty because LOCAL_FS needs none, and it is present
// rather than omitted because the Python connector indexes it.
func writeUploadOK(w http.ResponseWriter, srcPath, dest string, o putOptions) {
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": nil,
		"code":    nil,
		"data": map[string]any{
			"command":       "UPLOAD",
			"src_locations": []string{srcPath},
			"parallel":      o.Parallel,
			"threshold":     67108864,
			"autoCompress":  o.AutoCompress,
			"overwrite":     o.Overwrite,
			// AUTO_DETECT is Snowflake's default and means "look at the file".
			"sourceCompression": strings.ToLower(o.SourceCompression),
			"stageInfo": map[string]any{
				"locationType":          "LOCAL_FS",
				"location":              dest + string(os.PathSeparator),
				"path":                  "",
				"region":                "",
				"isClientSideEncrypted": false,
				"creds":                 map[string]any{},
			},
			"queryId":  "q1",
			"sqlState": "00000",
		},
	})
}

// stageFile resolves a stage path to a file on disk the way COPY INTO needs
// it, and the .gz arm is the direct consequence of implementing PUT.
//
// AUTO_COMPRESS defaults to TRUE, so `PUT file://orders.csv @~` leaves
// `orders.csv.gz` in the stage. Real Snowflake matches stage paths by PREFIX,
// so `COPY INTO t FROM @~/orders.csv` finds that file there. This emulator
// matches one exact path, so without this it would answer "no such file" for
// the file it had just been asked to upload.
//
// THIS IS NARROWER THAN SNOWFLAKE, deliberately. Prefix matching there loads
// EVERY file under the prefix; this resolves one name and its compressed
// spelling. A prefix naming several files is a different statement and is not
// implemented, rather than being half-implemented as "the first one".
// stageFile is stageFiles where exactly one file is meant. GET names one file.
func stageFile(dir, path string) (string, error) {
	files, err := stageFiles(dir, path)
	if err != nil {
		return "", err
	}
	if len(files) > 1 {
		return "", fmt.Errorf("%q names %d files in the stage; this statement takes one", path, len(files))
	}
	return files[0], nil
}

// preparePutDir makes the destination a directory the CLIENT can write into.
//
// MkdirAll APPLIES THE UMASK, so a directory requested 0777 comes out 0755
// under the usual 0022 -- which is exactly how the original defect was missed.
// The mode is therefore set explicitly afterwards.
//
// THE CHMOD IS BEST-EFFORT, AND THAT IS NOT LAZINESS. `os.Chmod` fails when
// this process does not own the directory EVEN IF THE MODE IS ALREADY WHAT WE
// WANT -- measured, on a directory that is already 0777:
//
//	/private/var/tmp  perm=0777  chmod err=operation not permitted
//
// So treating a chmod error as fatal would REFUSE A PUT THAT WORKS TODAY. The
// case is not hypothetical: `PUT @~/orders.csv` with no subdirectory makes the
// destination the stage ROOT, which in a container deployment is a mounted
// volume this process very likely does not own. There MkdirAll is a no-op and
// the upload proceeds; failing on the chmod would break it.
//
// The connector can also create the directory itself, as the CLIENT's uid,
// when it finds it absent -- and that is a directory we can never chmod.
//
// What actually matters is the OUTCOME, not who set it: if the directory is
// world-writable the client can write, however it got that way. Only when it
// is not do we refuse, and then we say why, because the driver's own message
// blames blob storage.
func (s *Server) preparePutDir(dest string) error {
	if err := os.MkdirAll(dest, 0o777); err != nil {
		return err
	}
	if err := os.Chmod(dest, 0o777); err != nil {
		fi, statErr := os.Stat(dest)
		if statErr != nil {
			return statErr
		}
		if fi.Mode().Perm()&0o002 == 0 {
			return fmt.Errorf(
				"stage directory %s is %04o and cannot be made writable (%v); "+
					"the driver writes the bytes and runs as a different user, "+
					"so it would fail with a permission error naming blob storage",
				dest, fi.Mode().Perm(), err)
		}
	}
	return nil
}
