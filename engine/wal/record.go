package wal

import (
	"encoding/binary"
	"fmt"
)

// RecordType identifies which catalog/index mutation a TypedRecord's payload
// encodes. This is the minimal, directly-traceable set of mutation kinds the
// WAL exists to protect (see docs/LLD/wal.md's invariant: "every mutation to
// the catalog or any index ... must be logged in the WAL before it is
// applied"): a catalog Put/Delete (engine/catalog) and a B+Tree index
// Insert/Delete (engine/btree). Subtask 1.3.4's recovery replay dispatches on
// this byte to decide which mutation to redo.
//
// 0 is intentionally left unused as an invalid/zero-value sentinel, matching
// this repo's convention of not giving on-disk enums a meaningful zero value
// (see engine/catalog/record.go's RecordStatus, which does start at 0 for a
// different reason — but here an unset RecordType would otherwise silently
// decode as a valid CatalogPut, which is worse than refusing to decode at
// all).
type RecordType byte

const (
	// RecordTypeInvalid is the zero value and never a valid on-disk record type.
	RecordTypeInvalid RecordType = 0

	// RecordCatalogPut represents a catalog record being created or updated.
	RecordCatalogPut RecordType = 1

	// RecordCatalogDelete represents a catalog record being removed.
	RecordCatalogDelete RecordType = 2

	// RecordBTreeInsert represents a B+Tree index key being inserted.
	RecordBTreeInsert RecordType = 3

	// RecordBTreeDelete represents a B+Tree index key being removed.
	RecordBTreeDelete RecordType = 4

	// RecordSplitCommit represents subtask 2b.3.6's ("Commit entire split as
	// a single WAL-covered, fsynced transaction") atomic split-commit point:
	// one record that durably describes everything a completed
	// engine/split auto-split must make true — the final (post-split)
	// catalog record for the original fileID, plus every (newPath, newFileID)
	// pair the split produced. This is deliberately a single, self-contained
	// record (rather than, say, reusing RecordCatalogPut alongside a separate
	// B+Tree/graph record) so that ONE Writer.Append/fsync is the entire
	// split's point of no return: see split.ExecuteSplitAtomic and
	// split.RecoverSplitCommits, which is this record type's dedicated
	// recovery pass (catalog.RecoverFromWAL deliberately skips it, exactly as
	// it already skips RecordBTreeInsert/RecordBTreeDelete today — see that
	// function's own doc comment on coexisting record types owned by other
	// packages).
	RecordSplitCommit RecordType = 5
)

// String returns a human-readable name for r, used in error messages.
func (r RecordType) String() string {
	switch r {
	case RecordCatalogPut:
		return "CatalogPut"
	case RecordCatalogDelete:
		return "CatalogDelete"
	case RecordBTreeInsert:
		return "BTreeInsert"
	case RecordBTreeDelete:
		return "BTreeDelete"
	case RecordSplitCommit:
		return "SplitCommit"
	default:
		return fmt.Sprintf("RecordType(%d)", byte(r))
	}
}

// recordTypeSize is the fixed width, in bytes, of the type tag prefixing
// every encoded TypedRecord.
const recordTypeSize = 1

// uint32LenSize is the fixed width, in bytes, of a length prefix for a
// variable-width field (a []byte blob or a string), matching this package's
// (writer.go's) little-endian length-prefix idiom.
const uint32LenSize = 4

// TypedRecord is the envelope this package hands to Writer.Append: a 1-byte
// RecordType tag followed by that type's kind-specific payload bytes. No
// additional length prefix is needed around the whole envelope because
// Writer.Append's own record header (see writer.go) already carries the
// total payload length and a CRC32 checksum.
type TypedRecord struct {
	Type    RecordType
	Payload []byte
}

// Encode serializes t into a single byte slice suitable for passing to
// Writer.Append (directly, or via AppendAndApply).
func (t TypedRecord) Encode() []byte {
	out := make([]byte, recordTypeSize+len(t.Payload))
	out[0] = byte(t.Type)
	copy(out[recordTypeSize:], t.Payload)
	return out
}

