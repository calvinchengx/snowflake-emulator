package server

import "testing"

func TestRewriteTransientAndCurrent(t *testing.T) {
	sess := session{Database: "TEST_DB", Schema: "PUBLIC", Warehouse: "wh1"}
	out, _, special := rewriteSQL("create or replace transient table one as select 1 as id", sess)
	if special {
		t.Fatal("not special")
	}
	if out != "CREATE OR REPLACE TABLE one as select 1 as id" {
		t.Fatalf("got %q", out)
	}
	out, extra, special := rewriteSQL("USE WAREHOUSE e2e_wh", sess)
	if !special || extra != "use_warehouse" || out != "e2e_wh" {
		t.Fatalf("use warehouse: %q %q %v", out, extra, special)
	}
}

func TestExtractSQLNested(t *testing.T) {
	s := extractSQL([]byte(`{"data":{"sqlText":"SELECT 1"}}`))
	if s != "SELECT 1" {
		t.Fatalf("got %q", s)
	}
}
