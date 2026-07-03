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
// docs/LLD/wal.md: every Append fsyncs the record to disk before returning,
// matching this repo's WriteAt+Sync durability idiom (see
// engine/catalog/file.go's FileManager.WritePage and
// engine/catalog/idalloc.go's IDAllocator.Next).
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

	flags := os.O_RDWR | os.O_CREATE
	if resuming {
		flags |= os.O_APPEND
	}

	path := segmentPath(dir, segmentNum)
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return nil, fmt.Errorf("wal: opening segment %s: %w", path, err)
	}

	var size int64
	if resuming {
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("wal: stat segment %s: %w", path, err)
		}
		size = info.Size()
	}

	return &Writer{
		dir:             dir,
		maxSegmentBytes: maxSegmentBytes,
		segmentNum:      segmentNum,
		file:            f,
		size:            size,
	}, nil
}

// segmentPath returns the conventional on-disk path for segment n within dir.
func segmentPath(dir string, n int) string {
	return filepath.Join(dir, fmt.Sprintf("%s%d%s", segmentFilePrefix, n, segmentFileSuffix))
}

// latestSegmentNum scans dir for existing "wal-<N>.log" files and returns the
// highest N found along with resuming=true, or (0, false, nil) if dir
// contains no segment files yet (a brand-new WAL).
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
		return 0, false, nil
	}

	sort.Ints(found)
	return found[len(found)-1], true, nil
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
// payload of every record in on-disk order. It returns an error if the file
// contains a truncated header, a truncated payload, or a payload whose CRC32
// does not match its header — all of which indicate a torn/corrupted record.
// This exists both to support this subtask's own tests (verifying that a
// segment's own bytes fully and cleanly parse, with no split records) and as
// a starting point for subtask 1.3.4's recovery replay and 1.3.5's
// crash-injection detection.
func ReadSegment(path string) ([][]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("wal: reading segment %s: %w", path, err)
	}

	var records [][]byte
	off := 0
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

		// Copy the payload out so callers don't retain a reference into the
		// full file buffer for every record.
		out := make([]byte, len(payload))
		copy(out, payload)
		records = append(records, out)

		off = payloadEnd
	}

	return records, nil
}
