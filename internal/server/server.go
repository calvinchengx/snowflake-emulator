package server

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/calvinchengx/snowflake-emulator/internal/config"
	"github.com/calvinchengx/snowflake-emulator/internal/engine"
)

type Server struct {
	Cfg     config.Config
	PAT     string
	Fresh   bool
	handler http.Handler
	mu      sync.Mutex
	tokens  map[string]session
	wh      map[string]warehouse
	iceberg map[string]string
}

type session struct {
	Database  string
	Schema    string
	Warehouse string
}

type warehouse struct {
	Name      string
	Size      string
	Suspended bool
}

func New(cfg config.Config) (*Server, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.StageDir, 0o755); err != nil {
		return nil, err
	}
	patPath := filepath.Join(cfg.DataDir, "admin.pat")
	s := &Server{
		Cfg:     cfg,
		tokens:  map[string]session{},
		wh:      map[string]warehouse{},
		iceberg: map[string]string{},
	}
	b, err := os.ReadFile(patPath)
	if err != nil {
		tok, err := randomHex(24)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(patPath, []byte(tok+"\n"), 0o644); err != nil {
			return nil, err
		}
		s.PAT = tok
		s.Fresh = true
	} else {
		s.PAT = strings.TrimSpace(string(b))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.health)
	mux.HandleFunc("/session/v1/login-request", s.login)
	mux.HandleFunc("/session/heartbeat", s.heartbeat)
	mux.HandleFunc("/session/logout", s.logout)
	mux.HandleFunc("/session/token-request", s.tokenRequest)
	mux.HandleFunc("/queries/v1/query-request", s.query)
	mux.HandleFunc("/queries/v1/abort-request", s.okEmpty)
	mux.HandleFunc("/session/authenticator-request", s.okEmpty)
	mux.HandleFunc("/telemetry/send", s.okEmpty)
	mux.HandleFunc("/api/v2/statements", s.sqlAPI)
	mux.HandleFunc("/api/v2/warehouses", s.warehousesAPI)
	mux.HandleFunc("/iceberg/v1/namespaces", s.icebergNamespaces)
	mux.HandleFunc("/iceberg/v1/namespaces/", s.icebergNamespace)
	s.handler = mux
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

type loginBody struct {
	Data struct {
		LoginName   string `json:"LOGIN_NAME"`
		Password    string `json:"PASSWORD"`
		Token       string `json:"TOKEN"`
		AccountName string `json:"ACCOUNT_NAME"`
	} `json:"data"`
}

func readBody(r *http.Request) []byte {
	body, _ := io.ReadAll(r.Body)
	if len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err == nil {
			out, err := io.ReadAll(zr)
			_ = zr.Close()
			if err == nil {
				return out
			}
		}
	}
	return body
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	var in loginBody
	_ = json.Unmarshal(body, &in)
	pass := in.Data.Password
	if pass == "" {
		pass = in.Data.Token
	}
	if pass == "" {
		pass = r.URL.Query().Get("password")
	}
	if pass == "" || pass == "dev" || pass != s.PAT {
		writeFail(w, http.StatusUnauthorized, "390100", "incorrect username or password")
		return
	}
	tok, err := randomHex(16)
	if err != nil {
		writeFail(w, http.StatusInternalServerError, "000000", err.Error())
		return
	}
	sess := session{
		Database:  first(r.URL.Query().Get("databaseName"), "TEST_DB"),
		Schema:    first(r.URL.Query().Get("schemaName"), "PUBLIC"),
		Warehouse: r.URL.Query().Get("warehouse"),
	}
	s.mu.Lock()
	s.tokens[tok] = sess
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": nil,
		"code":    nil,
		"data": map[string]any{
			"token":                     tok,
			"masterToken":               tok,
			"validityInSeconds":         3600,
			"masterValidityInSeconds":   14400,
			"displayUserName":           "ADMIN",
			"serverVersion":             "8.0.0-emulator",
			"firstLogin":                false,
			"healthCheckInterval":       45,
			"sessionId":                 1,
			"idToken":                   nil,
			"idTokenValidityInSeconds":  0,
			"mfaToken":                  nil,
			"mfaTokenValidityInSeconds": 0,
			"sessionInfo": map[string]any{
				"databaseName":  sess.Database,
				"schemaName":    sess.Schema,
				"warehouseName": sess.Warehouse,
				"roleName":      "ACCOUNTADMIN",
			},
			"parameters": []map[string]any{
				{"name": "TIMEZONE", "value": "UTC"},
				{"name": "AUTOCOMMIT", "value": true},
				{"name": "CLIENT_SESSION_KEEP_ALIVE", "value": false},
				{"name": "CLIENT_SESSION_KEEP_ALIVE_HEARTBEAT_FREQUENCY", "value": 3600},
				{"name": "CLIENT_RESULT_CHUNK_SIZE", "value": 160},
				{"name": "CLIENT_PREFETCH_THREADS", "value": 4},
				{"name": "TIMESTAMP_OUTPUT_FORMAT", "value": "YYYY-MM-DD HH24:MI:SS.FF3 TZHTZM"},
				{"name": "TIMESTAMP_NTZ_OUTPUT_FORMAT", "value": ""},
				{"name": "DATE_OUTPUT_FORMAT", "value": "YYYY-MM-DD"},
				{"name": "TIME_OUTPUT_FORMAT", "value": "HH24:MI:SS"},
				{"name": "TIMESTAMP_LTZ_OUTPUT_FORMAT", "value": ""},
				{"name": "TIMESTAMP_TZ_OUTPUT_FORMAT", "value": ""},
				{"name": "CLIENT_TIMESTAMP_TYPE_MAPPING", "value": "TIMESTAMP_LTZ"},
				{"name": "PYTHON_CONNECTOR_QUERY_RESULT_FORMAT", "value": "json"},
			},
		},
	})
}

