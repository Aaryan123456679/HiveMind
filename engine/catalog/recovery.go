package catalog

import (
	"errors"
	"fmt"

	"github.com/Aaryan123456679/HiveMind/engine/wal"
)

// RecoverFromWAL reconstructs cat's in-memory index by replaying the WAL rooted at
// walDir and re-applying every CatalogPut/CatalogDelete mutation it finds, in
// on-disk order, via cat.Put/cat.Delete.
//
// This exists to close the gap flagged by Catalog's own doc comment (catalog.go):
// NewCatalog never scans catalog.dat to rebuild its fileID->location index from
// whatever records already exist on disk, so a freshly-opened Catalog against an
// existing catalog.dat file starts with an EMPTY index even though the underlying
// page bytes are still durably present. docs/LLD/wal.md's "Recovery" section
// documents exactly this restart model: "On startup, the engine replays the WAL
// from the last checkpoint pointer forward, reapplying any mutations that were
// logged but not yet reflected in the checkpointed state." RecoverFromWAL is that
// replay-and-reapply step, scoped to engine/catalog's own mutation types.
//
// Callers (see content_test.go's TestContentDurabilityRestart) are expected to call
// this once, immediately after opening a fresh Catalog against an existing
// catalog.dat and its associated WAL directory, before serving any reads.
//
// Each replayed CatalogPut re-applies the FULL encoded CatalogRecord it carries via
// cat.Put, which (per Catalog.Put's own doc comment) always does a fresh
// delete-then-reinsert rather than an in-place update. Because the replaying
// Catalog's index starts empty, there is no "old slot" for Put to tombstone here —
// each replayed record simply lands on a newly allocated/reused page slot. This
// reconstructs a correct index (every surviving fileID resolves to a location
// holding its most-recently-Put CatalogRecord) but does not reclaim whatever page
// space the ORIGINAL process's now-orphaned slots occupied; that is a storage-
// efficiency concern (compaction), not a correctness one, and is out of scope for
// this subtask's read-after-restart acceptance criterion.
//
// RecoverFromWAL only understands RecordCatalogPut and RecordCatalogDelete; any
// other record type present in the WAL (e.g. engine/btree's RecordBTreeInsert/
// RecordBTreeDelete) is skipped rather than treated as an error, since this
// function's job is reconstructing Catalog state specifically, not asserting
// exclusive ownership of the WAL directory's contents.
func RecoverFromWAL(cat *Catalog, walDir string) error {
	if cat == nil {
		return fmt.Errorf("catalog: RecoverFromWAL: cat must not be nil")
	}

	err := wal.Replay(walDir, func(rec wal.TypedRecord) error {
		switch rec.Type {
		case wal.RecordCatalogPut:
			payload, err := rec.AsCatalogPut()
			if err != nil {
				return fmt.Errorf("decoding CatalogPut payload: %w", err)
			}
			crec, err := Decode(payload.Record)
			if err != nil {
				return fmt.Errorf("decoding CatalogRecord for fileID %d: %w", payload.FileID, err)
			}
			if err := cat.Put(crec); err != nil {
				return fmt.Errorf("replaying Put for fileID %d: %w", payload.FileID, err)
			}
			return nil

		case wal.RecordCatalogDelete:
			payload, err := rec.AsCatalogDelete()
			if err != nil {
				return fmt.Errorf("decoding CatalogDelete payload: %w", err)
			}
			if err := cat.Delete(payload.FileID); err != nil && !errors.Is(err, ErrNotFound) {
				return fmt.Errorf("replaying Delete for fileID %d: %w", payload.FileID, err)
			}
			return nil

		default:
			// Not a catalog mutation this function is responsible for reconstructing
			// (e.g. a B+Tree index record); nothing to do.
			return nil
		}
	})
	if err != nil {
		return fmt.Errorf("catalog: RecoverFromWAL: replaying %s: %w", walDir, err)
	}
	return nil
}
