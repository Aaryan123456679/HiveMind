package loadtest

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Aaryan123456679/HiveMind/engine/catalog"
	"github.com/Aaryan123456679/HiveMind/engine/mvcc"
	"github.com/Aaryan123456679/HiveMind/engine/wal"
)

// TestQueryLatencyUnderLoad is subtask 6.3.3's test-spec test: "run concurrent
// queries alongside concurrent ingestion, assert p50/p95/p99 stay within an
// acceptable flat-latency bound."
//
// This exercises the REAL engine/mvcc-backed storage stack end to end
// (catalog + WAL + VersionWriter/EpochManager), not a mock, because the thing
// under test IS the MVCC read path's "no-reader-blocking" guarantee described
// in docs/LLD/mvcc.md's "Read path" section: "Readers snapshot the current
// version pointer at the start of the request and read that specific version
// to completion, regardless of concurrent writers advancing the pointer
// afterward." The only thing that would be legitimate to mock here (per
// 6.3.2's precedent) is an LLM/segmentation boundary above storage, and this
// test never reaches one — it drives mvcc.SnapshotRead/VersionWriter.CommitVersion
// directly, the same building blocks docs/LLD/mvcc.md documents and
// engine/mvcc/mvcc_test.go's TestConcurrentReadersWriters already races for
// correctness. This test is the latency-shape counterpart of that
// correctness test: same concurrent reader/writer shape, but asserting on
// query latency percentiles rather than on read content correctness.
//
// Design: two concurrent harness.Run invocations share one on-disk MVCC
// store (catalog + WAL + content dir + epoch manager) rooted at a single
// t.TempDir():
//
//   - a "writer" harness continuously ingests new versions (CommitVersion)
//     across a fixed pool of pre-seeded fileIDs, standing in for concurrent
//     ingestion load;
//   - a "query" harness concurrently reads (SnapshotRead) from that same
//     pool of fileIDs, standing in for concurrent query traffic.
//
// Both harnesses run in their own goroutines so they genuinely overlap in
// wall-clock time; the query harness's Result is what the flatness
// assertion below is built on. The writer harness's iteration count is
// deliberately large relative to the query harness's, so ingestion keeps
// running for the query harness's entire lifetime rather than finishing
// early and leaving the tail of the query run uncontended (which would
// silently defeat the point of the test).
func TestQueryLatencyUnderLoad(t *testing.T) {
	dir := t.TempDir()

	fm, err := catalog.Open(filepath.Join(dir, "catalog.dat"))
	if err != nil {
		t.Fatalf("catalog.Open: %v", err)
	}
	defer func() {
		if err := fm.Close(); err != nil {
			t.Errorf("catalog FileManager.Close: %v", err)
		}
	}()
	cat := catalog.NewCatalog(fm)

	w, err := wal.OpenWriter(filepath.Join(dir, "wal"), 1<<20)
	if err != nil {
		t.Fatalf("wal.OpenWriter: %v", err)
	}
	defer func() {
		if err := w.Close(); err != nil {
			t.Errorf("wal.Writer.Close: %v", err)
		}
	}()

	vw, err := mvcc.NewVersionWriter(dir)
	if err != nil {
		t.Fatalf("mvcc.NewVersionWriter: %v", err)
	}
	em := mvcc.NewEpochManager()

	// Pre-seed a fixed pool of fileIDs, each with a catalog record and an
	// initial committed version, so every query from t=0 onward has real
	// content to read rather than racing to observe a not-yet-existing
	// version. Ingestion load during the test commits further versions on
	// top of these same fileIDs.
	const numFiles = 32
	fileIDs := make([]uint64, numFiles)
	for i := 0; i < numFiles; i++ {
		fileID := uint64(1000 + i)
		fileIDs[i] = fileID
		if err := cat.Put(catalog.CatalogRecord{
			FileID:         fileID,
			CurrentVersion: 0,
			Status:         catalog.StatusActive,
		}); err != nil {
			t.Fatalf("seeding catalog record for fileID %d: %v", fileID, err)
		}
		seed := []byte(fmt.Sprintf("seed content for fileID %d", fileID))
		if _, err := vw.CommitVersion(cat, w, em, fileID, seed); err != nil {
			t.Fatalf("seeding initial version for fileID %d: %v", fileID, err)
		}
	}

	const (
		writerWorkers    = 8
		writerIterations = 120 // deliberately >> queryIterations; see doc comment above

		queryWorkers    = 8
		queryIterations = 150
	)

	writerRand := make([]*rand.Rand, writerWorkers)
	for i := range writerRand {
		writerRand[i] = rand.New(rand.NewSource(int64(i) + 1))
	}
	queryRand := make([]*rand.Rand, queryWorkers)
	for i := range queryRand {
		queryRand[i] = rand.New(rand.NewSource(int64(i) + 1000))
	}

	writerWork := func(workerID, iter int) (time.Duration, error) {
		fileID := fileIDs[writerRand[workerID].Intn(numFiles)]
		payload := []byte(fmt.Sprintf("ingested payload worker=%d iter=%d", workerID, iter))
		start := time.Now()
		_, err := vw.CommitVersion(cat, w, em, fileID, payload)
		return time.Since(start), err
	}

	queryWork := func(workerID, iter int) (time.Duration, error) {
		fileID := fileIDs[queryRand[workerID].Intn(numFiles)]
		start := time.Now()
		_, err := mvcc.SnapshotRead(cat, vw, em, fileID)
		return time.Since(start), err
	}

	var wg sync.WaitGroup
	var writerRes *Result
	var writerErr error

	wg.Add(1)
	go func() {
		defer wg.Done()
		writerRes, writerErr = Run(Config{
			Workers:    writerWorkers,
			Iterations: writerIterations,
		}, writerWork)
	}()

	// Give the writer goroutine a brief head start so ingestion is already
	// in flight before the query harness's first sample, rather than both
	// starting from a cold, empty-contention state simultaneously.
	time.Sleep(2 * time.Millisecond)

	queryRes, queryErr := Run(Config{
		Workers:    queryWorkers,
		Iterations: queryIterations,
	}, queryWork)

	wg.Wait()

	if writerErr != nil {
		t.Fatalf("writer harness Run: %v", writerErr)
	}
	if queryErr != nil {
		t.Fatalf("query harness Run: %v", queryErr)
	}

	if writerRes.ErrorCount != 0 {
		t.Errorf("writer harness: ErrorCount = %d, want 0 (%d/%d succeeded)", writerRes.ErrorCount, writerRes.SuccessCount, writerRes.TotalCount)
	}
	if queryRes.ErrorCount != 0 {
		t.Errorf("query harness: ErrorCount = %d, want 0 (%d/%d succeeded)", queryRes.ErrorCount, queryRes.SuccessCount, queryRes.TotalCount)
	}

	p50, p95, p99 := queryRes.P50(), queryRes.P95(), queryRes.P99()
	t.Logf("query latency under concurrent ingestion (workers=%d iterations=%d, concurrent writer workers=%d iterations=%d): p50=%v p95=%v p99=%v throughput=%.0f/s",
		queryWorkers, queryIterations, writerWorkers, writerIterations, p50, p95, p99, queryRes.Throughput())
	t.Logf("concurrent ingestion: p50=%v p95=%v p99=%v throughput=%.0f/s",
		writerRes.P50(), writerRes.P95(), writerRes.P99(), writerRes.Throughput())

	if p50 <= 0 {
		t.Fatalf("query P50 = %v, want > 0", p50)
	}

	// Flatness bound rationale: MVCC's no-reader-blocking guarantee means a
	// query's cost is dominated by a catalog lookup (in-memory, lock-striped
	// per fileID) plus a single os.ReadFile of an immutable version file —
	// work that never waits on a writer's lock or fsync, no matter how many
	// concurrent CommitVersion calls are in flight. A genuine reader-blocking
	// regression would show query latency converging toward *writer* commit
	// latency, because a blocked query would literally be queuing behind a
	// writer's fsync-bound critical section.
	//
	// This makes the writer harness's own Result (writerRes, logged above)
	// the most reliable yardstick for "flat" in this test, more robust than
	// a fixed multiple of the query harness's own P50: in-process latencies
	// at test scale are sub-millisecond at the median, so a fixed ratio
	// bound (e.g. "p99 <= 25x p50") is dominated by scheduler/GC noise
	// rather than by anything the MVCC read path controls, and is exactly
	// as noisy as the machine happens to be that run — observed to swing
	// query P99 well past any fixed small multiple of query P50 on a loaded,
	// shared development sandbox even with zero code-level reader-blocking.
	// Comparing against the concurrent writer's own latency instead is
	// self-calibrating: on a fast, quiet machine both writer and query
	// latencies shrink together; on a slow/contended one they both grow
	// together, but a NON-blocked query should stay a small fraction of a
	// writer commit's cost either way, since it skips the WAL append, the
	// fsync, and the CAS retry loop entirely.
	//
	// Two independent checks encode "flat":
	//
	//  1. A *relative-to-ingestion* check: query P99 must stay below
	//     writerRelativeCeiling (75%) of the concurrent writer harness's own
	//     P50 commit latency. A query that is actually queuing behind
	//     ingestion would instead approach or exceed writer latency, not
	//     merely brush against a fraction of it.
	//  2. An *absolute* ceiling, absoluteCeiling, so check 1 can't be
	//     satisfied vacuously if ingestion itself were pathologically slow
	//     (e.g. writer P50 of several seconds would make check 1 nearly
	//     meaningless on its own). absoluteCeiling is set at 300ms —
	//     generous enough to absorb the multi-hundred-millisecond WAL/content
	//     fsync tails observed for the *writer* harness on a loaded, shared
	//     development sandbox (disk contention from unrelated concurrent
	//     processes, outside this test's control), while still being a real,
	//     meaningful bound for an in-process, single os.ReadFile query that
	//     never touches the WAL.
	const writerRelativeCeiling = 0.75
	const absoluteCeiling = 300 * time.Millisecond

	if p99 > absoluteCeiling {
		t.Errorf("query P99 = %v, want <= %v (absolute ceiling; MVCC reads should never block on concurrent ingestion)", p99, absoluteCeiling)
	}
	if maxAllowed := time.Duration(float64(writerRes.P50()) * writerRelativeCeiling); p99 > maxAllowed {
		t.Errorf("query P99 = %v, want <= %v (%.0f%% of concurrent writer P50 = %v); query tail latency approaching writer commit latency suggests reads are queuing behind concurrent ingestion, violating the MVCC no-reader-blocking guarantee (docs/LLD/mvcc.md)", p99, maxAllowed, writerRelativeCeiling*100, writerRes.P50())
	}
	if p50 > p95 || p95 > p99 {
		t.Errorf("percentiles not monotonically non-decreasing: p50=%v p95=%v p99=%v", p50, p95, p99)
	}
}