// DecodeTypedRecord parses data (as previously produced by TypedRecord.Encode,
// typically read back via ReadSegment) into a TypedRecord.
func DecodeTypedRecord(data []byte) (TypedRecord, error) {
	if len(data) < recordTypeSize {
		return TypedRecord{}, fmt.Errorf("wal: record too short to contain a type tag: got %d bytes, need at least %d", len(data), recordTypeSize)
	}
	t := RecordType(data[0])
	if t == RecordTypeInvalid || t > RecordSplitCommit {
		return TypedRecord{}, fmt.Errorf("wal: decode: invalid record type %d", byte(t))
	}
	return TypedRecord{
		Type:    t,
		Payload: data[recordTypeSize:],
	}, nil
}

// putUint32Prefixed appends a uint32 little-endian length prefix followed by
// b to buf, returning the extended slice.
func putUint32Prefixed(buf []byte, b []byte) []byte {
	var lenBuf [uint32LenSize]byte
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(b)))
	buf = append(buf, lenBuf[:]...)
	buf = append(buf, b...)
	return buf
}

// readUint32Prefixed reads a uint32 little-endian length prefix followed by
// that many bytes from data starting at offset off, returning the extracted
// bytes and the offset immediately following them.
func readUint32Prefixed(data []byte, off int) (b []byte, next int, err error) {
	if off+uint32LenSize > len(data) {
		return nil, 0, fmt.Errorf("wal: truncated length prefix at offset %d (%d bytes remain, need %d)", off, len(data)-off, uint32LenSize)
	}
	n := int(binary.LittleEndian.Uint32(data[off:]))
	start := off + uint32LenSize
	end := start + n
	if end > len(data) {
		return nil, 0, fmt.Errorf("wal: truncated length-prefixed field at offset %d (declared length %d, %d bytes remain)", off, n, len(data)-start)
	}
	return data[start:end], end, nil
}

// --- CatalogPut ---

// CatalogPutPayload is the payload of a RecordCatalogPut record: a catalog
// mutation that creates or updates the catalog entry for FileID. Record holds
// the already-encoded bytes of the corresponding catalog.CatalogRecord (i.e.
// the output of CatalogRecord.Encode()). This package deliberately does not
// import engine/catalog: it treats Record as an opaque length-prefixed blob,
// so the WAL layer stays agnostic to catalog-specific semantics — decoding
// those bytes back into a catalog.CatalogRecord is the recovery layer's
// (1.3.4's) job, since that is where catalog semantics are needed.
//
// FileID is carried as an explicit top-level field, even though it is also
// present inside the encoded Record bytes, so that recovery/dispatch code can
// key off it without first decoding the full catalog record.
type CatalogPutPayload struct {
	FileID uint64
	Record []byte
}

// Encode serializes p.
func (p CatalogPutPayload) Encode() []byte {
	buf := make([]byte, 0, 8+uint32LenSize+len(p.Record))
	var idBuf [8]byte
	binary.LittleEndian.PutUint64(idBuf[:], p.FileID)
	buf = append(buf, idBuf[:]...)
	buf = putUint32Prefixed(buf, p.Record)
	return buf
}

// DecodeCatalogPutPayload parses data as a CatalogPutPayload.
func DecodeCatalogPutPayload(data []byte) (CatalogPutPayload, error) {
	if len(data) < 8 {
		return CatalogPutPayload{}, fmt.Errorf("wal: CatalogPutPayload too short: got %d bytes, need at least 8", len(data))
	}
	fileID := binary.LittleEndian.Uint64(data[:8])
	record, _, err := readUint32Prefixed(data, 8)
	if err != nil {
		return CatalogPutPayload{}, fmt.Errorf("wal: decoding CatalogPutPayload.Record: %w", err)
	}
	out := make([]byte, len(record))
	copy(out, record)
	return CatalogPutPayload{FileID: fileID, Record: out}, nil
}

