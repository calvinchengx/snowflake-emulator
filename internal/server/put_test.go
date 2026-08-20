package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/calvinchengx/snowflake-emulator/internal/config"
)

// put drives the handler the way the drivers do: the statement goes in, the
// upload instructions come back. No bytes move here, because in the real
// protocol the client moves them.
func put(t *testing.T, s *Server, sql string) (map[string]any, bool) {
	t.Helper()
	w := httptest.NewRecorder()
	if !s.handleStageSQL(w, sql) {
		t.Fatalf("PUT was not recognised as a stage statement: %q", sql)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	ok, _ := body["success"].(bool)
	data, _ := body["data"].(map[string]any)
	return data, ok
}

func stageServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	return &Server{Cfg: config.Config{StageDir: dir}}, dir
}

func TestPutAnswersTheContractBothDriversRead(t *testing.T) {
	// The field list is not decoration. gosnowflake reads data.command and
	// data.stageInfo; the Python connector's _parse_command raises unless
	// src_locations is a LIST and stageInfo carries locationType, location
	// and creds. A response missing any of them fails inside the driver,
	// where the error names nothing useful.
	s, dir := stageServer(t)
	data, ok := put(t, s, "PUT file:///tmp/orders.csv @~")
	if !ok {
		t.Fatalf("PUT was refused: %v", data)
	}
	if data["command"] != "UPLOAD" {
		t.Errorf("command = %v, want UPLOAD", data["command"])
	}
	src, isList := data["src_locations"].([]any)
	if !isList || len(src) != 1 || src[0] != "/tmp/orders.csv" {
		t.Errorf("src_locations = %#v, want a one-element list holding the path", data["src_locations"])
	}
	info, _ := data["stageInfo"].(map[string]any)
	if info["locationType"] != "LOCAL_FS" {
		t.Errorf("locationType = %v, want LOCAL_FS", info["locationType"])
	}
	if got := info["location"]; got != dir+string(os.PathSeparator) {
		t.Errorf("location = %v, want the stage directory %q", got, dir)
	}
	if _, present := info["creds"]; !present {
		t.Error("creds must be present even though LOCAL_FS needs none: the Python connector indexes it")
	}
}

func TestAutoCompressDefaultsToSnowflakesTrue(t *testing.T) {
	// TRUE is what a real account does, so `PUT file://orders.csv @~` lands
	// orders.csv.gz there. Answering FALSE would be more convenient here and
	// would teach a consumer the wrong stage contents.
	s, _ := stageServer(t)
	data, _ := put(t, s, "PUT file:///tmp/orders.csv @~")
	if data["autoCompress"] != true {
		t.Errorf("autoCompress = %v, want true", data["autoCompress"])
	}
	data, _ = put(t, s, "PUT file:///tmp/orders.csv @~ AUTO_COMPRESS = FALSE")
	if data["autoCompress"] != false {
		t.Errorf("AUTO_COMPRESS = FALSE was not honoured: %v", data["autoCompress"])
	}
}

func TestAPutOptionWeCannotHonourIsRefused(t *testing.T) {
	// Same rule the file formats keep. A dropped option that changes what the
	// driver does is a success report for something else.
	s, _ := stageServer(t)
	data, ok := put(t, s, "PUT file:///tmp/orders.csv @~ MAGIC = TRUE")
	if ok {
		t.Fatal("an unknown PUT option was accepted")
	}
	if msg, _ := data["errorMessage"].(string); !strings.Contains(msg, "MAGIC") {
		t.Errorf("the refusal must name the option, got %q", msg)
	}
}

func TestPutFromSomewhereWeCannotReadIsRefusedByName(t *testing.T) {
	s, _ := stageServer(t)
	data, ok := put(t, s, "PUT s3://bucket/orders.csv @~")
	if ok {
		t.Fatal("PUT from s3:// was accepted; this emulator uploads from file:// only")
	}
	if msg, _ := data["errorMessage"].(string); !strings.Contains(msg, "s3://") {
		t.Errorf("the refusal must name the scheme, got %q", msg)
	}
}