func (s *Server) okEmpty(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": map[string]any{}})
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.auth(r); !ok {
		writeFail(w, http.StatusUnauthorized, "390114", "session expired")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": map[string]any{}})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	tok := bearer(r)
	s.mu.Lock()
	delete(s.tokens, tok)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (s *Server) tokenRequest(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.auth(r); !ok {
		writeFail(w, http.StatusUnauthorized, "390114", "session expired")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data":    map[string]any{"sessionToken": bearer(r), "masterToken": bearer(r)},
	})
}

func (s *Server) query(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.auth(r)
	if !ok {
		writeFail(w, http.StatusUnauthorized, "390114", "session expired")
		return
	}
	raw := readBody(r)
	sqlText := extractSQL(raw)
	s.runSQL(w, bearer(r), sess, sqlText)
}

func (s *Server) sqlAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	sess, ok := s.auth(r)
	if !ok {
		// SQL API also accepts the PAT as Bearer.
		if bearer(r) == s.PAT {
			sess = session{Database: "TEST_DB", Schema: "PUBLIC"}
			ok = true
		}
	}
	if !ok {
		writeFail(w, http.StatusUnauthorized, "390114", "session expired")
		return
	}
	raw := readBody(r)
	var in struct {
		Statement string `json:"statement"`
		Database  string `json:"database"`
		Schema    string `json:"schema"`
		Warehouse string `json:"warehouse"`
	}
	_ = json.Unmarshal(raw, &in)
	if in.Database != "" {
		sess.Database = in.Database
	}
	if in.Schema != "" {
		sess.Schema = in.Schema
	}
	if in.Warehouse != "" {
		sess.Warehouse = in.Warehouse
	}
	s.runSQL(w, bearer(r), sess, in.Statement)
}

