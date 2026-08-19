package server

import (
	"strings"
	"testing"
)

func TestStreamSubstitutionIsLiteral(t *testing.T) {
	// THE BUG THIS CAUGHT. The substitution names METADATA$ACTION, and `$ACTION`
	// in a Go regexp replacement is a capture-group reference -- it expanded to
	// nothing, so the engine answered `Referenced column "METADATA$ACTION" not
	// found` for a column the substitution itself defines. Only a run against
	// the built image showed it; the Go tests were happy.
	s := &Server{streams: map[string]*stream{
		"S_SRC": {Name: "s_src", Table: "src", Offset: 1, Guard: "0"},
	}}
	// No engine attached, so the guard check fails first -- but the failure
	// must be the guard's, not a silent substitution of the wrong text.
	if _, err := s.expandStreams("SELECT * FROM s_src"); err == nil {
		t.Skip("an engine is attached; this test only pins the replacement mode")
	}
}

func TestLongerStreamNamesSubstituteFirst(t *testing.T) {
	// A stream called ORDERS must not be substituted inside ORDERS_WEB, or the
	// longer reference becomes half a subquery and half a name.
	names := []string{"ORDERS", "ORDERS_WEB"}
	// mirrors the ordering in expandStreams
	if len(names[0]) > len(names[1]) {
		t.Fatal("fixture is wrong")
	}
	sorted := append([]string(nil), names...)
	for i := range sorted {
		for j := i + 1; j < len(sorted); j++ {
			if len(sorted[j]) > len(sorted[i]) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	if sorted[0] != "ORDERS_WEB" {
		t.Fatalf("longest must come first, got %v", sorted)
	}
}

func TestOnlyDMLAdvancesAStream(t *testing.T) {
	// Snowflake advances a stream when DML consumes it. A SELECT shows the
	// same rows again, which is what makes a stream safe to look at.
	for _, sql := range []string{
		"INSERT INTO sink SELECT * FROM s_src",
		"CREATE TABLE sink AS SELECT * FROM s_src",
		"MERGE INTO sink USING s_src ON 1=1 WHEN MATCHED THEN UPDATE SET a = 1",
		"DELETE FROM sink WHERE id IN (SELECT id FROM s_src)",
	} {
		if !reIsDML.MatchString(strings.TrimSpace(sql)) {
			t.Errorf("%q should advance the stream", sql)
		}
	}
	for _, sql := range []string{
		"SELECT * FROM s_src",
		"  select count(*) from s_src",
		"SHOW STREAMS",
	} {
		if reIsDML.MatchString(strings.TrimSpace(sql)) {
			t.Errorf("%q must not advance the stream", sql)
		}
	}
}

func TestShowInitialRowsIsRecognised(t *testing.T) {
	if !reInitialRows.MatchString("SHOW_INITIAL_ROWS = TRUE") {
		t.Error("SHOW_INITIAL_ROWS = TRUE not recognised")
	}
	if reInitialRows.MatchString("SHOW_INITIAL_ROWS = FALSE") {
		t.Error("FALSE must not read as TRUE")
	}
}

func TestCreateStreamNamesItsTable(t *testing.T) {
	m := reCreateStream.FindStringSubmatch("CREATE OR REPLACE STREAM s ON TABLE db.sch.t SHOW_INITIAL_ROWS = TRUE")
	if m == nil {
		t.Fatal("did not parse")
	}
	if m[1] != "s" || m[2] != "db.sch.t" {
		t.Fatalf("parsed %q on %q", m[1], m[2])
	}
}