func TestPutIntoAStageThatDoesNotExistIsRefused(t *testing.T) {
	s, _ := stageServer(t)
	if _, ok := put(t, s, "PUT file:///tmp/orders.csv @landing"); ok {
		t.Fatal("PUT into an uncreated named stage was accepted")
	}
}

func TestPutNamesThePathTheClientCanWrite(t *testing.T) {
	// The container case. This process sees /stages; a client on the host
	// sees somewhere else, and the driver -- not this process -- does the
	// copying. Answering our own path would send the bytes into a directory
	// the client either cannot write or does not share, and the failure would
	// surface two statements later at COPY INTO.
	ours := t.TempDir()   // what this process sees, e.g. /stages
	theirs := t.TempDir() // what the client sees, the host side of the mount
	s := &Server{Cfg: config.Config{StageDir: ours, StageClientDir: theirs}}
	data, ok := put(t, s, "PUT file:///tmp/orders.csv @~")
	if !ok {
		t.Fatalf("PUT was refused: %v", data)
	}
	info, _ := data["stageInfo"].(map[string]any)
	got, _ := info["location"].(string)
	if !strings.HasPrefix(got, theirs) {
		t.Errorf("location = %q, want it under the client's view %q", got, theirs)
	}
	if strings.HasPrefix(got, ours) {
		t.Errorf("location = %q is OUR path; the client cannot write there", got)
	}
}

func TestCopyIntoFindsWhatPutLeftBehind(t *testing.T) {
	// The two halves have to meet. AUTO_COMPRESS is on by default, so the
	// file in the stage is orders.csv.gz while the consumer's COPY INTO names
	// orders.csv -- which real Snowflake resolves by prefix. Without the .gz
	// arm this emulator answers "no such file" for the file it just accepted.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "orders.csv.gz"), []byte("gz"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := stageFile(dir, "orders.csv")
	if err != nil {
		t.Fatalf("COPY INTO could not find the uploaded file: %v", err)
	}
	if filepath.Base(got) != "orders.csv.gz" {
		t.Errorf("resolved %q, want orders.csv.gz", got)
	}
}

func TestAnExactNameStillWinsAndAMissingFileStillFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "orders.csv"), []byte("a,b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := stageFile(dir, "orders.csv")
	if err != nil || filepath.Base(got) != "orders.csv" {
		t.Fatalf("exact match must win: %q %v", got, err)
	}
	// The honest failure this repository keeps having to restore: a stage
	// file that is not there is an error, never an empty load reported ok.
	if _, err := stageFile(dir, "absent.csv"); err == nil {
		t.Error("a missing stage file was resolved")
	}
}

