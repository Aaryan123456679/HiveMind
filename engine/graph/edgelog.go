// Package graph (this file): subtask 3.1.2's append-only, per-node edge log writer.
//
// This is a distinct mechanism from both existing files in this package:
//
//   - edge_append.go's EdgeAppender (task-2b.3.4, issue #12) is a single, shared
//     append-only log rooted at one directory, used narrowly by engine/split/execute.go
//     to durably record SPLIT_SIBLING/REDIRECT edges as part of the atomic split-commit
//     WAL transaction. Its crash-recovery-replay gap was resolved by task-2b.3.6 (see
//     .cdr/memory/pending.md); this subtask does not touch or supersede it.
//   - csr.go's CSRGraph/WriteCSR/LoadCSR (task-3.1.1) is a whole-snapshot, read-optimized
//     format for graph.dat, only ever rewritten wholesale.
//
// EdgeLog is the general, durable landing zone for newly discovered edges of *any* type
// (ENTITY_COOCCUR with weight, LLM_ASSERTED, and future split edges), organized as one
// append-only log per source fileID rather than one shared array/log. This is what lets
// concurrent writers touching different fileIDs (e.g. two ingestion workers processing
// different files at once) proceed without contending on a shared lock: each source
// fileID gets its own engine/wal.Writer instance, and wal.Writer already guards its own
// state with its own internal mutex. A later subtask (3.1.3) periodically compacts the
// accumulated per-node log entries into csr.go's CSR array, merging/weight-incrementing
// as needed; that compaction step is out of scope here. Edge-type creation/validation
// support beyond rejecting the invalid zero-value sentinel is subtask 3.1.4's job
// (engine/graph/edge.go) - this file only persists and reads back whatever CSREdge values
// it is given, reusing that type verbatim since it already has exactly the entry shape
// docs/LLD/graph.md specifies ({targetFileID, edgeType, weight, lastUpdated}).
//
// On-disk layout: <root>/<sourceFileID>/wal-<N>.log, one such directory per source
// fileID that has ever had an edge appended, using engine/wal's own segment-rotation and
// naming convention (mirrors edge_append.go's use of the same primitive, but with N
// per-node directories instead of one shared directory).
package graph

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/Aaryan123456679/HiveMind/engine/wal"
)

// EdgeLog manages a collection of per-source-fileID append-only edge logs rooted at a
// single base directory. Each source fileID gets its own subdirectory and its own
// engine/wal.Writer instance, so concurrent AppendEdge calls for different fileIDs never
// contend on a shared lock. AppendEdge calls for the *same* fileID are serialized by that
// fileID's own wal.Writer (matching "per-node log" semantics: a single node's log is
// still an ordered, single-writer-at-a-time append log).
//
// EdgeLog is safe for concurrent use by multiple goroutines.
type EdgeLog struct {
	root string

	mu      sync.RWMutex
	writers map[uint64]*wal.Writer
}

// OpenEdgeLog opens (creating if necessary) an EdgeLog rooted at root. Per-node
// subdirectories and their underlying wal.Writer instances are opened lazily, on first
// AppendEdge/ReadNode call for that fileID, rather than eagerly enumerating every
// pre-existing per-node subdirectory - this keeps OpenEdgeLog itself O(1) regardless of
// how many distinct fileIDs already have logs under root.
func OpenEdgeLog(root string) (*EdgeLog, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("graph: creating edge log root %s: %w", root, err)
	}
	return &EdgeLog{
		root:    root,
		writers: make(map[uint64]*wal.Writer),
	}, nil
}

// nodeDir returns the per-node subdirectory path for sourceFileID's edge log.
func (l *EdgeLog) nodeDir(sourceFileID uint64) string {
	return filepath.Join(l.root, strconv.FormatUint(sourceFileID, 10))
}

