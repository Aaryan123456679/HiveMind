package wal

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Replay is this package's recovery entrypoint: on startup, a caller invokes
// Replay(dir, apply) to reapply every mutation that was durably logged to the
// WAL rooted at dir but is not yet reflected in checkpointed state, in
// on-disk order, exactly once, before resuming normal operation (e.g. before
// calling OpenWriter to resume appending).
//
// Replay reads dir's checkpoint pointer via LoadCheckpoint. If no checkpoint
// exists yet (found=false — a fresh WAL), replay starts from the very
// beginning: segment 0, offset 0. Otherwise it starts at the checkpoint's
// own segment, skipping every record before OffsetInSegment within that
// segment (those are already durably applied and must not be re-applied),
// and replaying every record from OffsetInSegment onward in that segment,
// then every record in full in each subsequent segment, in ascending
// segment-number order.
//
// For each record, Replay decodes it as a TypedRecord and validates that its
// Type is one of the package's known valid record types, rejecting
// RecordTypeInvalid (the reserved zero value) and any unrecognized type byte
// with a hard error rather than silently skipping or silently succeeding —
// this closes a gap flagged during subtask 1.3.2's verification, where
// DecodeTypedRecord itself performs no such validation. Only once a record
// passes this check is apply invoked with it, in order, exactly once. If
// apply is nil, Replay still validates every record but performs no
// callback (useful for a dry-run integrity check).
//
// If the checkpoint pointer already covers everything durably written (its
// segment is the last existing segment and its offset already equals that
// segment's current on-disk size), there is nothing left to replay: no
// segment yields any records at or past that point, so apply is never
// invoked and Replay returns nil. This is a correct no-op, not a special
// case requiring separate handling — it falls out of the same general loop
// used for the non-empty case.
//
// Replay does not itself decode a TypedRecord's payload into a concrete
// engine/catalog or engine/btree mutation and apply it to a live store; that
// real wiring (turning a CatalogPutPayload/CatalogDeletePayload/
// BTreeInsertPayload/BTreeDeletePayload back into an actual catalog/B+Tree
// mutation) is the responsibility of the caller-supplied apply callback,
// deferred to the later subtask(s) that integrate this package with
// engine/catalog and engine/btree. This mirrors record.go's AppendAndApply,
// which similarly takes an opaque apply func() error without this package
// importing engine/catalog or engine/btree.
func Replay(dir string, apply func(TypedRecord) error) error {
	segNum, offset, found, err := LoadCheckpoint(dir)
	if err != nil {
		return fmt.Errorf("wal: replay: loading checkpoint in %s: %w", dir, err)
	}
	if !found {
		segNum, offset = 0, 0
	}

	segments, err := listSegmentNumbers(dir)
	if err != nil {
		return fmt.Errorf("wal: replay: listing segments in %s: %w", dir, err)
	}
	if len(segments) == 0 {
		// A fresh WAL directory with no segment files at all: nothing has
		// ever been written, so there is nothing to replay.
		return nil
	}

	lastSegment := segments[len(segments)-1]
	if segNum > lastSegment {
		return fmt.Errorf("wal: replay: checkpoint segment %d in %s is beyond the last existing segment %d; on-disk state is inconsistent with the checkpoint", segNum, dir, lastSegment)
	}

	for _, n := range segments {
		if n < segNum {
			// Fully covered by the checkpoint (and, per checkpoint.go's
			// ArchivableSegments semantics, eligible for archival): every
			// record in this segment was already durably applied prior to
			// the checkpoint being taken. Replaying it would double-apply.
			continue
		}

		startOffset := int64(0)
		if n == segNum {
			startOffset = offset
		}

		path := segmentPath(dir, int(n))
		records, err := readSegmentFrom(path, startOffset)
		if err != nil {
			return fmt.Errorf("wal: replay: reading segment %d in %s: %w", n, dir, err)
		}

		for _, raw := range records {
			rec, err := DecodeTypedRecord(raw)
			if err != nil {
				return fmt.Errorf("wal: replay: decoding record in segment %d: %w", n, err)
			}
			if !isValidRecordType(rec.Type) {
				return fmt.Errorf("wal: replay: segment %d contains a record with invalid or unrecognized type %s; refusing to replay", n, rec.Type)
			}
			if apply != nil {
				if err := apply(rec); err != nil {
					return fmt.Errorf("wal: replay: applying %s record from segment %d: %w", rec.Type, n, err)
				}
			}
		}
	}

	return nil
}