func (s *Server) warehousesAPI(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.auth(r); !ok && bearer(r) != s.PAT {
		writeFail(w, http.StatusUnauthorized, "390114", "session expired")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Method == http.MethodPost {
		var in struct {
			Name string `json:"name"`
			Size string `json:"size"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Name == "" {
			writeFail(w, http.StatusBadRequest, "001000", "warehouse name required")
			return
		}
		s.wh[strings.ToUpper(in.Name)] = warehouse{Name: in.Name, Size: first(in.Size, "XSMALL")}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": map[string]any{"name": in.Name}})
		return
	}
	list := make([]map[string]any, 0, len(s.wh))
	for _, wh := range s.wh {
		list = append(list, map[string]any{"name": wh.Name, "size": wh.Size, "suspended": wh.Suspended})
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": list})
}

func (s *Server) runSQL(w http.ResponseWriter, tok string, sess session, sqlText string) {
	sqlText = strings.TrimSpace(sqlText)
	if sqlText == "" {
		writeFail(w, http.StatusBadRequest, "000904", "empty statement")
		return
	}
	if s.handleCatalogSQL(w, sqlText) {
		return
	}
	rewritten, extra, special := rewriteSQL(sqlText, sess)
	if special && extra == "use_warehouse" {
		sess.Warehouse = rewritten
		if tok != "" {
			s.mu.Lock()
			if cur, ok := s.tokens[tok]; ok {
				cur.Warehouse = rewritten
				s.tokens[tok] = cur
			}
			s.mu.Unlock()
		}
		writeQueryOK(w, []string{"status"}, [][]string{{"ok"}}, "duckdb")
		return
	}
	sqlText = rewritten
	upper := strings.ToUpper(sqlText)

	if handled, err := s.handleWarehouseSQL(sqlText, upper); handled {
		if err != nil {
			writeFail(w, http.StatusOK, "001003", err.Error())
			return
		}
		if showWH.MatchString(strings.TrimSpace(sqlText)) {
			s.mu.Lock()
			var rows [][]string
			for _, wh := range s.wh {
				state := "STARTED"
				if wh.Suspended {
					state = "SUSPENDED"
				}
				rows = append(rows, []string{wh.Name, wh.Size, state})
			}
			s.mu.Unlock()
			writeQueryOK(w, []string{"name", "size", "state"}, rows, "duckdb")
			return
		}
		writeQueryOK(w, []string{"status"}, [][]string{{"ok"}}, "duckdb")
		return
	}

	if strings.Contains(upper, "ICEBERG") && strings.HasPrefix(strings.TrimSpace(upper), "CREATE") {
		if strings.TrimSpace(s.Cfg.PolarisURL) == "" {
			writeFail(w, http.StatusOK, "000501", "no Iceberg catalog attached: set SNOWFLAKE_POLARIS_URL")
			return
		}
		name := icebergTableName(sqlText)
		s.mu.Lock()
		s.iceberg[strings.ToUpper(name)] = name
		s.mu.Unlock()
		sqlText = regexp.MustCompile(`(?i)ICEBERG\s+`).ReplaceAllString(sqlText, "")
	}

	if strings.TrimSpace(s.Cfg.DuckDB) == "" {
		writeFail(w, http.StatusOK, "000001", engine.MissingAttachError().Error())
		return
	}

	if sess.Warehouse != "" {
		s.mu.Lock()
		wh, ok := s.wh[strings.ToUpper(sess.Warehouse)]
		s.mu.Unlock()
		if ok && wh.Suspended {
			writeFail(w, http.StatusOK, "000606", fmt.Sprintf("warehouse %s is suspended", sess.Warehouse))
			return
		}
	}

	if strings.HasPrefix(upper, "COPY INTO") {
		sqlText, err := s.rewriteCopy(sqlText)
		if err != nil {
			writeFail(w, http.StatusOK, "001007", err.Error())
			return
		}
		res, err := engine.Exec(s.Cfg.DuckDB, sqlText)
		if err != nil {
			writeFail(w, http.StatusOK, "001008", err.Error())
			return
		}
		writeQueryOK(w, res.Columns, res.Rows, res.Dialect)
		return
	}

	res, err := engine.Exec(s.Cfg.DuckDB, sqlText)
	if err != nil {
		writeFail(w, http.StatusOK, "002001", err.Error())
		return
	}
	writeQueryOK(w, res.Columns, res.Rows, res.Dialect)
}

var createWH = regexp.MustCompile(`(?i)CREATE\s+WAREHOUSE\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z0-9_]+)`)
var alterWH = regexp.MustCompile(`(?i)ALTER\s+WAREHOUSE\s+([A-Za-z0-9_]+)\s+(SUSPEND|RESUME)`)
var showWH = regexp.MustCompile(`(?i)^SHOW\s+WAREHOUSES`)

func (s *Server) handleWarehouseSQL(sqlText, upper string) (bool, error) {
	if m := createWH.FindStringSubmatch(sqlText); m != nil {
		name := m[1]
		s.mu.Lock()
		s.wh[strings.ToUpper(name)] = warehouse{Name: name, Size: "XSMALL"}
		s.mu.Unlock()
		return true, nil
	}
	if m := alterWH.FindStringSubmatch(sqlText); m != nil {
		name, op := m[1], strings.ToUpper(m[2])
		s.mu.Lock()
		wh, ok := s.wh[strings.ToUpper(name)]
		if !ok {
			s.mu.Unlock()
			return true, fmt.Errorf("warehouse %s not found", name)
		}
		wh.Suspended = op == "SUSPEND"
		s.wh[strings.ToUpper(name)] = wh
		s.mu.Unlock()
		return true, nil
	}
	if showWH.MatchString(strings.TrimSpace(sqlText)) {
		return true, nil
	}
	return false, nil
}

var (
	reShowSchemas = regexp.MustCompile(`(?i)^SHOW\s+(?:TERSE\s+)?SCHEMAS`)
	reShowObjects = regexp.MustCompile(`(?i)^SHOW\s+(?:TERSE\s+)?(?:OBJECTS|TABLES)`)
	// dbt-snowflake 1.12 lists user-defined functions before running a model.
	// Answered here rather than in the rewrite layer because the answer is an
	// EMPTY result that still carries columns, and that cannot survive the
	// duckdb round trip: `duckdb -json` prints `[]` for zero rows, so the
	// schema is gone by the time Exec returns and the response degrades to
	// status/ok. dbt then does schema_functions.select([...]) and agate raises
	// ValueError on the missing names.
	reShowFunctions = regexp.MustCompile(`(?i)^SHOW\s+(?:TERSE\s+)?(?:USER\s+)?FUNCTIONS\b`)
	reDescribeTbl   = regexp.MustCompile(`(?i)^DESC(?:RIBE)?\s+TABLE\s+(.+)`)
)

func (s *Server) handleCatalogSQL(w http.ResponseWriter, sqlText string) bool {
	trimmed := strings.TrimSpace(sqlText)
	if reShowSchemas.MatchString(trimmed) {
		writeQueryOK(w, []string{"name"}, [][]string{{"PUBLIC"}, {"MAIN"}, {"GOLD"}, {"INFORMATION_SCHEMA"}}, "duckdb")
		return true
	}
	if reShowFunctions.MatchString(trimmed) {
		// The emulator defines no UDFs, so the honest answer is no rows. The
		// column list is what dbt selects; is_builtin is Y/N in Snowflake and
		// dbt keeps the N rows.
		writeQueryOK(w, []string{"created_on", "name", "schema_name",
			"catalog_name", "is_builtin"}, nil, "duckdb")
		return true
	}
	if reShowObjects.MatchString(trimmed) {
		if strings.TrimSpace(s.Cfg.DuckDB) == "" {
			writeQueryOK(w, []string{"database_name", "schema_name", "name", "kind", "is_dynamic", "is_iceberg"}, nil, "duckdb")
			return true
		}
		res, err := engine.Exec(s.Cfg.DuckDB, "SHOW TABLES")
		if err != nil {
			writeFail(w, http.StatusOK, "002001", err.Error())
			return true
		}
		rows := make([][]string, 0, len(res.Rows))
		for _, r := range res.Rows {
			name := ""
			if len(r) > 0 {
				name = r[len(r)-1]
			}
			if name == "" {
				continue
			}
			rows = append(rows, []string{"TEST_DB", "PUBLIC", name, "TABLE", "N", "N"})
		}
		writeQueryOK(w, []string{"database_name", "schema_name", "name", "kind", "is_dynamic", "is_iceberg"}, rows, "duckdb")
		return true
	}
	if m := reDescribeTbl.FindStringSubmatch(trimmed); m != nil {
		if strings.TrimSpace(s.Cfg.DuckDB) == "" {
			writeFail(w, http.StatusOK, "000001", engine.MissingAttachError().Error())
			return true
		}
		table := strings.Trim(strings.TrimSpace(m[1]), `";`)
		table = reThreePart.ReplaceAllString(table, "")
		table = rePublicDot.ReplaceAllString(table, "")
		res, err := engine.Exec(s.Cfg.DuckDB, "DESCRIBE "+table)
		if err != nil {
			writeFail(w, http.StatusOK, "002001", err.Error())
			return true
		}
		rows := make([][]string, 0, len(res.Rows))
		for _, r := range res.Rows {
			colName, colType := "", "TEXT"
			if len(r) > 0 {
				colName = r[0]
			}
			if len(r) > 1 {
				colType = r[1]
			}
			rows = append(rows, []string{colName, colType})
		}
		writeQueryOK(w, []string{"name", "type"}, rows, "duckdb")
		return true
	}
	return false
}