// NewCatalogPutRecord builds a ready-to-append TypedRecord for a catalog Put
// mutation.
func NewCatalogPutRecord(fileID uint64, encodedRecord []byte) TypedRecord {
	return TypedRecord{
		Type:    RecordCatalogPut,
		Payload: CatalogPutPayload{FileID: fileID, Record: encodedRecord}.Encode(),
	}
}

// AsCatalogPut decodes t's payload as a CatalogPutPayload. It returns an
// error if t.Type is not RecordCatalogPut.
func (t TypedRecord) AsCatalogPut() (CatalogPutPayload, error) {
	if t.Type != RecordCatalogPut {
		return CatalogPutPayload{}, fmt.Errorf("wal: AsCatalogPut called on record of type %s, want %s", t.Type, RecordCatalogPut)
	}
	return DecodeCatalogPutPayload(t.Payload)
}

// --- CatalogDelete ---

// CatalogDeletePayload is the payload of a RecordCatalogDelete record: a
// catalog mutation that removes the catalog entry for FileID.
type CatalogDeletePayload struct {
	FileID uint64
}

// Encode serializes p.
func (p CatalogDeletePayload) Encode() []byte {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, p.FileID)
	return buf
}

// DecodeCatalogDeletePayload parses data as a CatalogDeletePayload.
func DecodeCatalogDeletePayload(data []byte) (CatalogDeletePayload, error) {
	if len(data) != 8 {
		return CatalogDeletePayload{}, fmt.Errorf("wal: CatalogDeletePayload wrong size: got %d bytes, want 8", len(data))
	}
	return CatalogDeletePayload{FileID: binary.LittleEndian.Uint64(data)}, nil
}

// NewCatalogDeleteRecord builds a ready-to-append TypedRecord for a catalog
// Delete mutation.
func NewCatalogDeleteRecord(fileID uint64) TypedRecord {
	return TypedRecord{
		Type:    RecordCatalogDelete,
		Payload: CatalogDeletePayload{FileID: fileID}.Encode(),
	}
}

// AsCatalogDelete decodes t's payload as a CatalogDeletePayload. It returns
// an error if t.Type is not RecordCatalogDelete.
func (t TypedRecord) AsCatalogDelete() (CatalogDeletePayload, error) {
	if t.Type != RecordCatalogDelete {
		return CatalogDeletePayload{}, fmt.Errorf("wal: AsCatalogDelete called on record of type %s, want %s", t.Type, RecordCatalogDelete)
	}
	return DecodeCatalogDeletePayload(t.Payload)
}

// --- BTreeInsert ---

// BTreeInsertPayload is the payload of a RecordBTreeInsert record: a B+Tree
// index mutation inserting Path -> FileID, matching
// engine/btree.Insert(store, alloc, rootNodeID, path, fileID)'s (path,
// fileID) inputs.
type BTreeInsertPayload struct {
	Path   string
	FileID uint64
}

// Encode serializes p.
func (p BTreeInsertPayload) Encode() []byte {
	buf := make([]byte, 0, uint32LenSize+len(p.Path)+8)
	buf = putUint32Prefixed(buf, []byte(p.Path))
	var idBuf [8]byte
	binary.LittleEndian.PutUint64(idBuf[:], p.FileID)
	buf = append(buf, idBuf[:]...)
	return buf
}

// DecodeBTreeInsertPayload parses data as a BTreeInsertPayload.
func DecodeBTreeInsertPayload(data []byte) (BTreeInsertPayload, error) {
	pathBytes, next, err := readUint32Prefixed(data, 0)
	if err != nil {
		return BTreeInsertPayload{}, fmt.Errorf("wal: decoding BTreeInsertPayload.Path: %w", err)
	}
	if next+8 != len(data) {
		return BTreeInsertPayload{}, fmt.Errorf("wal: BTreeInsertPayload trailing bytes: expected FileID at offset %d, total length %d", next, len(data))
	}
	fileID := binary.LittleEndian.Uint64(data[next:])
	return BTreeInsertPayload{Path: string(pathBytes), FileID: fileID}, nil
}

