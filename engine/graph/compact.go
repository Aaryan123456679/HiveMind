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
package graph

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// edgeLogNodeIDs lists every source fileID that currently has a per-node
// subdirectory under an EdgeLog's root (i.e. every fileID that has ever had an
// edge appended to it, whether or not any of those edges have since been
// compacted away). It mirrors EdgeLog.nodeDir's own naming convention
// (root/<fileID>/...) to interpret each subdirectory name as a fileID, and
// silently skips any entry that isn't a plain, non-negative base-10 integer
// (e.g. a stray unrelated file placed directly under root), matching this
// package's existing tolerance for unrecognized directory entries (see
// listWALSegments).
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

	nodeIDs, err := edgeLogNodeIDs(log.root)
	if err != nil {
		return nil, fmt.Errorf("graph: compaction failed to enumerate edge log nodes: %w", err)
	}

	var compactedNodeIDs []uint64
	for _, id := range nodeIDs {
		logEdges, err := log.ReadNode(id)
		if err != nil {
			return nil, fmt.Errorf("graph: compaction failed to read edge log for node %d: %w", id, err)
		}
		if len(logEdges) == 0 {
			continue
		}
		adjacency[id] = mergeEdges(adjacency[id], logEdges)
		compactedNodeIDs = append(compactedNodeIDs, id)
	}

	newGraph := BuildCSR(adjacency)

	dir := filepath.Dir(graphPath)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("graph: compaction failed to create graph dir %s: %w", dir, err)
		}
	}

	if err := WriteCSR(graphPath, newGraph); err != nil {
		return nil, fmt.Errorf("graph: compaction failed to write %s: %w", graphPath, err)
	}

	// graphPath is now durably updated. Only past this point do we truncate
	// any per-node edge log - see this file's package doc comment for why
	// this ordering is the crux of compaction's crash-safety.
	var truncErrs []error
	for _, id := range compactedNodeIDs {
		if err := log.TruncateNode(id); err != nil {
			truncErrs = append(truncErrs, fmt.Errorf("graph: compaction failed to truncate edge log for node %d after successful write: %w", id, err))
		}
	}

	return newGraph, errors.Join(truncErrs...)
}
