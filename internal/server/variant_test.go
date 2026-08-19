package server

import "testing"

func TestVariantBecomesJson(t *testing.T) {
	cases := map[string]string{
		"CREATE TABLE raw (v VARIANT)":               "CREATE TABLE raw (v JSON)",
		"CREATE TABLE raw (a INT, v VARIANT, b INT)": "CREATE TABLE raw (a INT, v JSON, b INT)",
		"CREATE TABLE raw (o OBJECT, a ARRAY)":       "CREATE TABLE raw (o JSON, a JSON)",
	}
	for in, want := range cases {
		if got := rewriteVariantTypes(in); got != want {
			t.Errorf("%q ->\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestVariantOnlyInDDL(t *testing.T) {
	// `variant` is an ordinary word. A SELECT naming a column that way, or a
	// string carrying it, is not a type declaration.
	for _, in := range []string{
		"SELECT variant FROM t",
		"SELECT 'VARIANT' AS note",
	} {
		if got := rewriteVariantTypes(in); got != in {
			t.Errorf("%q was rewritten to %q", in, got)
		}
	}
	if got := rewriteVariantTypes("CREATE TABLE t (a INT) -- 'VARIANT'"); got != "CREATE TABLE t (a INT) -- 'VARIANT'" {
		t.Errorf("a literal in DDL was rewritten: %q", got)
	}
}

func TestColonPathBecomesJsonExtract(t *testing.T) {
	cases := map[string]string{
		"SELECT v:id FROM raw":           "SELECT json_extract(v, '$.id') FROM raw",
		"SELECT v:a.b FROM raw":          "SELECT json_extract(v, '$.a.b') FROM raw",
		"SELECT v:lines[0].sku FROM raw": "SELECT json_extract(v, '$.lines[0].sku') FROM raw",
		"SELECT raw.v:id FROM raw":       "SELECT json_extract(raw.v, '$.id') FROM raw",
	}
	for in, want := range cases {
		if got := rewriteColonPaths(in); got != want {
			t.Errorf("%q ->\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestACastAfterAPathUnwrapsTheValue(t *testing.T) {
	// THE CASE THAT MATTERS. json_extract returns JSON, so a string arrives
	// wearing its quotes: v:email::string would be "a@x.com" INCLUDING them,
	// a value that compares unequal to itself across engines.
	got := rewriteColonPaths("SELECT v:email::string FROM raw")
	want := "SELECT CAST(json_extract_string(v, '$.email') AS string) FROM raw"
	if got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
	got = rewriteColonPaths("SELECT v:id::int + 1 FROM raw")
	want = "SELECT CAST(json_extract_string(v, '$.id') AS int) + 1 FROM raw"
	if got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
}

func TestDoubleColonIsACastNotAPath(t *testing.T) {
	for _, in := range []string{
		"SELECT a::int FROM t",
		"SELECT count(*)::varchar FROM t",
	} {
		if got := rewriteColonPaths(in); got != in {
			t.Errorf("%q was read as a path: %q", in, got)
		}
	}
}

func TestColonsInStringsAndElsewhereAreLeftAlone(t *testing.T) {
	for _, in := range []string{
		"SELECT 'a:b' AS note FROM t",
		"SELECT 'http://x/y' AS url",
		"SELECT CASE WHEN a THEN 1 ELSE 2 END FROM t",
	} {
		if got := rewriteColonPaths(in); got != in {
			t.Errorf("%q was rewritten to %q", in, got)
		}
	}
}

func TestTwoPathsInOneStatement(t *testing.T) {
	got := rewriteColonPaths("SELECT v:a, v:b FROM raw")
	want := "SELECT json_extract(v, '$.a'), json_extract(v, '$.b') FROM raw"
	if got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
}
