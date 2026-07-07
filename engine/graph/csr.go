// Package graph (this file): subtask 3.1.1's CSR-like compact adjacency array format,
// persisted to a single whole-snapshot file (conventionally named "graph.dat").
//
// This is deliberately NOT an incremental/append format: unlike edge_append.go's
// EdgeAppender (a WAL-segment-backed append-only log), a CSR array is a compacted,
// read-optimized structure that is only ever rebuilt wholesale (by a later subtask's
// compaction step, 3.1.3, merging the accumulated per-node edge log, 3.1.2, into a
// fresh array). Because there is no meaningful "append one edge into a CSR array"
// operation without breaking the offsets array's contiguity invariant, WriteCSR always
// atomically rewrites the entire file (temp file + fsync + rename), following the same
// whole-file durability convention engine/catalog/content.go's writeContentFile already
// established for this repo's non-log binary files.
//
// On-disk layout (all multi-byte integers little-endian):
//
//	Header (28 bytes):
//	  [0:4]   magic       "GCS1"
//	  [4:8]   version     uint32 = csrFormatVersion
//	  [8:16]  nodeCount   uint64 - number of distinct source fileIDs with adjacency entries
//	  [16:24] edgeCount   uint64 - total number of edges across all nodes
//	  [24:28] payloadCRC  uint32 - CRC32(IEEE) of every byte that follows the header
//
//	Payload:
//	  nodeIDs : nodeCount * 8 bytes       - sorted ascending source fileIDs (uint64 LE)
//	  offsets : (nodeCount+1) * 8 bytes   - CSR offsets array (uint64 LE); offsets[i]..offsets[i+1]
//	            is nodeIDs[i]'s neighbor range in the edges array; offsets[0] == 0,
//	            offsets[nodeCount] == edgeCount
//	  edges   : edgeCount * csrEdgeEncodedSize bytes - flat neighbor array, one fixed-width
//	            record per edge (Target uint64, Type byte, Weight uint32, LastUpdated int64)
//
// See docs/LLD/graph.md for the module-level storage layout this format fulfills, and
// plan.md under this subtask's .cdr run directory for the full design rationale.
package graph

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
)

// csrMagic identifies a graph.dat file as this package's CSR format.
var csrMagic = [4]byte{'G', 'C', 'S', '1'}

// csrFormatVersion is the current on-disk format version. Bump this if the layout changes
// in a backward-incompatible way.
const csrFormatVersion uint32 = 1

// csrHeaderSize is the fixed size, in bytes, of the header described above.
const csrHeaderSize = 4 + 4 + 8 + 8 + 4

const (
	offCSRMagic      = 0
	offCSRVersion    = 4
	offCSRNodeCount  = 8
	offCSREdgeCount  = 16
	offCSRPayloadCRC = 24
)

// csrEdgeEncodedSize is the fixed on-disk width, in bytes, of one encoded CSREdge record:
// Target (8) + Type (1) + Weight (4) + LastUpdated (8).
const csrEdgeEncodedSize = 8 + 1 + 4 + 8

const (
	offCSREdgeTarget      = 0
	offCSREdgeType        = offCSREdgeTarget + 8
	offCSREdgeWeight      = offCSREdgeType + 1
	offCSREdgeLastUpdated = offCSREdgeWeight + 4
)

// CSREdge is one adjacency entry stored in the CSR array: the target fileID an edge points
// to, its type, an accumulated weight, and when it was last updated. This mirrors the edge
// shape docs/LLD/graph.md specifies ({targetFileID, edgeType, weight, lastUpdated}); how
// Weight/LastUpdated are computed (e.g. ENTITY_COOCCUR increments) is a later subtask's
// (3.1.3 compaction) concern — this package only persists and reloads whatever values it
// is given.
type CSREdge struct {
	Target      uint64
	Type        EdgeType
	Weight      uint32
	LastUpdated int64
}

// encode serializes e into a fixed-size, csrEdgeEncodedSize-byte little-endian buffer.
func (e CSREdge) encode(buf []byte) {
	binary.LittleEndian.PutUint64(buf[offCSREdgeTarget:], e.Target)
	buf[offCSREdgeType] = byte(e.Type)
	binary.LittleEndian.PutUint32(buf[offCSREdgeWeight:], e.Weight)
	binary.LittleEndian.PutUint64(buf[offCSREdgeLastUpdated:], uint64(e.LastUpdated))
}

