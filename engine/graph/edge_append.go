// Package graph is part of the HiveMind storage engine.
//
// This file implements subtask 2b.3.4's minimal, append-only graph edge
// writer. It is intentionally NOT a graph engine: no CSR storage,
// compaction, or multi-hop traversal/query API is provided here — that is
// explicitly deferred to Epic 3. This package currently offers only the
// smallest primitive needed by a later subtask (2b.3.5, which will wire
// engine/split/execute.go to append SPLIT_SIBLING/REDIRECT edges as part of
// a split transaction): a fixed-shape Edge type and a durable,
// ordering-preserving AppendEdge write.
//
// Durability is provided by reusing engine/wal's low-level, content-agnostic
// segment writer (wal.Writer / wal.OpenWriter / wal.ReadSegment) rather than
// reinventing this repo's established WriteAt+fsync+segment-rotation
// idiom. This package deliberately does NOT participate in engine/wal's
// higher-level TypedRecord/RecordType/Replay machinery, which is the shared
// crash-recovery path built for engine/catalog and engine/btree mutations
// (subtasks 1.3.x): coupling that already-shipped, tested recovery path to
// a still-unwired consumer would be scope creep beyond a minimal append-only
// primitive. A future subtask (2b.3.6, which commits an entire split as one
// WAL-covered transaction) can decide how/whether to fold graph edge writes
// into that broader transaction; nothing here forecloses that.
package graph

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Aaryan123456679/HiveMind/engine/wal"
)

// EdgeType identifies the kind of relationship a graph Edge represents.
//
// The zero value, EdgeTypeInvalid, is reserved as a non-valid sentinel,
// matching this repo's convention for on-disk enums whose zero value must
// not silently decode as something meaningful (see wal.RecordTypeInvalid's
// rationale, which this mirrors).
type EdgeType uint8

const (
	// EdgeTypeInvalid is the zero value and never a valid on-disk edge type.
	EdgeTypeInvalid EdgeType = iota

	// EdgeSplitSibling represents an edge between two files that were
	// produced by splitting the same original file (siblings of one split).
	EdgeSplitSibling

	// EdgeRedirect represents an edge from a redirect stub (the original,
	// now-stub file left behind at the old path after a split) to one of
	// its redirect targets.
	EdgeRedirect
)

// String returns a human-readable name for t, used in error messages.
func (t EdgeType) String() string {
	switch t {
	case EdgeSplitSibling:
		return "SplitSibling"
	case EdgeRedirect:
		return "Redirect"
	default:
		return fmt.Sprintf("EdgeType(%d)", byte(t))
	}
}

// Edge is the minimal graph edge shape this subtask defines: a directed
// relationship between two catalog fileIDs (see engine/catalog.CatalogRecord.FileID),
// tagged with its EdgeType. Source is the file the edge originates from
// (e.g. a redirect stub, for EdgeRedirect); Target is the file it points at.
type Edge struct {
	Source uint64
	Target uint64
	Type   EdgeType
}

// edgeEncodedSize is the fixed on-disk width, in bytes, of an encoded Edge:
// Source (8) + Target (8) + Type (1). This mirrors this repo's existing
// fixed-width little-endian record conventions (see
// engine/catalog/record.go, engine/wal/record.go).
const edgeEncodedSize = 8 + 8 + 1

const (
	offEdgeSource = 0
	offEdgeTarget = offEdgeSource + 8
	offEdgeType   = offEdgeTarget + 8
)

// encode serializes e into a fixed-size, edgeEncodedSize-byte little-endian
// buffer suitable for passing to a wal.Writer's Append.
func (e Edge) encode() []byte {
	buf := make([]byte, edgeEncodedSize)
	binary.LittleEndian.PutUint64(buf[offEdgeSource:], e.Source)
	binary.LittleEndian.PutUint64(buf[offEdgeTarget:], e.Target)
	buf[offEdgeType] = byte(e.Type)
	return buf
}

