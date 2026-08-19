package engine

import (
	"os"
	"regexp"
	"testing"
)

// The duckdb version is written in the Dockerfile and again in every CI step
// that installs the CLI. They have to be the same version, and nothing said so.
//
// This is not tidiness. The pinned CLI (v1.2.2) EXITS 0 AFTER REFUSING a
// statement where a newer one exits 1, so an emulator built on one and tested
// against the other reports honest failures in CI that the shipped image does
// not give. That is not a hypothetical: it is how a silent 200 on every
// unsupported statement survived a day of measurement, because a host build
// answered honestly and the container did not.
func TestEveryDuckdbPinIsTheSameVersion(t *testing.T) {
	version := regexp.MustCompile(`duckdb/releases/download/v([0-9]+\.[0-9]+\.[0-9]+)/`)
	seen := map[string][]string{}
	for _, path := range []string{"../../Dockerfile", "../../.github/workflows/ci.yml"} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		found := version.FindAllStringSubmatch(string(body), -1)
		if len(found) == 0 {
			t.Fatalf("%s names no duckdb release; if the pin moved, this test needs a look", path)
		}
		for _, m := range found {
			seen[m[1]] = append(seen[m[1]], path)
		}
	}
	if len(seen) != 1 {
		t.Fatalf("the duckdb pin disagrees with itself: %v -- an emulator built "+
			"on one version and tested against another reports failures the "+
			"shipped image does not", seen)
	}
}