func TestAPrefixIsRefusedByNameRatherThanByDuckdb(t *testing.T) {
	// Measured before this existed: `COPY INTO t FROM @~/feed/` came back as
	// `duckdb: IO Error: No files found that match the pattern "/stages/feed"`.
	// That is a refusal, so nothing was silent, but it names a path inside a
	// container and a duckdb concept. A reader has to deduce that the FEATURE
	// is missing rather than the file.
	dir := t.TempDir()
	feed := filepath.Join(dir, "feed")
	if err := os.MkdirAll(feed, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"part_0.csv", "part_1.csv"} {
		if err := os.WriteFile(filepath.Join(feed, name), []byte("n\n1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ path, why string }{
		{"feed/", "a trailing slash"},
		{"feed", "a directory"},
		{"feed/*.csv", "a glob"},
		{"", "the whole stage"},
	} {
		_, err := stageFile(dir, tc.path)
		if err == nil {
			t.Errorf("%q was resolved; a prefix must be refused", tc.path)
			continue
		}
		if !strings.Contains(err.Error(), "COPY INTO from a prefix is not implemented") {
			t.Errorf("%q refused as %q, which does not name the missing feature", tc.path, err)
		}
	}
}

func TestTheRefusalSaysWhatToDoInstead(t *testing.T) {
	// A refusal that names the gap and not the remedy sends the reader to the
	// source. Both halves are asserted because both were written on purpose.
	_, err := stageFile(t.TempDir(), "feed/")
	if err == nil {
		t.Fatal("a prefix was resolved")
	}
	for _, want := range []string{"name one file", "Snowflake loads every file under a prefix"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q is missing %q", err, want)
		}
	}
}

func TestANamedFileIsStillNotAPrefix(t *testing.T) {
	// The guard must not swallow the ordinary case, including the .gz arm
	// that PUT depends on.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "orders.csv.gz"), []byte("gz"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := stageFile(dir, "orders.csv")
	if err != nil || filepath.Base(got) != "orders.csv.gz" {
		t.Fatalf("a named file must still resolve: %q %v", got, err)
	}
}

// TestPutDestinationIsWritableByAnotherUser is the Airflow 3 cell's bug.
//
// `PUT` is a two-party operation: this server decides where the file goes and
// answers with the path, and the DRIVER writes the bytes. When the two run as
// different users -- a containerised client against a containerised warehouse,
// which is the ordinary shape -- a destination directory created 0755 by this
// process is one the client cannot write into:
//
//	PermissionError: [Errno 13] Permission denied:
//	    '/stages/contoso_pos_customers/part-0001.csv.gz'
//
// The driver wraps that as `253003: While putting file(s) there was an error`,
// which names blob storage and gives no hint that a umask decided it.
//
// ASSERTS THE MODE, not that a write succeeded, because a test running as the
// same uid that created the directory can write to it whatever the mode is --
// which is precisely why the unit tests missed this and a two-container stack
// found it.
func TestPutDestinationIsWritableByAnotherUser(t *testing.T) {
	stage := t.TempDir()
	s := &Server{Cfg: config.Config{StageDir: stage}}
	dest := filepath.Join(stage, "feed")

	if err := s.preparePutDir(dest); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o777 {
		t.Fatalf("stage subdirectory is %04o, want 0777 -- a client running as "+
			"another user cannot write the bytes it was told to write", perm)
	}
}

// TestPutDirAcceptsAnAlreadyWritableDirectoryItCannotChmod is the regression a
// peer review caught in the fix above, before it shipped.
//
// `os.Chmod` fails when this process does not own the directory EVEN IF THE
// MODE IS ALREADY WHAT WE WANT. Treating that as fatal would refuse a PUT that
// works today: `PUT @~/orders.csv` with no subdirectory makes the destination
// the stage ROOT, which in a container deployment is a mounted volume this
// process very likely does not own. MkdirAll is a no-op there and the upload
// proceeds -- unless a chmod error stops it.
//
// The connector can also create that directory itself, as the client's uid,
// which is one we can never chmod.
func TestPutDirAcceptsAnAlreadyWritableDirectoryItCannotChmod(t *testing.T) {
	// A world-writable directory this process does not own. If the test runs as
	// root there is no such thing, and the case cannot be exercised -- said out
	// loud rather than passing vacuously.
	var target string
	for _, c := range []string{"/private/var/tmp", "/var/tmp", "/tmp"} {
		fi, err := os.Stat(c)
		if err != nil || fi.Mode().Perm()&0o002 == 0 {
			continue
		}
		if os.Chmod(c, fi.Mode().Perm()) != nil { // cannot chmod: what we want
			target = c
			break
		}
	}
	if target == "" {
		t.Skip("no world-writable directory this process cannot chmod (running as root?)")
	}

	s := &Server{Cfg: config.Config{StageDir: target}}
	if err := s.preparePutDir(target); err != nil {
		t.Fatalf("refused a directory the client can already write to: %v", err)
	}
}
