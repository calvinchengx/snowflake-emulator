package server

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// dbt Projects on Snowflake: a dbt project is an ACCOUNT OBJECT, and running
// it is a statement rather than something a client does from outside.
//
// That is the whole reason this exists. Everywhere else in this family dbt is
// an ordinary warehouse client -- e2e/dbt runs it from the host, and so does
// every platform. On Snowflake `EXECUTE DBT PROJECT` runs dbt INSIDE the
// account, which is what lets a task graph chain `run` and `test` with AFTER,
// and it is the shape Snowflake documents for orchestrating dbt:
//
//	CREATE TASK build AS EXECUTE DBT PROJECT my_project ARGS='run'
//	CREATE TASK test AFTER build AS EXECUTE DBT PROJECT my_project ARGS='test'
//
// So the emulator runs dbt the same way it runs duckdb: a CLI on argv, from
// this image. No second service, and no network hop between the statement and
// the thing that executes it.
//
// A FAILING dbt RUN FAILS THE QUERY, and that is measured against Snowflake's
// own documentation rather than chosen. `EXECUTE DBT PROJECT` returns Success,
// EXCEPTION, STDOUT and OUTPUT_ARCHIVE_URL as a result set, and it used to
// return Success = FALSE for a failed run -- a successful statement carrying a
// failure, which a task graph would happily run its downstream nodes after.
// Snowflake changed exactly that in October 2025: "Any dbt Project errors --
// like compile or test failures -- now appear as query failures", noting it
// "might cause a breaking change for anyone relying on the previous method of
// checking the return values". The release note says why, and it is the reason
// this emulator copies the new behaviour rather than the older document: it
// "makes it easier to handle them with tasks or other orchestration tools".
type dbtProject struct {
	Name   string
	Source string // as written: @~/sub, @my_stage/sub
	Target string
}

// dbtOutputPrefix is where in the user stage a project's output is left. A
// stage path rather than a URL because that is what a caller here can read:
// GET fetches it, and there is no signed-URL service to emulate.
const dbtOutputPrefix = "_dbt_output"

var (
	reCreateDbtProject = regexp.MustCompile(`(?is)^CREATE\s+(?:OR\s+REPLACE\s+)?DBT\s+PROJECT\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z0-9_$."]+)\s+FROM\s+'?([^'\s]+)'?\s*(.*)$`)
	reExecDbtProject   = regexp.MustCompile(`(?is)^EXECUTE\s+DBT\s+PROJECT\s+([A-Za-z0-9_$."]+)\s*(.*)$`)
	reDropDbtProject   = regexp.MustCompile(`(?i)^DROP\s+DBT\s+PROJECT\s+(?:IF\s+EXISTS\s+)?([A-Za-z0-9_$."]+)`)
	reShowDbtProjects  = regexp.MustCompile(`(?i)^SHOW\s+(?:TERSE\s+)?DBT\s+PROJECTS\b`)
	reDbtArgs          = regexp.MustCompile(`(?is)ARGS\s*=\s*'([^']*)'`)
	reDbtDefaultTarget = regexp.MustCompile(`(?i)DEFAULT_TARGET\s*=\s*'?([A-Za-z0-9_]+)'?`)
	reDbtEnvVars       = regexp.MustCompile(`(?is)ENV_VARS\s*=\s*\(([^)]*)\)`)
	reDbtEnvPair       = regexp.MustCompile(`(?is)([A-Za-z_][A-Za-z0-9_]*)\s*=\s*'([^']*)'`)
	reDbtProfileKey    = regexp.MustCompile(`(?im)^profile:\s*(.+)$`)
	// A source may be a workspace, a git-repository stage or another project on
	// a real account. Only the internal named stage is served here, and the
	// others are refused by name rather than half-accepted.
	reDbtStageSource = regexp.MustCompile(`^@([A-Za-z0-9_$~"]+)((?:/[^\s]*)?)$`)
)