// NewBTreeInsertRecord builds a ready-to-append TypedRecord for a B+Tree
// Insert mutation.
func NewBTreeInsertRecord(path string, fileID uint64) TypedRecord {
	return TypedRecord{
		Type:    RecordBTreeInsert,
		Payload: BTreeInsertPayload{Path: path, FileID: fileID}.Encode(),
	}
}

// AsBTreeInsert decodes t's payload as a BTreeInsertPayload. It returns an
// error if t.Type is not RecordBTreeInsert.
func (t TypedRecord) AsBTreeInsert() (BTreeInsertPayload, error) {
	if t.Type != RecordBTreeInsert {
		return BTreeInsertPayload{}, fmt.Errorf("wal: AsBTreeInsert called on record of type %s, want %s", t.Type, RecordBTreeInsert)
	}
	return DecodeBTreeInsertPayload(t.Payload)
}

// --- BTreeDelete ---

// BTreeDeletePayload is the payload of a RecordBTreeDelete record: a B+Tree
// index mutation removing Path, matching
// engine/btree.Delete(store, alloc, rootNodeID, path)'s path input.
type BTreeDeletePayload struct {
	Path string
}

// Encode serializes p.
func (p BTreeDeletePayload) Encode() []byte {
	buf := make([]byte, 0, uint32LenSize+len(p.Path))
	buf = putUint32Prefixed(buf, []byte(p.Path))
	return buf
}

// DecodeBTreeDeletePayload parses data as a BTreeDeletePayload.
func DecodeBTreeDeletePayload(data []byte) (BTreeDeletePayload, error) {
	pathBytes, next, err := readUint32Prefixed(data, 0)
	if err != nil {
		return BTreeDeletePayload{}, fmt.Errorf("wal: decoding BTreeDeletePayload.Path: %w", err)
	}
	if next != len(data) {
		return BTreeDeletePayload{}, fmt.Errorf("wal: BTreeDeletePayload trailing bytes: %d unexpected bytes after Path", len(data)-next)
	}
	return BTreeDeletePayload{Path: string(pathBytes)}, nil
}

// NewBTreeDeleteRecord builds a ready-to-append TypedRecord for a B+Tree
// Delete mutation.
func NewBTreeDeleteRecord(path string) TypedRecord {
	return TypedRecord{
		Type:    RecordBTreeDelete,
		Payload: BTreeDeletePayload{Path: path}.Encode(),
	}
}

// AsBTreeDelete decodes t's payload as a BTreeDeletePayload. It returns an
// error if t.Type is not RecordBTreeDelete.
func (t TypedRecord) AsBTreeDelete() (BTreeDeletePayload, error) {
	if t.Type != RecordBTreeDelete {
		return BTreeDeletePayload{}, fmt.Errorf("wal: AsBTreeDelete called on record of type %s, want %s", t.Type, RecordBTreeDelete)
	}
	return DecodeBTreeDeletePayload(t.Payload)
}

// --- SplitCommit ---

// SplitCommitEntry is one (NewPath, FileID, SizeBytes) triple produced by an
// auto-split, carried inside a SplitCommitPayload so that recovery can redo
// the B+Tree insert (and, deterministically re-derive the graph edges) for
// that new file, AND reconstruct a fresh catalog.CatalogRecord for it (see
// engine/split/execute.go's ExecuteSplitAtomic/RecoverSplitCommits, which
// both now cat.Put a StatusActive record per entry using SizeBytes) —
// without needing any other durable source of truth.
//
// SizeBytes was added as a fix during issue #14's (2b.5) concurrent
// race-test implementation: prior to this field's existence, a completed
// split never created ANY catalog.CatalogRecord for its new fileIDs, making
// every split-off file permanently unreadable/unappendable via
// catalog.ContentStore.Read/Append (both require cat.Get to succeed first).
// See .cdr/runs/2026-07-07/034-implementation/architecture-discovery.md for
// the full writeup.
type SplitCommitEntry struct {
	NewPath   string
	FileID    uint64
	SizeBytes uint64
}

