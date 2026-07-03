package catalog

import (
	"encoding/binary"
	"fmt"
)

// RecordStatus is the lifecycle status of a catalog record.
type RecordStatus uint8

const (
	// StatusActive means the record is the live, authoritative entry for its fileID.
	StatusActive RecordStatus = iota
	// StatusSplitting means an auto-split is in progress for this file.
	StatusSplitting
	// StatusSplit means the file has finished splitting into its redirect targets.
	StatusSplit
	// StatusRedirect means this record is a stub left behind at the old path, pointing
	// readers at RedirectTargetIDs.
	StatusRedirect
)

// MaxRedirectTargets is the maximum number of redirect target fileIDs a single catalog
// record can hold. This bounds the record to a fixed on-disk size. The split design
// typically produces 2-3 children per split, so 8 leaves comfortable room to spare.
const MaxRedirectTargets = 8

// CatalogRecord is the in-memory representation of a single catalog entry: the metadata
// HiveMind tracks for one topic file. See docs/LLD/catalog.md for the on-disk design.
type CatalogRecord struct {
	// FileID is the monotonically increasing, atomically allocated identifier for this
	// file. Never reused.
	FileID uint64
	// PathHash is a fixed-size (64-bit) hash of the file's topic path. A simple uint64
	// hash is sufficient for a first pass; it is not a full content hash.
	PathHash uint64
	// CurrentVersion is the MVCC version counter for this file's content.
	CurrentVersion uint64
	// SizeBytes is the current size of the file's content, in bytes.
	SizeBytes uint64
	// Status is the lifecycle status of the record (ACTIVE/SPLITTING/SPLIT/REDIRECT).
	Status RecordStatus
	// RedirectTargetIDs holds the fileIDs this record redirects to once it has been
	// split (Status == StatusSplit or StatusRedirect). Empty/nil when the record is not
	// a split stub. Length must not exceed MaxRedirectTargets.
	RedirectTargetIDs []uint64
	// ParentTopicID is the fileID of the parent topic this file belongs to, if any.
	ParentTopicID uint64
	// LastModified is the last-modified timestamp, stored as Unix nanoseconds.
	LastModified int64
}

// Fixed byte offsets/widths for the encoded record layout. All multi-byte integers are
// little-endian, the standard byte order for on-disk formats defined by this project.
const (
	offFileID         = 0
	offPathHash       = offFileID + 8
	offCurrentVersion = offPathHash + 8
	offSizeBytes      = offCurrentVersion + 8
	offStatus         = offSizeBytes + 8
	offRedirectCount  = offStatus + 1
	offReserved       = offRedirectCount + 1
	reservedWidth     = 2 // padding for future use / alignment
	offRedirectIDs    = offReserved + reservedWidth
	redirectIDsWidth  = 8 * MaxRedirectTargets
	offParentTopicID  = offRedirectIDs + redirectIDsWidth
	offLastModified   = offParentTopicID + 8

	// RecordEncodedSize is the exact fixed size in bytes of an encoded CatalogRecord.
	RecordEncodedSize = offLastModified + 8
)

// Encode serializes the record into a new RecordEncodedSize-byte little-endian buffer.
// It returns an error if len(RedirectTargetIDs) exceeds MaxRedirectTargets rather than
// silently dropping targets; callers that build records programmatically must keep
// len(RedirectTargetIDs) <= MaxRedirectTargets (Decode symmetrically rejects an
// over-long buffer on the read path).
func (r CatalogRecord) Encode() ([]byte, error) {
	count := len(r.RedirectTargetIDs)
	if count > MaxRedirectTargets {
		return nil, fmt.Errorf("catalog: too many redirect targets: got %d, max %d", count, MaxRedirectTargets)
	}

	buf := make([]byte, RecordEncodedSize)

	binary.LittleEndian.PutUint64(buf[offFileID:], r.FileID)
	binary.LittleEndian.PutUint64(buf[offPathHash:], r.PathHash)
	binary.LittleEndian.PutUint64(buf[offCurrentVersion:], r.CurrentVersion)
	binary.LittleEndian.PutUint64(buf[offSizeBytes:], r.SizeBytes)
	buf[offStatus] = byte(r.Status)

	buf[offRedirectCount] = byte(count)
	// buf[offReserved:offReserved+reservedWidth] left as zero padding.

	for i := 0; i < count; i++ {
		off := offRedirectIDs + i*8
		binary.LittleEndian.PutUint64(buf[off:], r.RedirectTargetIDs[i])
	}
	// Remaining redirect slots (beyond count, up to MaxRedirectTargets) stay zero.

	binary.LittleEndian.PutUint64(buf[offParentTopicID:], r.ParentTopicID)
	binary.LittleEndian.PutUint64(buf[offLastModified:], uint64(r.LastModified))

	return buf, nil
}

// Decode deserializes a RecordEncodedSize-byte little-endian buffer (as produced by
// Encode) back into a CatalogRecord. It returns an error if the buffer is the wrong
// length or declares an out-of-range redirect target count.
func Decode(data []byte) (CatalogRecord, error) {
	if len(data) != RecordEncodedSize {
		return CatalogRecord{}, fmt.Errorf("catalog: invalid record length: got %d bytes, want %d", len(data), RecordEncodedSize)
	}

	count := int(data[offRedirectCount])
	if count > MaxRedirectTargets {
		return CatalogRecord{}, fmt.Errorf("catalog: invalid redirect target count: got %d, max %d", count, MaxRedirectTargets)
	}

	var redirectTargetIDs []uint64
	if count > 0 {
		redirectTargetIDs = make([]uint64, count)
		for i := 0; i < count; i++ {
			off := offRedirectIDs + i*8
			redirectTargetIDs[i] = binary.LittleEndian.Uint64(data[off:])
		}
	}

	rec := CatalogRecord{
		FileID:            binary.LittleEndian.Uint64(data[offFileID:]),
		PathHash:          binary.LittleEndian.Uint64(data[offPathHash:]),
		CurrentVersion:    binary.LittleEndian.Uint64(data[offCurrentVersion:]),
		SizeBytes:         binary.LittleEndian.Uint64(data[offSizeBytes:]),
		Status:            RecordStatus(data[offStatus]),
		RedirectTargetIDs: redirectTargetIDs,
		ParentTopicID:     binary.LittleEndian.Uint64(data[offParentTopicID:]),
		LastModified:      int64(binary.LittleEndian.Uint64(data[offLastModified:])),
	}

	return rec, nil
}