func dbtKey(name string) string { return strings.Trim(strings.ToUpper(name), `"`) }

// dbtSubcommand is the part of ARGS that names what dbt should do. Snowflake
// supports run, test and deps.
var dbtSupportedSubcommands = map[string]bool{"run": true, "test": true, "deps": true}

func (s *Server) handleDbtProjectSQL(w http.ResponseWriter, sess session, sqlText string) bool {
	trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sqlText), ";"))

	if m := reCreateDbtProject.FindStringSubmatch(trimmed); m != nil {
		name, source, rest := dbtKey(m[1]), strings.TrimSpace(m[2]), m[3]
		sm := reDbtStageSource.FindStringSubmatch(source)
		if sm == nil {
			writeFail(w, http.StatusOK, "002101", fmt.Sprintf(
				"dbt project source %q is not an internal stage. A workspace, a git "+
					"repository stage or another project are all sources a real account "+
					"takes and this emulator does not serve; use '@stage/path'.", source))
			return true
		}
		if _, err := s.stageDir(strings.TrimPrefix(sm[1], "@")); err != nil {
			writeFail(w, http.StatusOK, "002102", err.Error())
			return true
		}
		p := dbtProject{Name: name, Source: source}
		if t := reDbtDefaultTarget.FindStringSubmatch(rest); t != nil {
			p.Target = t[1]
		}
		// ENV_VARS BELONGS ON EXECUTE, not here, and saying so is worth more
		// than quietly accepting it. Snowflake puts a project's environment in
		// its `env.yml` and lets `EXECUTE DBT PROJECT ... ENV_VARS = (...)`
		// override for one run; there is no ENV_VARS on CREATE. Taking it here
		// would store values that the statement a caller actually runs would
		// then ignore -- an env_var() falling to its default with the
		// configuration sitting right there in SHOW DBT PROJECTS.
		if reDbtEnvVars.MatchString(rest) {
			writeFail(w, http.StatusOK, "002104",
				"ENV_VARS is not a CREATE DBT PROJECT parameter: put it on "+
					"EXECUTE DBT PROJECT, which is where Snowflake overrides a "+
					"project's environment for a run")
			return true
		}
		s.mu.Lock()
		if s.dbtProjects == nil {
			s.dbtProjects = map[string]dbtProject{}
		}
		s.dbtProjects[name] = p
		s.mu.Unlock()
		writeQueryOK(w, []string{"status"},
			[][]string{{fmt.Sprintf("DBT Project %s successfully created.", name)}}, "duckdb")
		return true
	}

	if m := reDropDbtProject.FindStringSubmatch(trimmed); m != nil {
		name := dbtKey(m[1])
		s.mu.Lock()
		delete(s.dbtProjects, name)
		s.mu.Unlock()
		writeQueryOK(w, []string{"status"},
			[][]string{{fmt.Sprintf("DBT Project %s successfully dropped.", name)}}, "duckdb")
		return true
	}

	if reShowDbtProjects.MatchString(trimmed) {
		s.mu.Lock()
		names := make([]string, 0, len(s.dbtProjects))
		for n := range s.dbtProjects {
			names = append(names, n)
		}
		sort.Strings(names)
		rows := make([][]string, 0, len(names))
		for _, n := range names {
			p := s.dbtProjects[n]
			rows = append(rows, []string{p.Name, p.Source, p.Target})
		}
		s.mu.Unlock()
		writeQueryOK(w, []string{"name", "source", "default_target"}, rows, "duckdb")
		return true
	}

	if m := reExecDbtProject.FindStringSubmatch(trimmed); m != nil {
		env, err := dbtEnvVars(m[2])
		if err != nil {
			writeFail(w, http.StatusOK, "002103", err.Error())
			return true
		}
		out, archive, err := s.execDbtProject(sess, dbtKey(m[1]), m[2], env)
		if err != nil {
			// A QUERY FAILURE, not a row saying FALSE. See the type comment.
			writeFail(w, http.StatusOK, "002105", err.Error())
			return true
		}
		writeQueryOK(w,
			[]string{"Success", "EXCEPTION", "STDOUT", "OUTPUT_ARCHIVE_URL"},
			[][]string{{"TRUE", "None", out, archive}}, "duckdb")
		return true
	}
	return false
}

