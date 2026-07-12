package loadtest

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Aaryan123456679/HiveMind/engine/catalog"
	"github.com/Aaryan123456679/HiveMind/engine/rpc"
	hivemindv1 "github.com/Aaryan123456679/HiveMind/engine/rpc/gen"
	"github.com/Aaryan123456679/HiveMind/engine/wal"
)

// This file is subtask 6.3.2's (GitHub issue #32) concurrent ingestion throughput
// benchmark: "does_engine/loadtest/'s BenchmarkIngestionThroughput actually exercise
// the real storage engine, not a toy stand-in?" per docs/LLD/eval.md's
// "engine/loadtest/" section:
//
//	"Custom load-generation harness ... used for: Concurrent ingestion throughput
//	benchmarks (testing.B, LLM calls mocked to isolate the storage engine itself)."
//
// Design: every ingestion call in this file goes through *rpc.Server.PutSegment --
// the SAME production RPC handler engine/rpc/server.go implements for the real
// ingestion path (see its doc comment: "writes a segment produced by the ingestion
// segmentation agent into a topic file"), which itself drives the real
// engine/catalog.Catalog + engine/catalog.ContentStore + engine/wal.Writer stack
// (IDAllocator.Next, WAL-before-apply CatalogRecord Put, on-disk content file
// write/rename). Nothing about the storage path is reimplemented or stubbed here.
//
// The ONLY thing mocked is the LLM/segmentation call boundary that, in production,
// sits upstream of PutSegment: the agent-side step that turns raw source material
// into a markdown segment (PutSegmentRequest.content) is not part of engine/rpc or
// engine/catalog at all (it lives in agents/, out of this Go module's scope), so
// "mocking" it here just means synthesizing that segment content in-process
// (mockSegment below) instead of shelling out to a real LLM call, exactly matching
// eval.md's "LLM calls mocked to isolate the storage engine itself" framing. This
// keeps the benchmark's concurrency/allocation/throughput numbers a genuine
// reflection of the storage engine's own behavior under concurrent ingestion,
// unpolluted by LLM latency variance or network I/O.
//
// Per this repo's engine/loadtest.WorkFunc contract, concurrency is driven by
// [Run] (engine/loadtest/harness.go, subtask 6.3.1, already merged) rather than a
// hand-rolled goroutine/WaitGroup/channel loop, so this benchmark reuses the exact
// same Config/WorkFunc/Result surface every other load test in this package will
// use (concurrent query latency, auto-split race correctness). b.N is split across
// a fixed worker pool the same way testing.B's own b.RunParallel would (see
// iterationsPerWorker below), so ns/op still means "amortized per-op wall time
// under bench-decided concurrency" -- the number go test's -bench output reports
// by convention -- while [Result] additionally supplies real percentile/throughput
// data (P50/P95/P99, Throughput()) that testing.B has no built-in concept of, and
// which this benchmark surfaces via b.ReportMetric.

// ingestionBenchWorkers is the fixed number of concurrent ingestion goroutines this
// benchmark drives. Chosen to comfortably oversubscribe typical CI/dev-machine core
// counts (matching engine/catalog/catalog_bench_test.go's benchParallelism=32
// convention for the same reason: surface lock-contention/queuing effects without
// requiring an unusually high core count) while staying well under
// catalog.numStripes/ContentStore's own striped-lock width, so the benchmark
// measures real concurrent-ingestion throughput rather than being artificially
// capped by an oversized worker pool relative to the storage engine's own
// concurrency granularity.
const ingestionBenchWorkers = 32

// ingestionBenchAppendFileCount is the number of pre-seeded topic files the
// "AppendExisting" sub-benchmark's workers append to concurrently, round-robin.
// Comparable in spirit to catalog_bench_test.go's benchNumFileIDs, scaled down
// because this sub-benchmark performs real disk I/O (WAL fsync + content
// read-modify-write) per op rather than an in-memory map operation, so a huge
// fileID space would mostly just grow setup time rather than better exercise
// concurrency (ContentStore's per-fileID stripe is what serializes contention, and
// ingestionBenchAppendFileCount already exceeds ingestionBenchWorkers many times
// over, so multiple workers do genuinely contend on the same fileID's stripe --
// exactly the real-world "many segments landing in the same topic file" ingestion
// shape).
const ingestionBenchAppendFileCount = 256

// iterationsPerWorker splits b.N total operations across a fixed number of
// workers, mirroring how testing.B's own b.RunParallel divides b.N across
// goroutines (see its doc comment: "the total number of calls is b.N"). Guarantees
// at least 1 iteration per worker so a small -benchtime run still exercises every
// worker at least once.
func iterationsPerWorker(totalOps, workers int) int {
	if workers <= 0 {
		workers = 1
	}
	iterations := totalOps / workers
	if iterations < 1 {
		iterations = 1
	}
	return iterations
}