// decodeEdge parses data (as previously produced by Edge.encode) into an
// Edge. It returns an error if data is not exactly edgeEncodedSize bytes or
// if the encoded EdgeType is not a value this package recognizes.
func decodeEdge(data []byte) (Edge, error) {
	if len(data) != edgeEncodedSize {
		return Edge{}, fmt.Errorf("graph: encoded edge has wrong size: got %d bytes, want %d", len(data), edgeEncodedSize)
	}

	e := Edge{
		Source: binary.LittleEndian.Uint64(data[offEdgeSource:]),
		Target: binary.LittleEndian.Uint64(data[offEdgeTarget:]),
		Type:   EdgeType(data[offEdgeType]),
	}

	switch e.Type {
	case EdgeSplitSibling, EdgeRedirect:
		// valid
	default:
		return Edge{}, fmt.Errorf("graph: decoded edge has invalid type %d", data[offEdgeType])
	}

	return e, nil
}

// defaultMaxSegmentBytes is the segment-rotation threshold used when a
// caller does not need control over it. This is large enough that a
// realistic run of edge appends stays within a single segment file; the
// value only bounds a single log file's size before wal.Writer rotates to a
// new one, matching engine/wal's own rotation behavior.
const defaultMaxSegmentBytes = 4 * 1024 * 1024

// EdgeAppender is a minimal, append-only, durable writer for graph Edges. It
// wraps a wal.Writer rooted at its own directory: every Append call fsyncs
// the edge to disk before returning (the same durability guarantee
// wal.Writer already provides for catalog/btree mutations), and appends
// preserve strict on-disk ordering — nothing here reorders or compacts
// edges.
//
// EdgeAppender provides no traversal or query API: CSR storage, compaction,
// and multi-edge traversal are explicitly deferred to Epic 3. The package-level
// ReadAll function exists solely to support verifying append-only durability
// (e.g. in tests), not as a general read API.
type EdgeAppender struct {
	w *wal.Writer
}

// OpenEdgeAppender opens (creating if necessary) an append-only edge log
// rooted at dir. If dir already contains a prior edge log, OpenEdgeAppender
// resumes appending after its existing contents (via wal.OpenWriter's own
// resume behavior) rather than overwriting them.
func OpenEdgeAppender(dir string) (*EdgeAppender, error) {
	w, err := wal.OpenWriter(dir, defaultMaxSegmentBytes)
	if err != nil {
		return nil, fmt.Errorf("graph: opening edge appender at %s: %w", dir, err)
	}
	return &EdgeAppender{w: w}, nil
}

// AppendEdge durably appends edge to the log, fsyncing before it returns.
func (a *EdgeAppender) AppendEdge(edge Edge) error {
	if edge.Type != EdgeSplitSibling && edge.Type != EdgeRedirect {
		return fmt.Errorf("graph: cannot append edge with invalid type %v", edge.Type)
	}
	if _, err := a.w.Append(edge.encode()); err != nil {
		return fmt.Errorf("graph: appending edge: %w", err)
	}
	return nil
}

// Close closes the underlying writer's open segment file.
func (a *EdgeAppender) Close() error {
	return a.w.Close()
}

// ReadAll reads back every edge previously durably appended to dir (across
// all of its segment files, in on-disk append order), for verifying
// durability/ordering. It is not a general query API: no filtering,
// indexing, or per-fileID lookup is provided (that is deferred to Epic 3's
// graph engine).
func ReadAll(dir string) ([]Edge, error) {
	segmentPaths, err := listEdgeSegments(dir)
	if err != nil {
		return nil, err
	}

	var edges []Edge
	for _, path := range segmentPaths {
		records, err := wal.ReadSegment(path)
		if err != nil {
			return nil, fmt.Errorf("graph: reading edge log segment %s: %w", path, err)
		}
		for _, rec := range records {
			edge, err := decodeEdge(rec)
			if err != nil {
				return nil, fmt.Errorf("graph: decoding edge log segment %s: %w", path, err)
			}
			edges = append(edges, edge)
		}
	}
	return edges, nil
}

// listEdgeSegments returns the paths of every "wal-<N>.log" segment file in
// dir, sorted in ascending segment-number order (matching engine/wal's own
// "wal-<N>.log" naming convention, since EdgeAppender's durability is backed
// directly by a wal.Writer rooted at dir).
func listEdgeSegments(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("graph: listing edge log dir %s: %w", dir, err)
	}

	type numberedSegment struct {
		num  int
		path string
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

	paths := make([]string, len(segments))
	for i, s := range segments {
		paths[i] = s.path
	}
	return paths, nil
}
