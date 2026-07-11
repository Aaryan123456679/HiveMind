// Package graph (this file): subtask 3.1.3's periodic compaction step, which
// folds every source fileID's accumulated per-node edge-log entries (edgelog.go,
// task-3.1.2) into a fresh, complete csr.go (task-3.1.1) CSR snapshot.
//
// Compact is the only place in this package that merges edge-log entries with
// an existing (or absent) graph.dat: csr.go's WriteCSR/LoadCSR only persist and
// reload whatever CSRGraph they are given, and edgelog.go's AppendEdge/ReadNode
// only durably record/replay a single node's raw log - neither performs any
// merging, deduplication, or weight aggregation across entries. That is this
// file's entire job.
//
// # Weight-aggregation semantics
//
// docs/LLD/graph.md describes EdgeEntityCooccur (see edge_append.go) as
// "incremented when the ingestion segmentation agent extracts co-occurring
// entities across files". Read together with csr.go's CSREdge.Weight (a
// caller-supplied value this package only persists/reloads, never computes
// itself) and this subtask's acceptance criterion ("weight increments on
// repeated ENTITY_COOCCUR edges"), the semantics this file implements are:
//
//   - EdgeEntityCooccur: every occurrence of the same (source, target) pair -
//     whether already folded into the existing graph.dat from a prior
//     compaction, or freshly appended to the edge log since then (possibly
//     more than once) - contributes its Weight to a running SUM for that
//     edge. LastUpdated on the merged entry is the MAXIMUM (most recent)
//     LastUpdated across every occurrence merged.
//   - Every other edge type (EdgeSplitSibling, EdgeRedirect, EdgeLLMAsserted):
//     a repeated (source, target, type) triple is deduplicated to exactly one
//     CSR entry (never emitted twice - these edges are structural/assertional
//     facts, not occurrence counts, and docs/LLD/graph.md's own framing of
//     e.g. SPLIT_SIBLING as created once per split event would make summing
//     its weight semantically wrong). The most-recently-observed occurrence
//     (by LastUpdated, log order breaking ties) wins; its Weight/LastUpdated
//     are kept as-is, not summed.
//
// # Crash-safety ordering
//
// Compact reads the existing graph.dat (if any) and every edge log's pending
// entries, merges them entirely in memory, and writes the result via csr.go's
// WriteCSR - already atomic (temp file + fsync + rename) since task-3.1.1, and
// left completely unchanged by this file. Per-node edge logs are ONLY
// truncated (via EdgeLog.TruncateNode) AFTER WriteCSR's rename has durably
// succeeded, one node at a time, never before and never interleaved with the
// write itself:
//
//   - A crash any time before WriteCSR's rename completes leaves the OLD
//     graph.dat (or no graph.dat, for a first-ever compaction) untouched and
//     every edge log fully intact (nothing truncated yet). Retrying Compact
//     from scratch afterwards is safe: no edge is lost, and because Compact
//     always rebuilds a complete snapshot from the full logs rather than
//     incrementally appending, no edge is duplicated either.
//   - A crash after the rename succeeds but before every per-node log has
//     been truncated leaves graph.dat already durably correct, but one or
//     more edge logs still holding entries that are now ALSO reflected in
//     graph.dat. This is a narrow, accepted risk (mirroring engine/wal's own
//     Checkpoint precedent, where marking "durably applied up to here" is
//     necessarily a separate, best-effort step after the underlying mutation
//     is already durable): a subsequent Compact run will re-read and re-merge
//     those not-yet-truncated entries, which for EdgeEntityCooccur means
//     re-summing weights that were already counted once. Compact does not
//     attempt to make truncation part of the same atomic operation as
//     WriteCSR's rename (infeasible - truncation touches N separate per-node
//     log directories, not one file); instead it treats the post-rename
//     graph.dat as authoritative-and-durable regardless of truncation outcome
//     and reports truncation failures separately (see Compact's doc comment
//     below) rather than treating them as reasons to consider the compaction
//     itself failed.
//
// # Retry idempotency
//
// The crash-after-rename-but-before-truncate window above used to have a much
// worse consequence than "redundant work on retry": a retried Compact would
// re-read the same not-yet-truncated log entries as "incoming" and merge them
// AGAIN against an "existing" graph.dat that (from the prior, already-durable
// run) already reflected their contribution - permanently double-counting
// (and, on repeated failed-truncate retries, further compounding) any
// EdgeEntityCooccur weight involved. This was not self-correcting, unlike
// most of this file's other documented retry properties.
//
// The fix is a small durable "compact-state" sidecar file next to graphPath
// (see loadCompactState/saveCompactState below), recording, per source
// fileID, the highest edge-log segment number already durably folded into
// graphPath. Compact consults this before deciding what counts as "incoming"
// for a node (via EdgeLog.ReadNodeAfter): segments at or below the recorded
// number are known to already be reflected in the "existing" adjacency loaded
// from graphPath and are skipped, regardless of whether they were ever
// successfully truncated off disk. The sidecar is written atomically (temp
// file + fsync + rename, matching WriteCSR's own pattern) immediately after
// WriteCSR's rename succeeds and before any TruncateNode call is attempted,
// so its contents always describe graphPath's actual, currently-durable
// contents - not merely what compaction intended or attempted to truncate.
//
// This closes the compounding-corruption bug for the tested and documented
// failure window (TruncateNode failing after a successful WriteCSR rename).
// It narrows, but does not claim to eliminate to zero, one further edge: a
// real process crash landing in the sub-millisecond window between WriteCSR's
// rename returning and saveCompactState's own rename completing would still
// leave a stale (pre-this-round) compact-state on disk. Unlike the bug being
// fixed here, that residual window (a) requires an actual crash, not merely a
// failing operation (permission error, disk full, EBUSY, etc. - the entire
// class this fix does close, and the only class this package's existing
// crash-injection tests exercise or that issue #15's 3.1.3 spec requires),
// and (b) is bounded to at most one extra re-summing per such crash rather
// than being deterministically and unboundedly compounding on every ordinary
// truncate failure the way the original bug was. This tradeoff - closing the
// common, deterministically-reproducible corruption path with a single extra
// small atomic file, rather than pursuing full multi-file transactional
// atomicity across graph.dat and every per-node edge-log directory - is the
// "simplest and most robust" option evaluated for this fix; folding the
// marker into graph.dat itself was considered and rejected because it would
// require changing csr.go's on-disk format and LoadCSR's payload-length
// validation, which every other caller of LoadCSR (including this file's own
// existing-graph reload) depends on staying exactly as task-3.1.1 defined it.
//
// # Segment-number reuse (second fix cycle)
//
// The compact-state sidecar above fixed the crash-after-rename-but-before-
// truncate window, but in doing so introduced a second, more severe bug: the
// sidecar records "already folded into graphPath" using an edge log's
// segment NUMBERS (see EdgeLog.ReadNodeAfter), not any content hash or
// per-edge identity. EdgeLog.TruncateNode used to fully remove a node's
// per-node log directory once its entries were durably folded in, and
// engine/wal's OpenWriter always starts a brand-new (or newly-empty)
// directory's segment numbering at 0. So the very next edge appended to that
// same node after ANY successful, uneventful truncation - no crash or
// failure required at all - would silently be written to a reused segment
// number the sidecar had already marked "already accounted for", and the
// next ordinary Compact run would skip it as a result: a real edge, appended
// on the completely normal happy path, permanently and silently discarded.
// This is strictly worse than the bug the sidecar itself fixed, because it
// requires no failure injection and no crash whatsoever - only two ordinary
// Compact cycles on the same node.
//
// The fix (see EdgeLog.TruncateNode's own doc comment for the full
// crash-safety trace): TruncateNode no longer deletes a node's log
// directory. Instead, before removing any segment file, it durably records
// (via wal.WriteSegmentFloor) a floor one past the highest segment number
// that directory has ever used, so segment numbers are never reused across a
// truncation - closing the collision this fix cycle exists to fix - while
// leaving the original compact-state sidecar mechanism above, and its own
// documented residual crash window, completely unchanged.
//
// # Lock-ordering fix (concurrent AppendEdge vs. read-then-truncate)
//
// Everything above concerns crash-safety and retry idempotency for a single,
// uncontended Compact run. A separate, genuinely concurrent bug exists even
// with no crash or failure injected at all: Compact decides what an edge
// log's "incoming" content is via EdgeLog.ReadNodeAfter at one point in
// time, then - only after the entire new graph.dat has been written and its
// compact-state sidecar saved, which for a large graph can take
// arbitrarily long relative to a single node's own log - removes that same
// content via EdgeLog.TruncateNode, which independently re-lists whatever
// segment files happen to exist on disk AT THAT LATER TIME rather than
// exactly what ReadNodeAfter saw. A concurrent AppendEdge landing on that
// node anywhere in between - most commonly by appending into the very same,
// still-current segment file ReadNodeAfter already fully read, since
// engine/wal only rotates to a brand-new segment file once the current one
// would exceed its size threshold (4 MiB by default; a single edge record is
// a few dozen bytes) - would never be observed by that Compact run's
// ReadNodeAfter (it didn't exist yet) and would then be permanently deleted
// by TruncateNode's later, independent re-listing before ever being folded
// into graph.dat: a real edge, appended on the ordinary concurrent-writer
// happy path, silently and permanently lost.
//
// Note this rules out a purely segment-number-based fix ("only truncate
// segments numbered <= the maxSeg ReadNodeAfter observed"): the
// concurrently-appended record can land INSIDE, not just after, the already-
// read segment (no new segment number is created), so a scalar segment
// cutoff alone cannot distinguish "bytes already merged" from "bytes
// appended afterwards" within that one file. Splitting a segment file's own
// bytes at truncation time (physically separating an already-merged head
// from a not-yet-merged tail written after the read, and handing that tail
// to the still-open engine/wal.Writer) is not a primitive engine/wal
// exposes today and would be a substantially larger change than this fix
// requires.
//
// The fix instead closes the window directly: EdgeLog.LockNode exposes a
// per-node lock (independent of EdgeLog's own writer-cache mutex) that
// AppendEdge now holds for its entire body, and that Compact now acquires,
// per node, immediately before that node's ReadNodeAfter call - keeping it
// held (see heldNodeLocks below) all the way through that same node's later
// TruncateNode call, spanning the intervening WriteCSR/saveCompactState
// calls. This makes "AppendEdge for node X" and "Compact's read-then-
// truncate of node X" strictly mutually exclusive: a concurrent AppendEdge
// attempted mid-Compact simply blocks until Compact finishes truncating and
// releases the lock, then proceeds normally (landing in a fresh append
// after truncation, safely visible to the NEXT Compact run) - it can no
// longer land inside the now-closed gap at all. The lock is scoped per node,
// not global, so AppendEdge calls to any OTHER node are completely
// unaffected; only nodes actually being compacted in a given Compact run are
// blocked, and only for that run's duration.
package graph

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// compactStateSuffix names the small sidecar file, next to graphPath, that
// Compact uses to track retry idempotency (see package doc comment above).
const compactStateSuffix = ".compact-state"