// mockSegment synthesizes deterministic, realistic-shaped markdown segment
// content standing in for what an LLM segmentation call would have produced from
// raw source material -- this is the ONLY mocked boundary in this benchmark (see
// this file's package-level doc comment). It intentionally produces a real
// []byte of a size comparable to an actual ingested segment (a short markdown
// section with a header and a few sentences, a few hundred bytes), rather than a
// trivial fixed constant, so PutSegment's real WAL-record encoding, content-file
// write, and B+Tree/catalog Put paths all do genuine, size-representative I/O
// during the benchmark -- not an unrealistically tiny payload that would make
// per-op overhead look artificially dominated by fixed costs (fsync, syscalls)
// rather than payload-proportional costs.
func mockSegment(workerID, iter int) []byte {
	return []byte(fmt.Sprintf(
		"# Segment %d.%d\n\nThis section was produced by a mocked LLM segmentation "+
			"call standing in for the real ingestion-agent boundary (see subtask "+
			"6.3.2's doc comment in ingestion_bench_test.go): worker %d, iteration %d. "+
			"It exists purely to give the storage engine a realistically-sized "+
			"payload to persist -- WAL record, content file bytes, and catalog "+
			"record -- during this concurrent-ingestion throughput benchmark, "+
			"without incurring any real LLM network latency or nondeterminism.\n\n"+
			"## Details\n\nLorem ipsum dolor sit amet, consectetur adipiscing elit. "+
			"Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua.\n",
		workerID, iter, workerID, iter,
	))
}

// newIngestionBenchServer wires up a real, disk-backed *rpc.Server -- the exact
// same production dependency graph engine/rpc/server_test.go's newFixture builds
// for PutSegment's own correctness tests (catalog.Catalog + catalog.ContentStore +
// catalog.IDAllocator + wal.Writer) -- under a fresh b.TempDir(), for
// BenchmarkIngestionThroughput to drive concurrently. pathIndex/graph/edgeLog/
// entityIndex are all left nil: NewServer documents all four as optional
// (PutSegment only touches pathIndex, and only when the caller supplies a
// non-empty PutSegmentRequest.path, which this benchmark never does), so omitting
// them keeps this benchmark's setup focused on exactly the dependencies
// PutSegment's create/append paths actually exercise -- catalog, content, WAL --
// matching this subtask's "isolating storage-engine performance" acceptance
// criterion instead of also paying B+Tree/graph setup and lock overhead that
// concurrent ingestion throughput, as such, does not depend on.
func newIngestionBenchServer(b *testing.B) *rpc.Server {
	b.Helper()
	root := b.TempDir()

	fm, err := catalog.Open(filepath.Join(root, "catalog.dat"))
	if err != nil {
		b.Fatalf("catalog.Open: %v", err)
	}
	b.Cleanup(func() {
		if err := fm.Close(); err != nil {
			b.Errorf("FileManager.Close: %v", err)
		}
	})

	cat := catalog.NewCatalog(fm)

	idAlloc, err := catalog.NewIDAllocator(fm)
	if err != nil {
		b.Fatalf("catalog.NewIDAllocator: %v", err)
	}
	b.Cleanup(func() {
		if err := idAlloc.Close(); err != nil {
			b.Errorf("IDAllocator.Close: %v", err)
		}
	})

	w, err := wal.OpenWriter(filepath.Join(root, "wal"), 1<<20)
	if err != nil {
		b.Fatalf("wal.OpenWriter: %v", err)
	}
	b.Cleanup(func() {
		if err := w.Close(); err != nil {
			b.Errorf("wal.Writer.Close: %v", err)
		}
	})

	cs, err := catalog.OpenContentStore(root, cat, w)
	if err != nil {
		b.Fatalf("catalog.OpenContentStore: %v", err)
	}

	srv, err := rpc.NewServer(cat, cs, idAlloc, nil, nil, nil, nil)
	if err != nil {
		b.Fatalf("rpc.NewServer: %v", err)
	}
	return srv
}

// reportIngestionMetrics attaches [Result]'s throughput/latency-percentile data
// (which testing.B itself has no concept of) to b's standard -bench output via
// b.ReportMetric, alongside the ns/op, B/op, and allocs/op that b.ReportAllocs
// plus b's own timer already provide from the timed region this Result was
// collected in. Fails the benchmark if any ingestion call reported an error --
// a concurrent-ingestion throughput number computed over partially-failed calls
// would silently overstate real throughput.
func reportIngestionMetrics(b *testing.B, res *Result) {
	b.Helper()
	if res.ErrorCount != 0 {
		b.Fatalf("ingestion benchmark: %d/%d calls returned an error (want 0)", res.ErrorCount, res.TotalCount)
	}
	b.ReportMetric(res.Throughput(), "ingests/sec")
	b.ReportMetric(float64(res.P50().Nanoseconds()), "p50-ns")
	b.ReportMetric(float64(res.P95().Nanoseconds()), "p95-ns")
	b.ReportMetric(float64(res.P99().Nanoseconds()), "p99-ns")
}

