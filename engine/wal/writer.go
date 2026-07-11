package wal

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// recordHeaderSize is the fixed size, in bytes, of the on-disk header that
// precedes every record's payload:
//
//	[0:4] uint32 LE  length of the payload in bytes
//	[4:8] uint32 LE  CRC32 (IEEE) checksum of the payload bytes
//
// This mirrors this repo's established length-prefixed binary encoding
// convention (see engine/btree/node.go's key encoding, engine/catalog's
// little-endian fixed-header layouts) with a checksum added so record
// integrity can be verified on read — a property future subtasks (1.3.5:
// crash-injection/torn-record recovery) depend on directly.
const recordHeaderSize = 8

const (
	offRecordLength = 0
	offRecordCRC    = 4
)

// segmentFilePrefix and segmentFileSuffix together define this package's
// segment-naming convention: "wal-<N>.log", where N is a plain (not
// zero-padded) monotonically increasing base-10 integer starting at 0 for a
// brand-new WAL directory. This lives inside whatever directory the caller
// passes to OpenWriter (conventionally a "wal/" directory relative to the
// engine's data root, matching the acceptance criterion's
// "wal/wal-<segment>.log" path shape); this package itself only owns the
// "wal-<N>.log" filename convention within that directory.
const (
	segmentFilePrefix = "wal-"
	segmentFileSuffix = ".log"
)

// Writer appends records to an append-only, size-rotated sequence of WAL
// segment files. It is the durability mechanism described in
// docs/LLD/wal.md: every Append writes the record (header, then payload)
// via a plain sequential file.Write at the file's current append position,
// then calls file.Sync before returning -- so no Append call returns until
// its record is durably on disk.
//
// This is a plain sequential-write-then-Sync idiom, not engine/catalog's
// WriteAt+Sync idiom (see engine/catalog/file.go's FileManager.WritePage
// and engine/catalog/idalloc.go's IDAllocator.Next): catalog's idiom writes
// to a computed absolute offset within a fixed-layout, random-access file
// (pages/slots), whereas Append only ever writes at end-of-file in an
// append-only log and never seeks to an arbitrary offset. Both are
// reasonable, deliberate choices for their respective access patterns; this
// file's approach should not be read as mirroring catalog's WriteAt-based one.
//
// Writer does NOT define record semantics/types (deferred to subtask 1.3.2)
// and does NOT perform crash-recovery / torn-tail validation of a resumed
// segment's existing bytes (deferred to subtasks 1.3.4/1.3.5). OpenWriter
// only guarantees correct segment-number continuation when reopening an
// existing WAL directory, so a second writer never clobbers prior segments.
//
// Writer is safe for concurrent use by multiple goroutines: Append
// serializes the append-or-rotate critical section under a single mutex,
// matching the narrow, single-purpose locking style of IDAllocator (a plain
// mutex, not a striped/global lock) since every call must synchronously
// perform durable I/O anyway.
type Writer struct {
	mu sync.Mutex

	dir             string
	maxSegmentBytes int64

	segmentNum int
	file       *os.File
	size       int64 // current segment file's on-disk size, guarded by mu
}

// OpenWriter opens (creating if necessary) a WAL writer rooted at dir, with
// segments rotated once they would exceed maxSegmentBytes. dir is created
// (including parents) if it does not already exist.
//
// If dir already contains segment files from a prior run, OpenWriter resumes
// numbering from the highest-numbered existing segment (reopened in append
// mode, with its current size restored) rather than starting over at 0 or
// overwriting existing data. It does not validate the resumed segment's tail
// for torn/partial records — that is deferred to subtask 1.3.4's recovery
// replay and 1.3.5's crash-injection handling.
func OpenWriter(dir string, maxSegmentBytes int64) (*Writer, error) {
	if maxSegmentBytes <= recordHeaderSize {
		return nil, fmt.Errorf("wal: maxSegmentBytes %d must be greater than record header size %d", maxSegmentBytes, recordHeaderSize)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("wal: creating dir %s: %w", dir, err)
	}

	segmentNum, resuming, err := latestSegmentNum(dir)
	if err != nil {
		return nil, err
	}

	path := segmentPath(dir, segmentNum)

	var size int64
	if resuming {
		// Subtask 1.3.5: a resumed segment may end in a torn record if the
		// prior process crashed mid-Append (a header claiming N payload
		// bytes with fewer than N actually on disk, or a header itself cut
		// short). Validate the resumed segment's own bytes BEFORE reopening
		// it for append, and physically discard any torn tail by truncating
		// the file to the last valid record boundary — the same
		// detect-and-discard rule ReadSegment/Replay apply when parsing
		// (see ReadSegment's doc comment below), so a resumed Writer's
		// on-disk state and a fresh Replay's view of that same directory
		// always agree on where "the last valid record" ends.
		//
		// A CRC mismatch on a full-length record, by contrast, is genuine
		// corruption, not a torn write, and is NOT silently discarded here:
		// OpenWriter fails closed with a clear error rather than resuming
		// (and therefore appending) onto a segment already known to be
		// corrupt.
		validSize, truncated, verr := repairTornTail(path)
		if verr != nil {
			return nil, fmt.Errorf("wal: opening segment %s: %w", path, verr)
		}
		if truncated {
			if err := os.Truncate(path, validSize); err != nil {
				return nil, fmt.Errorf("wal: truncating torn tail of segment %s to %d bytes: %w", path, validSize, err)
			}
		}
		size = validSize
	}

	flags := os.O_RDWR | os.O_CREATE
	if resuming {
		flags |= os.O_APPEND
	}

	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return nil, fmt.Errorf("wal: opening segment %s: %w", path, err)
	}

	return &Writer{
		dir:             dir,
		maxSegmentBytes: maxSegmentBytes,
		segmentNum:      segmentNum,
		file:            f,
		size:            size,
	}, nil
}