// execDbtProject materialises the project out of its stage and runs dbt over
// it, returning what dbt printed.
// dbtEnvVars reads an ENV_VARS clause, refusing keys dbt would never see.
//
// Snowflake's rule, quoted because it is absolute: "Every key in env: and in
// any override must be prefixed with DBT_ ... Every key must be UPPERCASE",
// and "these rules are enforced on every run. If you break them, the run
// fails." Accepting `contoso_silver_database` here would be the silent kind of
// wrong -- env_var() falls to its default and the models read the wrong thing
// with nothing failing -- and it would also let a project run here that a real
// account refuses, which is the direction that ships.
func dbtEnvVars(rest string) (map[string]string, error) {
	e := reDbtEnvVars.FindStringSubmatch(rest)
	if e == nil {
		return nil, nil
	}
	env := map[string]string{}
	for _, kv := range reDbtEnvPair.FindAllStringSubmatch(e[1], -1) {
		key := kv[1]
		if key != strings.ToUpper(key) || !strings.HasPrefix(key, "DBT_") {
			return nil, fmt.Errorf("ENV_VARS key %q is not usable: Snowflake requires "+
				"them UPPERCASE and prefixed DBT_", key)
		}
		env[key] = kv[2]
	}
	return env, nil
}

func (s *Server) execDbtProject(sess session, name, rest string, env map[string]string) (stdout, archive string, err error) {
	s.mu.Lock()
	p, ok := s.dbtProjects[name]
	s.mu.Unlock()
	if !ok {
		return "", "", fmt.Errorf("dbt project %s does not exist", name)
	}

	args := "run"
	if m := reDbtArgs.FindStringSubmatch(rest); m != nil {
		args = strings.TrimSpace(m[1])
	}
	fields := dbtArgFields(args)
	if len(fields) == 0 {
		return "", "", fmt.Errorf("EXECUTE DBT PROJECT needs ARGS naming a dbt command, e.g. ARGS='run'")
	}
	if !dbtSupportedSubcommands[strings.ToLower(fields[0])] {
		// Named rather than passed through: Snowflake supports run, test and
		// deps, and letting `seed` or `snapshot` reach the CLI here would make
		// this emulator answer a statement a real account refuses.
		return "", "", fmt.Errorf("dbt subcommand %q is not supported by EXECUTE DBT PROJECT: "+
			"Snowflake supports run, test and deps", fields[0])
	}

	sm := reDbtStageSource.FindStringSubmatch(p.Source)
	if sm == nil {
		return "", "", fmt.Errorf("dbt project %s has an unusable source %q", name, p.Source)
	}
	stageRoot, err := s.stageDir(sm[1])
	if err != nil {
		return "", "", err
	}
	src := filepath.Join(stageRoot, filepath.FromSlash(strings.TrimPrefix(sm[2], "/")))
	if _, err := os.Stat(filepath.Join(src, "dbt_project.yml")); err != nil {
		return "", "", fmt.Errorf("dbt project %s: %s has no dbt_project.yml at its root", name, p.Source)
	}

	// COPIED OUT OF THE STAGE, not run in place. dbt writes target/ and logs/
	// beside the project, and a stage is a place a caller PUTs files into and
	// lists -- filling it with build output would change what the caller sees
	// there on every run.
	work, err := os.MkdirTemp("", "dbt-project-")
	if err != nil {
		return "", "", err
	}
	defer func() { _ = os.RemoveAll(work) }()
	proj := filepath.Join(work, "project")
	if err := copyTree(src, proj); err != nil {
		return "", "", err
	}

	profiles := filepath.Join(work, "profiles")
	if err := os.MkdirAll(profiles, 0o755); err != nil {
		return "", "", err
	}
	if err := s.writeDbtProfile(sess, proj, profiles); err != nil {
		return "", "", err
	}

	argv := append(fields, "--project-dir", proj, "--profiles-dir", profiles)
	cmd := exec.Command("dbt", argv...)
	// HOME points INTO the work directory, and dbt does not run without it.
	// dbt-core 2.x keeps state under ~/.dbt (leases, and the parse cache); the
	// image runs as an unprivileged uid whose home is /, so dbt's first act was
	// `Failed to create directory: /.dbt/leases: Permission denied` -- reported
	// as "Finished 'run' with 1 error", which names neither the directory nor
	// the reason. Pointing HOME at the per-run temp directory makes it writable
	// wherever this runs and takes the state with it when the run is cleaned
	// up, so two runs never share a lease.
	cmd.Env = append(os.Environ(),
		"DBT_SEND_ANONYMOUS_USAGE_STATS=false",
		"HOME="+work,
	)
	// The project's own ENV_VARS, which is how a dbt project on Snowflake gets
	// an `env_var()` answered. Applied last so a project cannot be silently
	// overridden by whatever this process happens to have in its environment.
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, runErr := cmd.CombinedOutput()

	// THE ARTEFACTS LAND BEFORE THE ERROR IS RETURNED, and they land in the
	// STAGE rather than in the response.
	//
	// This is the shape Snowflake chose and it is better than carrying them
	// back inline. A failing `dbt test` is exactly when run_results.json is
	// worth having -- it names WHICH test failed and on how many rows, where
	// the failure alone says only that something did -- and a failed run has no
	// result set to carry anything. Leaving them somewhere the caller fetches
	// AFTERWARDS means the evidence outlives the failure instead of dying with
	// it. databricks-emulator learned the same thing from the other side: it
	// returned artefacts inline, and the exception reporting the failure took
	// them with it.
	archive, archErr := s.storeDbtOutput(name, proj)
	if archErr != nil {
		return string(out), "", fmt.Errorf("dbt ran and its output could not be stored: %w", archErr)
	}

	if runErr != nil {
		if _, statErr := exec.LookPath("dbt"); statErr != nil {
			return "", "", fmt.Errorf("EXECUTE DBT PROJECT needs dbt in this image and it is "+
				"not there (%v); the published image carries it from 0.2.0 onward", statErr)
		}
		return string(out), archive, fmt.Errorf("dbt %s failed: %s. Its output is in %s",
			args, lastMeaningfulLine(string(out)), archive)
	}
	return string(out), archive, nil
}