// BenchmarkIngestionThroughput is subtask 6.3.2's (GitHub issue #32) test-spec
// benchmark:
//
//	go test ./engine/loadtest/... -bench BenchmarkIngestionThroughput -benchmem
//
// It measures concurrent ingestion throughput through the real storage engine
// (via *rpc.Server.PutSegment, see this file's package-level doc comment) with the
// LLM/segmentation call mocked out (mockSegment), isolating storage-engine
// performance from LLM latency/cost. Two sub-benchmarks cover the two ingestion
// shapes PutSegment itself distinguishes (per its own doc comment: "file_id == 0
// means create a new file ... file_id != 0 means append to the existing file"):
//
//   - Create: every concurrent call ingests a brand-new topic file (heavy on
//     IDAllocator.Next + WAL-before-apply CatalogRecord Put + fresh content-file
//     creation) -- the "many new documents landing concurrently" shape.
//   - AppendExisting: every concurrent call appends to one of a fixed pool of
//     pre-seeded topic files, round-robin (heavy on ContentStore's per-fileID
//     striped-lock read-modify-write path, including genuine same-fileID
//     contention once concurrent workers wrap around the pool) -- the "many
//     segments landing in existing topic files" shape.
func BenchmarkIngestionThroughput(b *testing.B) {
	b.Run("Create", func(b *testing.B) {
		srv := newIngestionBenchServer(b)
		ctx := context.Background()

		cfg := Config{
			Workers:    ingestionBenchWorkers,
			Iterations: iterationsPerWorker(b.N, ingestionBenchWorkers),
		}

		work := func(workerID, iter int) (time.Duration, error) {
			content := mockSegment(workerID, iter)
			start := time.Now()
			_, err := srv.PutSegment(ctx, &hivemindv1.PutSegmentRequest{
				FileId:  catalog.InvalidFileID, // 0: create a new topic file.
				Content: content,
			})
			return time.Since(start), err
		}

		b.ReportAllocs()
		b.ResetTimer()
		res, err := Run(cfg, work)
		b.StopTimer()
		if err != nil {
			b.Fatalf("Run: %v", err)
		}

		reportIngestionMetrics(b, res)
	})

	b.Run("AppendExisting", func(b *testing.B) {
		srv := newIngestionBenchServer(b)
		ctx := context.Background()

		// Seed ingestionBenchAppendFileCount topic files up front (outside the
		// timed region below) via the SAME real PutSegment create path, each with
		// a small initial segment, so the timed AppendExisting calls measure only
		// the append path's own concurrent throughput.
		fileIDs := make([]uint64, ingestionBenchAppendFileCount)
		for i := range fileIDs {
			resp, err := srv.PutSegment(ctx, &hivemindv1.PutSegmentRequest{
				FileId:  catalog.InvalidFileID,
				Content: mockSegment(-1, i),
			})
			if err != nil {
				b.Fatalf("seeding file %d: %v", i, err)
			}
			fileIDs[i] = resp.GetFileId()
		}

		cfg := Config{
			Workers:    ingestionBenchWorkers,
			Iterations: iterationsPerWorker(b.N, ingestionBenchWorkers),
		}

		work := func(workerID, iter int) (time.Duration, error) {
			// Spread appends round-robin across the pre-seeded pool rather than
			// pinning each worker to its own fileID, so the benchmark's
			// concurrency genuinely exercises ContentStore's striped per-fileID
			// locking (workers legitimately contend once the pool wraps around),
			// the same "no goroutine statically pinned to a fixed subset" shape
			// catalog_bench_test.go's BenchmarkStripedVsGlobalLock documents for
			// an analogous reason.
			fileID := fileIDs[(workerID*iterationsPerWorker(b.N, ingestionBenchWorkers)+iter)%len(fileIDs)]
			content := mockSegment(workerID, iter)
			start := time.Now()
			_, err := srv.PutSegment(ctx, &hivemindv1.PutSegmentRequest{
				FileId:  fileID,
				Content: content,
			})
			return time.Since(start), err
		}

		b.ReportAllocs()
		b.ResetTimer()
		res, err := Run(cfg, work)
		b.StopTimer()
		if err != nil {
			b.Fatalf("Run: %v", err)
		}

		reportIngestionMetrics(b, res)
	})
}