// repairTornTail validates the segment file at path against this package's
// crash-tolerant parsing rule (see ReadSegment's doc comment below) and
// reports the byte length it should be truncated to, if any torn tail is
// present. It does not itself modify path; a caller that wants the torn
// tail physically discarded must truncate to the returned validSize itself
// when truncated is true. A CRC mismatch (genuine corruption, not a torn
// write) is returned as a hard error instead of a truncation instruction.
func repairTornTail(path string) (validSize int64, truncated bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false, fmt.Errorf("resumed segment %s: %w", path, err)
	}

	_, validEnd, tornTail, err := parseSegmentRecords(data, 0)
	if err != nil {
		return 0, false, fmt.Errorf("resumed segment %s failed integrity validation (corrupt record, not just a torn write): %w", path, err)
	}

	return int64(validEnd), tornTail, nil
}

// segmentPath returns the conventional on-disk path for segment n within dir.
func segmentPath(dir string, n int) string {
	return filepath.Join(dir, fmt.Sprintf("%s%d%s", segmentFilePrefix, n, segmentFileSuffix))
}

// latestSegmentNum scans dir for existing "wal-<N>.log" files and returns the
// highest N found along with resuming=true, or (0, false, nil) if dir
// contains no segment files yet (a brand-new WAL) and no segment-floor
// marker (see WriteSegmentFloor) has been recorded for dir.
//
// If dir currently has no segment files but a caller previously called
// WriteSegmentFloor(dir, floor) - meaning dir's segment files existed once,
// were fully removed by that caller (e.g. graph.EdgeLog.TruncateNode), and
// segment numbering must not restart at 0 because something else durably
// recorded facts about segment numbers <= floor-1 - segmentNum starts at
// that floor instead. As soon as any real segment file exists, that file's
// own number takes precedence and the floor marker is ignored entirely (it
// has already served its purpose for that segment's lifetime).
func latestSegmentNum(dir string) (n int, resuming bool, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, false, fmt.Errorf("wal: reading dir %s: %w", dir, err)
	}

	var found []int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, segmentFilePrefix) || !strings.HasSuffix(name, segmentFileSuffix) {
			continue
		}
		numStr := strings.TrimSuffix(strings.TrimPrefix(name, segmentFilePrefix), segmentFileSuffix)
		num, err := strconv.Atoi(numStr)
		if err != nil {
			// Not a segment file we recognize; ignore rather than error, in
			// case the directory holds other, unrelated files.
			continue
		}
		found = append(found, num)
	}

	if len(found) == 0 {
		floor, ferr := readSegmentFloor(dir)
		if ferr != nil {
			if !os.IsNotExist(ferr) {
				return 0, false, ferr
			}
			return 0, false, nil
		}
		if floor > 0 {
			return floor, false, nil
		}
		return 0, false, nil
	}

	sort.Ints(found)
	return found[len(found)-1], true, nil
}

// segmentFloorFile is the name of a small control file, sibling to a WAL
// directory's segment files, written by WriteSegmentFloor.
const segmentFloorFile = ".segment-floor"

