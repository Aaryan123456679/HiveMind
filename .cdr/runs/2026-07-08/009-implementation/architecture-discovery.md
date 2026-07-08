# Architecture discovery

Read (via `awk`, since the `Read` tool intermittently returned corrupted/
word-dropped output for these two files in this session - raw file bytes via
`awk`/md5 were used to cross-check and were clean):

- `engine/graph/compact.go` (full, pre-fix): `Compact()`, `mergeEdges()`,
  `edgeLogNodeIDs()`, package doc comment describing the "accepted risk" this
  fix removes.
- `engine/graph/edgelog.go` (full, pre-fix): `EdgeLog`, `AppendEdge`,
  `ReadNode`, `TruncateNode`, `listWALSegments` (parses `wal-<N>.log` names
  into `numberedSegment{num,path}` internally, then discarded the numbers).
- `engine/graph/csr.go`: `WriteCSR`/`LoadCSR` - atomic temp-file+fsync+rename
  pattern with a magic/version/CRC header, reused as the template for the new
  compact-state sidecar's own atomicity and integrity checking.
- `engine/graph/compact_test.go` (all 3 existing Test* functions, especially
  the two crash-injection tests' exact failure-injection technique:
  `os.Chmod(dir, 0o500)` to deterministically fail either `WriteCSR`'s
  `os.CreateTemp` (crash-before-rename test) or `TruncateNode`'s `os.Remove`
  (truncate-failure test) - not a real process crash/partial-write).
- `.cdr/runs/2026-07-08/007-implementation/handoff.json` and
  `008-verification/verification.json` (via `headroom_retrieve` for
  compressed string fields) for the prior implementer's key decisions and the
  verifier's exact reproduction/finding text.

Key finding: `listWALSegments` already parses each segment's number
internally before discarding it down to a `[]string` of paths - the numbering
information needed for a "read only segments newer than N" primitive already
existed one line away from being exposed, requiring no new parsing logic.