// storeDbtOutput copies what dbt wrote into the stage, and returns the stage
// reference a caller reads it back with -- the OUTPUT_ARCHIVE_URL column.
//
// run_results.json ALONE. manifest.json is large, changes on every parse, and
// no caller has asked for it; dbt's own words are already returned as STDOUT.
// run_results is the one a pipeline needs, because it says which tests were
// EVALUATED rather than which exist -- the difference between a snapshot
// naming guarantees and a snapshot naming guarantees something checked.
//
// OVERWRITTEN PER PROJECT rather than accumulated. A stage that grows a
// directory per run is one a consumer has to garbage-collect, and nothing here
// knows when a caller has finished reading one.
func (s *Server) storeDbtOutput(name, projectDir string) (string, error) {
	body, err := os.ReadFile(filepath.Join(projectDir, "target", "run_results.json"))
	if err != nil {
		// A run that produced none is not an error: `dbt deps` writes no
		// run_results, and a project that failed to parse never got that far.
		// An empty archive path says that, where an invented empty file would
		// be believed.
		return "", nil
	}
	// Through stagePath for the same reason the stage handlers are: `name`
	// carries a caller-supplied project name through dbtKey, which trims and
	// uppercases but cannot neutralise `..`. CodeQL did not flag this one.
	dir, err := s.stagePath(dbtOutputPrefix, name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "run_results.json"), body, 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("@~/%s/%s", dbtOutputPrefix, name), nil
}