// WriteSegmentFloor durably (temp file + fsync + atomic rename, matching
// this package's own segment-file durability idiom) records floor as the
// minimum segment number a future OpenWriter call against dir must use once
// dir's existing segment files have all been removed.
//
// This exists for callers that fully truncate (remove every existing
// segment file from) a WAL directory but must prevent a subsequent
// OpenWriter call from restarting segment numbering at 0 - because some
// other durable record elsewhere (e.g. engine/graph/compact.go's
// compact-state sidecar) already refers to segment numbers up through
// floor-1 as "already accounted for", and reusing one of those numbers for
// new, not-yet-accounted-for data would let that other record incorrectly
// treat the new data as already covered. See engine/graph/edgelog.go's
// TruncateNode, the first caller of this function, for the full rationale
// and the ordering (floor written BEFORE segment files are removed) that
// makes this safe even if the calling process crashes partway through.
//
// WriteSegmentFloor is monotonic: if dir already has a recorded floor >=
// floor, the existing (higher or equal) value is left untouched rather than
// being lowered - a caller computing floor from a possibly-stale segment
// listing must never be able to regress an already-published floor.
func WriteSegmentFloor(dir string, floor int) error {
	if floor < 0 {
		return fmt.Errorf("wal: segment floor must be >= 0, got %d", floor)
	}

	if existing, err := readSegmentFloor(dir); err == nil {
		if existing >= floor {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	path := filepath.Join(dir, segmentFloorFile)
	tmp, err := os.CreateTemp(dir, segmentFloorFile+".*.tmp")
	if err != nil {
		return fmt.Errorf("wal: creating temp segment floor file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(strconv.Itoa(floor)); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("wal: writing segment floor file in %s: %w", dir, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("wal: syncing segment floor file in %s: %w", dir, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("wal: closing segment floor file in %s: %w", dir, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("wal: renaming segment floor file in %s: %w", dir, err)
	}
	return nil
}

// readSegmentFloor reads dir's segment floor file, if any, returning
// (0, an os.IsNotExist-satisfying error) if dir has no recorded floor.
func readSegmentFloor(dir string) (int, error) {
	data, err := os.ReadFile(filepath.Join(dir, segmentFloorFile))
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("wal: malformed segment floor file in %s: %w", dir, err)
	}
	return n, nil
}

// Append writes payload as a new record to the current segment, rotating to
// a fresh segment first if appending would exceed maxSegmentBytes. It
// returns the byte offset within the segment file where the record's header
// begins (callers needing to also know which segment number that offset
// belongs to should track Writer.SegmentNum() alongside it, or rely on
// future subtasks' record-type layer to carry that pairing).
//
// A single record whose encoded size (header + payload) exceeds
// maxSegmentBytes can never be written without either splitting it across
// segments or silently truncating it — both of which this package refuses
// to do. Such a call returns a hard error and writes nothing, matching this
// repo's established "hard-error, not truncate, on overflow" idiom (see
// engine/btree/node.go's encode functions).
//
// Append fsyncs the record to disk before returning.
func (w *Writer) Append(payload []byte) (offset int64, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	total := int64(recordHeaderSize) + int64(len(payload))
	if total > w.maxSegmentBytes {
		return 0, fmt.Errorf("wal: record of %d bytes (header+payload) exceeds maxSegmentBytes %d: cannot write without splitting across segments", total, w.maxSegmentBytes)
	}

	// Rotate BEFORE writing if this record would overflow the current
	// segment. A record is never partially written to one segment and
	// continued in the next.
	if w.size > 0 && w.size+total > w.maxSegmentBytes {
		if err := w.rotateLocked(); err != nil {
			return 0, err
		}
	}

	var header [recordHeaderSize]byte
	binary.LittleEndian.PutUint32(header[offRecordLength:], uint32(len(payload)))
	binary.LittleEndian.PutUint32(header[offRecordCRC:], crc32.ChecksumIEEE(payload))

	offset = w.size

	if _, err := w.file.Write(header[:]); err != nil {
		return 0, fmt.Errorf("wal: writing record header to segment %d: %w", w.segmentNum, err)
	}
	if len(payload) > 0 {
		if _, err := w.file.Write(payload); err != nil {
			return 0, fmt.Errorf("wal: writing record payload to segment %d: %w", w.segmentNum, err)
		}
	}
	if err := w.file.Sync(); err != nil {
		return 0, fmt.Errorf("wal: syncing segment %d: %w", w.segmentNum, err)
	}

	w.size += total
	return offset, nil
}

// rotateLocked closes the current segment and opens the next one, resetting
// size bookkeeping. Callers must hold w.mu.
func (w *Writer) rotateLocked() error {
	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("wal: syncing segment %d before rotation: %w", w.segmentNum, err)
	}
	if err := w.file.Close(); err != nil {
		return fmt.Errorf("wal: closing segment %d during rotation: %w", w.segmentNum, err)
	}

	nextNum := w.segmentNum + 1
	path := segmentPath(w.dir, nextNum)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("wal: opening segment %s during rotation: %w", path, err)
	}

	w.segmentNum = nextNum
	w.file = f
	w.size = 0
	return nil
}

