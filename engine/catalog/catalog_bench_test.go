package catalog

import (
	"sync"
	"sync/atomic"
	"testing"
)

// benchNumFileIDs is the number of distinct fileIDs the benchmark hammers
// concurrently. It is comparable in order of magnitude to numFileIDsStress
// (2a.3.1's TestStripedConcurrencyStress) and, being well over numStripes (256),
// spreads load across every stripe.
const benchNumFileIDs = 4096

// benchRecordFor builds a minimal, valid CatalogRecord for fileID, suitable for
// repeated Put calls in a benchmark loop (mirrors recordForVersion in
// catalog_test.go, without the version-tracking machinery that test needs but this
// benchmark does not).
func benchRecordFor(fileID uint64) CatalogRecord {
	return CatalogRecord{
		FileID:         fileID,
		PathHash:       fileID * 31,
		CurrentVersion: 1,
		SizeBytes:      1024,
		Status:         StatusActive,
		ParentTopicID:  0,
		LastModified:   1234567890,
	}
}

// --- Benchmark-only baseline/comparison harness -----------------------------
//
// Both stripedRecordStore and globalLockRecordStore below are deliberately
// minimal, benchmark-only constructs (never referenced by production code) that
// isolate ONE variable: per-fileID lock granularity. Neither goes through
// Catalog's real FileManager/Page-backed disk I/O; an earlier iteration of this
// benchmark wrapped the real, disk-backed Catalog (via FileManager) for both
// variants, but the per-op cost was then dominated by pread/pwrite syscalls
// (~1-2 microseconds each on this platform) which swamped the sub-100ns
// difference in lock acquisition/contention overhead this benchmark exists to
// surface -- both variants measured statistically indistinguishable (within
// noise) regardless of concurrency, because the bottleneck was disk I/O, not
// locking. This is exactly the "does not need to be a full production-quality
// alternate implementation, just a benchmarking harness faithful enough to
// produce a meaningful comparison" latitude for constructing the baseline: both
// variants here store the SAME real CatalogRecord.Encode()-produced bytes in an
// in-memory map, reuse the SAME stripeFor(fileID) hash Catalog itself uses (see
// catalog.go), and differ ONLY in whether that map access is guarded by 256
// per-stripe mutexes (Striped, mirroring Catalog's actual stripes design) or one
// mutex shared across every fileID (GlobalLock, the naive baseline). This keeps
// the comparison fair while making the locking-strategy effect the dominant,
// measurable signal.

// stripedRecordStore mirrors Catalog's striped-mutex design: numStripes
// independent shards, each an unexported map guarded by its own mutex and keyed
// by the SAME stripeFor(fileID) function Catalog uses, so operations on fileIDs
// that hash to different stripes never contend with each other.
type stripedRecordStore struct {
	stripes [numStripes]sync.Mutex
	shards  [numStripes]map[uint64][]byte
}

func newStripedRecordStore() *stripedRecordStore {
	s := &stripedRecordStore{}
	for i := range s.shards {
		s.shards[i] = make(map[uint64][]byte)
	}
	return s
}

func (s *stripedRecordStore) Put(rec CatalogRecord) error {
	data, err := rec.Encode()
	if err != nil {
		return err
	}
	idx := stripeFor(rec.FileID)
	s.stripes[idx].Lock()
	defer s.stripes[idx].Unlock()
	s.shards[idx][rec.FileID] = data
	return nil
}

func (s *stripedRecordStore) Get(fileID uint64) (CatalogRecord, error) {
	idx := stripeFor(fileID)
	s.stripes[idx].Lock()
	defer s.stripes[idx].Unlock()
	data, ok := s.shards[idx][fileID]
	if !ok {
		return CatalogRecord{}, ErrNotFound
	}
	return Decode(data)
}

// globalLockRecordStore is the naive single-global-lock baseline: ONE mutex
// guards ONE shared map, regardless of which fileID is being operated on. Every
// concurrent Put/Get across every fileID serializes behind this single lock.
type globalLockRecordStore struct {
	mu sync.Mutex
	m  map[uint64][]byte
}

func newGlobalLockRecordStore() *globalLockRecordStore {
	return &globalLockRecordStore{m: make(map[uint64][]byte)}
}

func (g *globalLockRecordStore) Put(rec CatalogRecord) error {
	data, err := rec.Encode()
	if err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.m[rec.FileID] = data
	return nil
}

func (g *globalLockRecordStore) Get(fileID uint64) (CatalogRecord, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	data, ok := g.m[fileID]
	if !ok {
		return CatalogRecord{}, ErrNotFound
	}
	return Decode(data)
}

// benchParallelism oversubscribes goroutines relative to GOMAXPROCS so that lock
// contention/queuing actually manifests under -cpu values matching typical CI
// hardware, rather than requiring an unusually high core count to see the effect.
const benchParallelism = 32

// BenchmarkStripedVsGlobalLock compares a striped-mutex design (matching
// Catalog's actual stripeFor(fileID)-keyed locking granularity) against a naive
// single-global-lock baseline under many-fileID concurrent contention, per
// subtask 2a.3.2's acceptance criteria. Run with:
//
//	go test ./engine/catalog/... -bench BenchmarkStripedVsGlobalLock -benchmem -run ^$
//
// Both sub-benchmarks drive the identical workload shape (b.RunParallel issuing
// Put+Get pairs spread across benchNumFileIDs distinct fileIDs via a shared
// atomic counter, so no goroutine is statically pinned to a fixed subset of
// fileIDs) against otherwise-identical storage (see the harness doc comment
// above); the only variable under test is the locking strategy. Under
// concurrent contention, "GlobalLock" is expected to show meaningfully higher
// ns/op than "Striped", since GlobalLock serializes every fileID behind one lock
// while Striped only serializes operations that happen to hash to the same
// stripe.
func BenchmarkStripedVsGlobalLock(b *testing.B) {
	b.Run("Striped", func(b *testing.B) {
		store := newStripedRecordStore()
		for fileID := uint64(1); fileID <= benchNumFileIDs; fileID++ {
			if err := store.Put(benchRecordFor(fileID)); err != nil {
				b.Fatalf("seed Put(%d): %v", fileID, err)
			}
		}
		var counter atomic.Uint64

		b.ReportAllocs()
		b.SetParallelism(benchParallelism)
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				fileID := counter.Add(1)%benchNumFileIDs + 1
				rec := benchRecordFor(fileID)
				if err := store.Put(rec); err != nil {
					b.Fatalf("Put(%d): %v", fileID, err)
				}
				if _, err := store.Get(fileID); err != nil {
					b.Fatalf("Get(%d): %v", fileID, err)
				}
			}
		})
	})

	b.Run("GlobalLock", func(b *testing.B) {
		store := newGlobalLockRecordStore()
		for fileID := uint64(1); fileID <= benchNumFileIDs; fileID++ {
			if err := store.Put(benchRecordFor(fileID)); err != nil {
				b.Fatalf("seed Put(%d): %v", fileID, err)
			}
		}
		var counter atomic.Uint64

		b.ReportAllocs()
		b.SetParallelism(benchParallelism)
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				fileID := counter.Add(1)%benchNumFileIDs + 1
				rec := benchRecordFor(fileID)
				if err := store.Put(rec); err != nil {
					b.Fatalf("Put(%d): %v", fileID, err)
				}
				if _, err := store.Get(fileID); err != nil {
					b.Fatalf("Get(%d): %v", fileID, err)
				}
			}
		})
	})
}