// writeDbtProfile generates the profile dbt will resolve, under the name the
// PROJECT asks for.
//
// dbt reads `profile:` from dbt_project.yml and looks that name up, so a
// profile written under any other key sends dbt looking for one that was never
// written -- the consumer would have to rename its own profile to run here,
// which makes the project emulator-only. The same defect was found and fixed in
// databricks-emulator's dbt_task; it is avoided here rather than repeated.
//
// A DELIBERATE DEVIATION, named rather than hidden: a real account merges the
// project's own profiles.yml with the caller's session, which is why Snowflake
// documents account and user as allowed to be empty there. This writes the
// whole profile instead. Reading and merging YAML would need a YAML dependency
// this repository does not have, and the emulator knows every value that
// matters -- it is the account.
// dbtArgFields splits ARGS the way a shell would, respecting quotes.
//
// NOT strings.Fields. ARGS is a string of dbt CLI arguments, and the one a
// medallion needs most carries JSON:
//
//	ARGS='run --vars {"bronze_pos_orders": "bronze_pos_orders"}'
//
// Splitting that on whitespace hands dbt a fragment per key and it fails on
// something that looks nothing like the cause. Quotes group, and are removed,
// exactly as a shell does before dbt would ever see them.
func dbtArgFields(args string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	started := false
	for _, r := range args {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			started = true
		case r == ' ' || r == '\t' || r == '\n':
			if started || cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteRune(r)
		}
	}
	if started || cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func (s *Server) writeDbtProfile(sess session, projectDir, profilesDir string) error {
	body, err := os.ReadFile(filepath.Join(projectDir, "dbt_project.yml"))
	if err != nil {
		return err
	}
	profile := "default"
	if m := reDbtProfileKey.FindSubmatch(body); m != nil {
		v := strings.TrimSpace(string(m[1]))
		if i := strings.Index(v, "#"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		if v = strings.Trim(v, `"'`); v != "" {
			profile = v
		}
	}

	wh, db, sch := sess.Warehouse, sess.Database, sess.Schema
	if wh == "" {
		wh = "COMPUTE_WH"
	}
	if db == "" {
		db = "TEST_DB"
	}
	if sch == "" {
		sch = "PUBLIC"
	}
	host, port := s.listenHostPort()

	// http and insecure_mode, because this emulator serves plain HTTP: the
	// connector otherwise builds an https URL and never arrives.
	yaml := fmt.Sprintf(`%s:
  target: emulator
  outputs:
    emulator:
      type: snowflake
      account: test
      user: admin
      password: %s
      host: %s
      port: %s
      protocol: http
      insecure_mode: true
      warehouse: %s
      database: %s
      schema: %s
      threads: 1
`, profile, s.PAT, host, port, wh, db, sch)
	return os.WriteFile(filepath.Join(profilesDir, "profiles.yml"), []byte(yaml), 0o600)
}

// lastMeaningfulLine is what dbt said last that was not blank, which is where
// its own summary of a failure lives. The whole log still goes back as STDOUT;
// this is only what the error message names, so a caller sees the reason
// without reading a hundred lines.
func lastMeaningfulLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return "no output"
}

// listenHostPort is this emulator as dbt must reach it.
//
// dbt runs in THIS container, so the loopback address is right and there is no
// question of which name the runtime uses -- the defect that took three fixes
// on the databricks side, where the dbt runtime is a separate container and a
// client-facing address resolved there to something else entirely. A single
// process has no such gap, which is the strongest argument for running dbt
// here rather than beside.
func (s *Server) listenHostPort() (string, string) {
	host, port, err := net.SplitHostPort(s.Cfg.Addr)
	if err != nil {
		return "127.0.0.1", "8448"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return host, port
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}
