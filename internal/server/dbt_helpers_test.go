package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/calvinchengx/snowflake-emulator/internal/config"
)

// The dbt-project path runs dbt IN THIS PROCESS, and these four helpers do the
// unglamorous work around it: find the address dbt should dial, copy the
// project into a scratch tree, and pull the one line worth reporting out of
// dbt's output. All four were at 0% — the dbt path is exercised end to end or
// not at all, and end-to-end tests do not reach the branches where these decide
// something.

func TestLastMeaningfulLine(t *testing.T) {
	// dbt's failure is the LAST thing it said, not the first, and its output
	// ends in blank lines. Reporting "" or the wrong line is how a failed run
	// gets summarised as something that did not go wrong.
	for name, tc := range map[string]struct{ in, want string }{
		"trailing blanks": {"first\nreal failure\n\n\n", "real failure"},
		"trailing spaces": {"first\nreal failure\n   \n\t\n", "real failure"},
		"single line":     {"only", "only"},
		"empty":           {"", "no output"},
		"whitespace only": {"\n \t\n", "no output"},
		"interior blanks": {"a\n\nb", "b"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := lastMeaningfulLine(tc.in); got != tc.want {
				t.Errorf("lastMeaningfulLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestListenHostPortIsAlwaysDialable(t *testing.T) {
	// dbt dials this address from inside this process. A wildcard bind is a
	// LISTEN address, not a destination: dialling 0.0.0.0 or :: is what a
	// caller cannot do, so both must come back as loopback.
	for name, tc := range map[string]struct{ addr, host, port string }{
		"explicit host": {"192.168.1.5:9999", "192.168.1.5", "9999"},
		"wildcard v4":   {"0.0.0.0:8448", "127.0.0.1", "8448"},
		"wildcard v6":   {"::" + ":8448", "127.0.0.1", "8448"},
		"empty host":    {":8448", "127.0.0.1", "8448"},
		"unparseable":   {"not-an-address", "127.0.0.1", "8448"},
	} {
		t.Run(name, func(t *testing.T) {
			s := &Server{Cfg: config.Config{Addr: tc.addr}}
			host, port := s.listenHostPort()
			if host != tc.host || port != tc.port {
				t.Errorf("listenHostPort(%q) = %s:%s, want %s:%s",
					tc.addr, host, port, tc.host, tc.port)
			}
		})
	}
}

func TestCopyTreeReproducesTheProject(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(filepath.Join(src, "models", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"dbt_project.yml":     "name: p\n",
		"models/a.sql":        "select 1\n",
		"models/nested/b.sql": "select 2\n",
	}
	for rel, body := range files {
		if err := os.WriteFile(filepath.Join(src, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := copyTree(src, dst); err != nil {
		t.Fatal(err)
	}
	// Every file, at its own relative path, with its own bytes. A copy that
	// flattened the tree would still "succeed" and then compile nothing.
	for rel, want := range files {
		got, err := os.ReadFile(filepath.Join(dst, rel))
		if err != nil {
			t.Errorf("%s missing from the copy: %v", rel, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
}

func TestCopyTreeReportsAMissingSource(t *testing.T) {
	// A silent success here would produce an empty project directory, and dbt
	// would report "no models found" — blaming the project for a copy that
	// never ran.
	if err := copyTree(filepath.Join(t.TempDir(), "nope"), t.TempDir()); err == nil {
		t.Error("copying a source that does not exist reported success")
	}
}