func (s *Server) rewriteCopy(sqlText string) (string, error) {
	// COPY INTO t FROM @~/file.csv  →  COPY t FROM 'stage/file.csv' (HEADER)
	re := regexp.MustCompile(`(?i)COPY\s+INTO\s+(\S+)\s+FROM\s+'?@[^/]*/([^'\s]+)'?`)
	m := re.FindStringSubmatch(sqlText)
	if m == nil {
		return "", fmt.Errorf("COPY INTO from internal stage only (SNOWFLAKE_STAGE_DIR)")
	}
	table, file := m[1], m[2]
	src := filepath.Join(s.Cfg.StageDir, file)
	if _, err := os.Stat(src); err != nil {
		return "", fmt.Errorf("stage file %s: %w", src, err)
	}
	return fmt.Sprintf("COPY %s FROM '%s' (HEADER)", table, src), nil
}

func (s *Server) auth(r *http.Request) (session, bool) {
	tok := bearer(r)
	if tok == "" {
		return session{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.tokens[tok]
	return sess, ok
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	h = strings.TrimSpace(h)
	if strings.HasPrefix(strings.ToLower(h), "snowflake token=") {
		v := h[len("Snowflake Token="):]
		return strings.Trim(v, `"'`)
	}
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func writeQueryOK(w http.ResponseWriter, cols []string, rows [][]string, dialect string) {
	rowtype := make([]map[string]any, len(cols))
	for i, c := range cols {
		rowtype[i] = map[string]any{"name": c, "type": "text", "nullable": true, "length": 0, "scale": 0, "precision": 0}
	}
	if len(cols) == 0 {
		rowtype = []map[string]any{{"name": "status", "type": "text", "nullable": true, "length": 0, "scale": 0, "precision": 0}}
		if rows == nil {
			rows = [][]string{{"ok"}}
		}
		cols = []string{"status"}
	}
	// A zero-row result must serialise as [] and not null. A nil slice marshals
	// to JSON null, and the Snowflake connector calls len() on rowset, so an
	// empty answer with real columns came back as "object of type 'NoneType'
	// has no len()" rather than an empty table.
	if rows == nil {
		rows = [][]string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": nil,
		"code":    nil,
		"data": map[string]any{
			"queryId":            "q1",
			"sqlState":           "00000",
			"rowtype":            rowtype,
			"rowset":             rows,
			"total":              len(rows),
			"returned":           len(rows),
			"queryResultFormat":  "json",
			"statementTypeId":    4096,
			"version":            1,
			"numberOfBinds":      0,
			"chunks":             []any{},
			"qrmk":               "",
			"chunkHeaders":       map[string]any{},
			"finalDatabaseName":  "TEST_DB",
			"finalSchemaName":    "PUBLIC",
			"finalWarehouseName": "",
			"finalRoleName":      "ACCOUNTADMIN",
			"dialect":            dialect,
		},
	})
}

func writeFail(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"success": false,
		"code":    code,
		"message": msg,
		"data":    map[string]any{"errorCode": code, "errorMessage": msg},
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func icebergTableName(sqlText string) string {
	re := regexp.MustCompile(`(?i)CREATE\s+ICEBERG\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z0-9_.]+)`)
	m := re.FindStringSubmatch(sqlText)
	if m == nil {
		return "table"
	}
	return m[1]
}

func (s *Server) icebergNamespaces(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(s.Cfg.PolarisURL) == "" {
		http.Error(w, "set SNOWFLAKE_POLARIS_URL", http.StatusNotImplemented)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"namespaces": [][]string{{"TEST_DB"}}})
}

func (s *Server) icebergNamespace(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(s.Cfg.PolarisURL) == "" {
		http.Error(w, "set SNOWFLAKE_POLARIS_URL", http.StatusNotImplemented)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := []map[string]any{}
	for _, name := range s.iceberg {
		ids = append(ids, map[string]any{"namespace": []string{"TEST_DB"}, "name": name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"identifiers": ids})
}
