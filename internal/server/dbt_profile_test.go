package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/calvinchengx/snowflake-emulator/internal/config"
)

// writeDbtProfile builds the profiles.yml dbt connects through, and it was at
// 0%: the dbt path is exercised end to end or not at all, and an end-to-end run
// asserts that dbt WORKED, never what it was handed. A wrong profile fails
// inside dbt with a connection error that names none of this.
//
// It is a pure function of (dbt_project.yml, session, listen address), so it
// needs no dbt at all.

func profileServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	srv, err := New(config.Config{
		DataDir: dir, StageDir: filepath.Join(dir, "stages"), Addr: "0.0.0.0:8448",
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func writeProject(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dbt_project.yml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readProfile(t *testing.T, profilesDir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(profilesDir, "profiles.yml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestDbtProfileNamesTheProjectsOwnProfile: dbt looks up the profile BY NAME
// from dbt_project.yml. Writing it under any other key makes dbt report
// "Could not find profile named ..." — which reads as the user's mistake.
func TestDbtProfileNamesTheProjectsOwnProfile(t *testing.T) {
	srv := profileServer(t)
	for name, tc := range map[string]struct{ project, want string }{
		"plain":            {"name: p\nprofile: myprofile\n", "myprofile"},
		"quoted":           {"name: p\nprofile: \"quoted\"\n", "quoted"},
		"single quoted":    {"name: p\nprofile: 'single'\n", "single"},
		"trailing comment": {"name: p\nprofile: withcomment # why\n", "withcomment"},
		"absent":           {"name: p\n", "default"},
		"empty value":      {"name: p\nprofile:\n", "default"},
	} {
		t.Run(name, func(t *testing.T) {
			projectDir := writeProject(t, tc.project)
			profilesDir := t.TempDir()
			if err := srv.writeDbtProfile(session{}, projectDir, profilesDir); err != nil {
				t.Fatal(err)
			}
			got := readProfile(t, profilesDir)
			if !strings.HasPrefix(strings.TrimSpace(got), tc.want+":") {
				t.Errorf("profile is keyed %q, want %q:\n%s",
					strings.SplitN(strings.TrimSpace(got), ":", 2)[0], tc.want, got)
			}
		})
	}
}

// TestDbtProfileIsDialableAndPlainHTTP.
//
// Two things dbt cannot recover from: an https URL against a plain-HTTP
// emulator (the connector never arrives), and a wildcard host (0.0.0.0 is a
// LISTEN address, not a destination). Both fail inside the connector, far from
// the code that chose them.
func TestDbtProfileIsDialableAndPlainHTTP(t *testing.T) {
	srv := profileServer(t)
	profilesDir := t.TempDir()
	if err := srv.writeDbtProfile(session{}, writeProject(t, "name: p\n"), profilesDir); err != nil {
		t.Fatal(err)
	}
	got := readProfile(t, profilesDir)
	if strings.Contains(got, "0.0.0.0") {
		t.Errorf("the profile tells dbt to dial a wildcard bind address:\n%s", got)
	}
	if !strings.Contains(got, "127.0.0.1") {
		t.Errorf("the profile does not name a dialable host:\n%s", got)
	}
	for _, want := range []string{"http", "insecure_mode"} {
		if !strings.Contains(got, want) {
			t.Errorf("the profile omits %q, so the connector builds an https URL "+
				"this emulator does not serve:\n%s", want, got)
		}
	}
}

// TestDbtProfileDefaultsTheSessionsUnsetFields: a session that never issued
// USE WAREHOUSE/DATABASE/SCHEMA still has to produce a working profile, and
// dbt refuses a profile with empty values rather than defaulting them itself.
func TestDbtProfileDefaultsTheSessionsUnsetFields(t *testing.T) {
	srv := profileServer(t)
	profilesDir := t.TempDir()
	if err := srv.writeDbtProfile(session{}, writeProject(t, "name: p\n"), profilesDir); err != nil {
		t.Fatal(err)
	}
	got := readProfile(t, profilesDir)
	for _, want := range []string{"COMPUTE_WH", "TEST_DB", "PUBLIC"} {
		if !strings.Contains(got, want) {
			t.Errorf("an unset session produced a profile without %s:\n%s", want, got)
		}
	}

	// A session that DID set them must win over those defaults.
	profilesDir2 := t.TempDir()
	sess := session{Warehouse: "WH_X", Database: "DB_X", Schema: "SC_X"}
	if err := srv.writeDbtProfile(sess, writeProject(t, "name: p\n"), profilesDir2); err != nil {
		t.Fatal(err)
	}
	got2 := readProfile(t, profilesDir2)
	for _, want := range []string{"WH_X", "DB_X", "SC_X"} {
		if !strings.Contains(got2, want) {
			t.Errorf("the session's own %s was overwritten by a default:\n%s", want, got2)
		}
	}
}

func TestDbtProfileReportsAMissingProject(t *testing.T) {
	// No dbt_project.yml means this is not a dbt project. Writing a profile
	// anyway would let the run proceed and fail later, further from the cause.
	srv := profileServer(t)
	if err := srv.writeDbtProfile(session{}, t.TempDir(), t.TempDir()); err == nil {
		t.Error("a directory with no dbt_project.yml produced a profile")
	}
}