// compactStateMagic/compactStateVersion identify compact-state sidecar files,
// mirroring csr.go's own magic+version+CRC framing for graph.dat itself.
var compactStateMagic = [4]byte{'G', 'C', 'P', 'S'}

const compactStateVersion = uint32(1)

// compactStateHeaderSize is magic(4) + version(4) + count(4) + payloadCRC(4).
const compactStateHeaderSize = 16

// compactStatePath returns the compact-state sidecar path for graphPath.
func compactStatePath(graphPath string) string {
	return graphPath + compactStateSuffix
}

// loadCompactState reads graphPath's compact-state sidecar, returning a nil
// (empty) map - not an error - if the file does not exist yet: this is the
// normal case for the very first compaction ever run against graphPath, and
// for any graphPath written before this fix existed. A nil/missing entry for
// a given fileID is treated identically to loadCompactState never having been
// called at all (afterSeg defaults to -1: "read every segment"), so this is
// purely additive and cannot regress the pre-existing full-log-inclusion
// behavior for nodes it has no recorded state for.
func loadCompactState(graphPath string) (map[uint64]uint64, error) {
	path := compactStatePath(graphPath)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("graph: reading compact state %s: %w", path, err)
	}
	if len(data) < compactStateHeaderSize {
		return nil, fmt.Errorf("graph: compact state %s too short: got %d bytes, want at least %d-byte header", path, len(data), compactStateHeaderSize)
	}
	var magic [4]byte
	copy(magic[:], data[0:4])
	if magic != compactStateMagic {
		return nil, fmt.Errorf("graph: compact state %s has invalid magic %q, want %q", path, magic, compactStateMagic)
	}
	version := binary.LittleEndian.Uint32(data[4:8])
	if version != compactStateVersion {
		return nil, fmt.Errorf("graph: compact state %s has unsupported format version %d, want %d", path, version, compactStateVersion)
	}
	count := binary.LittleEndian.Uint32(data[8:12])
	wantCRC := binary.LittleEndian.Uint32(data[12:16])
	body := data[compactStateHeaderSize:]
	if wantLen := uint64(count) * 16; uint64(len(body)) != wantLen {
		return nil, fmt.Errorf("graph: compact state %s payload length mismatch: got %d bytes, want %d (count=%d)", path, len(body), wantLen, count)
	}
	if gotCRC := crc32.ChecksumIEEE(body); gotCRC != wantCRC {
		return nil, fmt.Errorf("graph: compact state %s failed payload CRC check (want %08x, got %08x)", path, wantCRC, gotCRC)
	}

	state := make(map[uint64]uint64, count)
	for i := uint32(0); i < count; i++ {
		off := i * 16
		id := binary.LittleEndian.Uint64(body[off:])
		seg := binary.LittleEndian.Uint64(body[off+8:])
		state[id] = seg
	}
	return state, nil
}