// SegmentNum returns the number of the segment Writer is currently appending
// to.
func (w *Writer) SegmentNum() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.segmentNum
}

// Offset returns the number of bytes already durably written to the segment
// Writer is currently appending to (equivalently, the byte offset the NEXT
// Append call will return). Combined with SegmentNum(), this gives callers
// exactly the (segment, offset) pair checkpoint.go's CheckpointPointer /
// Checkpoint expect, e.g.:
//
//	Checkpoint(dir, uint64(w.SegmentNum()), w.Offset())
//
// This is a small, deliberately minimal addition for subtask 1.3.5: a real
// checkpoint caller needs a way to read Writer's current position without
// reaching into its unexported size field; it is not a broader API
// refactor.
func (w *Writer) Offset() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.size
}

// Close closes the current segment file. It does not fsync (Append already
// fsyncs after every write), but does flush the file descriptor via the
// underlying os.File.Close.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.file.Close(); err != nil {
		return fmt.Errorf("wal: closing segment %d: %w", w.segmentNum, err)
	}
	return nil
}

// ReadSegment parses a single segment file at path in full, returning the
// payload of every record in on-disk order.
//
// Subtask 1.3.5 (crash-injection recovery) establishes this package's
// crash-tolerant parsing rule, applied here via parseSegmentRecords: a
// truncated header or truncated payload at the tail of the file — exactly
// what a crash mid-Append produces, since Append only ever writes a header
// then its payload, in that order, and nothing else is ever appended after
// them until the next record — is treated as an incomplete write, NOT an
// error: parsing stops cleanly at that point and the records parsed so far
// are returned with a nil error. This directly implements the literal 1.3.5
// acceptance criterion ("the torn record is detected and discarded, and
// recovery proceeds from the last valid record").
//
// A payload whose CRC32 does not match its header, by contrast, is a
// different failure mode: a full-length record cannot be produced by a
// crash mid-write (the crash leaves a record short, never full-length with
// flipped bits), so a CRC mismatch indicates real bit-level corruption. This
// remains a hard error, closing the gap flagged in 1.3.4's verification (no
// CRC-corruption-through-Replay test existed): ReadSegment/Replay must never
// silently treat genuine corruption as if it were just a torn tail.
func ReadSegment(path string) ([][]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("wal: reading segment %s: %w", path, err)
	}

	records, _, _, err := parseSegmentRecords(data, 0)
	if err != nil {
		return nil, fmt.Errorf("wal: segment %s: %w", path, err)
	}
	return records, nil
}

// parseSegmentRecords parses records from data starting at byte offset
// startOffset, in on-disk order, applying this package's crash-tolerant
// parsing rule (see ReadSegment's doc comment above for the
// torn-tail-vs-CRC-corruption distinction this implements):
//
//   - A truncated header or truncated payload at the tail of data stops
//     parsing cleanly: the records parsed so far are returned with a nil
//     error, tornTail=true, and validEnd set to the byte offset immediately
//     before the torn bytes (i.e. what the segment should be truncated to,
//     to physically discard them).
//   - A payload whose CRC32 does not match its header's checksum is a hard
//     error: parsing stops immediately, with records/validEnd reflecting
//     only what was parsed strictly before the corrupt record.
//   - Reaching len(data) exactly (off == len(data)) ends the loop normally:
//     records, validEnd=len(data), tornTail=false, err=nil.
//
// Shared by ReadSegment (starts at offset 0) and recovery.go's
// readSegmentFrom (starts at an arbitrary checkpoint-relative offset), so
// both this package's own tests and 1.3.4's recovery replay observe
// identical torn-tail/corruption semantics.
func parseSegmentRecords(data []byte, startOffset int) (records [][]byte, validEnd int, tornTail bool, err error) {
	off := startOffset
	for off < len(data) {
		if off+recordHeaderSize > len(data) {
			return records, off, true, nil
		}

		length := binary.LittleEndian.Uint32(data[off+offRecordLength:])
		wantCRC := binary.LittleEndian.Uint32(data[off+offRecordCRC:])

		payloadStart := off + recordHeaderSize
		payloadEnd := payloadStart + int(length)
		if payloadEnd > len(data) {
			return records, off, true, nil
		}

		payload := data[payloadStart:payloadEnd]
		if gotCRC := crc32.ChecksumIEEE(payload); gotCRC != wantCRC {
			return records, off, false, fmt.Errorf("record at offset %d failed CRC check (want %08x, got %08x)", off, wantCRC, gotCRC)
		}

		// Copy the payload out so callers don't retain a reference into the
		// full file buffer for every record.
		out := make([]byte, len(payload))
		copy(out, payload)
		records = append(records, out)

		off = payloadEnd
	}

	return records, off, false, nil
}