// SplitCommitPayload is the payload of a RecordSplitCommit record: everything
// engine/split's ExecuteSplitAtomic (2b.3.6) needs to durably describe one
// completed split transaction, and everything RecoverSplitCommits needs to
// redo that transaction's catalog/B+Tree/graph effects after a crash.
//
// EncodedCatalogRecord holds the FULL, final (post-split) catalog.CatalogRecord
// for OriginalFileID, already encoded via CatalogRecord.Encode() — mirroring
// CatalogPutPayload's own "treat catalog bytes as opaque" convention, so this
// package still does not import engine/catalog. OldPath is the original
// topic path (whose B+Tree entry must be repointed at OriginalFileID, the
// reused redirect-stub fileID). Entries lists every new file the split
// produced, in the same canonical (NewPath-sorted) order the split's
// execution logic itself used, so a replayed B+Tree/graph reconstruction is
// byte-for-byte reproducible.
type SplitCommitPayload struct {
	OriginalFileID       uint64
	OldPath              string
	EncodedCatalogRecord []byte
	Entries              []SplitCommitEntry
}

// Encode serializes p.
func (p SplitCommitPayload) Encode() []byte {
	buf := make([]byte, 0, 8+uint32LenSize+len(p.OldPath)+uint32LenSize+len(p.EncodedCatalogRecord)+4)
	var idBuf [8]byte
	binary.LittleEndian.PutUint64(idBuf[:], p.OriginalFileID)
	buf = append(buf, idBuf[:]...)
	buf = putUint32Prefixed(buf, []byte(p.OldPath))
	buf = putUint32Prefixed(buf, p.EncodedCatalogRecord)

	var countBuf [4]byte
	binary.LittleEndian.PutUint32(countBuf[:], uint32(len(p.Entries)))
	buf = append(buf, countBuf[:]...)
	for _, e := range p.Entries {
		buf = putUint32Prefixed(buf, []byte(e.NewPath))
		var fidBuf [8]byte
		binary.LittleEndian.PutUint64(fidBuf[:], e.FileID)
		buf = append(buf, fidBuf[:]...)
		var sizeBuf [8]byte
		binary.LittleEndian.PutUint64(sizeBuf[:], e.SizeBytes)
		buf = append(buf, sizeBuf[:]...)
	}
	return buf
}

// DecodeSplitCommitPayload parses data as a SplitCommitPayload.
func DecodeSplitCommitPayload(data []byte) (SplitCommitPayload, error) {
	if len(data) < 8 {
		return SplitCommitPayload{}, fmt.Errorf("wal: SplitCommitPayload too short: got %d bytes, need at least 8", len(data))
	}
	originalFileID := binary.LittleEndian.Uint64(data[:8])
	off := 8

	oldPathBytes, off, err := readUint32Prefixed(data, off)
	if err != nil {
		return SplitCommitPayload{}, fmt.Errorf("wal: decoding SplitCommitPayload.OldPath: %w", err)
	}

	encodedRecord, off, err := readUint32Prefixed(data, off)
	if err != nil {
		return SplitCommitPayload{}, fmt.Errorf("wal: decoding SplitCommitPayload.EncodedCatalogRecord: %w", err)
	}
	record := make([]byte, len(encodedRecord))
	copy(record, encodedRecord)

	if off+4 > len(data) {
		return SplitCommitPayload{}, fmt.Errorf("wal: SplitCommitPayload truncated entry count at offset %d", off)
	}
	count := int(binary.LittleEndian.Uint32(data[off:]))
	off += 4

	entries := make([]SplitCommitEntry, 0, count)
	for i := 0; i < count; i++ {
		var pathBytes []byte
		pathBytes, off, err = readUint32Prefixed(data, off)
		if err != nil {
			return SplitCommitPayload{}, fmt.Errorf("wal: decoding SplitCommitPayload.Entries[%d].NewPath: %w", i, err)
		}
		if off+8 > len(data) {
			return SplitCommitPayload{}, fmt.Errorf("wal: SplitCommitPayload truncated Entries[%d].FileID at offset %d", i, off)
		}
		fileID := binary.LittleEndian.Uint64(data[off:])
		off += 8
		if off+8 > len(data) {
			return SplitCommitPayload{}, fmt.Errorf("wal: SplitCommitPayload truncated Entries[%d].SizeBytes at offset %d", i, off)
		}
		sizeBytes := binary.LittleEndian.Uint64(data[off:])
		off += 8
		entries = append(entries, SplitCommitEntry{NewPath: string(pathBytes), FileID: fileID, SizeBytes: sizeBytes})
	}

	if off != len(data) {
		return SplitCommitPayload{}, fmt.Errorf("wal: SplitCommitPayload has %d trailing bytes after %d entries", len(data)-off, count)
	}

	return SplitCommitPayload{
		OriginalFileID:       originalFileID,
		OldPath:              string(oldPathBytes),
		EncodedCatalogRecord: record,
		Entries:              entries,
	}, nil
}