// saveCompactState atomically (temp file + fsync + rename, matching csr.go's
// WriteCSR pattern exactly) replaces graphPath's compact-state sidecar with
// state. Called by Compact only after WriteCSR's own rename has already
// succeeded, and before any TruncateNode call - see package doc comment.
func saveCompactState(graphPath string, state map[uint64]uint64) error {
	ids := make([]uint64, 0, len(state))
	for id := range state {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	body := make([]byte, len(ids)*16)
	for i, id := range ids {
		off := i * 16
		binary.LittleEndian.PutUint64(body[off:], id)
		binary.LittleEndian.PutUint64(body[off+8:], state[id])
	}

	header := make([]byte, compactStateHeaderSize)
	copy(header[0:4], compactStateMagic[:])
	binary.LittleEndian.PutUint32(header[4:8], compactStateVersion)
	binary.LittleEndian.PutUint32(header[8:12], uint32(len(ids)))
	binary.LittleEndian.PutUint32(header[12:16], crc32.ChecksumIEEE(body))

	path := compactStatePath(graphPath)
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("graph: creating temp compact state file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(header); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("graph: writing compact state header to %s: %w", tmpPath, err)
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("graph: writing compact state body to %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("graph: syncing compact state file %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("graph: closing compact state file %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("graph: renaming %s to %s: %w", tmpPath, path, err)
	}
	return nil
}

// edgeLogNodeIDs lists every source fileID that currently has a per-node
// subdirectory under an EdgeLog's root (i.e. every fileID that has ever had an
// edge appended to it, whether or not any of those edges have since been
// compacted away). It mirrors EdgeLog.nodeDir's own naming convention
// (root/<fileID>/...) to interpret each subdirectory name as a fileID, and
// silently skips any entry that isn't a plain, non-negative base-10 integer
// (e.g. a stray unrelated file placed directly under root), matching this
// package's existing tolerance for unrecognized directory entries (see
// listWALSegmentsNumbered).
//
// Note that, since this fix cycle, EdgeLog.TruncateNode no longer removes a
// node's per-node directory (it keeps it around to hold a wal.WriteSegmentFloor
// marker - see TruncateNode's doc comment for why), so a fileID's directory -
// and therefore this function's inclusion of it - persists even once every
// edge ever appended to it has been compacted and truncated away. Compact
// below already tolerates this correctly: a node with no segments currently
// on disk simply contributes no logEdges and is a cheap no-op both to read
// (ReadNodeAfter) and to re-truncate (TruncateNode itself is a no-op when it
// finds no segment files).
func edgeLogNodeIDs(root string) ([]uint64, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("graph: listing edge log root %s: %w", root, err)
	}

	var ids []uint64
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id, err := strconv.ParseUint(e.Name(), 10, 64)
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// mergeEdges folds incoming (freshly read from a node's edge log, in on-disk
// append order) into existing (that node's adjacency already present in a
// previously-loaded graph.dat, or nil for a node with no prior CSR entry),
// applying this file's weight-aggregation semantics (see package doc comment
// above): EdgeEntityCooccur entries sharing the same Target are summed by
// Weight with LastUpdated taking the max; every other edge type is
// deduplicated by (Target, Type) with the most-recently-updated occurrence
// winning outright (no summing). The returned slice's order is not
// significant - csr.go's BuildCSR/WriteCSR do not require callers to pass
// edges in original append order once merged.
func mergeEdges(existing, incoming []CSREdge) []CSREdge {
	type key struct {
		target uint64
		typ    EdgeType
	}
	merged := make(map[key]CSREdge, len(existing)+len(incoming))

	apply := func(e CSREdge) {
		k := key{target: e.Target, typ: e.Type}
		prev, ok := merged[k]
		if !ok {
			merged[k] = e
			return
		}
		if e.Type == EdgeEntityCooccur {
			sum := prev
			sum.Weight = prev.Weight + e.Weight
			if e.LastUpdated > prev.LastUpdated {
				sum.LastUpdated = e.LastUpdated
			}
			merged[k] = sum
			return
		}
		// Last-write-wins for all other types: keep whichever occurrence has
		// the more recent LastUpdated. On an exact tie, prefer the later one
		// in iteration order (incoming edges are appended after existing
		// entries below, so this naturally prefers the newer log entry over
		// an equally-timestamped pre-existing CSR entry).
		if e.LastUpdated >= prev.LastUpdated {
			merged[k] = e
		}
	}

	for _, e := range existing {
		apply(e)
	}
	for _, e := range incoming {
		apply(e)
	}

	out := make([]CSREdge, 0, len(merged))
	for _, e := range merged {
		out = append(out, e)
	}
	return out
}

// Compact folds every accumulated per-node edge-log entry under log into a
// fresh CSR snapshot, written atomically to graphPath (via csr.go's WriteCSR),
// merging with graphPath's existing contents if any (see LoadCSR's own
// os.IsNotExist handling - a missing graphPath is treated as "no prior graph",
// not an error, so the very first compaction run works with no setup).
//
// On success, every per-node log that contributed entries to the new snapshot
// is truncated (EdgeLog.TruncateNode) AFTER the new graphPath has already been
// durably written, per this file's documented crash-safety ordering. Compact
// returns a non-nil error only in two cases, distinguishable by the caller
// via errors.Is/errors.Join inspection if needed:
//
//   - The merge-and-write itself failed (reading an existing graph.dat,
//     reading an edge log, or WriteCSR itself): in this case graphPath is
//     guaranteed unchanged from before the call (WriteCSR's own atomicity),
//     and every edge log is guaranteed untouched - safe to simply retry
//     Compact later.
//   - The merge-and-write succeeded (graphPath now durably reflects the
//     merge) but one or more per-node TruncateNode calls afterwards failed:
//     graphPath is correct and durable regardless: the returned error only
//     signals that some edge logs may still redundantly hold entries that
//     are already reflected in graphPath (safe to retry Compact again later
//     to finish truncating them; per this file's documented semantics, a
//     retry may re-sum already-counted EdgeEntityCooccur weight for any node
//     whose log was not successfully truncated - a narrow, accepted risk
//     documented in this file's package doc comment).
//
// compactNodeLockedHook, if non-nil, is invoked synchronously by Compact once
// per node, immediately after that node's per-node lock (EdgeLog.LockNode)
// has been acquired and ReadNodeAfter has returned for it - while the lock is
// still held. This is a test-only seam (nil, a no-op, in production)
// mirroring this repo's established synchronous-hook idiom for deterministic
// concurrency testing (see e.g. engine/btree/insert.go's crabRetryHook,
// engine/btree/lookup.go's optimisticReadHook/optimisticRetryHook,
// engine/split/execute.go's atomicCommitHook, engine/catalog/content.go's
// createWithHook). TestCompactConcurrentAppendNotLost (subtask 4.5.11.2,
// issue #49) uses it to deterministically start a concurrent AppendEdge for
// the same node at the exact instant Compact's read has completed but its
// later TruncateNode call has not yet run - see package doc comment
// ("Lock-ordering fix") for the race this closes.
var compactNodeLockedHook func(id uint64)

func Compact(graphPath string, log *EdgeLog) (*CSRGraph, error) {
	adjacency := make(map[uint64][]CSREdge)

	if existing, err := LoadCSR(graphPath); err == nil {
		for i := 0; i < existing.NodeCount(); i++ {
			id := existing.nodeIDs[i]
			adjacency[id] = existing.Neighbors(id)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("graph: compaction failed to load existing graph %s: %w", graphPath, err)
	}

	// prevState records, per fileID, the highest edge-log segment number a
	// prior compaction run already durably folded into the "existing"
	// adjacency just loaded above. See package doc comment ("Retry
	// idempotency"): segments at or below that number must NOT be re-read as
	// "incoming" below, or their contribution would be merged a second time.
	prevState, err := loadCompactState(graphPath)
	if err != nil {
		return nil, fmt.Errorf("graph: compaction failed to load compact state for %s: %w", graphPath, err)
	}

	nodeIDs, err := edgeLogNodeIDs(log.root)
	if err != nil {
		return nil, fmt.Errorf("graph: compaction failed to enumerate edge log nodes: %w", err)
	}

	newState := make(map[uint64]uint64, len(prevState))
	for id, seg := range prevState {
		newState[id] = seg
	}

	// heldNodeLocks tracks, per node id whose edge log has actually been read
	// and is destined for truncation below, the unlock function returned by
	// EdgeLog.LockNode. The lock for a given id is acquired immediately
	// before that id's ReadNodeAfter call and is deliberately NOT released
	// until after that same id's TruncateNode call has run (see the final
	// truncate loop below), so a concurrent AppendEdge for that node can
	// never land in the gap between "Compact decided what is incoming" and
	// "Compact removed what it just merged" - see package doc comment
	// ("Lock-ordering fix") and EdgeLog.LockNode's doc comment for the full
	// rationale (subtask 4.5.11.2, issue #49).
	//
	// releaseHeldLocks unlocks every entry still present in heldNodeLocks -
	// used both by the final truncate loop (removing entries as it goes) and
	// by every early-error return below (releasing whatever locks had been
	// acquired so far before propagating the error).
	heldNodeLocks := make(map[uint64]func())
	releaseHeldLocks := func() {
		for id, unlock := range heldNodeLocks {
			unlock()
			delete(heldNodeLocks, id)
		}
	}

	var compactedNodeIDs []uint64
	for _, id := range nodeIDs {
		afterSeg := -1
		if seg, ok := prevState[id]; ok {
			afterSeg = int(seg)
		}

		unlock := log.LockNode(id)

		logEdges, maxSeg, err := log.ReadNodeAfter(id, afterSeg)
		if compactNodeLockedHook != nil {
			compactNodeLockedHook(id)
		}
		if err != nil {
			unlock()
			releaseHeldLocks()
			return nil, fmt.Errorf("graph: compaction failed to read edge log for node %d: %w", id, err)
		}
		if len(logEdges) > 0 {
			adjacency[id] = mergeEdges(adjacency[id], logEdges)
		}
		if maxSeg < 0 {
			// No segments at all currently on disk for this id (e.g. an
			// edgeLogNodeIDs entry left over from an empty/stray directory) -
			// nothing to record and nothing to truncate, so nothing to
			// protect: release immediately rather than holding the lock
			// through the rest of this function for no reason.
			unlock()
			continue
		}
		// Every segment currently on disk for this node - whether freshly
		// read above or already covered by prevState - is about to be
		// durably reflected in the new graph.dat written below, so it is
		// both safe and desirable to truncate all of them now (self-healing
		// cleanup of any leftover segment from a prior failed truncate). The
		// lock acquired above stays held (via heldNodeLocks) until this same
		// node's TruncateNode call below, spanning the WriteCSR/
		// saveCompactState calls in between.
		newState[id] = uint64(maxSeg)
		compactedNodeIDs = append(compactedNodeIDs, id)
		heldNodeLocks[id] = unlock
	}

	newGraph := BuildCSR(adjacency)

	dir := filepath.Dir(graphPath)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			releaseHeldLocks()
			return nil, fmt.Errorf("graph: compaction failed to create graph dir %s: %w", dir, err)
		}
	}

	if err := WriteCSR(graphPath, newGraph); err != nil {
		releaseHeldLocks()
		return nil, fmt.Errorf("graph: compaction failed to write %s: %w", graphPath, err)
	}

	// graphPath is now durably updated. Persist the compact-state sidecar
	// before attempting any truncation, so that even if every TruncateNode
	// call below fails, a subsequent retry still knows exactly which
	// segments are already reflected in graphPath and will not re-merge
	// them - see package doc comment ("Retry idempotency").
	var postWriteErrs []error
	if err := saveCompactState(graphPath, newState); err != nil {
		postWriteErrs = append(postWriteErrs, fmt.Errorf("graph: compaction failed to persist compact state for %s (graphPath itself is still correct and durable, but a subsequent retry may re-merge not-yet-truncated segments for nodes affected by this failure): %w", graphPath, err))
	}

	// Only past this point do we truncate any per-node edge log - see this
	// file's package doc comment for why this ordering is the crux of
	// compaction's crash-safety. Each node's lock (acquired in the read loop
	// above, immediately before its ReadNodeAfter call) is released only
	// after that same node's TruncateNode call returns here, whether it
	// succeeds or fails - closing the read-then-truncate window a concurrent
	// AppendEdge could otherwise land in (subtask 4.5.11.2, issue #49).
	for _, id := range compactedNodeIDs {
		if err := log.TruncateNode(id); err != nil {
			postWriteErrs = append(postWriteErrs, fmt.Errorf("graph: compaction failed to truncate edge log for node %d after successful write: %w", id, err))
		}
		if unlock, ok := heldNodeLocks[id]; ok {
			unlock()
			delete(heldNodeLocks, id)
		}
	}

	return newGraph, errors.Join(postWriteErrs...)
}
