# Plan

Rewrite `docs/LLD/wal.md` in place, keeping the existing front-matter
convention (`last_synced_commit` line) and heading style, but replacing
"Status: scaffold only" with real content across these sections:

1. Purpose (lightly reworded, kept short)
2. Storage layout — record header format (8-byte, length+CRC32 LE),
   segment naming (`wal-<N>.log`), rotation rule (rotate-before-write,
   hard-error on oversized record), segment-floor mechanism.
3. Record types — table/list of RecordType constants + payload encodings
   + the RecordTypeInvalid/out-of-range validation guard (4.5.4.3 /
   4c60202).
4. Checkpointing — manifest.json schema, atomic write path, explicit note
   distinguishing it from btree's SaveRoot (4.5.4.4 / ab5e962),
   ArchivableSegments boundary semantics.
5. Recovery / Replay — LoadCheckpoint fallback, OffsetInSegment
   inclusive-start convention, RecordType validation gate, torn-tail
   discipline (torn-tail-vs-CRC-corruption, repairTornTail on resume,
   torn tail only legal in last segment) from task-1.3.5.
6. Invariant (retain, lightly reworded for prose quality).
7. Known risks (update to reflect what's now implemented vs. still
   open — e.g. archival/deletion of ArchivableSegments output is still
   unimplemented/out of scope).
8. Cross-references (retain as-is).

No production code changes. Update the `last_synced_commit` front-matter
line to current HEAD.