// isValidRecordType reports whether t is one of this package's known,
// non-reserved RecordType values. RecordTypeInvalid (the reserved zero
// value) and any byte outside the known set are both invalid.
func isValidRecordType(t RecordType) bool {
	switch t {
	case RecordCatalogPut, RecordCatalogDelete, RecordBTreeInsert, RecordBTreeDelete:
		return true
	default:
		return false
	}
}

// readSegmentFrom parses the segment file at path starting at byte offset
// startOffset, returning the payload of every record from that point to the
// end of the file, in on-disk order. It errors on a truncated header, a
// truncated payload, or a payload whose CRC32 does not match its header —
// the same integrity checks as ReadSegment (writer.go), which parses a
// segment in full from offset 0; readSegmentFrom differs only in accepting
// an arbitrary starting offset, needed so recovery can skip records already
// covered by a checkpoint that lands partway through a segment.
//
// startOffset must land exactly on a record boundary (as every
// CheckpointPointer.OffsetInSegment does, by construction: it is always one
// of Writer.Append's returned per-record offsets). If startOffset equals the
// file's total size, readSegmentFrom returns an empty slice and a nil error
// (nothing left in this segment to replay).
func readSegmentFrom(path string, startOffset int64) ([][]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("wal: reading segment %s: %w", path, err)
	}

	if startOffset < 0 || int(startOffset) > len(data) {
		return nil, fmt.Errorf("wal: segment %s: start offset %d out of range (segment is %d bytes)", path, startOffset, len(data))
	}

	var records [][]byte
	off := int(startOffset)
	for off < len(data) {
		if off+recordHeaderSize > len(data) {
			return nil, fmt.Errorf("wal: segment %s: truncated record header at offset %d (%d bytes remain, need %d)", path, off, len(data)-off, recordHeaderSize)
		}

		length := binary.LittleEndian.Uint32(data[off+offRecordLength:])
		wantCRC := binary.LittleEndian.Uint32(data[off+offRecordCRC:])

		payloadStart := off + recordHeaderSize
		payloadEnd := payloadStart + int(length)
		if payloadEnd > len(data) {
			return nil, fmt.Errorf("wal: segment %s: truncated record payload at offset %d (declared length %d, %d bytes remain)", path, off, length, len(data)-payloadStart)
		}

		payload := data[payloadStart:payloadEnd]
		if gotCRC := crc32.ChecksumIEEE(payload); gotCRC != wantCRC {
			return nil, fmt.Errorf("wal: segment %s: record at offset %d failed CRC check (want %08x, got %08x)", path, off, wantCRC, gotCRC)
		}

		out := make([]byte, len(payload))
		copy(out, payload)
		records = append(records, out)

		off = payloadEnd
	}

	return records, nil
}

// listSegmentNumbers scans dir for "wal-<N>.log" segment files and returns
// every N found, sorted ascending. It returns an empty (nil) slice, not an
// error, if dir contains no segment files. Files that don't match the
// "wal-<N>.log" naming convention are ignored, consistent with
// writer.go's latestSegmentNum and checkpoint.go's ArchivableSegments (both
// of which tolerate unrelated files sharing the same directory).
func listSegmentNumbers(dir string) ([]uint64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("wal: reading dir %s: %w", dir, err)
	}

	var found []uint64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, segmentFilePrefix) || !strings.HasSuffix(name, segmentFileSuffix) {
			continue
		}
		numStr := strings.TrimSuffix(strings.TrimPrefix(name, segmentFilePrefix), segmentFileSuffix)
		num, err := strconv.ParseUint(numStr, 10, 64)
		if err != nil {
			continue
		}
		found = append(found, num)
	}

	sort.Slice(found, func(i, j int) bool { return found[i] < found[j] })
	return found, nil
}
