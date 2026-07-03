# Plan — Subtask 1.3.1

1. `engine/wal/writer.go`
   - Constants: `recordHeaderSize = 8` (uint32 length + uint32 CRC32), offset
     constants `offRecordLength`, `offRecordCRC`.
   - `type Writer struct` — `mu sync.Mutex`, `dir string`, `maxSegmentBytes
     int64`, `segmentNum int`, `file *os.File`, `size int64` (current
     segment's on-disk size).
   - `OpenWriter(dir string, maxSegmentBytes int64) (*Writer, error)`:
     - validate `maxSegmentBytes > recordHeaderSize` (must be able to hold at
       least a zero-length record's header).
     - `os.MkdirAll(dir, 0o755)`.
     - scan `dir` for existing `wal-<N>.log` files, find max N (regex/prefix
       parse); if none found, start at segment 0 with a fresh file; if found,
       reopen the highest-numbered segment in append mode and stat its
       current size to resume `size` bookkeeping.
     - return `*Writer`.
   - `func segmentPath(dir string, n int) string` — `filepath.Join(dir,
     fmt.Sprintf("wal-%d.log", n))`.
   - `func (w *Writer) Append(payload []byte) (int64, error)`:
     - lock mu.
     - `total := recordHeaderSize + int64(len(payload))`.
     - if `total > w.maxSegmentBytes`: hard error, no write attempted (per
       btree/node.go "hard-error-not-truncate" idiom).
     - if `w.size > 0 && w.size+total > w.maxSegmentBytes`: rotate (close
       current file, open `segmentNum+1`, reset size to 0) BEFORE writing.
     - build header buffer, `binary.LittleEndian.PutUint32` length,
       `crc32.ChecksumIEEE(payload)` into CRC field.
     - `offset := w.size` (offset within the (possibly just-rotated) current
       segment file where this record's header starts).
     - `w.file.Write(header)`, `w.file.Write(payload)`, `w.file.Sync()`.
     - update `w.size += total`.
     - return offset, nil.
   - `func (w *Writer) Close() error` — closes current file.
   - Exported helper for tests/future subtasks: `ReadSegment(path string)
     ([][]byte, error)` that parses a segment file fully into a slice of
     payloads, returning an error on any truncated header/payload or
     length/CRC mismatch — this directly supports the issue's test-spec
     requirement to "read back both segment files directly (parsing the
     header+payload format)" without writer_test.go needing to duplicate
     framing logic, and gives 1.3.4/1.3.5 a starting point.

2. `engine/wal/writer_test.go`
   - `TestSegmentWriter`: `t.TempDir()`-backed dir, small `maxSegmentBytes`
     (e.g. 200 bytes), append enough small fixed-content records (with
     distinguishable content, e.g. `fmt.Sprintf("record-%03d", i)`) to force
     at least 2 rotations (>= 3 segments). After all appends:
     - list `wal-*.log` files in dir, assert at least 3 exist.
     - for each segment file, call `ReadSegment` and assert it parses
       cleanly (no error) — proves no split records within any single
       segment's own bytes.
     - assert no segment file's size exceeds `maxSegmentBytes` by more than
       one record's worth of slack (a segment is allowed to be under budget,
       since we rotate before exceeding, but never over by more than what a
       single record could have pushed it before the rotation check — i.e.
       each segment except possibly the pre-rotation check must satisfy
       `size <= maxSegmentBytes`).
     - concatenate all parsed payloads across segments in segment order and
       assert they equal, in order, exactly the sequence of records
       appended (round-trip integrity + ordering across segment boundary).
   - Additional focused unit tests:
     - `TestAppendOversizedRecordHardErrors`: a payload whose total encoded
       size exceeds `maxSegmentBytes` returns an error, no file corruption.
     - `TestOpenWriterResumesExistingSegments`: open a Writer, append a few
       records, Close, reopen `OpenWriter` on the same dir, append more,
       assert it continues at the correct next segment number / offset and
       old data is untouched.
   - Run with `-race` per the issue's test spec.

3. Update `.cdr/index/file.jsonl` (two new entries for writer.go/writer_test.go)
   and `.cdr/index/task.jsonl` (`task-1.3.1` -> implemented, real commit SHA).

4. Self-consistency: `go build ./engine/...`, `go vet ./engine/...`,
   `go test ./engine/wal/... -run TestSegmentWriter -race -v`, and
   `go test ./engine/wal/... -race -v` (full package).

5. One local commit, Problem/Solution/Impact style, no push.