// getOrOpenWriter returns the wal.Writer for sourceFileID's per-node log, opening it (and
// its subdirectory) on first use. The common case - the writer already exists - only
// takes a brief RLock, so distinct, already-opened per-node logs do not contend with each
// other on this manager-level lock either.
func (l *EdgeLog) getOrOpenWriter(sourceFileID uint64) (*wal.Writer, error) {
	l.mu.RLock()
	w, ok := l.writers[sourceFileID]
	l.mu.RUnlock()
	if ok {
		return w, nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	// Re-check under the write lock: another goroutine may have opened this fileID's
	// writer between our RUnlock above and taking the write lock here.
	if w, ok := l.writers[sourceFileID]; ok {
		return w, nil
	}

	dir := l.nodeDir(sourceFileID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("graph: creating edge log node dir %s: %w", dir, err)
	}
	w, err := wal.OpenWriter(dir, defaultMaxSegmentBytes)
	if err != nil {
		return nil, fmt.Errorf("graph: opening per-node edge log at %s: %w", dir, err)
	}
	l.writers[sourceFileID] = w
	return w, nil
}

// AppendEdge durably appends edge to sourceFileID's own per-node log, fsyncing before it
// returns (the same durability guarantee wal.Writer already provides catalog/btree
// mutations and edge_append.go's EdgeAppender). It returns an error if edge.Type is the
// EdgeTypeInvalid zero-value sentinel; no other type validation is performed here (that
// is subtask 3.1.4's job - see this file's package doc comment).
func (l *EdgeLog) AppendEdge(sourceFileID uint64, edge CSREdge) error {
	if edge.Type == EdgeTypeInvalid {
		return fmt.Errorf("graph: cannot append edge with invalid type %v", edge.Type)
	}
	w, err := l.getOrOpenWriter(sourceFileID)
	if err != nil {
		return err
	}
	buf := make([]byte, csrEdgeEncodedSize)
	edge.encode(buf)
	if _, err := w.Append(buf); err != nil {
		return fmt.Errorf("graph: appending edge to node %d's log: %w", sourceFileID, err)
	}
	return nil
}

// ReadNode reads back every edge previously durably appended to sourceFileID's per-node
// log (across all its segment files, in on-disk append order). It returns a nil slice,
// nil error if sourceFileID has no log yet (never had an edge appended). ReadNode is not
// a general query API: no filtering, indexing, or cross-node lookup is provided (that is
// deferred to compaction, 3.1.3, and the traversal API, 3.1.5).
func (l *EdgeLog) ReadNode(sourceFileID uint64) ([]CSREdge, error) {
	edges, _, err := l.ReadNodeAfter(sourceFileID, -1)
	return edges, err
}

// ReadNodeAfter reads back every edge durably appended to sourceFileID's per-node log
// that lives in a segment file numbered strictly greater than afterSeg (pass afterSeg
// == -1 to read everything, which is what ReadNode does). It also returns maxSeg, the
// highest segment number currently present on disk for sourceFileID across every
// segment seen (including ones skipped because they were <= afterSeg); maxSeg is
// afterSeg unchanged if sourceFileID currently has no segments on disk at all.
//
// This exists for compact.go's retry-idempotency fix: a segment numbered <= afterSeg
// is one compact.go has already durably folded into graph.dat on a prior compaction
// run (per its compact-state sidecar) but failed to truncate afterwards - skipping it
// here is what stops that already-durably-merged edge from being merged a second time
// on retry. See compact.go's package doc comment ("Retry idempotency") for the full
// rationale.
func (l *EdgeLog) ReadNodeAfter(sourceFileID uint64, afterSeg int) ([]CSREdge, int, error) {
	dir := l.nodeDir(sourceFileID)
	segments, err := listWALSegmentsNumbered(dir)
	if err != nil {
		return nil, afterSeg, err
	}
	maxSeg := afterSeg
	var edges []CSREdge
	for _, seg := range segments {
		if seg.num > maxSeg {
			maxSeg = seg.num
		}
		if seg.num <= afterSeg {
			// Already durably reflected in graph.dat by a prior compaction run
			// whose truncation of this segment failed - do not re-merge it.
			continue
		}
		records, err := wal.ReadSegment(seg.path)
		if err != nil {
			return nil, afterSeg, fmt.Errorf("graph: reading edge log segment %s: %w", seg.path, err)
		}
		for _, rec := range records {
			if len(rec) != csrEdgeEncodedSize {
				return nil, afterSeg, fmt.Errorf("graph: edge log segment %s has malformed record of %d bytes, want %d", seg.path, len(rec), csrEdgeEncodedSize)
			}
			edges = append(edges, decodeCSREdge(rec))
		}
	}
	return edges, maxSeg, nil
}

// TruncateNode discards every edge currently durably recorded in sourceFileID's
// per-node log, resetting it to empty. This is intended to be called by subtask
// 3.1.3's compaction (compact.go) ONLY after the edges it read via ReadNode have
// already been durably folded into a fresh graph.dat (i.e. after WriteCSR's
// atomic rename has succeeded) - never before, since truncating first would
// permanently lose any not-yet-compacted edge if the process then crashed before
// finishing the compaction write.
//
// If sourceFileID currently has an open wal.Writer (because AppendEdge or
// ReadNode has already been used for it), that writer is closed and dropped from
// the cache first, so a subsequent AppendEdge for the same fileID lazily reopens
// a brand-new wal.Writer rather than continuing to append to a file this method
// is about to delete out from under it. It is not an error for sourceFileID to
// have no log at all yet (nothing to truncate).
//
// # Segment numbering must never be reused after a successful truncation
//
// A second, more severe bug was found in this fix cycle beyond the one
// compact.go's package doc comment already documents (the "retry idempotency"
// crash-safety window): TruncateNode used to remove sourceFileID's entire node
// directory outright. Since wal.OpenWriter always starts a brand-new (or
// newly-empty) directory's segment numbering at 0, the very next AppendEdge for
// the same fileID after a successful truncation would silently start writing to
// "wal-0.log" again - the exact same segment number compact.go's compact-state
// sidecar had just durably recorded as "already folded into graph.dat" for this
// fileID. The next Compact run would then see that reused segment number,
// conclude (per the sidecar) that it was already accounted for, and skip it -
// permanently and silently discarding every edge appended after the node's
// first successful truncation, with no crash or failure injection required at
// all (issue #15, subtask 3.1.3, second fix cycle).
//
// The fix: TruncateNode no longer deletes the node directory. Instead, before
// removing any segment file, it durably records (via wal.WriteSegmentFloor) a
// "segment floor" one past the highest segment number currently on disk for
// this fileID, so the next wal.OpenWriter call for this directory - once its
// segment files really are all gone - resumes numbering from that floor rather
// than from 0. The floor is written BEFORE the segment files are removed
// specifically so that a crash between these two steps is safe: the
// not-yet-removed segment files are still on disk, so the next OpenWriter call
// resumes appending to them directly (wal.OpenWriter's own "existing segment
// files win over any floor marker" rule), which is exactly the same
// already-documented, safe-to-retry outcome as if this TruncateNode call had
// failed outright (see compact.go's package doc comment). Writing the floor
// AFTER removal, by contrast, would reopen the exact same window this fix
// closes: a crash in that gap would leave an empty directory with no floor
// recorded, and the next AppendEdge would restart numbering at 0 again.
func (l *EdgeLog) TruncateNode(sourceFileID uint64) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if w, ok := l.writers[sourceFileID]; ok {
		if err := w.Close(); err != nil {
			return fmt.Errorf("graph: closing edge log for node %d before truncate: %w", sourceFileID, err)
		}
		delete(l.writers, sourceFileID)
	}

	dir := l.nodeDir(sourceFileID)
	segments, err := listWALSegmentsNumbered(dir)
	if err != nil {
		return err
	}
	if len(segments) == 0 {
		// Nothing to truncate for this fileID: either it never had a log, or
		// a prior TruncateNode call already ran and no edge has been
		// appended since. Any segment-floor marker already recorded for dir
		// (see below) is left in place untouched - it still protects the
		// next AppendEdge from reusing a segment number that compact.go may
		// already have durably recorded as reflected in graph.dat.
		return nil
	}

	// segments is sorted ascending by listWALSegmentsNumbered, so the last
	// entry is the highest segment number currently on disk for this
	// fileID. See the doc comment above for why this must be recorded
	// BEFORE any segment file below is removed.
	maxSeg := segments[len(segments)-1].num
	if err := wal.WriteSegmentFloor(dir, maxSeg+1); err != nil {
		return fmt.Errorf("graph: recording segment floor for node %d before truncate: %w", sourceFileID, err)
	}

	for _, seg := range segments {
		if err := os.Remove(seg.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("graph: removing edge log segment %s: %w", seg.path, err)
		}
	}
	// Also remove manifest.json/manifest.json.tmp if wal.Checkpoint was ever used
	// against this node dir (edgelog.go itself never calls it, but be defensive
	// so a stale control file never confuses a future wal.OpenWriter resume check).
	_ = os.Remove(filepath.Join(dir, "manifest.json"))
	_ = os.Remove(filepath.Join(dir, "manifest.json.tmp"))
	// The node directory itself is intentionally no longer removed here: it
	// now holds the segment-floor marker written above, which must survive
	// so the next AppendEdge's wal.OpenWriter call sees it and does not
	// restart segment numbering at 0 (see doc comment above).

	return nil
}

