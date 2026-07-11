package wal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// manifestFileName is the name of the checkpoint control file kept inside a
// WAL directory (the same dir passed to OpenWriter). manifestTempFileName is
// the temp file used to make writes to it atomic (see Checkpoint).
const (
	manifestFileName     = "manifest.json"
	manifestTempFileName = "manifest.json.tmp"
)

// CheckpointPointer identifies "up to where in the WAL is durably applied":
// a segment number together with a byte offset local to that segment.
//
// This tuple form (rather than a single global/monotonic offset) is required
// because Writer.Append's returned offset (see writer.go) is itself a
// per-segment byte offset, reset to 0 on every rotation — this package never
// maintains a cross-segment monotonic counter. A checkpoint pointer must
// therefore carry both which segment and where within it, to be meaningful
// across segment rotation.
//
// Subtask 1.3.4's recovery replay is expected to interpret this pointer as:
// start replaying from segment SegmentNumber at byte offset OffsetInSegment
// (i.e. skip records already known to be durably applied), continuing
// forward through any higher-numbered segments in full.
type CheckpointPointer struct {
	SegmentNumber   uint64 `json:"segment_number"`
	OffsetInSegment int64  `json:"offset_in_segment"`
}

// Checkpoint durably records that state has been applied up to (segmentNumber,
// offsetInSegment) by atomically writing manifest.json inside dir (the WAL
// directory, i.e. the same dir passed to OpenWriter).
//
// manifest.json is deliberately encoding/json, not this package's binary
// record format: docs/LLD/wal.md calls for a small, human-inspectable control
// file tracking the checkpoint pointer, distinct in spirit and format from
// the append-only binary WAL segments it points into.
//
// The write is atomic: the encoded manifest is written to a temp file in the
// same directory, fsynced, and then moved into place via os.Rename (atomic on
// POSIX filesystems, and safe here because the temp file lives in the same
// directory/filesystem as the final path). This avoids ever leaving a torn or
// partially-written manifest.json on disk, even if the process crashes
// mid-write — a corrupt manifest would otherwise be able to break 1.3.4's
// recovery.
//
// This temp-file+Sync+os.Rename idiom is new to this codebase, not a
// pattern followed from an existing precedent: engine/btree/persist.go's
// SaveRoot durably persists its root node ID via a different, weaker
// technique — an in-place f.WriteAt followed by f.Sync, with no temp file
// and no rename. The two are structurally different atomic-write
// strategies; this file's use of temp-file+rename should not be read as
// mirroring SaveRoot's.
func Checkpoint(dir string, segmentNumber uint64, offsetInSegment int64) error {
	ptr := CheckpointPointer{
		SegmentNumber:   segmentNumber,
		OffsetInSegment: offsetInSegment,
	}

	data, err := json.MarshalIndent(ptr, "", "  ")
	if err != nil {
		return fmt.Errorf("wal: encoding checkpoint manifest: %w", err)
	}

	tmpPath := filepath.Join(dir, manifestTempFileName)
	finalPath := filepath.Join(dir, manifestFileName)

	f, err := os.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("wal: creating temp manifest %s: %w", tmpPath, err)
	}

	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("wal: writing temp manifest %s: %w", tmpPath, err)
	}

	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("wal: syncing temp manifest %s: %w", tmpPath, err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("wal: closing temp manifest %s: %w", tmpPath, err)
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("wal: renaming temp manifest %s to %s: %w", tmpPath, finalPath, err)
	}

	return nil
}

// LoadCheckpoint reads back the checkpoint pointer previously written by
// Checkpoint from dir's manifest.json.
//
// If dir contains no manifest.json yet (a fresh WAL with nothing checkpointed
// so far), LoadCheckpoint returns found=false with a nil error — this is an
// expected, non-error state, not a failure.
func LoadCheckpoint(dir string) (segmentNumber uint64, offsetInSegment int64, found bool, err error) {
	path := filepath.Join(dir, manifestFileName)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, false, nil
		}
		return 0, 0, false, fmt.Errorf("wal: reading manifest %s: %w", path, err)
	}

	var ptr CheckpointPointer
	if err := json.Unmarshal(data, &ptr); err != nil {
		return 0, 0, false, fmt.Errorf("wal: decoding manifest %s: %w", path, err)
	}

	return ptr.SegmentNumber, ptr.OffsetInSegment, true, nil
}

// ArchivableSegments returns the paths of segment files in dir ("wal-<N>.log",
// matching the same naming convention as writer.go's latestSegmentNum) whose
// segment number is strictly less than checkpointSegmentNumber, sorted
// ascending by segment number.
//
// The checkpoint's own segment (N == checkpointSegmentNumber) is deliberately
// excluded: the checkpoint offset may land partway through that segment, so
// it is not yet fully durably-applied-and-safe-to-archive as a whole. Any
// segment numbered higher than the checkpoint is newer than the checkpoint
// and therefore also not eligible.
//
// This function only identifies eligible segments; it does not delete,
// truncate, or otherwise archive them — actual archival/deletion is out of
// scope for this subtask and left to a later, separate concern.
func ArchivableSegments(dir string, checkpointSegmentNumber uint64) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("wal: reading dir %s: %w", dir, err)
	}

	type numberedSegment struct {
		num  uint64
		path string
	}

	var found []numberedSegment
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
			// Not a segment file this package recognizes; ignore, as
			// latestSegmentNum does for the same reason.
			continue
		}
		if num < checkpointSegmentNumber {
			found = append(found, numberedSegment{num: num, path: filepath.Join(dir, name)})
		}
	}

	sort.Slice(found, func(i, j int) bool { return found[i].num < found[j].num })

	paths := make([]string, len(found))
	for i, ns := range found {
		paths[i] = ns.path
	}
	return paths, nil
}