// NewSplitCommitRecord builds a ready-to-append TypedRecord for a split's
// atomic commit point.
func NewSplitCommitRecord(p SplitCommitPayload) TypedRecord {
	return TypedRecord{
		Type:    RecordSplitCommit,
		Payload: p.Encode(),
	}
}

// AsSplitCommit decodes t's payload as a SplitCommitPayload. It returns an
// error if t.Type is not RecordSplitCommit.
func (t TypedRecord) AsSplitCommit() (SplitCommitPayload, error) {
	if t.Type != RecordSplitCommit {
		return SplitCommitPayload{}, fmt.Errorf("wal: AsSplitCommit called on record of type %s, want %s", t.Type, RecordSplitCommit)
	}
	return DecodeSplitCommitPayload(t.Payload)
}

// --- fsync-before-apply write path ---

// AppendAndApply is this package's fsync-before-apply write path: it encodes
// rec, durably appends it to w (via Writer.Append, which — per subtask
// 1.3.1 — does not return until the record has been fsynced to disk), and
// ONLY THEN invokes apply.
//
// This is a structural guarantee, not just a documented convention: the WAL
// package itself, not the caller, decides when apply runs, so a call site
// cannot accidentally mutate catalog/index state before the corresponding
// WAL record is durable. This directly implements docs/LLD/wal.md's
// invariant ("every mutation to the catalog or any index must be logged in
// the WAL before it is applied in memory or on disk").
//
// Error handling:
//   - If encoding or the underlying Writer.Append fails, apply is never
//     called, and the error is returned with a zero offset. Nothing was
//     durably logged, so nothing should be applied.
//   - If Writer.Append succeeds but apply returns an error, AppendAndApply
//     still returns the (non-zero, valid) offset alongside the wrapped
//     error. This is deliberate: the mutation's intent is already safely
//     and durably persisted in the WAL at that point, regardless of whether
//     the in-memory/on-disk apply step succeeded. A failed apply does not
//     un-happen the durable log write — it is exactly the scenario subtask
//     1.3.4's recovery replay exists to reconcile (replaying the logged
//     mutation again on next startup), so callers should treat an apply
//     error here as "retry or recover the apply step," not as license to
//     also roll back or ignore the WAL record.
func AppendAndApply(w *Writer, rec TypedRecord, apply func() error) (offset int64, err error) {
	payload := rec.Encode()

	offset, err = w.Append(payload)
	if err != nil {
		return 0, fmt.Errorf("wal: appending %s record: %w", rec.Type, err)
	}

	if apply != nil {
		if applyErr := apply(); applyErr != nil {
			return offset, fmt.Errorf("wal: %s record durably appended at offset %d, but apply failed: %w", rec.Type, offset, applyErr)
		}
	}

	return offset, nil
}
