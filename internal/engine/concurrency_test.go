package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestConcurrentStatementsDoNotCollideOnTheFileLock is the test the Airflow 3
// cell paid for.
//
// Every statement runs the duckdb CLI against the warehouse file, and duckdb
// holds an EXCLUSIVE lock for the life of that process. Two statements in
// flight means the second is refused with
//
//	Could not set lock on file: Conflicting lock is held in ... (PID ...)
//
// which is a valid statement answered with an error -- the one thing this
// emulator's doctrine forbids. Real Snowflake serves concurrent queries.
//
// IT SURVIVED THIS LONG BECAUSE EVERY CONSUMER WAS SEQUENTIAL: the Tasks cell
// runs one step at a time from a Makefile, and the e2e suites issue one
// statement at a time. Cosmos rendering eight silver models as eight Airflow
// tasks was the first client to send several at once, and three of the eight
// failed while five succeeded -- a partial, non-deterministic failure that
// reads like flaky SQL rather than like a missing lock.
//
// NON-VACUITY: with `engineMu` removed this fails, and it fails the way the
// cell did -- some goroutines through, some refused. Checked, not assumed.
func TestConcurrentStatementsDoNotCollideOnTheFileLock(t *testing.T) {
	if _, err := exec.LookPath("duckdb"); err != nil {
		t.Skip("duckdb not on PATH")
	}
	db := filepath.Join(t.TempDir(), "warehouse.duckdb")

	// Seed it, so every goroutine below opens an EXISTING file -- which is the
	// shape that locks. A create-on-open race would be a different bug.
	if _, err := run(db, "CREATE TABLE t (n INTEGER); INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const workers = 8 // one per silver model, which is where this was found
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = run(db, "SELECT count(*) FROM t")
		}(i)
	}
	wg.Wait()

	var refused []string
	for i, err := range errs {
		if err == nil {
			continue
		}
		refused = append(refused, err.Error())
		if strings.Contains(err.Error(), "Conflicting lock") {
			t.Errorf("worker %d was refused for a lock the caller cannot see: %v", i, err)
		}
	}
	if len(refused) > 0 {
		t.Fatalf("%d of %d concurrent statements failed:\n  %s",
			len(refused), workers, strings.Join(refused, "\n  "))
	}
	_ = os.Remove(db)
}