// Close closes every currently-open per-node wal.Writer this EdgeLog has opened,
// collecting and returning any errors encountered along the way (via errors.Join) rather
// than stopping at the first failure, so a single stuck writer does not prevent the
// others from being closed cleanly.
func (l *EdgeLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var errs []error
	for fileID, w := range l.writers {
		if err := w.Close(); err != nil {
			errs = append(errs, fmt.Errorf("graph: closing edge log for node %d: %w", fileID, err))
		}
	}
	l.writers = make(map[uint64]*wal.Writer)
	return errors.Join(errs...)
}

// numberedSegment pairs a "wal-<N>.log" segment file's path with its parsed
// segment number N, so callers that need to reason about segment identity
// (compact.go's retry-idempotency logic, via ReadNodeAfter) can do so without
// re-parsing file names themselves.
type numberedSegment struct {
	num  int
	path string
}

// listWALSegmentsNumbered is listWALSegments' underlying implementation,
// additionally exposing each segment's parsed number. It returns (nil, nil)
// if dir does not exist (i.e. sourceFileID has never had an edge appended),
// sorted ascending by segment number.
func listWALSegmentsNumbered(dir string) ([]numberedSegment, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("graph: listing edge log dir %s: %w", dir, err)
	}

	var segments []numberedSegment
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "wal-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		numStr := strings.TrimSuffix(strings.TrimPrefix(name, "wal-"), ".log")
		num, err := strconv.Atoi(numStr)
		if err != nil {
			continue
		}
		segments = append(segments, numberedSegment{num: num, path: filepath.Join(dir, name)})
	}
	sort.Slice(segments, func(i, j int) bool { return segments[i].num < segments[j].num })
	return segments, nil
}
