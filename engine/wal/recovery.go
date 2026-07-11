package wal

import (
	"fmt"
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
		// readErr (a CRC-corruption error; see writer.go's parseSegmentRecords)
		// is intentionally NOT checked here, before applying records: even
		// when readSegmentFrom hits a corrupt record and stops, the records
		// slice it returns already contains every record parsed strictly
		// before the corrupt one, and those must still be applied, in
		// order, exactly once — replay must not discard already-valid,
		// already-durable mutations just because a later record in the same
		// segment turned out to be corrupt. readErr is surfaced below, once
		// everything parsed so far has been applied.
		records, tornTail, readErr := readSegmentFrom(path, startOffset)
		if tornTail && n != lastSegment {
			// A torn tail can only legitimately arise in the segment a
			// crashed process was actively writing to at the moment of the
			// crash — necessarily the highest-numbered (last) segment.
			// Finding one in an earlier segment means something other than
			// "the tail of the WAL was cut short by a crash", so this is
			// treated as a hard on-disk inconsistency rather than silently
			// discarded like a genuine torn tail.
			return fmt.Errorf("wal: replay: segment %d in %s ends in a torn record but is not the last segment (%d); on-disk state is inconsistent", n, dir, lastSegment)
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

		if readErr != nil {
			// A hard parse error (genuine CRC corruption; a torn tail never
			// reaches this point, since readSegmentFrom reports that via
			// tornTail with a nil error, not readErr). Every record parsed
			// strictly before the corrupt one has already been applied
			// above; surface the error now so the caller knows replay
			// stopped early and did not cover the rest of the WAL.
			return fmt.Errorf("wal: replay: reading segment %d in %s: %w", n, dir, readErr)
		}
	}

	return nil
}

// isValidRecordType reports whether t is one of this package's known,
// non-reserved RecordType values. RecordTypeInvalid (the reserved zero
// value) and any byte outside the known set are both invalid.
func isValidRecordType(t RecordType) bool {
	switch t {
	case RecordCatalogPut, RecordCatalogDelete, RecordBTreeInsert, RecordBTreeDelete, RecordSplitCommit:
		return true
	default:
		return false
	}
}

// readSegmentFrom parses the segment file at path starting at byte offset
// startOffset, returning the payload of every record from that point,
// applying this package's crash-tolerant parsing rule (see writer.go's
// parseSegmentRecords doc comment for the torn-tail-vs-CRC-corruption
// distinction): a torn tail (truncated header or truncated payload at the
// end of the file) stops parsing cleanly and is reported via tornTail=true
// with a nil error; a CRC mismatch on a full-length record is a hard error.
//
// writer.go's ReadSegment is a thin wrapper around this function
// (readSegmentFrom(path, 0)), so that the two no longer duplicate the
// read-file-then-parse logic that previously lived separately in each
// (subtask 4.5.14.4). readSegmentFrom's only reason to exist as a distinct,
// lower-level function is accepting an arbitrary starting offset, needed so
// Replay can skip records already covered by a checkpoint that lands
// partway through a segment; ReadSegment's callers only ever need to read a
// segment from its start and don't need the tornTail flag, so ReadSegment
// discards it.
//
// startOffset must land exactly on a record boundary (as every
// CheckpointPointer.OffsetInSegment does, by construction: it is always one
// of Writer.Append's/Writer.Offset's returned per-record offsets). If
// startOffset equals the file's total size, readSegmentFrom returns an
// empty slice, tornTail=false, and a nil error (nothing left in this
// segment to replay).
func readSegmentFrom(path string, startOffset int64) (records [][]byte, tornTail bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("wal: reading segment %s: %w", path, err)
	}

	if startOffset < 0 || int(startOffset) > len(data) {
		return nil, false, fmt.Errorf("wal: segment %s: start offset %d out of range (segment is %d bytes)", path, startOffset, len(data))
	}

	records, _, tornTail, err = parseSegmentRecords(data, int(startOffset))
	if err != nil {
		return records, tornTail, fmt.Errorf("wal: segment %s: %w", path, err)
	}
	return records, tornTail, nil
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