// decodeCSREdge parses data (as previously produced by CSREdge.encode) into a CSREdge. data
// must be exactly csrEdgeEncodedSize bytes.
func decodeCSREdge(data []byte) CSREdge {
	return CSREdge{
		Target:      binary.LittleEndian.Uint64(data[offCSREdgeTarget:]),
		Type:        EdgeType(data[offCSREdgeType]),
		Weight:      binary.LittleEndian.Uint32(data[offCSREdgeWeight:]),
		LastUpdated: int64(binary.LittleEndian.Uint64(data[offCSREdgeLastUpdated:])),
	}
}

// CSRGraph is an in-memory, immutable-once-built CSR (compressed sparse row) adjacency
// index: a sorted array of source fileIDs, a parallel offsets array, and a flat array of
// edges. It provides O(log n) neighbor lookup by fileID and compact, contiguous storage
// with no per-edge pointer overhead.
type CSRGraph struct {
	nodeIDs []uint64
	offsets []uint64
	edges   []CSREdge
}

// BuildCSR constructs a CSRGraph from an in-memory adjacency map (source fileID -> that
// file's outbound edges). Node IDs are sorted ascending for deterministic, byte-identical
// output given identical input. A source fileID present in adjacency with a nil/empty
// slice is still recorded as a node with zero edges (its offset range is empty).
func BuildCSR(adjacency map[uint64][]CSREdge) *CSRGraph {
	nodeIDs := make([]uint64, 0, len(adjacency))
	for id := range adjacency {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Slice(nodeIDs, func(i, j int) bool { return nodeIDs[i] < nodeIDs[j] })

	offsets := make([]uint64, len(nodeIDs)+1)
	var edges []CSREdge
	for i, id := range nodeIDs {
		offsets[i] = uint64(len(edges))
		edges = append(edges, adjacency[id]...)
	}
	offsets[len(nodeIDs)] = uint64(len(edges))

	return &CSRGraph{nodeIDs: nodeIDs, offsets: offsets, edges: edges}
}

// NodeCount returns the number of distinct source fileIDs with adjacency entries.
func (g *CSRGraph) NodeCount() int { return len(g.nodeIDs) }

// EdgeCount returns the total number of edges across all nodes.
func (g *CSRGraph) EdgeCount() int { return len(g.edges) }

// Neighbors returns fileID's outbound edges, or nil if fileID has no adjacency entry in
// this graph. The returned slice is a copy: callers may not mutate g's internal storage
// through it.
func (g *CSRGraph) Neighbors(fileID uint64) []CSREdge {
	i := sort.Search(len(g.nodeIDs), func(i int) bool { return g.nodeIDs[i] >= fileID })
	if i >= len(g.nodeIDs) || g.nodeIDs[i] != fileID {
		return nil
	}
	start, end := g.offsets[i], g.offsets[i+1]
	if start == end {
		return nil
	}
	out := make([]CSREdge, end-start)
	copy(out, g.edges[start:end])
	return out
}

// WriteCSR atomically writes g to path in this package's CSR format. It writes to a
// temporary sibling file first, fsyncs it, then renames it into place, so a crash mid-write
// can never leave a torn/partial graph.dat visible at path (rename is atomic on the same
// filesystem) — mirroring engine/catalog/content.go's writeContentFile convention for
// whole-file durable writes.
func WriteCSR(path string, g *CSRGraph) error {
	payload := encodeCSRPayload(g)

	header := make([]byte, csrHeaderSize)
	copy(header[offCSRMagic:], csrMagic[:])
	binary.LittleEndian.PutUint32(header[offCSRVersion:], csrFormatVersion)
	binary.LittleEndian.PutUint64(header[offCSRNodeCount:], uint64(len(g.nodeIDs)))
	binary.LittleEndian.PutUint64(header[offCSREdgeCount:], uint64(len(g.edges)))
	binary.LittleEndian.PutUint32(header[offCSRPayloadCRC:], crc32.ChecksumIEEE(payload))

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "graph.dat.*.tmp")
	if err != nil {
		return fmt.Errorf("graph: creating temp CSR file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(header); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("graph: writing CSR header to %s: %w", tmpPath, err)
	}
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("graph: writing CSR payload to %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("graph: syncing CSR file %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("graph: closing CSR file %s: %w", tmpPath, err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("graph: renaming %s to %s: %w", tmpPath, path, err)
	}

	return nil
}

// LoadCSR reads back a CSRGraph previously written by WriteCSR from path, validating the
// header's magic/version and the payload's CRC32 checksum. A corrupted or truncated file
// (including a torn header) is reported as an error, never silently mis-decoded.
func LoadCSR(path string) (*CSRGraph, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("graph: reading CSR file %s: %w", path, err)
	}

	if len(data) < csrHeaderSize {
		return nil, fmt.Errorf("graph: CSR file %s too short: got %d bytes, want at least %d-byte header", path, len(data), csrHeaderSize)
	}
	header := data[:csrHeaderSize]
	payload := data[csrHeaderSize:]

	var magic [4]byte
	copy(magic[:], header[offCSRMagic:offCSRMagic+4])
	if magic != csrMagic {
		return nil, fmt.Errorf("graph: CSR file %s has invalid magic %q, want %q", path, magic, csrMagic)
	}

	version := binary.LittleEndian.Uint32(header[offCSRVersion:])
	if version != csrFormatVersion {
		return nil, fmt.Errorf("graph: CSR file %s has unsupported format version %d, want %d", path, version, csrFormatVersion)
	}

	nodeCount := binary.LittleEndian.Uint64(header[offCSRNodeCount:])
	edgeCount := binary.LittleEndian.Uint64(header[offCSREdgeCount:])
	wantCRC := binary.LittleEndian.Uint32(header[offCSRPayloadCRC:])

	if gotCRC := crc32.ChecksumIEEE(payload); gotCRC != wantCRC {
		return nil, fmt.Errorf("graph: CSR file %s failed payload CRC check (want %08x, got %08x)", path, wantCRC, gotCRC)
	}

	wantPayloadLen := nodeCount*8 + (nodeCount+1)*8 + edgeCount*csrEdgeEncodedSize
	if uint64(len(payload)) != wantPayloadLen {
		return nil, fmt.Errorf("graph: CSR file %s payload length mismatch: got %d bytes, want %d (nodeCount=%d, edgeCount=%d)", path, len(payload), wantPayloadLen, nodeCount, edgeCount)
	}

	off := uint64(0)
	nodeIDs := make([]uint64, nodeCount)
	for i := range nodeIDs {
		nodeIDs[i] = binary.LittleEndian.Uint64(payload[off:])
		off += 8
	}

	offsets := make([]uint64, nodeCount+1)
	for i := range offsets {
		offsets[i] = binary.LittleEndian.Uint64(payload[off:])
		off += 8
	}

	edges := make([]CSREdge, edgeCount)
	for i := range edges {
		edges[i] = decodeCSREdge(payload[off : off+csrEdgeEncodedSize])
		off += csrEdgeEncodedSize
	}

	return &CSRGraph{nodeIDs: nodeIDs, offsets: offsets, edges: edges}, nil
}

// encodeCSRPayload serializes g's nodeIDs, offsets, and edges arrays (in that order) into a
// single contiguous byte slice, matching the layout LoadCSR expects.
func encodeCSRPayload(g *CSRGraph) []byte {
	size := len(g.nodeIDs)*8 + len(g.offsets)*8 + len(g.edges)*csrEdgeEncodedSize
	buf := make([]byte, size)

	off := 0
	for _, id := range g.nodeIDs {
		binary.LittleEndian.PutUint64(buf[off:], id)
		off += 8
	}
	for _, o := range g.offsets {
		binary.LittleEndian.PutUint64(buf[off:], o)
		off += 8
	}
	for _, e := range g.edges {
		e.encode(buf[off : off+csrEdgeEncodedSize])
		off += csrEdgeEncodedSize
	}

	return buf
}
